package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A fleet scope as a namespace sees it: the model-free profile, its toolbox
// and the policy that admits both to team-a.
func fleetScopeFixture() []client.Object {
	template := apiextensionsv1.JSON{Raw: []byte(`{"model":"mediated","url":"https://celln-model.invalid/v1/invoke"}`)}
	base := &api.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-trial", Labels: map[string]string{cellnplatform.MediationOnlyLabel: "true"}}, Spec: api.CellnRuntimeProfileSpec{Revision: "v1", Native: &api.CellnNativeProvisioning{SystemPrompt: "host persona", Template: template}}}
	tools := []api.ClusterCellnToolRef{{Name: "celln-trial-workspace-read", Revision: "v1"}, {Name: "celln-trial-grep", Revision: "v1"}}
	toolbox := base.DeepCopy()
	toolbox.Name = cellnplatform.ToolboxProfileName(base.Name)
	toolbox.Labels[cellnplatform.ToolboxLabel] = "true"
	toolbox.Annotations = map[string]string{cellnplatform.ToolboxToolsAnnotation: cellnplatform.ToolboxAnnotation(tools)}
	policy := &api.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "celln-fleet-trial"}, Spec: api.CellnExecutionPolicySpec{
		NamespaceSelector: cellnplatform.OpenSelector(cellnplatform.SystemNamespaces("sympozium-system")),
		RuntimeProfiles:   []api.CellnExecutionPolicyRuntime{{Ref: api.CellnRuntimeProfileRef{Name: base.Name, Revision: "v1"}}, {Ref: api.CellnRuntimeProfileRef{Name: toolbox.Name, Revision: "v1"}}},
		Tools:             []api.CellnExecutionPolicyTool{{Ref: tools[0]}, {Ref: tools[1]}},
		Ceilings:          api.CellnExecutionPolicyCeilings{MaxTurns: 256, MaxModelRequests: 1536, MaxOutputTokens: 786432, MaxParentLeaseSeconds: 86400, MaxTurnSeconds: 60},
	}}
	return []client.Object{base, toolbox, policy, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a", Labels: map[string]string{cellnplatform.NamespaceNameLabel: "team-a"}}}}
}

