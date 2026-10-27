package ipc

import (
	"strings"
	"testing"
)

func TestRetryCard_FallsBackWhenTheGateGaveNoReason(t *testing.T) {
	card := RetryCard(&GateVerdict{Attempt: 1, Action: GateVerdictActionContinue}, 2)
	if !strings.Contains(card, "without giving a reason") {
		t.Errorf("card leaves the model guessing:\n%s", card)
	}
	if strings.Contains(card, "### Gate Output") {
		t.Error("an empty gate output must not get a heading")
	}
	if !strings.Contains(card, "## Attempt 2") {
		t.Errorf("card does not say which attempt this is:\n%s", card)
	}
}

func TestRetryCard_CarriesTheGatesFeedback(t *testing.T) {
	card := RetryCard(&GateVerdict{
		Attempt: 1, Action: GateVerdictActionContinue,
		Reason: "tests fail", Output: "FAIL: 3 cases", MaxAttempts: 3,
	}, 2)
	for _, want := range []string{"## Attempt 2 of 3", "### Why It Was Rejected\ntests fail", "### Gate Output\nFAIL: 3 cases"} {
		if !strings.Contains(card, want) {
			t.Errorf("card is missing %q:\n%s", want, card)
		}
	}
}
