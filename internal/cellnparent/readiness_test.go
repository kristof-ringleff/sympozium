package cellnparent

import (
	"errors"
	"strings"
	"testing"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// readyParentFixture is a Running enduring parent whose initial turn committed
// with the given outcome and whose owner reports Ready for the current intent.
func readyParentFixture(t *testing.T, initialSucceeded bool, maxTurns int32) (*api.AgentRun, api.CellnParentBinding) {
	t.Helper()
	run, binding := admissionFixture(t)
	run.Spec.Enduring.MaxTurns = maxTurns
	digest, err := SpecDigest(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	binding.SpecSHA256 = digest
	run.Generation = 1
	run.Status.Phase = api.AgentRunPhaseRunning
	run.Status.Conditions = []metav1.Condition{{Type: ParentReadyCondition, Status: metav1.ConditionTrue, Reason: "Ready", ObservedGeneration: 1}}
	answer := "ready"
	if !initialSucceeded {
		answer = "Turn failed; no result committed: child refused: guest exited with code 1"
	}
	run.Status.CellnParent = &api.CellnParentStatus{Binding: binding, CreateAttempted: true, InitialTurn: &api.CellnParentTurnStatus{ID: "initial", Message: "hello", Child: testID, Attempted: true, Result: &api.CellnParentTurnResult{Succeeded: initialSucceeded, Answer: answer}}}
	return run, binding
}

func TestTurnReadiness(t *testing.T) {
	now := time.Date(2026, time.September, 19, 12, 0, 0, 0, time.UTC)
	owner := &api.CellnActiveTurn{Name: "mine", UID: "mine-uid"}
	for name, test := range map[string]struct {
		initialSucceeded bool
		change           func(*api.AgentRun)
		claimant         *api.CellnActiveTurn
		reason           string // "" means the turn is accepted
		message          string
		is               error
	}{
		"initial succeeded and parent ready": {initialSucceeded: true},
		// (a) a committed failure with a Ready parent is as open as a success.
		"initial failed and parent ready":        {},
		"later turn failed and parent ready":     {initialSucceeded: true, change: func(run *api.AgentRun) { run.Status.CellnParent.AcceptedTurns = 1 }},
		"initial failed, one follow-up spent":    {change: func(run *api.AgentRun) { run.Status.CellnParent.AcceptedTurns = 1 }},
		"initial failed, owner busy with a turn": {change: func(run *api.AgentRun) { run.Status.Conditions[0].Reason = "TurnActive" }},
		// (b) the owner's state is lost, stopped, refused or unknown.
		"context lost": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.OwnerOutcome = &api.CellnParentOwnerOutcome{Status: "ContextLost", ReachedReady: true}
		}, reason: TurnRefusedParentLost, message: "parent lost (ContextLost)"},
		"stopped": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.OwnerOutcome = &api.CellnParentOwnerOutcome{Status: "Stopped", ReachedReady: true}
		}, reason: TurnRefusedParentLost, message: "parent lost (Stopped)"},
		"teardown uncertain": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.OwnerOutcome = &api.CellnParentOwnerOutcome{Status: "TeardownUncertain"}
		}, reason: TurnRefusedParentLost, message: "parent lost (TeardownUncertain)"},
		"create refused": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.OwnerOutcome = &api.CellnParentOwnerOutcome{Status: OwnerCreateRefused}
		}, reason: TurnRefusedParentLost, message: "parent lost (CreateRefused)"},
		"reconciliation required": {change: func(run *api.AgentRun) {
			run.Status.Conditions[0].Status, run.Status.Conditions[0].Reason = metav1.ConditionFalse, "ReconciliationRequired"
		}, reason: TurnRefusedParentNotReady, message: "parent not ready (ReconciliationRequired)"},
		"never reported ready":       {change: func(run *api.AgentRun) { run.Status.Conditions = nil }, reason: TurnRefusedParentNotReady, message: "no readiness reported"},
		"readiness for older intent": {change: func(run *api.AgentRun) { run.Generation = 2 }, reason: TurnRefusedStaleReadiness, message: "does not match current run intent"},
		"intent changed under current readiness": {change: func(run *api.AgentRun) {
			run.Spec.Enduring.MaxTurns++
		}, reason: TurnRefusedChangedIntent, message: "does not match current run intent"},
		"run failed":   {change: func(run *api.AgentRun) { run.Status.Phase = api.AgentRunPhaseFailed }, reason: TurnRefusedNotRunning, message: "run is Failed"},
		"not admitted": {change: func(run *api.AgentRun) { run.Status.CellnParent = nil }, reason: TurnRefusedNotAdmitted, message: "not started"},
		"one-shot":     {change: func(run *api.AgentRun) { run.Spec.ExecutionLifecycle, run.Spec.Enduring = "", nil }, reason: TurnRefusedNotEnduring, message: "one-shot"},
		// (c) the initial turn is not committed yet.
		"initial turn not attempted": {change: func(run *api.AgentRun) { run.Status.CellnParent.InitialTurn = nil }, reason: TurnRefusedInitialPending, message: "initial turn still running"},
		"initial turn uncommitted":   {change: func(run *api.AgentRun) { run.Status.CellnParent.InitialTurn.Result = nil }, reason: TurnRefusedInitialPending, message: "initial turn still running"},
		// Serialization, lease, budget and deletion are unchanged by the outcome.
		"another turn active": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.ActiveTurn = &api.CellnActiveTurn{Name: "other", UID: "other-uid"}
		}, claimant: owner, reason: TurnRefusedActiveTurn, message: "already owns a different turn", is: ErrTurnBusy},
		"active turn, no claimant": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.ActiveTurn = &api.CellnActiveTurn{Name: "mine", UID: "mine-uid"}
		}, reason: TurnRefusedActiveTurn, message: "already owns a different turn", is: ErrTurnBusy},
		"slot owner re-claims after lease and readiness went": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.ActiveTurn = &api.CellnActiveTurn{Name: "mine", UID: "mine-uid"}
			run.Status.CellnParent.AdmittedAt = &metav1.Time{Time: now.Add(-time.Hour)}
			run.Status.Conditions[0].Status = metav1.ConditionFalse
		}, claimant: owner},
		"slot owner of a lost parent": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.ActiveTurn = &api.CellnActiveTurn{Name: "mine", UID: "mine-uid"}
			run.Status.CellnParent.OwnerOutcome = &api.CellnParentOwnerOutcome{Status: "ContextLost"}
		}, claimant: owner, reason: TurnRefusedParentLost, message: "parent lost (ContextLost)"},
		"lease expired": {change: func(run *api.AgentRun) {
			run.Status.CellnParent.AdmittedAt = &metav1.Time{Time: now.Add(-time.Hour)}
		}, reason: TurnRefusedLeaseExpired, message: "parent lease expired", is: ErrParentLeaseExpired},
		"max turns reached": {change: func(run *api.AgentRun) { run.Status.CellnParent.AcceptedTurns = 2 }, reason: TurnRefusedTurnLimitReached, message: "turn limit reached (3 of 3 turns used, including the initial turn)"},
		"max turns reached by a failed initial turn alone": {change: func(run *api.AgentRun) {
			run.Spec.Enduring.MaxTurns = 1
			run.Status.CellnParent.Binding.SpecSHA256, _ = SpecDigest(run.Spec)
		}, reason: TurnRefusedTurnLimitReached, message: "turn limit reached (1 of 1 turns used"},
		"deletion in progress": {change: func(run *api.AgentRun) {
			run.DeletionTimestamp = &metav1.Time{Time: now}
		}, reason: TurnRefusedDeleting, message: "being deleted"},
	} {
		t.Run(name, func(t *testing.T) {
			run, _ := readyParentFixture(t, test.initialSucceeded, 3)
			if test.change != nil {
				test.change(run)
			}
			err := TurnReadiness(run, test.claimant, now)
			if test.reason == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			var refusal *TurnRefusal
			if !errors.As(err, &refusal) || refusal.Reason != test.reason || !strings.Contains(refusal.Message, test.message) {
				t.Fatalf("got %v, want %s explaining %q", err, test.reason, test.message)
			}
			if test.is != nil && !errors.Is(err, test.is) {
				t.Fatalf("%v does not wrap %v", err, test.is)
			}
		})
	}
}

func TestTurnsUsedCountsTheInitialTurnWhateverItsOutcome(t *testing.T) {
	if TurnsUsed(nil) != 0 || TurnsUsed(&api.CellnParentStatus{}) != 0 {
		t.Fatal("an unstarted parent has used no turns")
	}
	failed := &api.CellnParentStatus{AcceptedTurns: 2, InitialTurn: &api.CellnParentTurnStatus{Result: &api.CellnParentTurnResult{Succeeded: false, Answer: "failed"}}}
	if TurnsUsed(failed) != 3 {
		t.Fatalf("TurnsUsed = %d, want 3: a failed initial turn still spent its turn", TurnsUsed(failed))
	}
}
