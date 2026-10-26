package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCellnPlatformProfilesAndWrappersFollowTheNamespacePolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = sympoziumv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	raw := func(s string) apiextensionsv1.JSON { return apiextensionsv1.JSON{Raw: []byte(s)} }
	profile := &sympoziumv1alpha1.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-trial"}, Spec: sympoziumv1alpha1.CellnRuntimeProfileSpec{Revision: "v1", Native: &sympoziumv1alpha1.CellnNativeProvisioning{SystemPrompt: "host persona", Template: raw(`{"model":"deepseek-chat","url":"https://api.deepseek.com/chat/completions"}`)}}}
	// A second backend of the same scope: its profile is labeled with the
	// backend name and gets its own wrapper names.
	local := &sympoziumv1alpha1.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-trial-local", Labels: map[string]string{cellnplatform.BackendLabel: "local"}}, Spec: sympoziumv1alpha1.CellnRuntimeProfileSpec{Revision: "v1", Native: &sympoziumv1alpha1.CellnNativeProvisioning{TurnModelRequests: 6, TurnOutputTokens: 24576, SystemPrompt: "host persona", Template: raw(`{"model":"qwen.gguf","url":"http://100.81.163.75:8080/v1/chat/completions","allow_insecure":true}`)}}}
	policy := &sympoziumv1alpha1.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "celln-fleet-trial"}, Spec: sympoziumv1alpha1.CellnExecutionPolicySpec{
		NamespaceSelector: cellnplatform.OpenSelector(cellnplatform.SystemNamespaces("sympozium-system")),
		RuntimeProfiles:   []sympoziumv1alpha1.CellnExecutionPolicyRuntime{{Ref: sympoziumv1alpha1.CellnRuntimeProfileRef{Name: profile.Name, Revision: "v1"}}, {Ref: sympoziumv1alpha1.CellnRuntimeProfileRef{Name: local.Name, Revision: "v1"}}},
		Tools:             []sympoziumv1alpha1.CellnExecutionPolicyTool{{Ref: sympoziumv1alpha1.ClusterCellnToolRef{Name: "celln-trial-workspace-read", Revision: "v1"}}},
		Ceilings:          sympoziumv1alpha1.CellnExecutionPolicyCeilings{MaxTurns: 256, MaxModelRequests: 1536, MaxOutputTokens: 786432, MaxParentLeaseSeconds: 86400, MaxTurnSeconds: 60},
	}}
	ns := func(name string) *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{cellnplatform.NamespaceNameLabel: name}}}
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(profile, local, policy, ns("team-a"), ns("sympozium-system")).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	get := func(namespace string) []CellnPlatformProfile {
		t.Helper()
		res := httptest.NewRecorder()
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/celln-platform/profiles?namespace="+namespace, nil))
		if res.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", namespace, res.Code, res.Body.String())
		}
		var out []CellnPlatformProfile
		if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	got := get("team-a")
	byName := map[string]CellnPlatformProfile{}
	for _, p := range got {
		byName[p.Name] = p
	}
	// Every profile is offered as mediation-only with no provider, model or
	// shared Agent of its own: the Agent's connection is the route.
	if p := byName["celln-native-trial"]; len(got) != 2 || !p.MediationOnly || p.Provider != "" || p.Model != "" || p.Agent != "" || p.Backend != "native" || p.Wrapper != "celln-native" || len(p.Tools) != 1 || p.Tools[0].Name != "celln-trial-workspace-read" || p.Ceilings.MaxTurns != 256 || p.SessionDefaults.MaxTurns != 64 {
		t.Fatalf("tenant profiles: %+v", got)
	}
	if p := byName["celln-native-trial-local"]; !p.MediationOnly || p.Provider != "" || p.Backend != "local" || p.Wrapper != "celln-local" || p.Agent != "" {
		t.Fatalf("second backend must be offered with its own wrapper name: %+v", p)
	}
	// The local backend allows 4096 output tokens per request, so its turns
	// reserve 24576: the shared ceilings pay for 32 of them, and its session
	// defaults are those 32 turns rather than 64 turns it could never take.
	if d := byName["celln-native-trial-local"].SessionDefaults; d.MaxTurns != 32 || d.MaxModelRequests != 192 || d.MaxOutputTokens != 786432 || d.LeaseSeconds != 14400 {
		t.Fatalf("session defaults must follow the profile's own per-turn allowance: %+v", d)
	}
	if got := get("sympozium-system"); len(got) != 0 {
		t.Fatalf("control-plane namespace offered profiles: %+v", got)
	}
	post := func(namespace, body string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/celln-platform/wrappers?namespace="+namespace, strings.NewReader(body)))
		return res
	}
	res := post("team-a", `{"profile":"celln-native-trial"}`)
	var wrappers cellnplatform.Wrappers
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &wrappers) != nil || wrappers.Runtime != "celln-native" || wrappers.Connection != "" || wrappers.Agent != "" || len(wrappers.Created) != 1 {
		t.Fatalf("ensure: %d %s", res.Code, res.Body.String())
	}
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "team-a", Name: "celln-native"}, &sympoziumv1alpha1.ModelConnection{}); err == nil {
		t.Fatal("a host-profile connection was created for the namespace")
	}
	if res := post("team-a", `{"profile":"celln-native-trial"}`); res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"created":[]`) {
		t.Fatalf("second ensure: %d %s", res.Code, res.Body.String())
	}
	if res := post("sympozium-system", `{"profile":"celln-native-trial"}`); res.Code != http.StatusForbidden {
		t.Fatalf("excluded namespace prepared: %d %s", res.Code, res.Body.String())
	}
	if res := post("team-a", `{}`); res.Code != http.StatusBadRequest {
		t.Fatalf("empty profile accepted: %d", res.Code)
	}
}

// Every profile the policy admits is offered as mediation-only: no node holds
// a model, so each entry names its runtime wrapper and no provider, model or
// shared Agent, whether or not the profile carries the mediation-only label.
func TestCellnPlatformListsMediationOnlyProfiles(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = sympoziumv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	template := apiextensionsv1.JSON{Raw: []byte(`{"model":"deepseek-chat","url":"https://api.deepseek.com/chat/completions"}`)}
	profile := func(name string, labels map[string]string) *sympoziumv1alpha1.CellnRuntimeProfile {
		return &sympoziumv1alpha1.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Spec: sympoziumv1alpha1.CellnRuntimeProfileSpec{Revision: "v1", Native: &sympoziumv1alpha1.CellnNativeProvisioning{SystemPrompt: "host persona", Template: template}}}
	}
	mediated := profile("celln-native-trial", map[string]string{cellnplatform.MediationOnlyLabel: "true"})
	unlabeled := profile("celln-native-trial-other", map[string]string{cellnplatform.BackendLabel: "other"})
	policy := &sympoziumv1alpha1.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "celln-fleet-trial"}, Spec: sympoziumv1alpha1.CellnExecutionPolicySpec{
		NamespaceSelector: cellnplatform.OpenSelector(cellnplatform.SystemNamespaces("sympozium-system")),
		RuntimeProfiles:   []sympoziumv1alpha1.CellnExecutionPolicyRuntime{{Ref: sympoziumv1alpha1.CellnRuntimeProfileRef{Name: mediated.Name, Revision: "v1"}}, {Ref: sympoziumv1alpha1.CellnRuntimeProfileRef{Name: unlabeled.Name, Revision: "v1"}}},
		Ceilings:          sympoziumv1alpha1.CellnExecutionPolicyCeilings{MaxTurns: 256, MaxModelRequests: 1536, MaxOutputTokens: 786432, MaxParentLeaseSeconds: 86400, MaxTurnSeconds: 60},
	}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mediated, unlabeled, policy, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a", Labels: map[string]string{cellnplatform.NamespaceNameLabel: "team-a"}}}).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	res := httptest.NewRecorder()
	srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/celln-platform/profiles?namespace=team-a", nil))
	var out []CellnPlatformProfile
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &out) != nil {
		t.Fatalf("status %d: %s", res.Code, res.Body.String())
	}
	if len(out) != 2 {
		t.Fatalf("profiles: %+v", out)
	}
	if other := out[1]; other.Name != unlabeled.Name || !other.MediationOnly || other.Provider != "" || other.Wrapper != "celln-other" {
		t.Fatalf("unlabeled profile: %+v", other)
	}
	p := out[0]
	if p.Name != mediated.Name || !p.MediationOnly || p.Agent != "" || p.Provider != "" || p.Model != "" || p.Wrapper != "celln-native" || p.SessionDefaults.MaxTurns == 0 {
		t.Fatalf("mediation-only profile: %+v", p)
	}
}

// A backend's toolbox is offered with the backend's own profile, not as a
// profile of its own: what an Agent with its own key selects to lend the
// starter toolbox (the toolbox profile, its wrapper and exactly its tools in
// closure order). Asked for with runtimeOnly, the namespace gets that wrapper.
func TestCellnPlatformOffersEachBackendsToolboxWithItsProfile(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = sympoziumv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	template := apiextensionsv1.JSON{Raw: []byte(`{"model":"deepseek-chat","url":"https://api.deepseek.com/chat/completions"}`)}
	base := &sympoziumv1alpha1.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-trial"}, Spec: sympoziumv1alpha1.CellnRuntimeProfileSpec{Revision: "v1", Native: &sympoziumv1alpha1.CellnNativeProvisioning{SystemPrompt: "host persona", Template: template}}}
	ordered := []sympoziumv1alpha1.ClusterCellnToolRef{{Name: "celln-trial-workspace-read", Revision: "v1"}, {Name: "celln-trial-grep", Revision: "v1"}}
	toolbox := base.DeepCopy()
	toolbox.Name = cellnplatform.ToolboxProfileName(base.Name)
	toolbox.Labels = map[string]string{cellnplatform.ToolboxLabel: "true"}
	toolbox.Annotations = map[string]string{cellnplatform.ToolboxToolsAnnotation: cellnplatform.ToolboxAnnotation(ordered)}
	policy := &sympoziumv1alpha1.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "celln-fleet-trial"}, Spec: sympoziumv1alpha1.CellnExecutionPolicySpec{
		NamespaceSelector: cellnplatform.OpenSelector(cellnplatform.SystemNamespaces("sympozium-system")),
		RuntimeProfiles:   []sympoziumv1alpha1.CellnExecutionPolicyRuntime{{Ref: sympoziumv1alpha1.CellnRuntimeProfileRef{Name: base.Name, Revision: "v1"}}, {Ref: sympoziumv1alpha1.CellnRuntimeProfileRef{Name: toolbox.Name, Revision: "v1"}}},
		// The policy lends them in another order; the toolbox's is the one.
		Tools:    []sympoziumv1alpha1.CellnExecutionPolicyTool{{Ref: ordered[1]}, {Ref: ordered[0]}},
		Ceilings: sympoziumv1alpha1.CellnExecutionPolicyCeilings{MaxTurns: 256, MaxModelRequests: 1536, MaxOutputTokens: 786432, MaxParentLeaseSeconds: 86400, MaxTurnSeconds: 60},
	}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(base, toolbox, policy, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a", Labels: map[string]string{cellnplatform.NamespaceNameLabel: "team-a"}}}).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	res := httptest.NewRecorder()
	srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/celln-platform/profiles?namespace=team-a", nil))
	var got []CellnPlatformProfile
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &got) != nil {
		t.Fatalf("profiles: %d %s", res.Code, res.Body.String())
	}
	if len(got) != 1 || got[0].Name != base.Name || got[0].Wrapper != "celln-native" || got[0].ToolboxProfile != toolbox.Name || got[0].ToolboxWrapper != "celln-native.toolbox" || len(got[0].ToolboxTools) != 2 || got[0].ToolboxTools[0] != ordered[0] || got[0].ToolboxTools[1] != ordered[1] {
		t.Fatalf("toolbox not offered with its backend: %+v", got)
	}
	post := func(body string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/celln-platform/wrappers?namespace=team-a", strings.NewReader(body)))
		return res
	}
	var wrappers cellnplatform.Wrappers
	if res := post(`{"profile":"celln-native-trial.toolbox","runtimeOnly":true}`); res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &wrappers) != nil || wrappers.Runtime != "celln-native.toolbox" || len(wrappers.Created) != 1 {
		t.Fatalf("toolbox runtime wrapper: %d %s", res.Code, res.Body.String())
	}
	var wrapper sympoziumv1alpha1.AgentRuntime
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "team-a", Name: "celln-native.toolbox"}, &wrapper); err != nil || wrapper.Spec.CellnProfileRef.Name != toolbox.Name {
		t.Fatalf("toolbox wrapper: %v %+v", err, wrapper.Spec)
	}
	// No profile has a shared Agent or connection: asking for the full wrapper
	// set yields the same runtime wrapper and nothing else.
	if res := post(`{"profile":"celln-native-trial.toolbox"}`); res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &wrappers) != nil || wrappers.Agent != "" || wrappers.Connection != "" || wrappers.Runtime != "celln-native.toolbox" {
		t.Fatalf("toolbox offered more than its runtime wrapper: %d %s", res.Code, res.Body.String())
	}
}

// A model-free profile (a node that calls no model: its template names only
// the scoped alias and the policy carries no route of its own for it) is
// still offered to the namespace as mediation-only. The Agent's connection is
// the route, so the entry names no provider, model or endpoint.
func TestCellnPlatformListsModelFreeProfileWithoutARoute(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = sympoziumv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	template := apiextensionsv1.JSON{Raw: []byte(`{"model":"mediated","url":"https://celln-model.invalid/v1/invoke"}`)}
	modelFree := &sympoziumv1alpha1.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-trial", Labels: map[string]string{cellnplatform.MediationOnlyLabel: "true"}}, Spec: sympoziumv1alpha1.CellnRuntimeProfileSpec{Revision: "v1", Native: &sympoziumv1alpha1.CellnNativeProvisioning{SystemPrompt: "host persona", Template: template}}}
	policy := &sympoziumv1alpha1.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "celln-fleet-trial"}, Spec: sympoziumv1alpha1.CellnExecutionPolicySpec{
		NamespaceSelector: cellnplatform.OpenSelector(cellnplatform.SystemNamespaces("sympozium-system")),
		RuntimeProfiles:   []sympoziumv1alpha1.CellnExecutionPolicyRuntime{{Ref: sympoziumv1alpha1.CellnRuntimeProfileRef{Name: modelFree.Name, Revision: "v1"}}},
		Ceilings:          sympoziumv1alpha1.CellnExecutionPolicyCeilings{MaxTurns: 256, MaxModelRequests: 1536, MaxOutputTokens: 786432, MaxParentLeaseSeconds: 86400, MaxTurnSeconds: 300},
	}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(modelFree, policy, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a", Labels: map[string]string{cellnplatform.NamespaceNameLabel: "team-a"}}}).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	res := httptest.NewRecorder()
	srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/celln-platform/profiles?namespace=team-a", nil))
	var out []CellnPlatformProfile
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &out) != nil {
		t.Fatalf("status %d: %s", res.Code, res.Body.String())
	}
	if len(out) != 1 {
		t.Fatalf("a model-free profile must be offered: %+v", out)
	}
	p := out[0]
	if !p.MediationOnly || p.Agent != "" || p.Provider != "" || p.Model != "" || p.Endpoint != "" || p.Wrapper != "celln-native" || p.Policy != policy.Name || p.Ceilings.MaxTurns != 256 || p.SessionDefaults.MaxTurns == 0 {
		t.Fatalf("model-free profile entry: %+v", p)
	}
}
