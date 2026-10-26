package cellninstall

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The installer renders the key id into the chart before the trust exists,
// then bootstraps under that id; every later run reads it back and never
// rotates it.
func TestMediationTrustUnderAChosenKeyID(t *testing.T) {
	ctx := context.Background()
	if _, found, err := ExistingMediationKeyID(ctx, fleetStore(), "sympozium-system"); found || err != nil {
		t.Fatalf("a fresh cluster (no namespaces) has trust: %v %v", found, err)
	}
	store := mediationStore(t)
	if _, found, err := ExistingMediationKeyID(ctx, store, "sympozium-system"); found || err != nil {
		t.Fatalf("no trust yet: %v %v", found, err)
	}
	o := mediationOptions()
	o.KeyID = "mediation-chosen-1"
	trust, err := PrepareMediationTrust(ctx, store, o)
	if err != nil || !trust.Created || trust.KeyID != "mediation-chosen-1" {
		t.Fatalf("bootstrap: %+v %v", trust, err)
	}
	keyID, found, err := ExistingMediationKeyID(ctx, store, "sympozium-system")
	if err != nil || !found || keyID != "mediation-chosen-1" {
		t.Fatalf("read back: %q %v %v", keyID, found, err)
	}
	again, err := PrepareMediationTrust(ctx, store, o)
	if err != nil || again.Created || again.KeyID != keyID {
		t.Fatalf("rerun: %+v %v", again, err)
	}
	other := o
	other.KeyID = "mediation-other"
	if _, err := PrepareMediationTrust(ctx, store, other); err == nil || !strings.Contains(err.Error(), "never rotated silently") {
		t.Fatalf("another key id was accepted: %v", err)
	}
	bad := o
	bad.KeyID = "has space"
	if _, err := PrepareMediationTrust(ctx, mediationStore(t), bad); err == nil {
		t.Fatal("an invalid key id was accepted")
	}

	// Partial trust is reported, never completed.
	partial := mediationStore(t, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "sympozium-system", Name: MediationControllerSecret}})
	if _, _, err := ExistingMediationKeyID(ctx, partial, "sympozium-system"); err == nil || !strings.Contains(err.Error(), "partially present") {
		t.Fatalf("partial trust: %v", err)
	}
}

func TestMediationCertificatesLastTenYears(t *testing.T) {
	ctx := context.Background()
	store := mediationStore(t)
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	o := mediationOptions()
	o.Now = func() time.Time { return now }
	if _, err := PrepareMediationTrust(ctx, store, o); err != nil {
		t.Fatal(err)
	}
	var trust corev1.ConfigMap
	var gateway, node corev1.Secret
	for key, object := range map[types.NamespacedName]client.Object{
		{Namespace: "sympozium-system", Name: MediationTrustConfigMap}: &trust,
		{Namespace: "sympozium-system", Name: MediationGatewaySecret}:  &gateway,
		{Namespace: fleetNamespace, Name: MediationNodeSecret}:         &node,
	} {
		if err := store.Get(ctx, key, object); err != nil {
			t.Fatal(err)
		}
	}
	expiry, ok := MediationCertificateExpiry([]byte(trust.Data["ca.crt"]), gateway.Data["tls.crt"], node.Data["tls.crt"])
	if !ok || !expiry.Equal(now.Add(DefaultMediationValidity)) {
		t.Fatalf("expiry %v %v, want %v", expiry, ok, now.Add(DefaultMediationValidity))
	}
	if DefaultMediationValidity < 10*365*24*time.Hour {
		t.Fatal("the default lifetime is shorter than ten years")
	}
	if _, ok := MediationCertificateExpiry([]byte("not a certificate")); ok {
		t.Fatal("garbage parsed as a certificate")
	}
}

func TestAutoMediationValues(t *testing.T) {
	image := "ghcr.io/sympozium-ai/sympozium/model-gateway@sha256:" + strings.Repeat("a", 64)
	values, err := AutoMediationValues("0b9c-uid", "mediation-2026-10-05-01020304", image)
	want := []string{"celln.mediation.enabled=true", "celln.mediation.clusterId=0b9c-uid", "celln.mediation.issuer.keyId=mediation-2026-10-05-01020304", "modelGateway.image=" + image}
	if err != nil || !slices.Equal(values, want) {
		t.Fatalf("values = %q %v", values, err)
	}
	for _, tc := range []struct{ cluster, key, image string }{
		{"", "k", image},
		{"a,b", "k", image},
		{"c", "bad key", image},
		{"c", "k", "ghcr.io/sympozium-ai/sympozium/model-gateway:v1"},
	} {
		if _, err := AutoMediationValues(tc.cluster, tc.key, tc.image); err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}
}
