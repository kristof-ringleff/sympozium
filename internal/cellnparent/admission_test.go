package cellnparent

import (
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const testID = "blake3:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func admissionFixture(t *testing.T) (*api.AgentRun, api.CellnParentBinding) {
	t.Helper()
	run := &api.AgentRun{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "run", UID: "run-uid"}, Spec: api.AgentRunSpec{
		Backend: "celln", ExecutionLifecycle: "enduring", CellnSelection: &api.CellnCatalogueSelection{},
		Enduring: &api.EnduringRunSpec{LeaseSeconds: 60, MaxTurns: 2, MaxModelRequests: 2, MaxOutputTokens: 1024},
	}}
	digest, err := SpecDigest(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	return run, api.CellnParentBinding{Target: "https://owner.example", Principal: "tenant/run-uid", RunUID: string(run.UID), SpecSHA256: digest, LaunchProfile: testID, Incarnation: testID}
}

func TestAdmissionRejectsRunReuseIntentChangesAndRetargeting(t *testing.T) {
	run, approval := admissionFixture(t)
	if err := ValidateAdmission(run, approval); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*api.AgentRun){
		func(r *api.AgentRun) { r.UID = "replacement-uid" },
		func(r *api.AgentRun) { r.Spec.Task = &api.TaskSpec{Prompt: "different input"} },
		func(r *api.AgentRun) { r.Spec.Enduring.MaxTurns++ },
		func(r *api.AgentRun) { r.Spec.CellnSelection.RuntimeRef = "other-runtime" },
		func(r *api.AgentRun) { r.Status.JobName = "existing-job" },
	} {
		changed := run.DeepCopy()
		mutate(changed)
		if ValidateAdmission(changed, approval) == nil {
			t.Fatal("changed run accepted")
		}
	}
	run.Status.CellnParent = &api.CellnParentStatus{Binding: approval}
	changed := approval
	changed.Target = "https://other-owner.example"
	if ValidateAdmission(run, changed) == nil {
		t.Fatal("owner changed after freeze")
	}
	changed = approval
	changed.Principal = "another-tenant"
	if ValidateAdmission(run, changed) == nil {
		t.Fatal("principal changed after freeze")
	}
}

func TestOwnerOriginRequiresProtectedTransport(t *testing.T) {
	const target = "http://celln-router.celln-system.svc.cluster.local:8787"
	t.Setenv("CELLN_ALLOW_INSECURE_HTTP", "false")
	if validateOwnerOrigin(target) == nil {
		t.Fatal("unacknowledged plaintext admitted")
	}
	t.Setenv("CELLN_ALLOW_INSECURE_HTTP", "true")
	if err := validateOwnerOrigin(target); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"/v1/parents", "?", "?token=x", "#fragment"} {
		if validateOwnerOrigin(target+suffix) == nil {
			t.Fatal("non-origin owner admitted")
		}
	}
}
