//go:build system

package system_test

import (
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// The gate verdict endpoint's happy path is NOT covered here on purpose.
// It only accepts a verdict while the run is PostRunning, and the live
// AgentRun controller in this suite keeps failing a run out of that phase
// ("postRun Job name missing") because envtest has no kubelet to run the
// postRun Job. The window is therefore not reliably reachable: the test
// passes alone and fails under the full suite. The happy path, the per-run
// token, the global token, the 401/409/413 paths and the retry action are all
// covered deterministically in internal/apiserver/gate_verdict_test.go.
//
// What remains here is the half that does not depend on phase: request
// validation happens before the run is fetched.
func TestGateVerdictEndpointRejectsAnUnknownAction(t *testing.T) {
	ns := createTestNamespace(t)

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "sys-run-gate-bad", Namespace: ns},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "sys-gate-agent",
			Task:     sympoziumv1alpha1.NewStringTask("Verify verdict validation"),
			Lifecycle: &sympoziumv1alpha1.LifecycleHooks{
				PostRun: []sympoziumv1alpha1.LifecycleHookContainer{
					{Name: "check", Image: "check:latest", Gate: true},
				},
			},
		},
	}
	if err := k8sClient.Create(testCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(testCtx, run) })

	rec := httpDo(t, http.MethodPost,
		"/api/v1/runs/"+run.Name+"/gate-verdict?namespace="+ns,
		map[string]string{"action": "escalate"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
