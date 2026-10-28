package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	fakerest "k8s.io/client-go/rest/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller/taskmodes"
	"github.com/sympozium-ai/sympozium/internal/ipc"
)

// podLogs serves each pod's agent log from byPod. client-go's fake clientset
// answers every GetLogs with "fake logs", and the controller reads an
// attempt's result from the log.
type podLogs struct {
	kubernetes.Interface
	byPod map[string]string
}

func (c *podLogs) CoreV1() corev1client.CoreV1Interface {
	return podLogsCoreV1{CoreV1Interface: c.Interface.CoreV1(), logs: c}
}

type podLogsCoreV1 struct {
	corev1client.CoreV1Interface
	logs *podLogs
}

func (c podLogsCoreV1) Pods(namespace string) corev1client.PodInterface {
	return podLogsPods{PodInterface: c.CoreV1Interface.Pods(namespace), logs: c.logs}
}

type podLogsPods struct {
	corev1client.PodInterface
	logs *podLogs
}

func (p podLogsPods) GetLogs(name string, _ *corev1.PodLogOptions) *rest.Request {
	body := p.logs.byPod[name]
	return (&fakerest.RESTClient{
		Client: fakerest.CreateHTTPClient(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
		NegotiatedSerializer: scheme.Codecs.WithoutConversion(),
		GroupVersion:         corev1.SchemeGroupVersion,
	}).Request()
}

// harnessAttemptPod is the pod attempt's Job ran: its agent container has
// exited, and the ipc-bridge sidecar may still be running.
func harnessAttemptPod(run *sympoziumv1alpha1.AgentRun, job *batchv1.Job, phase corev1.PodPhase) *corev1.Pod {
	bridge := corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	if phase == corev1.PodSucceeded {
		bridge = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            job.Name + "-pod",
			Namespace:       run.Namespace,
			Labels:          map[string]string{"sympozium.ai/agent-run": run.Name},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(job, batchv1.SchemeGroupVersion.WithKind("Job"))},
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent"}, {Name: "ipc-bridge"}}},
		Status: corev1.PodStatus{
			Phase: phase,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "agent", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
				{Name: "ipc-bridge", State: bridge},
			},
		},
	}
}

func harnessResultLog(t *testing.T, response string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"status": "success", "response": response})
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return resultMarkerStart + string(raw) + resultMarkerEnd
}

