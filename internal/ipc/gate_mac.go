package ipc

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// In-place gate retry moves two messages over channels that cannot say who
// wrote them:
//
//   - An attempt result reaches the controller as a marker in the agent
//     container's log. The runner also logs model-chosen strings there (tool
//     arguments, file paths, chat IDs), so the model can print a marker too.
//   - A verdict reaches the runner through the run's gate ConfigMap, mounted
//     read-only at GateDir. Nothing in the pod can write it, but anyone with
//     ConfigMap write access in the namespace can.
//
// So each message carries an HMAC under a per-run key that only the controller
// and the agent container hold. The key reaches the pod as one SecretKeyRef
// env var on the agent container. No tool can read it: /proc is outside every
// readable root, and the pod does not share its process namespace.

// GateKeyEnv is the agent container env var that carries the per-run gate key.
const GateKeyEnv = "GATE_ATTEMPT_KEY"

// AttemptMarkerStart and AttemptMarkerEnd bracket a signed AttemptResult on
// the runner's stdout. A parked pod never terminates, so this marker, not
// container exit, is how an attempt reports completion.
const (
	AttemptMarkerStart = "__SYMPOZIUM_ATTEMPT__"
	AttemptMarkerEnd   = "__SYMPOZIUM_ATTEMPT_END__"
)

// Sign sets r.MAC to the HMAC of r under key.
func (r *AttemptResult) Sign(key []byte) error {
	r.MAC = ""
	mac, err := gateMAC(key, r)
	r.MAC = mac
	return err
}

// Verify reports whether r.MAC is the HMAC of r under key. An empty key never
// verifies, so a run with no key fails closed.
func (r AttemptResult) Verify(key []byte) bool {
	got := r.MAC
	r.MAC = ""
	return verifyGateMAC(key, r, got)
}

// Sign sets v.MAC to the HMAC of v under key.
func (v *GateVerdict) Sign(key []byte) error {
	v.MAC = ""
	mac, err := gateMAC(key, v)
	v.MAC = mac
	return err
}

// Verify reports whether v.MAC is the HMAC of v under key. An empty key never
// verifies.
func (v GateVerdict) Verify(key []byte) bool {
	got := v.MAC
	v.MAC = ""
	return verifyGateMAC(key, v, got)
}

// gateMAC is the HMAC-SHA256 of v's JSON encoding. Both sides encode the same
// Go struct, so the bytes match without a separate canonical form.
func gateMAC(key []byte, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, key)
	m.Write(b)
	return hex.EncodeToString(m.Sum(nil)), nil
}

func verifyGateMAC(key []byte, v any, got string) bool {
	if len(key) == 0 || got == "" {
		return false
	}
	want, err := gateMAC(key, v)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(want), []byte(got))
}
