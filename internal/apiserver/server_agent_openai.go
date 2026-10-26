package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnparent"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// An Agent as an OpenAI-compatible endpoint. Point any OpenAI SDK at
//
//	base URL  <apiserver>/api/v1/agents/{name}/v1
//	API key   the Sympozium token
//	header    X-Sympozium-Namespace: <namespace>   (unless default)
//
// and POST /chat/completions talks to the Agent; GET /models lists it as the
// one model. The last user message is the message to the Agent: an Agent holds
// its own conversation, so earlier messages are not replayed. A Celln Agent
// answers from its live conversation (or starts one) and the request waits
// for the answer, streamed as one chunk when stream is true; a Kubernetes
// Agent with a session runtime (Pi, Hermes: openai-chat) is proxied to its
// running session, which speaks this protocol natively; any other Agent runs
// the message as a run and waits for its result.

const (
	// agentChatDefaultTimeout is how long a chat completion waits for the
	// Agent's answer before 504; the conversation continues regardless.
	agentChatDefaultTimeout = 3 * time.Minute
	agentChatMaxTimeout     = 10 * time.Minute
	agentChatPollInterval   = 250 * time.Millisecond
	agentChatKeepAlive      = 15 * time.Second
	// agentChatMaxBody allows the history OpenAI clients resend.
	agentChatMaxBody = 1 << 20
)

type openAIChatRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Stream   bool            `json:"stream"`
}

type openAIMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type openAIReply struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content"`
}

type openAIChoice struct {
	Index        int          `json:"index"`
	Message      *openAIReply `json:"message,omitempty"`
	Delta        *openAIReply `json:"delta,omitempty"`
	FinishReason *string      `json:"finish_reason"`
}

// openAIProvenance names the Sympozium objects behind a completion, so a
// caller can find the run and turn the SDK response came from.
type openAIProvenance struct {
	Namespace string `json:"namespace"`
	Agent     string `json:"agent"`
	Run       string `json:"run"`
	Turn      string `json:"turn,omitempty"`
	// Started reports that this message began the conversation.
	Started bool `json:"started"`
}

type openAIChatCompletion struct {
	ID        string            `json:"id"`
	Object    string            `json:"object"`
	Created   int64             `json:"created"`
	Model     string            `json:"model"`
	Choices   []openAIChoice    `json:"choices"`
	Sympozium *openAIProvenance `json:"sympozium,omitempty"`
}

type openAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type openAIErrorBody struct {
	Error openAIError `json:"error"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// writeOpenAIError answers in the error shape OpenAI SDKs raise from.
func writeOpenAIError(w http.ResponseWriter, status int, code, message string) {
	typ := "invalid_request_error"
	switch {
	case status == http.StatusNotFound:
		typ = "not_found_error"
	case status == http.StatusConflict:
		typ = "conflict_error"
	case status == http.StatusGatewayTimeout:
		typ = "timeout_error"
	case status >= 500:
		typ = "server_error"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(openAIErrorBody{Error: openAIError{Message: message, Type: typ, Code: code}})
}

// GET /api/v1/agents/{name}/v1/models: the Agent is the one model.
func (s *Server) agentModels(w http.ResponseWriter, r *http.Request) {
	ns, name := requestNamespace(r), r.PathValue("name")
	var agent api.Agent
	if err := s.client.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: name}, &agent); err != nil {
		if k8serrors.IsNotFound(err) {
			writeOpenAIError(w, http.StatusNotFound, "agent_not_found", fmt.Sprintf("agent %q not found in namespace %q", name, ns))
			return
		}
		writeOpenAIError(w, http.StatusInternalServerError, "agent_unavailable", err.Error())
		return
	}
	writeJSON(w, map[string]any{"object": "list", "data": []openAIModel{{ID: agent.Name, Object: "model", Created: agent.CreationTimestamp.Unix(), OwnedBy: ns}}})
}

// POST /api/v1/agents/{name}/v1/chat/completions.
func (s *Server) agentChatCompletions(w http.ResponseWriter, r *http.Request) {
	ns, name := requestNamespace(r), r.PathValue("name")
	var agent api.Agent
	if err := s.client.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: name}, &agent); err != nil {
		if k8serrors.IsNotFound(err) {
			writeOpenAIError(w, http.StatusNotFound, "agent_not_found", fmt.Sprintf("agent %q not found in namespace %q", name, ns))
			return
		}
		writeOpenAIError(w, http.StatusInternalServerError, "agent_unavailable", err.Error())
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, agentChatMaxBody))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_body", "request body unreadable or over 1 MiB: "+err.Error())
		return
	}
	var req openAIChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_json", "chat completion request must be JSON: "+err.Error())
		return
	}
	message, err := lastUserMessage(req.Messages)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_messages", err.Error())
		return
	}
	timeout, err := agentChatTimeout(r.URL.Query().Get("timeout"))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_timeout", err.Error())
		return
	}

	// A session runtime speaks this protocol itself: hand the request to the
	// Agent's running session, history and streaming included.
	celln := agent.Spec.Execution != nil && agent.Spec.Execution.Backend == "celln"
	if !celln && agent.Spec.RuntimeRef != "" {
		var runtime api.AgentRuntime
		if err := s.client.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: agent.Spec.RuntimeRef}, &runtime); err == nil && runtime.Spec.ContractVersion == "v1alpha2" && runtime.Spec.Session != nil && runtime.Spec.Session.Protocol == "openai-chat" {
			session := defaultHarnessSessionName(name)
			var live api.HarnessSession
			if err := s.client.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: session}, &live); err != nil {
				writeOpenAIError(w, http.StatusConflict, "session_missing", fmt.Sprintf("agent %q has no conversation session %q; create the Agent with its session runtime or start the session", name, session))
				return
			}
			if live.Spec.DesiredState != "running" || live.Status.Phase != "Ready" {
				writeOpenAIError(w, http.StatusConflict, "session_not_ready", fmt.Sprintf("session %q is %s (%s); wait for it to be Ready", session, live.Status.Phase, live.Spec.DesiredState))
				return
			}
			s.serveSessionChat(w, r, ns, session, body)
			return
		}
	}

	if err := validateAgentMessage(message); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_messages", "the last user message is the message to the Agent; "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	// A conversation that is starting, or answering an earlier message, is
	// not refusing this one: the request waits for it, within its timeout, as
	// a chat client expects of a request/response endpoint.
	var out *AgentMessageResponse
	for {
		var code int
		var refusal string
		var err error
		out, code, refusal, err = s.sendAgentMessage(r, ns, name, AgentMessageRequest{Message: message, Conversation: strings.TrimSpace(r.Header.Get("X-Sympozium-Conversation"))})
		if err == nil {
			break
		}
		if refusal != "" {
			w.Header().Set("X-Sympozium-Turn-Refusal", refusal)
		}
		if !transientTurnRefusal(refusal) {
			writeOpenAIError(w, code, "message_refused", err.Error())
			return
		}
		select {
		case <-ctx.Done():
			writeOpenAIError(w, http.StatusGatewayTimeout, "conversation_busy", fmt.Sprintf("the conversation did not take the message within %s (%s); retry, or with a longer ?timeout", timeout, err.Error()))
			return
		case <-time.After(s.pollInterval()):
		}
	}
	w.Header().Del("X-Sympozium-Turn-Refusal")
	sent := time.Now()
	provenance := &openAIProvenance{Namespace: ns, Agent: name, Run: out.Run.Name, Started: out.Started}
	id := "chatcmpl-" + out.Run.Name
	if out.Turn != nil {
		provenance.Turn = out.Turn.Name
		id = "chatcmpl-" + out.Turn.Name
		w.Header().Set("X-Sympozium-Turn", out.Turn.Name)
	}
	w.Header().Set("X-Sympozium-Run", out.Run.Name)

	completion := func(choice openAIChoice) openAIChatCompletion {
		return openAIChatCompletion{ID: id, Object: "chat.completion", Created: time.Now().Unix(), Model: name, Choices: []openAIChoice{choice}, Sympozium: provenance}
	}
	stop := "stop"
	if !req.Stream {
		answer, err := s.awaitAgentAnswer(ctx, ns, out, sent, nil)
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			writeOpenAIError(w, http.StatusGatewayTimeout, "answer_pending", fmt.Sprintf("no answer within %s; the conversation continues: read run %q%s, or retry with a longer ?timeout", timeout, out.Run.Name, turnHint(out)))
		case err != nil:
			writeOpenAIError(w, http.StatusServiceUnavailable, "answer_unavailable", err.Error())
		case answer.failed:
			writeOpenAIError(w, http.StatusBadGateway, "turn_failed", answer.text)
		default:
			writeJSON(w, completion(openAIChoice{Message: &openAIReply{Role: "assistant", Content: answer.text}, FinishReason: &stop}))
		}
		return
	}

	// Streaming: the role chunk goes out at once so the client sees the
	// connection is live, comments keep it open while the Agent works, and
	// the answer arrives as one content chunk.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	out1 := flushingWriter{w}
	event := func(v any) {
		data, _ := json.Marshal(v)
		_, _ = fmt.Fprintf(out1, "data: %s\n\n", data)
	}
	chunk := func(choice openAIChoice) openAIChatCompletion {
		c := completion(choice)
		c.Object = "chat.completion.chunk"
		return c
	}
	event(chunk(openAIChoice{Delta: &openAIReply{Role: "assistant"}}))
	answer, err := s.awaitAgentAnswer(ctx, ns, out, sent, func() { _, _ = io.WriteString(out1, ": keep-alive\n\n") })
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		event(openAIErrorBody{Error: openAIError{Message: fmt.Sprintf("no answer within %s; the conversation continues: read run %q%s", timeout, out.Run.Name, turnHint(out)), Type: "timeout_error", Code: "answer_pending"}})
	case err != nil:
		event(openAIErrorBody{Error: openAIError{Message: err.Error(), Type: "server_error", Code: "answer_unavailable"}})
	case answer.failed:
		event(openAIErrorBody{Error: openAIError{Message: answer.text, Type: "server_error", Code: "turn_failed"}})
	default:
		event(chunk(openAIChoice{Delta: &openAIReply{Content: answer.text}}))
		event(chunk(openAIChoice{Delta: &openAIReply{}, FinishReason: &stop}))
	}
	_, _ = io.WriteString(out1, "data: [DONE]\n\n")
}

// transientTurnRefusal reports a refusal that time resolves: the parent is
// still being admitted or prepared, or is busy with an earlier turn.
func transientTurnRefusal(reason string) bool {
	switch reason {
	case cellnparent.TurnRefusedNotAdmitted, cellnparent.TurnRefusedInitialPending, cellnparent.TurnRefusedActiveTurn, cellnparent.TurnRefusedParentNotReady, cellnparent.TurnRefusedStaleReadiness:
		return true
	}
	return false
}

// pollInterval is how often a waiting chat completion looks again.
func (s *Server) pollInterval() time.Duration {
	if s.answerPoll > 0 {
		return s.answerPoll
	}
	return agentChatPollInterval
}

func turnHint(out *AgentMessageResponse) string {
	if out.Turn == nil {
		return ""
	}
	return fmt.Sprintf(" (turn %q)", out.Turn.Name)
}

// agentAnswer is what the Agent's run or turn ended with.
type agentAnswer struct {
	text   string
	failed bool
}

// awaitAgentAnswer reads the run or turn the message reached until it carries
// an answer or ends without one. beat, when set, runs every keep-alive
// interval while waiting. The error is the context's when the wait ends
// first, or the client's when the object cannot be read.
func (s *Server) awaitAgentAnswer(ctx context.Context, ns string, out *AgentMessageResponse, sent time.Time, beat func()) (agentAnswer, error) {
	lastBeat := time.Now()
	for {
		answer, done, err := s.readAgentAnswer(ctx, ns, out, sent)
		if err != nil || done {
			return answer, err
		}
		select {
		case <-ctx.Done():
			return agentAnswer{}, ctx.Err()
		case <-time.After(s.pollInterval()):
		}
		if beat != nil && time.Since(lastBeat) >= agentChatKeepAlive {
			beat()
			lastBeat = time.Now()
		}
	}
}

// agentChatCacheGrace is how long a run or turn just created may be missing
// from the apiserver's cache-backed client before that counts as gone.
const agentChatCacheGrace = 15 * time.Second

// readAgentAnswer is one look at the run or turn: the answer when it has
// landed, done=false while it is still being produced (or, within
// agentChatCacheGrace of sent, not yet visible to the cache).
func (s *Server) readAgentAnswer(ctx context.Context, ns string, out *AgentMessageResponse, sent time.Time) (agentAnswer, bool, error) {
	notYetVisible := func(err error) bool { return k8serrors.IsNotFound(err) && time.Since(sent) < agentChatCacheGrace }
	if out.Turn != nil {
		var turn api.AgentRunTurn
		if err := s.client.Get(ctx, client.ObjectKey{Namespace: ns, Name: out.Turn.Name}, &turn); err != nil {
			if notYetVisible(err) {
				return agentAnswer{}, false, nil
			}
			return agentAnswer{}, false, fmt.Errorf("turn %q unavailable: %w", out.Turn.Name, err)
		}
		if turn.Status.Execution != nil && turn.Status.Execution.Result != nil {
			return agentAnswer{text: turn.Status.Execution.Result.Answer, failed: !turn.Status.Execution.Result.Succeeded}, true, nil
		}
		if c := meta.FindStatusCondition(turn.Status.Conditions, "CellnTurnComplete"); c != nil && c.Status == metav1.ConditionTrue {
			return agentAnswer{text: fmt.Sprintf("turn ended without an answer: %s: %s", c.Reason, c.Message), failed: true}, true, nil
		}
		return agentAnswer{}, false, nil
	}
	var run api.AgentRun
	if err := s.client.Get(ctx, client.ObjectKey{Namespace: ns, Name: out.Run.Name}, &run); err != nil {
		if notYetVisible(err) {
			return agentAnswer{}, false, nil
		}
		return agentAnswer{}, false, fmt.Errorf("run %q unavailable: %w", out.Run.Name, err)
	}
	switch {
	case run.Status.Phase == api.AgentRunPhaseFailed:
		reason := run.Status.Result
		if reason == "" {
			reason = "run failed"
			if run.Status.Error != "" {
				// The run's own error says why (a refused turn, an exhausted
				// tool budget, a lost node); a cleanup condition only says that
				// the native owner has gone.
				reason = "run failed: " + run.Status.Error
			} else if n := len(run.Status.Conditions); n > 0 {
				reason = fmt.Sprintf("run failed: %s: %s", run.Status.Conditions[n-1].Reason, run.Status.Conditions[n-1].Message)
			}
		}
		return agentAnswer{text: reason, failed: true}, true, nil
	case run.Status.Result != "":
		return agentAnswer{text: run.Status.Result}, true, nil
	case run.Status.Phase == api.AgentRunPhaseSucceeded || run.Status.Phase == api.AgentRunPhaseSkipped:
		return agentAnswer{text: fmt.Sprintf("run %s without an answer", strings.ToLower(string(run.Status.Phase))), failed: true}, true, nil
	}
	return agentAnswer{}, false, nil
}

// lastUserMessage is the newest user message's text: a string, or the text
// parts of a content array joined by newlines.
func lastUserMessage(messages []openAIMessage) (string, error) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		var text string
		if err := json.Unmarshal(messages[i].Content, &text); err == nil {
			return text, nil
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(messages[i].Content, &parts); err != nil {
			return "", errors.New("user message content must be a string or an array of text parts")
		}
		texts := make([]string, 0, len(parts))
		for _, part := range parts {
			if part.Type == "text" {
				texts = append(texts, part.Text)
			} else {
				return "", fmt.Errorf("user message content part %q is not supported; only text reaches an Agent", part.Type)
			}
		}
		return strings.Join(texts, "\n"), nil
	}
	return "", errors.New("messages must contain a user message; the last one is the message to the Agent")
}

// agentChatTimeout parses ?timeout= as a Go duration or whole seconds,
// bounded to agentChatMaxTimeout; empty means the default.
func agentChatTimeout(raw string) (time.Duration, error) {
	if raw == "" {
		return agentChatDefaultTimeout, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		seconds, convErr := strconv.Atoi(raw)
		if convErr != nil {
			return 0, fmt.Errorf("timeout %q must be a duration such as 90s or 2m, or whole seconds", raw)
		}
		d = time.Duration(seconds) * time.Second
	}
	if d <= 0 || d > agentChatMaxTimeout {
		return 0, fmt.Errorf("timeout must be between 1s and %s", agentChatMaxTimeout)
	}
	return d, nil
}
