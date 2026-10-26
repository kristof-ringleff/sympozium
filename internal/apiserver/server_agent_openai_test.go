package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func chatCompletions(srv *Server, ns, agent, body string, header map[string]string) *httptest.ResponseRecorder {
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agent+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Sympozium-Namespace", ns)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	req.SetPathValue("name", agent)
	srv.agentChatCompletions(res, req)
	return res
}

// answerTurns completes, as the controller would, every turn that appears in
// the namespace with the given answer, until the test ends.
func answerTurns(t *testing.T, cl client.Client, ns, answer string, succeeded bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		for ctx.Err() == nil {
			var turns api.AgentRunTurnList
			if cl.List(ctx, &turns, client.InNamespace(ns)) == nil {
				for i := range turns.Items {
					turn := &turns.Items[i]
					if turn.Status.Execution != nil {
						continue
					}
					turn.Status.Execution = &api.CellnParentTurnStatus{ID: "t", Message: turn.Spec.Message, Child: "blake3:" + strings.Repeat("0", 64), Attempted: true, Result: &api.CellnParentTurnResult{Succeeded: succeeded, Answer: answer}}
					_ = cl.Update(ctx, turn)
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
}

func openAIFixture(t *testing.T) (*Server, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	idle := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "idle", CreationTimestamp: metav1.Unix(1700000000, 0)}, Spec: api.AgentSpec{
		AuthRefs: []api.SecretRef{{Provider: "anthropic", Secret: "idle-key"}},
		Agents:   api.AgentsSpec{Default: api.AgentConfig{Model: "claude"}},
	}}
	busy := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "busy"}, Spec: api.AgentSpec{RuntimeRef: "celln-native", Execution: &api.AgentExecutionDefaults{Backend: "celln", ExecutionLifecycle: "enduring"}}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(idle, busy, liveConversation("busy-now", "busy", 8)).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	srv.answerPoll = 2 * time.Millisecond
	return srv, cl
}

// The newest user message continues the Agent's live conversation as a turn
// and the completion carries the turn's answer in the OpenAI shape, with the
// run and turn named for the caller.
func TestChatCompletionsContinuesTheConversation(t *testing.T) {
	srv, cl := openAIFixture(t)
	answerTurns(t, cl, "team-a", "pong", true)
	res := chatCompletions(srv, "team-a", "busy", `{"model":"anything","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"ping"},{"role":"assistant","content":"pong"},{"role":"user","content":[{"type":"text","text":"and"},{"type":"text","text":"again?"}]}],"temperature":0.2}`, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("completion: %d %s", res.Code, res.Body.String())
	}
	var got openAIChatCompletion
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Object != "chat.completion" || got.Model != "busy" || len(got.Choices) != 1 || got.Choices[0].Message == nil || got.Choices[0].Message.Content != "pong" || got.Choices[0].Message.Role != "assistant" || got.Choices[0].FinishReason == nil || *got.Choices[0].FinishReason != "stop" {
		t.Fatalf("completion shape: %s", res.Body.String())
	}
	if got.Sympozium == nil || got.Sympozium.Run != "busy-now" || got.Sympozium.Turn == "" || got.Sympozium.Started || got.ID != "chatcmpl-"+got.Sympozium.Turn {
		t.Fatalf("provenance: %+v id=%s", got.Sympozium, got.ID)
	}
	if res.Header().Get("X-Sympozium-Run") != "busy-now" || res.Header().Get("X-Sympozium-Turn") != got.Sympozium.Turn {
		t.Fatalf("headers: %v", res.Header())
	}
	var turns api.AgentRunTurnList
	if err := cl.List(t.Context(), &turns, client.InNamespace("team-a")); err != nil || len(turns.Items) != 1 || turns.Items[0].Spec.Message != "and\nagain?" {
		t.Fatalf("turn from the last user message: %v %+v", err, turns.Items)
	}
}

