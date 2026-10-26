package controller

import (
	"context"
	"github.com/go-logr/logr"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"testing"
)

// newTestCellnRun is a pending backend=celln run with a string task.
func newTestCellnRun(t *testing.T, name string, uid types.UID) *api.AgentRun {
	t.Helper()
	return &api.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: uid, Generation: 1},
		Spec:       api.AgentRunSpec{AgentRef: "my-instance", Backend: "celln", Task: api.NewStringTask("do stuff")},
	}
}

// reconcilePending is reached only for a run the scoped path did not select;
// enduring intent there has no plane left and must fail closed, never become a
// one-shot Job.
func TestEnduringIntentCannotFallThroughToOneShotExecution(t *testing.T) {
	run := newTestCellnRun(t, "enduring-refusal", "enduring-refusal-uid")
	run.Spec.CellnSelection = &api.CellnCatalogueSelection{}
	run.Spec.ExecutionLifecycle = "enduring"
	run.Spec.Enduring = &api.EnduringRunSpec{LeaseSeconds: 3600, MaxTurns: 10, MaxModelRequests: 20, MaxOutputTokens: 10240}
	r := newAgentRunTestReconciler(t, run)
	if _, err := r.reconcilePending(context.Background(), logr.Discard(), run); err != nil {
		t.Fatal(err)
	}
	var current api.AgentRun
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Phase != api.AgentRunPhaseFailed || current.Status.JobName != "" || current.Status.DeploymentName != "" {
		t.Fatalf("enduring intent was not refused before execution: %+v", current.Status)
	}
}

func TestEnduringCatalogueCannotFallBackWhenScopedDispatcherIsDisabled(t *testing.T) {
	run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "enduring", Namespace: "tenant", UID: types.UID("enduring-uid"), Generation: 1, Finalizers: []string{agentRunFinalizer}}, Spec: api.AgentRunSpec{AgentRef: "must-not-resolve-legacy", Backend: "celln", Task: api.NewStringTask("initial"), CellnSelection: &api.CellnCatalogueSelection{}, ExecutionLifecycle: "enduring", Enduring: &api.EnduringRunSpec{LeaseSeconds: 60, MaxTurns: 2, MaxModelRequests: 2, MaxOutputTokens: 1024}}, Status: api.AgentRunStatus{CellnOnly: true}}
	r := newAgentRunTestReconciler(t, run)
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequeueAfter == 0 {
		t.Fatal("disabled scoped enduring path did not remain fail-closed")
	}
	var current api.AgentRun
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &current); err != nil {
		t.Fatal(err)
	}
	if current.Status.CellnParent != nil || current.Status.JobName != "" || current.Status.Phase == api.AgentRunPhaseFailed {
		t.Fatalf("enduring catalogue intent escaped to replacement work: %+v", current.Status)
	}
}
