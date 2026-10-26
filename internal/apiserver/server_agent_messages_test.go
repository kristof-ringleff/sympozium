package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func postAgentMessage(srv *Server, ns, agent, body string) *httptest.ResponseRecorder {
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agent+"/messages?namespace="+ns, strings.NewReader(body))
	req.SetPathValue("name", agent)
	srv.postAgentMessage(res, req)
	return res
}

// liveConversation is an enduring Celln run whose scoped parent is ready for
// more turns.
func liveConversation(name, agent string, turns int32) *api.AgentRun {
	run := &api.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: name, UID: types.UID("uid-" + name), Generation: 1, Labels: map[string]string{"sympozium.ai/instance": agent}},
		Spec: api.AgentRunSpec{
			AgentRef: agent, AgentID: "primary", Backend: "celln", ExecutionLifecycle: "enduring",
			Task:           api.NewStringTask("hello"),
			CellnSelection: &api.CellnCatalogueSelection{RuntimeRef: "celln-native", ToolRefs: []api.CellnCatalogueToolRef{}},
			Enduring:       &api.EnduringRunSpec{LeaseSeconds: 600, MaxTurns: turns, MaxModelRequests: 12, MaxOutputTokens: 4096},
		},
		Status: api.AgentRunStatus{
			Phase:       api.AgentRunPhaseRunning,
			CellnScoped: &api.CellnScopedStatus{StartAttempted: true, ParentIncarnation: "inc-1", NativePhase: "Running"},
			Conditions:  []metav1.Condition{{Type: "CellnScopedExecution", Status: metav1.ConditionTrue, Reason: "EnduringParentReady", ObservedGeneration: 1}},
		},
	}
	return run
}

// A message to an Agent with no live conversation starts one from the
// Agent's own defaults; a message to an Agent in a conversation continues it,
// idempotently per request ID; a conversation of another Agent is not reachable.
func TestPostAgentMessage(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	idle := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "idle"}, Spec: api.AgentSpec{
		AuthRefs: []api.SecretRef{{Provider: "anthropic", Secret: "idle-key"}},
		Agents:   api.AgentsSpec{Default: api.AgentConfig{Model: "claude"}},
	}}
	busy := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "busy"}, Spec: api.AgentSpec{RuntimeRef: "celln-native"}}
	other := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "other"}, Spec: api.AgentSpec{RuntimeRef: "celln-native"}}
	finished := liveConversation("busy-old", "busy", 4)
	finished.Status.Phase = api.AgentRunPhaseSucceeded
	finished.CreationTimestamp = metav1.Unix(1000, 0)
	current := liveConversation("busy-now", "busy", 4)
	current.CreationTimestamp = metav1.Unix(2000, 0)
	theirs := liveConversation("other-now", "other", 4)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(idle, busy, other, finished, current, theirs).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())

	// Starts a conversation: the run names the Agent and carries the message.
	res := postAgentMessage(srv, "team-a", "idle", `{"message":"Reply with pong."}`)
	var started AgentMessageResponse
	if res.Code != http.StatusCreated || json.Unmarshal(res.Body.Bytes(), &started) != nil || !started.Started || started.Turn != nil || started.Run == nil {
		t.Fatalf("start: %d %s", res.Code, res.Body.String())
	}
	if started.Run.Spec.AgentRef != "idle" || started.Run.Spec.Task.GetPrompt() != "Reply with pong." || started.Run.Spec.Model.Provider != "anthropic" || started.Run.Spec.Model.AuthSecretRef != "idle-key" {
		t.Fatalf("run not built from the Agent: %+v", started.Run.Spec)
	}
	var runs api.AgentRunList
	if err := cl.List(t.Context(), &runs, client.InNamespace("team-a"), client.MatchingLabels{"sympozium.ai/instance": "idle"}); err != nil || len(runs.Items) != 1 {
		t.Fatalf("runs for idle: %v %d", err, len(runs.Items))
	}

	// Continues the newest live conversation, not the finished one.
	res = postAgentMessage(srv, "team-a", "busy", `{"message":"and then?","requestId":"req-1"}`)
	var continued AgentMessageResponse
	if res.Code != http.StatusAccepted || json.Unmarshal(res.Body.Bytes(), &continued) != nil || continued.Started || continued.Turn == nil || continued.Run == nil || continued.Run.Name != "busy-now" {
		t.Fatalf("continue: %d %s", res.Code, res.Body.String())
	}
	if continued.Turn.Spec.RunName != "busy-now" || continued.Turn.Spec.RunUID != string(current.UID) || continued.Turn.Spec.Message != "and then?" {
		t.Fatalf("turn: %+v", continued.Turn.Spec)
	}
	// The same request observes its original turn.
	again := postAgentMessage(srv, "team-a", "busy", `{"message":"and then?","requestId":"req-1"}`)
	var repeated AgentMessageResponse
	if again.Code != http.StatusOK || json.Unmarshal(again.Body.Bytes(), &repeated) != nil || repeated.Turn == nil || repeated.Turn.Name != continued.Turn.Name {
		t.Fatalf("repeat: %d %s", again.Code, again.Body.String())
	}
	// Without a request ID every message is a new turn.
	if res := postAgentMessage(srv, "team-a", "busy", `{"message":"more"}`); res.Code != http.StatusAccepted {
		t.Fatalf("new turn: %d %s", res.Code, res.Body.String())
	}
	// Naming the conversation works for the Agent's own run only.
	if res := postAgentMessage(srv, "team-a", "busy", `{"message":"named","conversation":"busy-now"}`); res.Code != http.StatusAccepted {
		t.Fatalf("named conversation: %d %s", res.Code, res.Body.String())
	}
	if res := postAgentMessage(srv, "team-a", "busy", `{"message":"theirs","conversation":"other-now"}`); res.Code != http.StatusNotFound {
		t.Fatalf("another Agent's conversation reached: %d %s", res.Code, res.Body.String())
	}
	// Three turns after the initial message spend a four-turn conversation.
	if res := postAgentMessage(srv, "team-a", "busy", `{"message":"one too many"}`); res.Code != http.StatusConflict {
		t.Fatalf("exhausted conversation: %d %s", res.Code, res.Body.String())
	}
	var turns api.AgentRunTurnList
	if err := cl.List(t.Context(), &turns, client.InNamespace("team-a")); err != nil || len(turns.Items) != 3 {
		t.Fatalf("turns: %v %d", err, len(turns.Items))
	}

	for body, code := range map[string]int{
		`{"message":""}`: http.StatusBadRequest,
		`{"message":"` + strings.Repeat("x", 2049) + `"}`: http.StatusBadRequest,
		`{"message":"hi","extra":true}`:                   http.StatusBadRequest,
		`{"message":"hi","requestId":"bad id"}`:           http.StatusBadRequest,
		`not json`:                                        http.StatusBadRequest,
	} {
		if res := postAgentMessage(srv, "team-a", "busy", body); res.Code != code {
			t.Fatalf("%s: %d, want %d", body, res.Code, code)
		}
	}
	if res := postAgentMessage(srv, "team-a", "nobody", `{"message":"hi"}`); res.Code != http.StatusNotFound {
		t.Fatalf("unknown agent: %d", res.Code)
	}
}
