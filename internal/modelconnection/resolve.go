// Package modelconnection resolves native model intent without reading credentials
// or granting host authority. A resolved run pins the connection UID and spec.
package modelconnection

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Resolve freezes a run's model intent from its Agent's connection: a
// connection that names its own Secret (spec.secretRef) or no credential at
// all, executed through the model gateway. The Secret is referenced by the
// connection only. Neither its name nor its bytes are copied into the returned
// ModelSpec: AuthSecretRef stays empty, so no pod or guest that injects
// spec.model credentials as environment can receive it. The gateway pins and
// reads the Secret from the connection.
func Resolve(ctx context.Context, reader client.Reader, namespace string, model api.ModelSpec) (api.ModelSpec, error) {
	if model.ConnectionRef == "" {
		return model, nil
	}
	var connection api.ModelConnection
	if err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: model.ConnectionRef}, &connection); err != nil {
		return model, fmt.Errorf("model connection %q unavailable: %w", model.ConnectionRef, err)
	}
	if err := connection.Spec.Validate(); err != nil {
		return model, err
	}
	if connection.Spec.Disabled || connection.DeletionTimestamp != nil {
		return model, fmt.Errorf("model connection is disabled or being deleted")
	}
	if !slices.Contains(connection.Spec.Models, model.Model) {
		return model, fmt.Errorf("model %q is not listed in connection %q", model.Model, model.ConnectionRef)
	}
	revision := Revision(&connection)
	if model.ConnectionRevision != "" && model.ConnectionRevision != revision {
		return model, fmt.Errorf("model connection changed; create a new run")
	}
	if model.AuthSecretRef != "" || model.ModelRef != "" || len(model.ProviderHeaders) != 0 || model.ProviderHeadersSecretRef != "" || len(model.NodeSelector) != 0 || (model.Thinking != "" && model.Thinking != "off") {
		return model, fmt.Errorf("native model connections cannot be combined with inline credentials, headers, placement or thinking settings")
	}
	s := connection.Spec
	origin, err := api.ModelEndpointOriginInsecure(s.Endpoint, s.AllowInsecure)
	if err != nil {
		return model, err
	}
	// A cluster Secret never crosses plain HTTP, whatever the connection opts into.
	if s.SecretRef != "" && !strings.HasPrefix(strings.ToLower(origin), "https://") {
		return model, fmt.Errorf("a Secret-backed model connection requires an HTTPS endpoint")
	}
	if (model.Provider != "" && model.Provider != s.Provider) || (model.BaseURL != "" && model.BaseURL != s.Endpoint) || (model.Protocol != "" && model.Protocol != s.Protocol) {
		return model, fmt.Errorf("inline model settings differ from the selected connection")
	}
	if model.AllowInsecure && !s.AllowInsecure {
		return model, fmt.Errorf("inline insecure setting is not authorized by the connection")
	}
	model.Provider, model.BaseURL, model.Protocol = s.Provider, s.Endpoint, s.Protocol
	model.AllowInsecure = s.AllowInsecure
	model.ConnectionRevision = revision
	return model, nil
}

// Revision is the identity a run pins when it is resolved against a
// connection: the object's UID and its spec. Every later authority check must
// compare against this same value, never a digest of its own.
func Revision(connection *api.ModelConnection) string {
	raw, _ := json.Marshal(struct {
		UID  types.UID
		Spec api.ModelConnectionSpec
	}{connection.UID, connection.Spec})
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

// Route returns the host-visible identity. The connection revision is separately
// pinned in the run/selection and must not alter the independent host template.
func Route(model api.ModelSpec) api.ModelSpec {
	model.ConnectionRef, model.ConnectionRevision = "", ""
	return model
}

// ResolveHarness reads a connection for a Kubernetes persistent harness. The
// Secret is referenced only; no credential bytes are fetched by this resolver.
func ResolveHarness(ctx context.Context, reader client.Reader, namespace, name, model string) (*api.AgentRuntimeModel, string, error) {
	var connection api.ModelConnection
	if err := reader.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &connection); err != nil {
		return nil, "", err
	}
	if err := connection.Spec.Validate(); err != nil {
		return nil, "", err
	}
	s := connection.Spec
	if s.Disabled || connection.DeletionTimestamp != nil {
		return nil, "", fmt.Errorf("model connection is disabled or being deleted")
	}
	if !slices.Contains(s.Models, model) {
		return nil, "", fmt.Errorf("model %q is not listed in connection %q", model, name)
	}
	if s.Protocol != "openai-chat" {
		return nil, "", fmt.Errorf("persistent Kubernetes harness connections currently require openai-chat; use an OpenAI compatible gateway for this provider")
	}
	// The maintained Hermes adapter consumes OpenAI-compatible endpoints.
	// Direct Anthropic routes require a harness that supports that protocol.
	base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimRight(s.Endpoint, "/"), "/chat/completions"), "/messages")
	raw, _ := json.Marshal(struct {
		UID   types.UID
		Spec  api.ModelConnectionSpec
		Model string
	}{connection.UID, s, model})
	revision := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	return &api.AgentRuntimeModel{Provider: s.Provider, Model: model, BaseURL: base, AuthSecretRef: s.SecretRef}, revision, nil
}