// One call creates a working Celln Agent: the connection and its key Secret
// (pinned to the origin, owned by the Agent), the toolbox runtime wrapper, the
// enduring lifecycle and the policy's session defaults. The same key is
// refused to a second Agent, and a connection moved elsewhere is refused.
func TestCreateCellnAgentInOneCall(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(fleetScopeFixture()...).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	post := func(body string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/agents?namespace=team-a", strings.NewReader(body)))
		return res
	}
	res := post(`{"name":"researcher","execution":{"backend":"celln"},"modelConnection":{"spec":{"provider":"openrouter","protocol":"openai-chat","endpoint":"https://openrouter.ai/api/v1/chat/completions","models":["qwen/qwen3-235b"]},"apiKey":"sk-or-not-a-real-key-0123456789"}}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "sk-or-not-a-real-key") {
		t.Fatalf("the key was echoed: %s", res.Body.String())
	}
	ctx := t.Context()
	var agent api.Agent
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: "researcher"}, &agent); err != nil {
		t.Fatal(err)
	}
	e := agent.Spec.Execution
	if e == nil || e.Backend != "celln" || e.ExecutionLifecycle != "enduring" || e.ModelConnectionRef != "researcher" || e.Model != "qwen/qwen3-235b" || e.Enduring == nil || e.CellnSelection == nil {
		t.Fatalf("execution defaults: %+v", e)
	}
	if e.CellnSelection.RuntimeRef != "celln-native.toolbox" || agent.Spec.RuntimeRef != "celln-native.toolbox" || len(e.CellnSelection.ClusterToolRefs) != 2 || e.CellnSelection.ClusterToolRefs[0].Name != "celln-trial-workspace-read" || e.CellnSelection.ToolRefs == nil {
		t.Fatalf("selection: %+v (runtimeRef %q)", e.CellnSelection, agent.Spec.RuntimeRef)
	}
	if e.Enduring.LeaseSeconds == 0 || e.Enduring.MaxTurns == 0 || e.Enduring.MaxTurns > 256 || e.Enduring.MaxOutputTokens > 786432 {
		t.Fatalf("session defaults: %+v", e.Enduring)
	}
	if agent.Spec.Agents.Default.Model != "qwen/qwen3-235b" || len(agent.Spec.AuthRefs) != 1 || agent.Spec.AuthRefs[0].Secret != "researcher-openrouter-key" || agent.Spec.AuthRefs[0].Provider != "openrouter" {
		t.Fatalf("agent model/auth: %+v %+v", agent.Spec.Agents.Default, agent.Spec.AuthRefs)
	}
	var wrapper api.AgentRuntime
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: "celln-native.toolbox"}, &wrapper); err != nil || wrapper.Spec.CellnProfileRef == nil || wrapper.Spec.CellnProfileRef.Name != "celln-native-trial.toolbox" {
		t.Fatalf("runtime wrapper: %v %+v", err, wrapper.Spec)
	}
	var connection api.ModelConnection
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: "researcher"}, &connection); err != nil || connection.Spec.SecretRef != "researcher-openrouter-key" || connection.Spec.Provider != "openrouter" {
		t.Fatalf("connection: %v %+v", err, connection.Spec)
	}
	var secret corev1.Secret
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: "researcher-openrouter-key"}, &secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data[api.DefaultSecretKey("openai-chat")])+secret.StringData[api.DefaultSecretKey("openai-chat")] != "sk-or-not-a-real-key-0123456789" {
		t.Fatalf("secret holds %v / %v", secret.Data, secret.StringData)
	}
	if secret.Annotations[modelkey.OriginAnnotation] != "https://openrouter.ai" || secret.Annotations[modelkey.OwnerAnnotation] != "Agent/researcher" {
		t.Fatalf("secret annotations: %v", secret.Annotations)
	}
	// Nothing shared appears in the namespace: no managed Agent or connection.
	var agents api.AgentList
	if err := cl.List(ctx, &agents, client.InNamespace("team-a")); err != nil || len(agents.Items) != 1 {
		t.Fatalf("agents: %v %d", err, len(agents.Items))
	}

	// The key belongs to researcher: another Agent naming its connection is refused.
	if res := post(`{"name":"copycat","model":"qwen/qwen3-235b","execution":{"backend":"celln","modelConnectionRef":"researcher"}}`); res.Code != http.StatusConflict {
		t.Fatalf("second Agent on the same key: %d %s", res.Code, res.Body.String())
	}
	// A keyless connection to another provider needs no key and gets the same
	// defaults; its model comes from the connection.
	if res := post(`{"name":"local","execution":{"backend":"celln"},"modelConnection":{"spec":{"provider":"llama-server","protocol":"openai-chat","endpoint":"https://llm.example.com/v1/chat/completions","models":["qwen3-8b"]}}}`); res.Code != http.StatusCreated {
		t.Fatalf("keyless create: %d %s", res.Code, res.Body.String())
	}
	var local api.Agent
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: "local"}, &local); err != nil || local.Spec.Agents.Default.Model != "qwen3-8b" || len(local.Spec.AuthRefs) != 0 || local.Spec.Execution.CellnSelection.RuntimeRef != "celln-native.toolbox" {
		t.Fatalf("keyless agent: %v %+v", err, local.Spec)
	}

	for body, code := range map[string]int{
		// Both ways of naming a connection at once.
		`{"name":"x","execution":{"backend":"celln","modelConnectionRef":"researcher"},"modelConnection":{"spec":{"provider":"openai","protocol":"openai-chat","endpoint":"https://api.openai.com/v1/chat/completions","models":["gpt-4o"]}}}`: http.StatusBadRequest,
		// An inline connection on a Kubernetes Agent.
		`{"name":"x","execution":{"backend":"job"},"modelConnection":{"spec":{"provider":"openai","protocol":"openai-chat","endpoint":"https://api.openai.com/v1/chat/completions","models":["gpt-4o"]}}}`: http.StatusBadRequest,
		// A key over plain HTTP never leaves the gateway.
		`{"name":"x","execution":{"backend":"celln"},"modelConnection":{"spec":{"provider":"custom","protocol":"openai-chat","endpoint":"http://10.0.0.5:8080/v1/chat/completions","allowInsecure":true,"models":["m"]},"apiKey":"sk-plain"}}`: http.StatusBadRequest,
	} {
		if res := post(body); res.Code != code {
			t.Fatalf("%s: %d %s, want %d", body, res.Code, res.Body.String(), code)
		}
	}

	// Moving researcher's connection to another origin is refused: the key is pinned.
	res = httptest.NewRecorder()
	srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/model-connections?namespace=team-a", strings.NewReader(`{"name":"researcher","spec":{"provider":"openrouter","protocol":"openai-chat","endpoint":"https://attacker.example/v1/chat/completions","models":["qwen/qwen3-235b"],"secretRef":"researcher-openrouter-key"}}`)))
	if res.Code != http.StatusConflict {
		t.Fatalf("origin move: %d %s", res.Code, res.Body.String())
	}
	var moved api.ModelConnection
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "team-a", Name: "researcher"}, &moved); err != nil || moved.Spec.Endpoint != "https://openrouter.ai/api/v1/chat/completions" {
		t.Fatalf("connection changed despite the refusal: %v %+v", err, moved.Spec)
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(`{}`), &out)
}

// A namespace no fleet serves cannot default a Celln Agent; the refusal says
// what to do, and names nothing the caller did not send.
func TestCreateCellnAgentWithoutAFleetSaysSo(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-b", Labels: map[string]string{cellnplatform.NamespaceNameLabel: "team-b"}}}).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	res := httptest.NewRecorder()
	srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/agents?namespace=team-b", strings.NewReader(`{"name":"lonely","execution":{"backend":"celln"},"modelConnection":{"spec":{"provider":"openai","protocol":"openai-chat","endpoint":"https://api.openai.com/v1/chat/completions","models":["gpt-4o"]},"apiKey":"sk-x"}}`)))
	if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "no Celln fleet profile is offered") || !strings.Contains(res.Body.String(), "sympozium install") {
		t.Fatalf("no fleet: %d %s", res.Code, res.Body.String())
	}
}

// The persona is agent configuration: execution.systemPrompt on the Agent
// reaches every run it starts (here through /messages), a run may override
// it, an Agent without one gets the fleet's default, and a persona the cell
// could not accept is refused at creation.
func TestCellnAgentPersonaReachesItsRuns(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(fleetScopeFixture()...).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	do := func(method, path, body string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(method, path, strings.NewReader(body)))
		return res
	}
	connection := `"modelConnection":{"spec":{"provider":"llama-server","protocol":"openai-chat","endpoint":"https://models.example/v1/chat/completions","models":["qwen"]}}`
	if res := do(http.MethodPost, "/api/v1/agents?namespace=team-a", `{"name":"terse","execution":{"backend":"celln","systemPrompt":"Answer in one word."},`+connection+`}`); res.Code != http.StatusCreated {
		t.Fatalf("create with persona: %d %s", res.Code, res.Body.String())
	}
	var agent api.Agent
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "team-a", Name: "terse"}, &agent); err != nil || agent.Spec.Execution.SystemPrompt != "Answer in one word." {
		t.Fatalf("persona not kept on the Agent: %v %+v", err, agent.Spec.Execution)
	}
	res := do(http.MethodPost, "/api/v1/agents/terse/messages?namespace=team-a", `{"message":"ping"}`)
	var started AgentMessageResponse
	if res.Code != http.StatusCreated || json.Unmarshal(res.Body.Bytes(), &started) != nil || started.Run.Spec.SystemPrompt != "Answer in one word." {
		t.Fatalf("run did not inherit the Agent's persona: %d %s", res.Code, res.Body.String())
	}
	// A run may still name its own.
	res = do(http.MethodPost, "/api/v1/runs?namespace=team-a", `{"agentRef":"terse","task":"ping","systemPrompt":"Answer in French."}`)
	var run api.AgentRun
	if res.Code != http.StatusCreated || json.Unmarshal(res.Body.Bytes(), &run) != nil || run.Spec.SystemPrompt != "Answer in French." {
		t.Fatalf("run override lost: %d %s", res.Code, res.Body.String())
	}
	// No persona anywhere: the fleet's default persona.
	if res := do(http.MethodPost, "/api/v1/agents?namespace=team-a", `{"name":"plain","execution":{"backend":"celln"},`+connection+`}`); res.Code != http.StatusCreated {
		t.Fatalf("create without persona: %d %s", res.Code, res.Body.String())
	}
	res = do(http.MethodPost, "/api/v1/agents/plain/messages?namespace=team-a", `{"message":"ping"}`)
	var plain AgentMessageResponse
	var profile api.CellnRuntimeProfile
	_ = cl.Get(t.Context(), types.NamespacedName{Name: "celln-native-trial"}, &profile)
	if res.Code != http.StatusCreated || json.Unmarshal(res.Body.Bytes(), &plain) != nil || plain.Run.Spec.SystemPrompt == "" || plain.Run.Spec.SystemPrompt != profile.Spec.Native.SystemPrompt {
		t.Fatalf("fleet default persona not applied: %d %q (profile %q)", res.Code, plain.Run.Spec.SystemPrompt, profile.Spec.Native.SystemPrompt)
	}
	// Beyond the cell's bound: refused with the bound named, on the Agent and on a run.
	long := strings.Repeat("x", api.MaxCellnSystemPromptBytes+1)
	if res := do(http.MethodPost, "/api/v1/agents?namespace=team-a", `{"name":"verbose","execution":{"backend":"celln","systemPrompt":"`+long+`"},`+connection+`}`); res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "2048") {
		t.Fatalf("oversized Agent persona accepted: %d %s", res.Code, res.Body.String())
	}
	if res := do(http.MethodPost, "/api/v1/runs?namespace=team-a", `{"agentRef":"terse","task":"ping","systemPrompt":"`+long+`"}`); res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "2048") {
		t.Fatalf("oversized run persona accepted: %d %s", res.Code, res.Body.String())
	}
}
