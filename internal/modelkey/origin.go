package modelkey

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// OriginAnnotation on a Secret names the one model endpoint origin
// (scheme://host[:port]) the key may be sent to. A key is pinned when it is
// created by the API or console, or when its Agent first claims it; after
// that the gateway refuses to send it anywhere else. Moving a key to another
// origin means a person entering a new key, not an agent rewriting its
// connection.
const OriginAnnotation = "sympozium.ai/model-key-origin"

// OriginMismatchError reports a key pinned to another origin than the
// connection's.
type OriginMismatchError struct {
	Secret, Pinned, Origin string
}

func (e *OriginMismatchError) Error() string {
	return fmt.Sprintf("model key Secret %q is pinned to %s and cannot be sent to %s; a connection to another provider needs its own key", e.Secret, e.Pinned, e.Origin)
}

// PinnedOrigin is the origin a Secret's key is pinned to, or "" when unpinned.
func PinnedOrigin(secret *corev1.Secret) string {
	return strings.ToLower(secret.Annotations[OriginAnnotation])
}

// PinnedTo reports whether the Secret's key is pinned to exactly this origin.
// An unpinned key is pinned to nothing, so it is never sent.
func PinnedTo(secret *corev1.Secret, origin string) bool {
	pinned := PinnedOrigin(secret)
	return pinned != "" && pinned == strings.ToLower(origin)
}

// Pin records the origin on a Secret that has none, in memory; the caller
// writes it. It refuses a Secret pinned elsewhere.
func Pin(secret *corev1.Secret, origin string) error {
	origin = strings.ToLower(origin)
	if origin == "" {
		return fmt.Errorf("a model key needs the origin it is for")
	}
	if pinned := PinnedOrigin(secret); pinned != "" {
		if pinned != origin {
			return &OriginMismatchError{Secret: secret.Name, Pinned: pinned, Origin: origin}
		}
		return nil
	}
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	secret.Annotations[OriginAnnotation] = origin
	return nil
}

// GrantsConnection reports whether the Agent grants the connection's Secret:
// it lists the Secret in spec.authRefs for the connection's provider (or any
// provider), or its own execution defaults select this connection, which is
// the Agent owner naming it. This is the one grant rule the resolver, the
// controller's claim and the API agree on.
func GrantsConnection(agent *api.Agent, connection *api.ModelConnection) bool {
	if connection.Spec.SecretRef == "" {
		return true
	}
	if execution := agent.Spec.Execution; execution != nil && execution.ModelConnectionRef != "" && execution.ModelConnectionRef == connection.Name {
		return true
	}
	return Grants(agent, connection.Spec.Provider, connection.Spec.SecretRef)
}

// AuthorizeConnection decides whether runs of agent may use the connection's
// Secret, and records the claim (owner and origin) on the Secret when the
// Agent takes an unowned or stale key it grants, or pins a key it owns that
// was never pinned. A connection without a Secret is always allowed. A key
// pinned to another origin is refused whoever owns it.
func AuthorizeConnection(ctx context.Context, c client.Client, agent *api.Agent, connection *api.ModelConnection) error {
	claim, err := decideConnection(ctx, c, agent, connection)
	if err != nil || claim == nil {
		return err
	}
	return c.Update(ctx, claim)
}

// CheckConnection is the read-only form of AuthorizeConnection.
func CheckConnection(ctx context.Context, c client.Reader, agent *api.Agent, connection *api.ModelConnection) error {
	_, err := decideConnection(ctx, c, agent, connection)
	return err
}

func decideConnection(ctx context.Context, c client.Reader, agent *api.Agent, connection *api.ModelConnection) (*corev1.Secret, error) {
	secret := strings.TrimSpace(connection.Spec.SecretRef)
	if secret == "" {
		return nil, nil
	}
	origin, err := connection.Spec.Origin()
	if err != nil {
		return nil, err
	}
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: agent.Namespace, Name: secret}, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	// The pin is checked before ownership: a key that is somebody's but
	// pointed elsewhere is refused with the reason that matters.
	if pinned := PinnedOrigin(&s); pinned != "" && pinned != origin {
		return nil, &OriginMismatchError{Secret: secret, Pinned: pinned, Origin: origin}
	}
	owner := Owner(agent)
	current, err := LiveOwner(ctx, c, &s)
	if err != nil {
		return nil, err
	}
	switch {
	case current == owner:
		if PinnedOrigin(&s) != "" {
			return nil, nil
		}
		// A key claimed before origins were pinned: pin it on its next use.
	case current != "":
		return nil, &ConflictError{Secret: secret, Owner: current}
	default:
		if !GrantsConnection(agent, connection) {
			return nil, &NotGrantedError{Secret: secret, Agent: agent.Name}
		}
	}
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Annotations[OwnerAnnotation] = owner
	s.Annotations[OriginAnnotation] = origin
	return &s, nil
}
