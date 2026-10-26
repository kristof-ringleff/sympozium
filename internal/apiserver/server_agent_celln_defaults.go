package apiserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	"github.com/sympozium-ai/sympozium/internal/modelkey"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

// One call creates a working Celln Agent. The request names the Agent and
// its own model connection (any provider; a key when the provider wants one);
// the server creates the Secret, pins it to the connection's origin, creates
// the ModelConnection, picks the namespace's fleet profile, ensures its
// runtime wrapper and sizes the conversation limits from the policy. The same
// objects created by hand (or by the console step by step) are accepted
// unchanged; this only fills what the caller left out.

// InlineModelConnection is the Agent's own connection, created with the Agent
// (CreateInstanceRequest.modelConnection).
type InlineModelConnection struct {
	// Name of the ModelConnection; the Agent's name when empty.
	Name string                  `json:"name,omitempty"`
	Spec api.ModelConnectionSpec `json:"spec"`
	// APIKey is written to a Secret the server names; never stored anywhere
	// else and never returned.
	APIKey string `json:"apiKey,omitempty"`
}

// prepareCellnAgent completes a Celln Agent's create request before it is
// validated: the inline connection becomes execution.modelConnectionRef, and a
// Celln Agent without a selection gets the namespace's offered profile, its
// toolbox wrapper and tools, the enduring lifecycle and the policy's session
// defaults. The code is the HTTP status for a refusal.
func (s *Server) prepareCellnAgent(ctx context.Context, ns string, req *CreateInstanceRequest) (int, error) {
	if req.ModelConnection != nil {
		if req.Execution == nil {
			req.Execution = &api.AgentExecutionDefaults{Backend: "celln"}
		}
		if req.Execution.Backend != "celln" {
			return http.StatusBadRequest, errors.New("modelConnection creates a Celln Agent's own connection; a Kubernetes Agent names provider, model and apiKey")
		}
		if req.Execution.ModelConnectionRef != "" {
			return http.StatusBadRequest, errors.New("select a model connection (execution.modelConnectionRef) or create one inline (modelConnection), not both")
		}
		if req.APIKey != "" || req.SecretName != "" || req.BaseURL != "" {
			return http.StatusBadRequest, errors.New("the inline connection carries the endpoint and key; leave apiKey, secretName and baseURL empty")
		}
		name := req.ModelConnection.Name
		if name == "" {
			name = req.Name
		}
		connection, code, err := s.saveModelConnection(ctx, ns, name, req.ModelConnection.Spec, req.ModelConnection.APIKey)
		if err != nil {
			return code, err
		}
		req.Execution.ModelConnectionRef = connection.Name
		if req.Model == "" && len(connection.Spec.Models) == 1 && connection.Spec.Models[0] != api.CellnAnyModel {
			req.Model = connection.Spec.Models[0]
		}
	}
	if req.Execution == nil || req.Execution.Backend != "celln" {
		return 0, nil
	}
	if len(req.Skills) > 0 {
		// The same refusal createAgent gives, before anything is looked up or
		// written for this Agent.
		return http.StatusBadRequest, errors.New("Celln execution defaults cannot be combined with SkillPacks; choose Kubernetes or explicitly remove the SkillPacks")
	}
	e := req.Execution
	if e.ExecutionLifecycle == "" {
		e.ExecutionLifecycle = "enduring"
	}
	if e.Model == "" {
		e.Model = req.Model
	}
	needsSelection := e.CellnSelection == nil || e.CellnSelection.RuntimeRef == ""
	needsLimits := e.ExecutionLifecycle == "enduring" && e.Enduring == nil
	if !needsSelection && !needsLimits {
		return 0, nil
	}
	authorised, err := cellnplatform.AuthorisedProfiles(ctx, s.client, ns)
	if err != nil && !apierrors.IsNotFound(err) {
		return http.StatusInternalServerError, fmt.Errorf("fleet profiles for namespace %q: %w", ns, err)
	}
	var offered *cellnplatform.Authorised
	for i := range authorised {
		if !cellnplatform.Toolbox(&authorised[i].Profile) {
			offered = &authorised[i]
			break
		}
	}
	if offered == nil {
		return http.StatusConflict, fmt.Errorf("no Celln fleet profile is offered to namespace %q: install the fleet (sympozium install) or name execution.cellnSelection.runtimeRef", ns)
	}
	if needsSelection {
		profile, tools, err := cellnplatform.OwnKeySelection(ctx, s.client, ns, offered.Profile.Name)
		if err != nil {
			return http.StatusInternalServerError, err
		}
		wrapper, err := cellnplatform.EnsureRuntimeWrapper(ctx, s.client, ns, profile)
		if err != nil {
			return http.StatusInternalServerError, err
		}
		toolRefs := []api.CellnCatalogueToolRef{}
		if e.CellnSelection != nil && e.CellnSelection.ToolRefs != nil {
			toolRefs = e.CellnSelection.ToolRefs
		}
		e.CellnSelection = &api.CellnCatalogueSelection{RuntimeRef: wrapper, ToolRefs: toolRefs, ClusterToolRefs: tools}
		if req.RuntimeRef == "" {
			req.RuntimeRef = wrapper
		}
	}
	if needsLimits {
		e.Enduring = cellninstall.SessionDefaultsFor(policyCeilings(&offered.Policy), &offered.Profile)
	}
	return 0, nil
}