// stream:true answers in SSE chunks: the role first, the answer, a stop, and
// [DONE]; the chunks carry the same provenance.
func TestChatCompletionsStreams(t *testing.T) {
	srv, cl := openAIFixture(t)
	answerTurns(t, cl, "team-a", "pong", true)
	res := chatCompletions(srv, "team-a", "busy", `{"messages":[{"role":"user","content":"ping"}],"stream":true}`, nil)
	if res.Code != http.StatusOK || !strings.HasPrefix(res.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream: %d %v", res.Code, res.Header())
	}
	var chunks []openAIChatCompletion
	var done bool
	for _, line := range strings.Split(res.Body.String(), "\n") {
		if line == "data: [DONE]" {
			done = true
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var chunk openAIChatCompletion
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatalf("chunk %q: %v", line, err)
		}
		chunks = append(chunks, chunk)
	}
	if !done || len(chunks) != 3 {
		t.Fatalf("frames: done=%v %d\n%s", done, len(chunks), res.Body.String())
	}
	if chunks[0].Object != "chat.completion.chunk" || chunks[0].Choices[0].Delta.Role != "assistant" || chunks[1].Choices[0].Delta.Content != "pong" || chunks[2].Choices[0].FinishReason == nil || *chunks[2].Choices[0].FinishReason != "stop" || chunks[1].Sympozium.Run != "busy-now" {
		t.Fatalf("chunks: %s", res.Body.String())
	}
}

// A turn that ends without success is an error the SDK raises, carrying the
// Agent's reason; a turn still running at the deadline is 504 with the run and
// turn to read, and the conversation keeps going.
func TestChatCompletionsReportsFailureAndTimeout(t *testing.T) {
	srv, cl := openAIFixture(t)
	timeoutRes := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/busy/v1/chat/completions?namespace=team-a&timeout=30ms", strings.NewReader(`{"messages":[{"role":"user","content":"slow"}]}`))
	req.SetPathValue("name", "busy")
	srv.agentChatCompletions(timeoutRes, req)
	var timeoutBody openAIErrorBody
	if timeoutRes.Code != http.StatusGatewayTimeout || json.Unmarshal(timeoutRes.Body.Bytes(), &timeoutBody) != nil || timeoutBody.Error.Code != "answer_pending" || !strings.Contains(timeoutBody.Error.Message, "busy-now") || timeoutRes.Header().Get("X-Sympozium-Turn") == "" {
		t.Fatalf("timeout: %d %s %v", timeoutRes.Code, timeoutRes.Body.String(), timeoutRes.Header())
	}
	var turns api.AgentRunTurnList
	if err := cl.List(t.Context(), &turns, client.InNamespace("team-a")); err != nil || len(turns.Items) != 1 {
		t.Fatalf("the timed-out turn still exists: %v %d", err, len(turns.Items))
	}

	answerTurns(t, cl, "team-a", "the model refused: budget exhausted", false)
	res := chatCompletions(srv, "team-a", "busy", `{"messages":[{"role":"user","content":"ping"}]}`, nil)
	var body openAIErrorBody
	if res.Code != http.StatusBadGateway || json.Unmarshal(res.Body.Bytes(), &body) != nil || body.Error.Code != "turn_failed" || body.Error.Message != "the model refused: budget exhausted" || body.Error.Type != "server_error" {
		t.Fatalf("failed turn: %d %s", res.Code, res.Body.String())
	}
}

