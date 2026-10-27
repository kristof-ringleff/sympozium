package ipc

import "fmt"

// RetryCard renders a continue verdict as the user turn that starts the next
// attempt. Much shorter than a successor's retry task: a successor has to be
// told the original task and shown its predecessor's output, a parked
// conversation holds both already.
//
// agent-runner renders it from the verdict it reads. A harness adapter cannot,
// so the controller renders the same text into GateVerdict.Card for it.
//
// The controller bounds the gate output before publishing, so nothing is
// truncated here.
func RetryCard(v *GateVerdict, nextAttempt int) string {
	reason := v.Reason
	if reason == "" {
		reason = "The response gate rejected that attempt without giving a reason."
	}

	header := fmt.Sprintf("## Attempt %d", nextAttempt)
	if v.MaxAttempts > 0 {
		header = fmt.Sprintf("## Attempt %d of %d", nextAttempt, v.MaxAttempts)
	}

	card := header + "\n\nYour previous response was rejected by the response gate. " +
		"Your earlier work is still here — the files you wrote and the tool results above are unchanged, " +
		"so correct that work rather than starting over.\n\n### Why It Was Rejected\n" + reason
	if v.Output != "" {
		card += "\n\n### Gate Output\n" + v.Output
	}
	return card
}
