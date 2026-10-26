package charts

import (
	"os/exec"
	"testing"
)

// renderNativeParent renders the chart with --set values and returns the
// output (or helm's combined output on failure). The name predates the fleet;
// every chart test renders through it.
func renderNativeParent(t *testing.T, values []string) ([]byte, error) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm required for chart rendering")
	}
	args := []string{"template", "test", "sympozium"}
	for _, value := range values {
		args = append(args, "--set", value)
	}
	return exec.Command("helm", args...).CombinedOutput()
}