// An Agent with no live conversation gets one started from its own defaults
// and the completion waits for the run's result.
func TestChatCompletionsStartsAConversation(t *testing.T) {
	srv, cl := openAIFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		for ctx.Err() == nil {
			var runs api.AgentRunList
			if cl.List(ctx, &runs, client.InNamespace("team-a"), client.MatchingLabels{"sympozium.ai/instance": "idle"}) == nil {
				for i := range runs.Items {
					if runs.Items[i].Status.Result == "" {
						runs.Items[i].Status.Phase = api.AgentRunPhaseSucceeded
						runs.Items[i].Status.Result = "hello from a fresh run"
						_ = cl.Update(ctx, &runs.Items[i])
					}
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	res := chatCompletions(srv, "team-a", "idle", `{"messages":[{"role":"user","content":"hello?"}]}`, nil)
	var got openAIChatCompletion
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &got) != nil || got.Choices[0].Message.Content != "hello from a fresh run" || !got.Sympozium.Started || got.Sympozium.Turn != "" || got.ID != "chatcmpl-"+got.Sympozium.Run {
		t.Fatalf("started: %d %s", res.Code, res.Body.String())
	}
	var runs api.AgentRunList
	if err := cl.List(t.Context(), &runs, client.InNamespace("team-a"), client.MatchingLabels{"sympozium.ai/instance": "idle"}); err != nil || len(runs.Items) != 1 || runs.Items[0].Spec.Task.GetPrompt() != "hello?" {
		t.Fatalf("run from the message: %v %+v", err, runs.Items)
	}
}

// A Kubernetes Agent on a session runtime (Pi, Hermes) is proxied to its
// running session unchanged, streaming included, through the fixed Service
// target; a session that is not ready is a conflict the SDK can show.
func TestChatCompletionsProxiesASessionAgent(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "pi-agent"}, Spec: api.AgentSpec{RuntimeRef: "pi"}}
	runtimeObj := &api.AgentRuntime{ObjectMeta: metav1.ObjectMeta{Name: "pi", Namespace: "team-a"}, Spec: api.AgentRuntimeSpec{ContractVersion: "v1alpha2", Session: &api.AgentRuntimeSession{Protocol: "openai-chat", Port: 8080}}}
	session := &api.HarnessSession{
		ObjectMeta: metav1.ObjectMeta{Name: "pi-agent-chat", Namespace: "team-a"},
		Spec:       api.HarnessSessionSpec{AgentRef: "pi-agent", RuntimeRef: "pi", DesiredState: "running"},
		Status:     api.HarnessSessionStatus{Phase: "Ready", ServiceName: "pi-agent-chat"},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(session).WithObjects(agent, runtimeObj, session).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	var target string
	var forwarded []byte
	previous := harnessSessionProxyClient
	harnessSessionProxyClient = &http.Client{Transport: harnessRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		target = request.URL.String()
		forwarded, _ = io.ReadAll(request.Body)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewBufferString(`{"id":"from-pi","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))}, nil
	})}
	t.Cleanup(func() { harnessSessionProxyClient = previous })

	body := `{"model":"pi","messages":[{"role":"system","content":"x"},{"role":"user","content":"hello"}],"stream":false}`
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/pi-agent/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("X-Sympozium-Namespace", "team-a")
	srv.Handler(nil).ServeHTTP(res, req)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"from-pi"`) || res.Header().Get("X-Sympozium-Request-ID") == "" {
		t.Fatalf("proxied: %d %s %v", res.Code, res.Body.String(), res.Header())
	}
	if target != "http://pi-agent-chat.team-a.svc:8080/v1/chat/completions" || string(forwarded) != body {
		t.Fatalf("target %q body %s", target, forwarded)
	}

	var current api.HarnessSession
	if err := cl.Get(t.Context(), client.ObjectKey{Namespace: "team-a", Name: "pi-agent-chat"}, &current); err != nil {
		t.Fatal(err)
	}
	current.Status.Phase = "Pending"
	if err := cl.Status().Update(t.Context(), &current); err != nil {
		t.Fatal(err)
	}
	res = chatCompletions(srv, "team-a", "pi-agent", body, nil)
	var errBody openAIErrorBody
	if res.Code != http.StatusConflict || json.Unmarshal(res.Body.Bytes(), &errBody) != nil || errBody.Error.Code != "session_not_ready" {
		t.Fatalf("not ready: %d %s", res.Code, res.Body.String())
	}
}

// The Agent is the one model its endpoint lists; bad requests are OpenAI
// errors.
func TestChatCompletionsModelsAndRefusals(t *testing.T) {
	srv, _ := openAIFixture(t)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/idle/v1/models?namespace=team-a", nil)
	req.SetPathValue("name", "idle")
	srv.agentModels(res, req)
	var models struct {
		Object string        `json:"object"`
		Data   []openAIModel `json:"data"`
	}
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &models) != nil || models.Object != "list" || len(models.Data) != 1 || models.Data[0].ID != "idle" || models.Data[0].Created != 1700000000 || models.Data[0].OwnedBy != "team-a" {
		t.Fatalf("models: %d %s", res.Code, res.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/agents/nobody/v1/models?namespace=team-a", nil)
	req.SetPathValue("name", "nobody")
	res = httptest.NewRecorder()
	srv.agentModels(res, req)
	if res.Code != http.StatusNotFound || !strings.Contains(res.Body.String(), `"not_found_error"`) {
		t.Fatalf("unknown agent models: %d %s", res.Code, res.Body.String())
	}

	for body, want := range map[string]string{
		`{"messages":[{"role":"system","content":"only"}]}`:                            "invalid_messages",
		`{"messages":[{"role":"user","content":[{"type":"image_url"}]}]}`:              "invalid_messages",
		`{"messages":[{"role":"user","content":"` + strings.Repeat("x", 2049) + `"}]}`: "invalid_messages",
		`{"messages":[{"role":"user","content":""}]}`:                                  "invalid_messages",
		`not json`: "invalid_json",
	} {
		res := chatCompletions(srv, "team-a", "busy", body, nil)
		var got openAIErrorBody
		if res.Code != http.StatusBadRequest || json.Unmarshal(res.Body.Bytes(), &got) != nil || got.Error.Code != want {
			t.Fatalf("%s: %d %s", body, res.Code, res.Body.String())
		}
	}
	res = chatCompletions(srv, "team-a", "nobody", `{"messages":[{"role":"user","content":"hi"}]}`, nil)
	if res.Code != http.StatusNotFound || !strings.Contains(res.Body.String(), "agent_not_found") {
		t.Fatalf("unknown agent: %d %s", res.Code, res.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/v1/agents/busy/v1/chat/completions?namespace=team-a&timeout=1h", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	req.SetPathValue("name", "busy")
	res = httptest.NewRecorder()
	srv.agentChatCompletions(res, req)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "invalid_timeout") {
		t.Fatalf("timeout bound: %d %s", res.Code, res.Body.String())
	}
	// Another Agent's conversation is not reachable by name.
	res = chatCompletions(srv, "team-a", "idle", `{"messages":[{"role":"user","content":"hi"}]}`, map[string]string{"X-Sympozium-Conversation": "busy-now"})
	if res.Code != http.StatusNotFound || !strings.Contains(res.Body.String(), "message_refused") {
		t.Fatalf("foreign conversation: %d %s", res.Code, res.Body.String())
	}
}

// A message to a conversation whose parent is still being admitted waits for
// it instead of failing, within the request's timeout; one that never becomes
// ready is 504 with the refusal reason.
func TestChatCompletionsWaitsForTheParent(t *testing.T) {
	srv, cl := openAIFixture(t)
	starting := liveConversation("busy-starting", "busy", 8)
	starting.CreationTimestamp = metav1.Unix(3000, 0)
	starting.Status = api.AgentRunStatus{Phase: api.AgentRunPhasePending}
	if err := cl.Create(t.Context(), starting); err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/busy/v1/chat/completions?namespace=team-a&timeout=40ms", strings.NewReader(`{"messages":[{"role":"user","content":"early"}]}`))
	req.SetPathValue("name", "busy")
	srv.agentChatCompletions(res, req)
	var body openAIErrorBody
	if res.Code != http.StatusGatewayTimeout || json.Unmarshal(res.Body.Bytes(), &body) != nil || body.Error.Code != "conversation_busy" || !strings.Contains(body.Error.Message, "not started yet") || res.Header().Get("X-Sympozium-Turn-Refusal") != "ParentNotAdmitted" {
		t.Fatalf("never ready: %d %s %v", res.Code, res.Body.String(), res.Header())
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		time.Sleep(10 * time.Millisecond)
		var run api.AgentRun
		if cl.Get(ctx, client.ObjectKey{Namespace: "team-a", Name: "busy-starting"}, &run) == nil {
			run.Status = liveConversation("busy-starting", "busy", 8).Status
			_ = cl.Update(ctx, &run)
		}
	}()
	answerTurns(t, cl, "team-a", "ready now", true)
	res = chatCompletions(srv, "team-a", "busy", `{"messages":[{"role":"user","content":"patient"}]}`, nil)
	var got openAIChatCompletion
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &got) != nil || got.Choices[0].Message.Content != "ready now" || got.Sympozium.Run != "busy-starting" || res.Header().Get("X-Sympozium-Turn-Refusal") != "" {
		t.Fatalf("waited: %d %s %v", res.Code, res.Body.String(), res.Header())
	}
}

// A run or turn the cache has not seen yet is pending, not gone, for a grace
// period after the message was sent.
func TestReadAgentAnswerToleratesCacheLag(t *testing.T) {
	srv, _ := openAIFixture(t)
	ghost := &AgentMessageResponse{Run: &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "ghost"}}, Started: true}
	if _, done, err := srv.readAgentAnswer(t.Context(), "team-a", ghost, time.Now()); err != nil || done {
		t.Fatalf("fresh: done=%v err=%v", done, err)
	}
	if _, _, err := srv.readAgentAnswer(t.Context(), "team-a", ghost, time.Now().Add(-agentChatCacheGrace-time.Second)); err == nil {
		t.Fatal("a run missing past the grace period is not an error")
	}
	ghostTurn := &AgentMessageResponse{Run: ghost.Run, Turn: &api.AgentRunTurn{ObjectMeta: metav1.ObjectMeta{Name: "ghost-turn"}}}
	if _, done, err := srv.readAgentAnswer(t.Context(), "team-a", ghostTurn, time.Now()); err != nil || done {
		t.Fatalf("fresh turn: done=%v err=%v", done, err)
	}
}

// The namespace header reaches every API route, not only the OpenAI ones: an
// Agent read with it is found in that namespace; a query parameter wins.
func TestNamespaceHeaderAppliesToAllRoutes(t *testing.T) {
	srv, _ := openAIFixture(t)
	handler := srv.Handler(nil)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/busy", nil)
	req.Header.Set("X-Sympozium-Namespace", "team-a")
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("header namespace: %d %s", res.Code, res.Body.String())
	}
	res = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/agents/busy?namespace=elsewhere", nil)
	req.Header.Set("X-Sympozium-Namespace", "team-a")
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("query parameter must win: %d %s", res.Code, res.Body.String())
	}
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/agents/busy", nil))
	if res.Code != http.StatusNotFound {
		t.Fatalf("no namespace means default: %d", res.Code)
	}
}

func TestAgentChatTimeout(t *testing.T) {
	for raw, want := range map[string]time.Duration{"": agentChatDefaultTimeout, "90s": 90 * time.Second, "2m": 2 * time.Minute, "45": 45 * time.Second} {
		if got, err := agentChatTimeout(raw); err != nil || got != want {
			t.Fatalf("%q: %v %v", raw, got, err)
		}
	}
	for _, raw := range []string{"0", "-5s", "11m", "soon"} {
		if _, err := agentChatTimeout(raw); err == nil {
			t.Fatalf("%q accepted", raw)
		}
	}
}