// policyCeilings are a policy's per-conversation maxima in the shape a run
// asks for them, clipped to what an AgentRun may carry.
func policyCeilings(policy *api.CellnExecutionPolicy) api.EnduringRunSpec {
	c := policy.Spec.Ceilings
	return api.EnduringRunSpec{LeaseSeconds: int32(min(c.MaxParentLeaseSeconds, 86400)), MaxTurns: int32(min(c.MaxTurns, 1024)), MaxModelRequests: int32(min(c.MaxModelRequests, 6144)), MaxOutputTokens: c.MaxOutputTokens}
}

// cellnKey is the Agent's own connection and the Secret it names, or nil
// when the Agent is not a Celln Agent with a connection.
func (s *Server) cellnKey(ctx context.Context, agent *api.Agent) (*api.ModelConnection, error) {
	if agent.Spec.Execution == nil || agent.Spec.Execution.Backend != "celln" || agent.Spec.Execution.ModelConnectionRef == "" {
		return nil, nil
	}
	var connection api.ModelConnection
	if err := s.client.Get(ctx, types.NamespacedName{Namespace: agent.Namespace, Name: agent.Spec.Execution.ModelConnectionRef}, &connection); err != nil {
		return nil, fmt.Errorf("model connection %q unavailable: %w", agent.Spec.Execution.ModelConnectionRef, err)
	}
	return &connection, nil
}

// checkCellnKey refuses, before anything is written, a connection whose key
// another live Agent owns or that is pinned to another origin.
func (s *Server) checkCellnKey(ctx context.Context, agent *api.Agent) (int, error) {
	connection, err := s.cellnKey(ctx, agent)
	if err != nil || connection == nil {
		return http.StatusInternalServerError, err
	}
	if err := modelkey.CheckConnection(ctx, s.client, agent, connection); err != nil {
		if modelkey.IsRefusal(err) {
			return http.StatusConflict, err
		}
		return http.StatusInternalServerError, err
	}
	return 0, nil
}

// claimCellnKey records the created Agent as the owner of its connection's
// key, pinned to the connection's origin, so the key is this Agent's from the
// moment it exists rather than from its first run. A refusal here lost a race
// with another claimant; the Agent is removed again and the refusal returned.
func (s *Server) claimCellnKey(ctx context.Context, agent *api.Agent) (int, error) {
	connection, err := s.cellnKey(ctx, agent)
	if err != nil || connection == nil {
		return http.StatusInternalServerError, err
	}
	if err := modelkey.AuthorizeConnection(ctx, s.client, agent, connection); err != nil {
		_ = s.client.Delete(ctx, agent)
		if modelkey.IsRefusal(err) {
			return http.StatusConflict, err
		}
		return http.StatusInternalServerError, err
	}
	return 0, nil
}
