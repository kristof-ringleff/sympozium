package modelkey

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func connection(name, secret, endpoint string) *api.ModelConnection {
	return &api.ModelConnection{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Spec: api.ModelConnectionSpec{Provider: "openai", Protocol: "openai-chat", Endpoint: endpoint, SecretRef: secret, Models: []string{"m"}}}
}

func TestFirstClaimPinsTheKeyToTheConnectionsOrigin(t *testing.T) {
	c := store(t, agent("a", "k"), secret("k", ""))
	conn := connection("model", "k", "https://api.openai.com/v1/chat/completions")
	if err := AuthorizeConnection(context.Background(), c, agent("a", "k"), conn); err != nil {
		t.Fatal(err)
	}
	var s corev1.Secret
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "k"}, &s); err != nil {
		t.Fatal(err)
	}
	if s.Annotations[OwnerAnnotation] != "Agent/a" || s.Annotations[OriginAnnotation] != "https://api.openai.com" {
		t.Fatalf("claim: %v", s.Annotations)
	}
	if !PinnedTo(&s, "https://api.openai.com") || PinnedTo(&s, "https://api.openai.com:443") || PinnedTo(&s, "https://evil.example") {
		t.Fatal("pin comparison")
	}
}

func TestAKeyPinnedElsewhereIsRefusedEvenToItsOwner(t *testing.T) {
	k := secret("k", "Agent/a")
	k.Annotations[OriginAnnotation] = "https://api.openai.com"
	c := store(t, agent("a", "k"), k)
	conn := connection("model", "k", "https://attacker.example/v1/chat/completions")
	err := AuthorizeConnection(context.Background(), c, agent("a", "k"), conn)
	var mismatch *OriginMismatchError
	if !errors.As(err, &mismatch) || !IsRefusal(err) || mismatch.Pinned != "https://api.openai.com" || mismatch.Origin != "https://attacker.example" {
		t.Fatalf("expected an origin mismatch, got %v", err)
	}
	// The pin is checked before ownership: another Agent gets the same answer.
	if err := CheckConnection(context.Background(), c, agent("b", "k"), conn); !errors.As(err, &mismatch) {
		t.Fatalf("expected an origin mismatch, got %v", err)
	}
	// The pin is never rewritten.
	var s corev1.Secret
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "k"}, &s)
	if s.Annotations[OriginAnnotation] != "https://api.openai.com" {
		t.Fatalf("pin moved: %v", s.Annotations)
	}
}

func TestAnOwnedKeyClaimedBeforePinsExistIsPinnedOnItsNextUse(t *testing.T) {
	c := store(t, agent("a", "k"), secret("k", "Agent/a"))
	conn := connection("model", "k", "https://api.openai.com/v1/chat/completions")
	if err := AuthorizeConnection(context.Background(), c, agent("a", "k"), conn); err != nil {
		t.Fatal(err)
	}
	var s corev1.Secret
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "k"}, &s)
	if s.Annotations[OriginAnnotation] != "https://api.openai.com" || s.Annotations[OwnerAnnotation] != "Agent/a" {
		t.Fatalf("legacy claim not pinned: %v", s.Annotations)
	}
}

func TestOwnershipStillHoldsUnderThePin(t *testing.T) {
	k := secret("k", "Agent/a")
	k.Annotations[OriginAnnotation] = "https://api.openai.com"
	c := store(t, agent("a", "k"), agent("b", "k"), k)
	conn := connection("model", "k", "https://api.openai.com/v1/chat/completions")
	var conflict *ConflictError
	if err := AuthorizeConnection(context.Background(), c, agent("b", "k"), conn); !errors.As(err, &conflict) {
		t.Fatalf("expected a conflict, got %v", err)
	}
}

func TestGrantsConnectionAgreesWithTheResolver(t *testing.T) {
	conn := connection("model", "k", "https://api.openai.com/v1/chat/completions")
	keyless := connection("local", "", "http://llama:8080/v1/chat/completions")
	byRef := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "r"}, Spec: api.AgentSpec{Execution: &api.AgentExecutionDefaults{ModelConnectionRef: "model"}}}
	otherRef := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "o"}, Spec: api.AgentSpec{Execution: &api.AgentExecutionDefaults{ModelConnectionRef: "other"}}}
	anyProvider := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "any"}, Spec: api.AgentSpec{AuthRefs: []api.SecretRef{{Secret: "k"}}}}
	otherProvider := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "ap"}, Spec: api.AgentSpec{AuthRefs: []api.SecretRef{{Provider: "anthropic", Secret: "k"}}}}
	for _, tc := range []struct {
		name  string
		agent *api.Agent
		conn  *api.ModelConnection
		want  bool
	}{
		{"authRefs for the provider", agent("a", "k"), conn, true},
		{"authRefs for any provider", anyProvider, conn, true},
		{"authRefs for another provider", otherProvider, conn, false},
		{"execution.modelConnectionRef names it", byRef, conn, true},
		{"execution.modelConnectionRef names another", otherRef, conn, false},
		{"no grant", agent("n", ""), conn, false},
		{"keyless needs no grant", agent("n", ""), keyless, true},
	} {
		if got := GrantsConnection(tc.agent, tc.conn); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	// The claim refuses exactly what GrantsConnection refuses, with no
	// authRefs needed when the Agent selects the connection itself.
	c := store(t, byRef, otherRef, secret("k", ""))
	if err := AuthorizeConnection(context.Background(), c, byRef, conn); err != nil {
		t.Fatal(err)
	}
	var s corev1.Secret
	_ = c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "k"}, &s)
	if s.Annotations[OwnerAnnotation] != "Agent/r" {
		t.Fatalf("selection did not claim: %v", s.Annotations)
	}
	var notGranted *NotGrantedError
	c = store(t, otherRef, secret("k", ""))
	if err := AuthorizeConnection(context.Background(), c, otherRef, conn); !errors.As(err, &notGranted) {
		t.Fatalf("expected not granted, got %v", err)
	}
}

func TestPinHelpers(t *testing.T) {
	s := secret("k", "")
	if err := Pin(s, ""); err == nil {
		t.Fatal("empty origin pinned")
	}
	if err := Pin(s, "https://API.OpenAI.com"); err != nil || PinnedOrigin(s) != "https://api.openai.com" {
		t.Fatalf("pin: %v %q", err, PinnedOrigin(s))
	}
	if err := Pin(s, "https://api.openai.com"); err != nil {
		t.Fatalf("re-pin to the same origin: %v", err)
	}
	if err := Pin(s, "https://other.example"); !IsRefusal(err) {
		t.Fatalf("pin moved: %v", err)
	}
}
