package main

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
)

func TestDoctorMediation(t *testing.T) {
	record := func(content string) client.Object {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: cellnSystemNamespace, Name: cellninstall.MediationRecordConfigMap}, Data: map[string]string{"mediation.json": content}}
	}
	trust := func(names ...string) []client.Object {
		controller, gatewaySecret, node, trustMap := names[0], names[1], names[2], names[3]
		return []client.Object{
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: helmNamespace, Name: controller}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: helmNamespace, Name: gatewaySecret}},
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: cellnSystemNamespace, Name: node}},
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: helmNamespace, Name: trustMap}},
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: cellnSystemNamespace, Name: trustMap}},
		}
	}
	defaults := trust(cellninstall.MediationControllerSecret, cellninstall.MediationGatewaySecret, cellninstall.MediationNodeSecret, cellninstall.MediationTrustConfigMap)
	gateway := func(available int32) client.Object {
		return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: helmNamespace, Name: "sympozium-model-gateway", Generation: 1}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, AvailableReplicas: available}}
	}
	policy := func(allowed, denied []string) client.Object {
		return &sympoziumv1alpha1.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "celln-fleet-starter"}, Spec: sympoziumv1alpha1.CellnExecutionPolicySpec{AllowedOrigins: allowed, DeniedOrigins: denied}}
	}
	const enabled = `{"enabled":true}`
	with := func(groups ...[]client.Object) []client.Object {
		var out []client.Object
		for _, g := range groups {
			out = append(out, g...)
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		objects []client.Object
		release *releaseInfo
		status  string
		want    []string
		absent  []string
	}{
		{name: "no fleet", status: statusPass, want: []string{"no Celln fleet is installed"}},
		{name: "no gateway recorded", objects: []client.Object{configureDaemonSet()}, status: statusFail, want: []string{"records no model gateway", "sympozium install"}},
		{name: "enabled and coherent, any origin", objects: with([]client.Object{configureDaemonSet(), record(enabled), gateway(1), policy(nil, nil)}, defaults), status: statusPass,
			want: []string{"enabled", "every Agent brings its own connection and key", "any origin the gateway reaches"}, absent: []string{"AUTH_ROUTE_MISMATCH", "route"}},
		{name: "enabled and narrowed", objects: with([]client.Object{configureDaemonSet(), record(enabled), gateway(1), policy([]string{"*.openai.com"}, []string{"evil.example"})}, defaults), status: statusPass,
			want: []string{"only to *.openai.com", "never to evil.example"}},
		{name: "no bootstrap", objects: []client.Object{configureDaemonSet(), record(enabled), gateway(1), policy(nil, nil)}, status: statusFail,
			want: []string{"Secret " + helmNamespace + "/celln-mediation-controller", "Secret celln-system/celln-mediation-node", "ConfigMap celln-system/celln-mediation-trust", "sympozium install"}},
		{name: "bootstrap objects under the release's names", objects: with([]client.Object{configureDaemonSet(), record(enabled), gateway(1), policy(nil, nil)}, trust("own-controller", "own-gateway", "own-node", "own-trust")),
			release: &releaseInfo{Config: map[string]interface{}{"celln": map[string]interface{}{"mediation": map[string]interface{}{"controllerSecret": "own-controller", "gatewaySecret": "own-gateway", "nodeSecret": "own-node", "trustConfigMap": "own-trust"}}}}, status: statusPass, want: []string{"enabled"}},
		{name: "no gateway", objects: with([]client.Object{configureDaemonSet(), record(enabled), policy(nil, nil)}, defaults), status: statusFail, want: []string{"deploys no model gateway"}},
		{name: "gateway not ready", objects: with([]client.Object{configureDaemonSet(), record(enabled), gateway(0), policy(nil, nil)}, defaults), status: statusFail, want: []string{"sympozium-model-gateway", "not ready", "logs deploy/sympozium-model-gateway"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testDoctor(t, append([]client.Object{kvmNode("node-a")}, tc.objects...)...)
			f := d.checkMediation(context.Background(), tc.release)
			var text strings.Builder
			printDoctorReport(&text, doctorReport{Findings: []doctorFinding{f}})
			if f.Status != tc.status {
				t.Fatalf("got %s:\n%s", f.Status, text.String())
			}
			for _, want := range tc.want {
				if !strings.Contains(text.String(), want) {
					t.Fatalf("missing %q in\n%s", want, text.String())
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(text.String(), absent) {
					t.Fatalf("unexpected %q in\n%s", absent, text.String())
				}
			}
		})
	}
	// It is part of every run, after the fleet checks.
	d := testDoctor(t, kvmNode("node-a"))
	if f := findingOf(t, d.run(context.Background()), "Mediated model access"); f.Status != statusPass {
		t.Fatalf("a cluster without a fleet: %+v", f)
	}
}

// The CA key is discarded, so the certificates cannot be renewed in place:
// doctor warns in their last 60 days and fails once they lapse.
func TestDoctorMediationCertificateExpiry(t *testing.T) {
	minted := func(validity time.Duration) []client.Object {
		t.Helper()
		store := preflightClient(t, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: helmNamespace}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: cellnSystemNamespace}})
		gw, rc := cellninstall.DefaultMediationHosts("sympozium", helmNamespace)
		// Minted so that doctorNow falls inside the certificates' lifetime.
		now := func() time.Time { return doctorNow.Add(-time.Hour) }
		if _, err := cellninstall.PrepareMediationTrust(context.Background(), store, cellninstall.MediationOptions{ClusterID: "c", SystemNamespace: helmNamespace, GatewayHosts: gw, ReceiverHosts: rc, Validity: validity, Now: now}); err != nil {
			t.Fatal(err)
		}
		var out []client.Object
		for _, key := range []struct{ ns, name string }{{helmNamespace, cellninstall.MediationControllerSecret}, {helmNamespace, cellninstall.MediationGatewaySecret}, {cellnSystemNamespace, cellninstall.MediationNodeSecret}} {
			var s corev1.Secret
			if err := store.Get(context.Background(), client.ObjectKey{Namespace: key.ns, Name: key.name}, &s); err != nil {
				t.Fatal(err)
			}
			s.ResourceVersion = ""
			out = append(out, &s)
		}
		for _, ns := range []string{helmNamespace, cellnSystemNamespace} {
			var cm corev1.ConfigMap
			if err := store.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: cellninstall.MediationTrustConfigMap}, &cm); err != nil {
				t.Fatal(err)
			}
			cm.ResourceVersion = ""
			out = append(out, &cm)
		}
		return out
	}
	record := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: cellnSystemNamespace, Name: cellninstall.MediationRecordConfigMap}, Data: map[string]string{"mediation.json": `{"enabled":true}`}}
	gateway := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: helmNamespace, Name: "sympozium-model-gateway", Generation: 1}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, AvailableReplicas: 1}}
	for _, tc := range []struct {
		name     string
		validity time.Duration
		status   string
		want     string
	}{
		{name: "the default ten years", validity: cellninstall.DefaultMediationValidity, status: statusPass, want: "every Agent brings its own connection"},
		{name: "inside the last 60 days", validity: 30 * 24 * time.Hour, status: statusWarn, want: "mediation certificates expire"},
		{name: "expired", validity: 30 * time.Minute, status: statusFail, want: "expired"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := append([]client.Object{kvmNode("node-a"), configureDaemonSet(), record, gateway}, minted(tc.validity)...)
			d := testDoctor(t, objects...)
			f := d.checkMediation(context.Background(), nil)
			var text strings.Builder
			printDoctorReport(&text, doctorReport{Findings: []doctorFinding{f}})
			if f.Status != tc.status || !strings.Contains(text.String(), tc.want) {
				t.Fatalf("got %s, want %s mentioning %q:\n%s", f.Status, tc.status, tc.want, text.String())
			}
			if tc.name == "the default ten years" && strings.Contains(text.String(), "expire") {
				t.Fatalf("a ten-year certificate warned:\n%s", text.String())
			}
		})
	}
}
