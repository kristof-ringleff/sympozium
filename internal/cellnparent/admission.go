// Package cellnparent holds the enduring-run contracts shared by the API
// server and the controller: the turn record (NewTurn, BindTurn), the single
// "may this parent take another turn" rule (TurnReadiness) and conversation
// continuation (Continuation, Transcript). The native parent plane that once
// provisioned and drove parents from this package was removed in 0.12; every
// Celln run now executes through the scoped platform path (internal/cellnscoped).
// The parent binding validation here still reads status.cellnParent so the API
// can explain a run that plane left behind.
package cellnparent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"unicode"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/celln"
)

var (
	// ErrReconcile reports recorded state that no longer matches the original
	// parent identity; callers reconcile rather than invent a replacement.
	ErrReconcile = errors.New("parent outcome requires reconciliation; preserve incarnation and turn ID")
	hashPattern  = regexp.MustCompile(`^blake3:[0-9a-f]{64}$`)
)

// SpecDigest binds the complete typed run spec, including lifecycle, catalogue
// revisions, initial task and limits. This is Go JSON encoding, not RFC JCS.
func SpecDigest(spec api.AgentRunSpec) (string, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	if len(raw) > 262144 {
		return "", fmt.Errorf("parent run spec exceeds bound")
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum), nil
}

// ValidateAdmission checks that a recorded parent binding still describes this
// run: same UID, same complete intent, well-formed identities and owner origin.
// Never construct a binding from tenant annotations or reuse a one-shot grant.
func ValidateAdmission(run *api.AgentRun, approval api.CellnParentBinding) error {
	if run.Status.Phase != "" && run.Status.Phase != api.AgentRunPhasePending && run.Status.Phase != api.AgentRunPhaseRunning {
		return fmt.Errorf("parent startup cannot admit a terminal or unrelated lifecycle phase")
	}
	if run.UID == "" || run.DeletionTimestamp != nil || run.Spec.ValidateLifecycle() != "" || (run.Spec.ExecutionLifecycle != "enduring" && !run.Spec.PlatformOneShotShape()) {
		return fmt.Errorf("parent admission requires a live enduring AgentRun or a platform one-shot")
	}
	digest, err := SpecDigest(run.Spec)
	if err != nil {
		return err
	}
	if approval.RunUID != string(run.UID) || approval.SpecSHA256 != digest || !hashPattern.MatchString(approval.LaunchProfile) || !hashPattern.MatchString(approval.Incarnation) || approval.Principal == "" || len(approval.Principal) > 512 {
		return fmt.Errorf("parent approval does not bind run identity and complete intent")
	}
	for _, ch := range approval.Principal {
		if unicode.IsControl(ch) {
			return fmt.Errorf("invalid parent principal")
		}
	}
	if err := validateOwnerOrigin(approval.Target); err != nil {
		return err
	}
	if run.Status.CellnParent != nil && run.Status.CellnParent.Binding != approval {
		return fmt.Errorf("frozen parent admission changed; reconcile original owner")
	}
	if run.Status.CellnIssuance != nil || run.Status.JobName != "" || run.Status.DeploymentName != "" {
		return fmt.Errorf("parent admission cannot replace another execution path")
	}
	return nil
}

func validateOwnerOrigin(target string) error {
	if len(target) > 2048 {
		return fmt.Errorf("parent approval requires an exact owner origin")
	}
	c, err := celln.New(celln.Config{BaseURL: target, AllowInsecure: os.Getenv("CELLN_ALLOW_INSECURE_HTTP") == "true"})
	if err != nil {
		return fmt.Errorf("parent owner requires protected transport: %w", err)
	}
	c.Close()
	return nil
}
