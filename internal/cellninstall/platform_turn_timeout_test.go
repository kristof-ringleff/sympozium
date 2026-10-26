package cellninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/zeebo/blake3"
	"k8s.io/apimachinery/pkg/types"
)

// rewriteWorker edits a materialized backend's worker in both published files
// and re-seals the receipt, as a node configuring another cap would.
func rewriteWorker(t *testing.T, dir, backend string, native, catalogue func(capabilities, limits map[string]any)) {
	t.Helper()
	sub := filepath.Join(dir, backend)
	hashes := map[string]string{}
	for file, edit := range map[string]func(map[string]any){
		"native-template.json": func(doc map[string]any) {
			native(doc["worker"].(map[string]any)["capabilities"].(map[string]any), nil)
		},
		"catalogue.json": func(doc map[string]any) {
			catalogue(nil, doc["worker"].(map[string]any)["limits"].(map[string]any))
		},
	} {
		raw, err := os.ReadFile(filepath.Join(sub, file))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		edit(doc)
		raw, _ = json.Marshal(doc)
		if err := os.WriteFile(filepath.Join(sub, file), raw, 0600); err != nil {
			t.Fatal(err)
		}
		hashes[file] = fmt.Sprintf("blake3:%x", blake3.Sum256(raw))
	}
	var configured map[string]any
	raw, _ := os.ReadFile(filepath.Join(sub, "configured.json"))
	if err := json.Unmarshal(raw, &configured); err != nil {
		t.Fatal(err)
	}
	configured["nativeTemplateHash"], configured["catalogueHash"] = hashes["native-template.json"], hashes["catalogue.json"]
	raw, _ = json.Marshal(configured)
	if err := os.WriteFile(filepath.Join(sub, "configured.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// A worker request whose lifetime the catalogue does not state is refused:
// the profile's limits and the request must grant one turn the same time.
func TestInstallPlatformRefusesWorkerLifetimeTheCatalogueDoesNotState(t *testing.T) {
	dir, packageHash, principal := starterConfiguration(t)
	rewriteWorker(t, dir, "native",
		func(capabilities, _ map[string]any) { capabilities["timeoutMs"] = 240000 },
		func(_, _ map[string]any) {})
	o := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out"), Scope: "trial", ClusterID: "cluster-uid", PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
	err := InstallPlatform(context.Background(), platformInstallStore(t), o)
	if err == nil || !strings.Contains(err.Error(), "bounded lifetime matching its catalogue") {
		t.Fatalf("err = %v, want the lifetime refusal", err)
	}
	// Stated in both, a longer lifetime is the scope's turn ceiling.
	dir, packageHash, principal = starterConfiguration(t)
	rewriteWorker(t, dir, "native",
		func(capabilities, _ map[string]any) { capabilities["timeoutMs"] = 240000 },
		func(_, limits map[string]any) { limits["timeoutMillis"] = 240000 })
	o.ConfigurationDir, o.PackageHash, o.Principal, o.OutputDir = dir, packageHash, principal, filepath.Join(t.TempDir(), "out-long")
	store := platformInstallStore(t)
	ctx := context.Background()
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatalf("a longer turn lifetime was refused: %v", err)
	}
	_, policyName, _ := PlatformCatalogueNames("trial")
	var policy api.CellnExecutionPolicy
	if err := store.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil || policy.Spec.Ceilings.MaxTurnSeconds != 240 {
		t.Fatalf("maxTurnSeconds: %v %+v", err, policy.Spec.Ceilings)
	}
	var profile api.CellnRuntimeProfile
	if err := store.Get(ctx, types.NamespacedName{Name: PlatformProfileName("trial", "native")}, &profile); err != nil || profile.Spec.Limits.TimeoutMillis != 240000 {
		t.Fatalf("profile timeoutMillis: %v %d", err, profile.Spec.Limits.TimeoutMillis)
	}
}