// A resuming harness's retry chain, driven through Reconcile from the first
// attempt's Job to the approved second attempt. Each attempt runs in a Job pod
// of its own, the gate starts only once that pod has ended, and the second pod
// gets the verdict, the same HOME and no preRun hooks.
func TestReconcile_AResumingHarnessRetriesInANewPodOfTheSameRun(t *testing.T) {
	ctx := context.Background()

	agent := parityAgent()
	agent.Spec.PolicyRef = "harness-ok"
	agent.Spec.RuntimeRef = "my-harness"
	policy := policyWithRegistries("harness-ok")
	runtime := runtimeSpec(runtimeTestImage, "resume")
	runtime.Name = "my-harness"
	runtime.Namespace = "default"
	runtime.Spec.ContractVersion = taskmodes.HarnessContractVersion
	runtime.Status.Conditions = []metav1.Condition{{
		Type: sympoziumv1alpha1.AgentRuntimeReadyCondition, Status: metav1.ConditionTrue, Reason: "Ready",
	}}

	run := parityRun()
	run.Spec.Lifecycle = &sympoziumv1alpha1.LifecycleHooks{
		GateDefault: "block",
		Retry:       inPlace(&sympoziumv1alpha1.RetrySpec{MaxAttempts: 3, On: []string{"gate"}}),
		PreRun:      []sympoziumv1alpha1.LifecycleHookContainer{{Name: "clone", Image: "clone:latest"}},
		PostRun:     []sympoziumv1alpha1.LifecycleHookContainer{{Name: "check", Image: "check:latest", Gate: true}},
	}

	r := newAgentRunTestReconciler(t, run, agent, policy, runtime)
	r.EventBus = &recordingEventBus{}
	logs := &podLogs{Interface: kubefake.NewClientset(), byPod: map[string]string{}}
	r.Clientset = logs

	reconcile := func() *sympoziumv1alpha1.AgentRun {
		t.Helper()
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		got := getRun(t, r, run.Name)
		if got.Status.Error != "" {
			t.Fatalf("run failed in phase %s: %s", got.Status.Phase, got.Status.Error)
		}
		return got
	}
	reconcileUntil := func(what string, done func(*sympoziumv1alpha1.AgentRun) bool) *sympoziumv1alpha1.AgentRun {
		t.Helper()
		for range 10 {
			if got := reconcile(); done(got) {
				return got
			}
		}
		got := getRun(t, r, run.Name)
		t.Fatalf("never reached %s: phase = %s attempt = %d job = %q", what, got.Status.Phase, got.Status.Attempt, got.Status.JobName)
		return nil
	}
	getJob := func(name string) *batchv1.Job {
		t.Helper()
		var job batchv1.Job
		if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, &job); err != nil {
			t.Fatalf("get Job %s: %v", name, err)
		}
		return &job
	}
	setPodPhase := func(pod *corev1.Pod, job *batchv1.Job, phase corev1.PodPhase) {
		t.Helper()
		pod.Status = harnessAttemptPod(run, job, phase).Status
		if err := r.Status().Update(ctx, pod); err != nil {
			t.Fatalf("update pod %s: %v", pod.Name, err)
		}
	}
	// The verdict as the API's gate-verdict handler writes it, stamped with
	// the attempt it was made against.
	submitVerdict := func(action, reason, response string) {
		t.Helper()
		current := getRun(t, r, run.Name)
		attempt := currentAttempt(current)
		raw, err := json.Marshal(map[string]any{"action": action, "reason": reason, "response": response, "attempt": attempt})
		if err != nil {
			t.Fatalf("marshal verdict: %v", err)
		}
		if current.Annotations == nil {
			current.Annotations = map[string]string{}
		}
		current.Annotations["sympozium.ai/gate-verdict"] = string(raw)
		current.Annotations[gateVerdictAttemptAnnotation] = fmt.Sprint(attempt)
		if err := r.Update(ctx, current); err != nil {
			t.Fatalf("submit verdict: %v", err)
		}
	}
	// The gate hook reports, then its Job completes.
	finishGate := func(name string) {
		t.Helper()
		job := getJob(name)
		job.Status.Succeeded = 1
		if err := r.Status().Update(ctx, job); err != nil {
			t.Fatalf("complete gate Job %s: %v", name, err)
		}
	}

	// ── attempt 1 ──
	got := reconcileUntil("attempt 1 running", func(ar *sympoziumv1alpha1.AgentRun) bool {
		return ar.Status.Phase == sympoziumv1alpha1.AgentRunPhaseRunning
	})
	firstJob := getJob(run.Name)
	if got.Status.JobName != firstJob.Name {
		t.Fatalf("status.jobName = %q, want %q", got.Status.JobName, firstJob.Name)
	}
	if !hasInitContainer(firstJob, "pre-clone") {
		t.Error("the first attempt's pod does not run the preRun hook")
	}

	// The agent container exits while the ipc-bridge still runs. The attempt
	// is recorded, but nothing may judge /workspace yet.
	firstPod := harnessAttemptPod(run, firstJob, corev1.PodRunning)
	if err := r.Create(ctx, firstPod); err != nil {
		t.Fatalf("create attempt 1 pod: %v", err)
	}
	logs.byPod[firstPod.Name] = harnessResultLog(t, "attempt 1: slugify without the empty case")

	got = reconcileUntil("attempt 1 awaiting its gate", func(ar *sympoziumv1alpha1.AgentRun) bool {
		return ar.Status.Phase == sympoziumv1alpha1.AgentRunPhaseAwaitingGate
	})
	gate1 := got.Status.PostRunJobName
	if gate1 != gateJobNameForAttempt(run, 1) {
		t.Fatalf("postRunJobName = %q, want %q", gate1, gateJobNameForAttempt(run, 1))
	}
	reconcile()
	if jobExists(t, r, gate1) {
		t.Fatal("the gate started while a container of the attempt's pod was still running")
	}

	setPodPhase(firstPod, firstJob, corev1.PodSucceeded)
	reconcile()
	if !jobExists(t, r, gate1) {
		t.Fatal("the gate did not start once the attempt's pod had ended")
	}

	// ── the gate asks for a retry ──
	finishGate(gate1)
	submitVerdict("retry", "npm test failed", "not ok 2 - hidden: an empty result becomes \"untitled\"")
	got = reconcileUntil("attempt 2 pending", func(ar *sympoziumv1alpha1.AgentRun) bool {
		return ar.Status.Attempt == 2
	})
	if got.Status.Phase != sympoziumv1alpha1.AgentRunPhasePending {
		t.Fatalf("phase = %s after a retry verdict, want the run back in Pending for its next pod", got.Status.Phase)
	}

	verdicts := gateVerdicts(t, r, run)
	if len(verdicts) != 1 || verdicts[0].Attempt != 1 || verdicts[0].Action != ipc.GateVerdictActionContinue {
		t.Fatalf("gate verdicts = %+v, want one continue verdict for attempt 1", verdicts)
	}
	for _, want := range []string{"## Attempt 2 of 3", "npm test failed", "an empty result becomes"} {
		if !strings.Contains(verdicts[0].Card, want) {
			t.Errorf("the card the harness passes on is missing %q:\n%s", want, verdicts[0].Card)
		}
	}

	// ── attempt 2: a new Job pod of the same run ──
	got = reconcileUntil("attempt 2 running", func(ar *sympoziumv1alpha1.AgentRun) bool {
		return ar.Status.Phase == sympoziumv1alpha1.AgentRunPhaseRunning
	})
	secondJob := getJob(agentJobName(got))
	if secondJob.Name == firstJob.Name || got.Status.JobName != secondJob.Name {
		t.Fatalf("attempt 2 Job = %q, status.jobName = %q, want a Job of its own", secondJob.Name, got.Status.JobName)
	}
	if hasInitContainer(secondJob, "pre-clone") {
		t.Error("attempt 2 ran the preRun hook again over the workspace attempt 1 left")
	}
	spec := secondJob.Spec.Template.Spec
	if home := findVolume(t, spec.Volumes, "harness-home"); home.PersistentVolumeClaim == nil || home.PersistentVolumeClaim.ClaimName != harnessHomePVCName(run) {
		t.Errorf("attempt 2 HOME = %+v, want the run's PVC %s, which holds the session", home.VolumeSource, harnessHomePVCName(run))
	}
	findVolume(t, spec.Volumes, gateChannelVolume)
	if env := agentEnv(secondJob); env["GATE_IN_PLACE_START_ATTEMPT"] != "2" {
		t.Errorf("GATE_IN_PLACE_START_ATTEMPT = %q, want 2", env["GATE_IN_PLACE_START_ATTEMPT"])
	}

	// Attempt 1's pod is still listed. The run must follow attempt 2's.
	secondPod := harnessAttemptPod(run, secondJob, corev1.PodSucceeded)
	if err := r.Create(ctx, secondPod); err != nil {
		t.Fatalf("create attempt 2 pod: %v", err)
	}
	logs.byPod[secondPod.Name] = harnessResultLog(t, "attempt 2: slugify returns \"untitled\" when empty")

	got = reconcileUntil("attempt 2 awaiting its gate", func(ar *sympoziumv1alpha1.AgentRun) bool {
		return ar.Status.Phase == sympoziumv1alpha1.AgentRunPhaseAwaitingGate
	})
	if got.Status.PodName != secondPod.Name {
		t.Errorf("status.podName = %q, want attempt 2's pod %q", got.Status.PodName, secondPod.Name)
	}
	if !strings.HasPrefix(got.Status.Result, "attempt 2:") {
		t.Errorf("result = %q, want attempt 2's", got.Status.Result)
	}
	gate2 := gateJobNameForAttempt(run, 2)
	reconcileUntil("attempt 2's gate Job", func(*sympoziumv1alpha1.AgentRun) bool { return jobExists(t, r, gate2) })

	// ── the gate approves ──
	finishGate(gate2)
	submitVerdict("approve", "npm test passed", "")
	got = reconcileUntil("the run succeeded", func(ar *sympoziumv1alpha1.AgentRun) bool {
		return ar.Status.Phase == sympoziumv1alpha1.AgentRunPhaseSucceeded
	})
	if !strings.HasPrefix(got.Status.Result, "attempt 2:") {
		t.Errorf("result = %q, want attempt 2's", got.Status.Result)
	}
	reconcile() // reconcileCompleted
	err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: harnessHomePVCName(run)}, &corev1.PersistentVolumeClaim{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("harness HOME PVC after the run: err = %v, want it deleted", err)
	}
}

func hasInitContainer(job *batchv1.Job, name string) bool {
	for _, c := range job.Spec.Template.Spec.InitContainers {
		if c.Name == name {
			return true
		}
	}
	return false
}

func agentEnv(job *batchv1.Job) map[string]string {
	env := map[string]string{}
	for _, c := range job.Spec.Template.Spec.Containers {
		if c.Name != "agent" {
			continue
		}
		for _, e := range c.Env {
			env[e.Name] = e.Value
		}
	}
	return env
}
