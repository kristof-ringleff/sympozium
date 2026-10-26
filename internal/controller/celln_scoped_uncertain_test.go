package controller

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// A run whose owner keeps reporting its context unavailable ends after the
// grace period; a blip, another reason or another condition state does not.
func TestScopedUncertainEndsTheRunAfterGrace(t *testing.T) {
	now := time.Now()
	run := func(status metav1.ConditionStatus, reason string, since time.Duration) *api.AgentRun {
		return &api.AgentRun{Status: api.AgentRunStatus{Conditions: []metav1.Condition{{
			Type: "CellnScopedExecution", Status: status, Reason: reason, LastTransitionTime: metav1.NewTime(now.Add(-since)),
		}}}}
	}
	for name, tc := range map[string]struct {
		run  *api.AgentRun
		ends bool
	}{
		"sustained":           {run(metav1.ConditionUnknown, "NativeOwnerUncertain", 3*time.Minute), true},
		"blip":                {run(metav1.ConditionUnknown, "NativeOwnerUncertain", 30*time.Second), false},
		"other uncertainty":   {run(metav1.ConditionUnknown, "ExecutionOutcomeUnconfirmed", 10*time.Minute), false},
		"running":             {run(metav1.ConditionTrue, "EnduringParentReady", 10*time.Minute), false},
		"no condition at all": {&api.AgentRun{}, false},
	} {
		if got := scopedUncertainExpired(tc.run, now); got != tc.ends {
			t.Errorf("%s: ends=%v, want %v", name, got, tc.ends)
		}
	}
}

// An enduring conversation that answered and whose parent deadline has passed
// ended by reaching its lease; anything else that loses its parent did not.
func TestScopedLeaseElapsedOnlyForAnsweredEnduringRunsPastTheirDeadline(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	run := func(lifecycle, result string) *api.AgentRun {
		return &api.AgentRun{Spec: api.AgentRunSpec{ExecutionLifecycle: lifecycle}, Status: api.AgentRunStatus{Result: result}}
	}
	for name, tc := range map[string]struct {
		run      *api.AgentRun
		deadline int64
		elapsed  bool
	}{
		"answered, past the deadline":   {run("enduring", "pong"), now.Unix() - 60, true},
		"answered, at the deadline":     {run("enduring", "pong"), now.Unix(), true},
		"answered, before the deadline": {run("enduring", "pong"), now.Unix() + 60, false},
		"never answered":                {run("enduring", ""), now.Unix() - 60, false},
		"one-shot":                      {run("one-shot", "pong"), now.Unix() - 60, false},
		"no deadline recorded":          {run("enduring", "pong"), 0, false},
	} {
		if got := scopedLeaseElapsed(tc.run, tc.deadline, now); got != tc.elapsed {
			t.Errorf("%s: elapsed=%v, want %v", name, got, tc.elapsed)
		}
	}
}

func TestScopedEndedSummaryExposesOnlyMappedCauses(t *testing.T) {
	if got := scopedEndedSummary("turn handler failed; parent context lost: aggregate parent budget exhausted"); !strings.Contains(got, "AUTH_BUDGET_EXHAUSTED") || strings.Contains(got, "turn handler") {
		t.Fatalf("budget: %s", got)
	}
	if got := scopedEndedSummary("/var/lib/secret/path exploded at 0xdeadbeef"); strings.Contains(got, "deadbeef") || strings.Contains(got, "/var/lib") {
		t.Fatalf("raw native diagnostics leaked: %s", got)
	}
}

// A failed turn's text names the owner's cause and the harness's own error,
// never the event stream the worker printed in between.
func TestBoundedTurnFailureKeepsCauseAndHarnessError(t *testing.T) {
	text := "Turn failed; no result committed: child refused: guest exited with code 1: CELLN_HARNESS_EVENT {\"type\":\"model\"} CELLN_HARNESS_EVENT {\"type\":\"tool\",\"result\":{\"content\":\"secret-looking page text\"}} CELLN_HARNESS_ERROR tool call budget exhausted"
	got := boundedTurnFailure(text)
	want := "Turn failed; no result committed: child refused: guest exited with code 1: tool call budget exhausted"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := boundedTurnFailure("Turn refused; no worker started: " + strings.Repeat("x", 600)); len(got) > 520 || !strings.HasSuffix(got, "…") {
		t.Fatalf("not bounded: %d", len(got))
	}
}
