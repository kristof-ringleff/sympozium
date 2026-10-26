package apiserver

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// POST /api/v1/agents/{name}/messages: one call to talk to an Agent. The
// message continues the Agent's live conversation when it has one (or the
// one the caller names), and otherwise starts a conversation with the Agent's
// own execution defaults: its backend, lifecycle, connection and runtime. The
// caller never spells out a run; everything an Agent needs to run is on the
// Agent. The answer arrives on the returned run (status.execution.result) or
// turn (status), which the caller polls or watches as it does today.

// AgentMessageRequest is the body of POST /api/v1/agents/{name}/messages.
type AgentMessageRequest struct {
	Message string `json:"message"`
	// Conversation names the run to continue. Empty continues the Agent's
	// newest live enduring conversation, or starts one when there is none.
	Conversation string `json:"conversation,omitempty"`
	// RequestID makes a continued turn idempotent; a repeated request with the
	// same ID observes its original turn. Generated when empty.
	RequestID string `json:"requestId,omitempty"`
}

// AgentMessageResponse names the run the message reached and, for a continued
// conversation, the turn that carries it.
type AgentMessageResponse struct {
	Run *api.AgentRun `json:"run"`
	// Turn is set when the message continued a conversation.
	Turn *api.AgentRunTurn `json:"turn,omitempty"`
	// Started reports that this message began the conversation.
	Started bool `json:"started"`
}

const agentMessageMaxBytes = 2048

func (s *Server) postAgentMessage(w http.ResponseWriter, r *http.Request) {
	ns := requestNamespace(r)
	name := r.PathValue("name")
	var input AgentMessageRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid JSON: message (and optionally conversation, requestId) expected", http.StatusBadRequest)
		return
	}
	if err := validateAgentMessage(input.Message); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var agent api.Agent
	if err := s.client.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: name}, &agent); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, fmt.Sprintf("agent %q not found in namespace %q", name, ns), http.StatusNotFound)
			return
		}
		http.Error(w, "failed to get agent: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out, code, refusal, err := s.sendAgentMessage(r, ns, name, input)
	if err != nil {
		if refusal != "" {
			w.Header().Set("X-Sympozium-Turn-Refusal", refusal)
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	writeJSON(w, out)
}

// validateAgentMessage is the one bound on a message to an Agent, shared by
// the messages and chat-completions endpoints: the turn protocol's limit.
func validateAgentMessage(message string) error {
	if strings.TrimSpace(message) == "" || len(message) > agentMessageMaxBytes || strings.ContainsRune(message, '\x00') {
		return fmt.Errorf("message must be 1-%d UTF-8 bytes without NUL", agentMessageMaxBytes)
	}
	return nil
}

// sendAgentMessage delivers a message to an Agent that exists: as a turn of
// its live conversation (202, or 200 for a repeated request ID) or as the
// first message of a new one (201). The refusal names a turn refusal reason
// for the response header; the code is the HTTP status in every case.
func (s *Server) sendAgentMessage(r *http.Request, ns, name string, input AgentMessageRequest) (*AgentMessageResponse, int, string, error) {
	run, err := s.agentConversation(r, ns, name, input.Conversation)
	if err != nil {
		code := http.StatusServiceUnavailable
		if k8serrors.IsNotFound(err) {
			code = http.StatusNotFound
		}
		return nil, code, "", err
	}
	if run != nil {
		requestID := input.RequestID
		if requestID == "" {
			var nonce [12]byte
			if _, err := rand.Read(nonce[:]); err != nil {
				return nil, http.StatusServiceUnavailable, "", errors.New("request identity unavailable")
			}
			requestID = "msg-" + hex.EncodeToString(nonce[:])
		} else if len(requestID) > 64 || !validRequestID(requestID) {
			return nil, http.StatusBadRequest, "", errors.New("invalid request ID")
		}
		turn, code, refusal, err := s.admitTurn(r.Context(), run, requestID, input.Message)
		if err != nil {
			return nil, code, refusal, err
		}
		return &AgentMessageResponse{Run: run, Turn: turn}, code, "", nil
	}

	// No live conversation: start one. The run request names the Agent and
	// the message only; createRun inherits everything else from the Agent's
	// execution defaults and applies the same validation an explicit run gets.
	body, _ := json.Marshal(CreateRunRequest{AgentRef: name, Task: input.Message})
	proxy := httptest.NewRecorder()
	req := r.Clone(r.Context())
	req.Method = http.MethodPost
	req.URL = &url.URL{Path: "/api/v1/runs", RawQuery: "namespace=" + url.QueryEscape(ns)}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	s.createRun(proxy, req)
	if proxy.Code != http.StatusCreated {
		return nil, proxy.Code, "", errors.New(strings.TrimSpace(proxy.Body.String()))
	}
	var created api.AgentRun
	if err := json.Unmarshal(proxy.Body.Bytes(), &created); err != nil {
		return nil, http.StatusInternalServerError, "", fmt.Errorf("run created but unreadable: %w", err)
	}
	return &AgentMessageResponse{Run: &created, Started: true}, http.StatusCreated, "", nil
}

// agentConversation finds the run a message continues: the named one, which
// must belong to the Agent, or the Agent's newest live enduring run. nil
// means there is none and a conversation starts.
func (s *Server) agentConversation(r *http.Request, ns, agent, conversation string) (*api.AgentRun, error) {
	if conversation != "" {
		var run api.AgentRun
		if err := s.client.Get(r.Context(), types.NamespacedName{Namespace: ns, Name: conversation}, &run); err != nil {
			if k8serrors.IsNotFound(err) {
				return nil, k8serrors.NewNotFound(api.GroupVersion.WithResource("agentruns").GroupResource(), conversation)
			}
			return nil, fmt.Errorf("conversation unavailable: %w", err)
		}
		if run.Spec.AgentRef != agent && run.Labels["sympozium.ai/instance"] != agent {
			return nil, k8serrors.NewNotFound(api.GroupVersion.WithResource("agentruns").GroupResource(), conversation)
		}
		return &run, nil
	}
	var runs api.AgentRunList
	if err := s.client.List(r.Context(), &runs, client.InNamespace(ns), client.MatchingLabels{"sympozium.ai/instance": agent}); err != nil {
		return nil, fmt.Errorf("conversations unavailable: %w", err)
	}
	live := make([]*api.AgentRun, 0, len(runs.Items))
	for i := range runs.Items {
		run := &runs.Items[i]
		if run.DeletionTimestamp != nil || run.Spec.ExecutionLifecycle != "enduring" || run.Spec.Enduring == nil {
			continue
		}
		switch run.Status.Phase {
		case api.AgentRunPhaseRunning, api.AgentRunPhasePending, "":
			live = append(live, run)
		}
	}
	if len(live) == 0 {
		return nil, nil
	}
	sort.Slice(live, func(i, j int) bool {
		a, b := live[i].CreationTimestamp, live[j].CreationTimestamp
		if a.Equal(&b) {
			return live[i].Name > live[j].Name
		}
		return b.Before(&a)
	})
	return live[0], nil
}

func validRequestID(id string) bool {
	for _, c := range []byte(id) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return id != ""
}
