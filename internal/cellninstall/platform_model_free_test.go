package cellninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zeebo/blake3"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
)

// writeModelFreeConfiguration materializes a backend as a node running a
// model-free Celln starter-configure publishes it: no model profile, no model
// in the receipt, and a worker template naming only the scoped alias.
func writeModelFreeConfiguration(t *testing.T, dir, name string) {
	t.Helper()
	sub := filepath.Join(dir, name)
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	for _, file := range []string{"catalogue.json", "native-template.json", "configured.json"} {
		raw, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		raw = []byte(strings.TrimSuffix(string(raw), "\n"))
		if file == "native-template.json" {
			var native map[string]any
			if err := json.Unmarshal(raw, &native); err != nil {
				t.Fatal(err)
			}
			delete(native, "modelProfile")
			template := native["template"].(map[string]any)
			template["model"], template["url"] = "mediated", "https://celln-model.invalid/v1/invoke"
			raw, _ = json.Marshal(native)
		}
		if file == "configured.json" {
			if err := json.Unmarshal(raw, &metadata); err != nil {
				t.Fatal(err)
			}
			delete(metadata, "modelProfile")
			delete(metadata, "model")
			continue
		}
		if err := os.WriteFile(filepath.Join(sub, file), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for file, key := range map[string]string{"catalogue.json": "catalogueHash", "native-template.json": "nativeTemplateHash"} {
		raw, _ := os.ReadFile(filepath.Join(sub, file))
		metadata[key] = fmt.Sprintf("blake3:%x", blake3.Sum256(raw))
	}
	raw, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(sub, "configured.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// writeKeyedConfiguration materializes a backend as a 0.11 node holding a
// fleet key published it: the testdata as it is, whose receipt and template
// name a model profile and a model. Its receipt is sealed consistently so the
// only thing the installer can object to is the model.
func writeKeyedConfiguration(t *testing.T, dir, name string) {
	t.Helper()
	sub := filepath.Join(dir, name)
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	for _, file := range []string{"catalogue.json", "native-template.json", "configured.json"} {
		raw, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		raw = []byte(strings.TrimSuffix(string(raw), "\n"))
		if file == "configured.json" {
			if err := json.Unmarshal(raw, &metadata); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(sub, file), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for file, key := range map[string]string{"catalogue.json": "catalogueHash", "native-template.json": "nativeTemplateHash"} {
		raw, _ := os.ReadFile(filepath.Join(sub, file))
		metadata[key] = fmt.Sprintf("blake3:%x", blake3.Sum256(raw))
	}
	raw, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(sub, "configured.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// The one model-free backend is read as mediation-only without any credential
// marker: its profile names no model profile, the policy carries no route,
// and a namespace gets only its runtime wrapper, never a shared Agent or
// host-profile connection. A node that still publishes a fleet-held model is
// refused.
func TestInstallPlatformOffersModelFreeBackendAsMediationOnly(t *testing.T) {
	ctx := context.Background()
	dir, packageHash, principal := starterConfiguration(t)
	b, err := readBackendConfiguration(dir, "native", packageHash, principal)
	if err != nil || b.name != "native" || b.configured.ModelProfile != "" || b.configured.Model.Provider != "" || b.configured.Model.Model != "" || b.native.ModelProfile != "" || b.harness.Model != "mediated" {
		t.Fatalf("model-free configuration: %+v %v", b, err)
	}
	store := platformInstallStore(t)
	options := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out"), Scope: "trial", ClusterID: "cluster-uid", PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
	if err := InstallPlatform(ctx, store, options); err != nil {
		t.Fatal(err)
	}
	var profile api.CellnRuntimeProfile
	if err := store.Get(ctx, types.NamespacedName{Name: PlatformProfileName("trial", "native")}, &profile); err != nil {
		t.Fatal(err)
	}
	if !cellnplatform.MediationOnly(&profile) || profile.Spec.Native.ModelProfile != "" {
		t.Fatalf("model-free profile: %+v", profile)
	}
	names := cellnplatform.WrapperNames("native")
	var runtime api.AgentRuntime
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: names.Runtime}, &runtime); err != nil || runtime.Spec.CellnProfileRef == nil || runtime.Spec.CellnProfileRef.Name != profile.Name {
		t.Fatalf("runtime wrapper: %v %+v", err, runtime.Spec)
	}
	var agent api.Agent
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: names.Agent}, &agent); err == nil {
		t.Fatalf("a model-free backend must not get a shared Agent: %+v", agent)
	}
	var connection api.ModelConnection
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: names.Connection}, &connection); err == nil {
		t.Fatalf("a model-free backend must not get a host-profile connection: %+v", connection.Spec)
	}
	// The receipt of a node that still calls a model itself is refused: since
	// 0.12 no Celln node holds a model.
	keyedDir := filepath.Join(t.TempDir(), "keyed")
	if err := os.Mkdir(keyedDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeKeyedConfiguration(t, keyedDir, "native")
	if _, err := readBackendConfiguration(keyedDir, "native", packageHash, principal); err == nil || !strings.Contains(err.Error(), "model-bound") {
		t.Fatalf("keyed configuration accepted: %v", err)
	}
	options.ConfigurationDir, options.OutputDir = keyedDir, filepath.Join(t.TempDir(), "keyed-out")
	if err := InstallPlatform(ctx, platformInstallStore(t), options); err == nil || !strings.Contains(err.Error(), "model-bound") {
		t.Fatalf("keyed configuration installed: %v", err)
	}
}

// A template that approves an insecure route is refused: a model-free node
// has no route for the approval to apply to.
func TestModelFreeConfigurationRefusesInsecureTemplate(t *testing.T) {
	dir, packageHash, principal := starterConfiguration(t)
	path := filepath.Join(dir, "native", "native-template.json")
	var native map[string]any
	raw, _ := os.ReadFile(path)
	_ = json.Unmarshal(raw, &native)
	native["template"].(map[string]any)["allow_insecure"] = true
	raw, _ = json.Marshal(native)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	receiptRaw, _ := os.ReadFile(filepath.Join(dir, "native", "configured.json"))
	_ = json.Unmarshal(receiptRaw, &receipt)
	receipt["nativeTemplateHash"] = fmt.Sprintf("blake3:%x", blake3.Sum256(raw))
	receiptRaw, _ = json.Marshal(receipt)
	if err := os.WriteFile(filepath.Join(dir, "native", "configured.json"), receiptRaw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBackendConfiguration(dir, "native", packageHash, principal); err == nil || !strings.Contains(err.Error(), "native template does not match") {
		t.Fatalf("insecure model-free template accepted: %v", err)
	}
}

// keyedScope seeds what a 0.11 installation of scope "trial" left in the
// cluster: a native profile and a second backend's (claude) profile, both
// holding a fleet model profile; the scope's policy naming both and carrying
// an operator's origin guardrail; tenant-a's managed wrappers (the runtime
// wrapper bound to the keyed revision, the shared Agent and the host-profile
// connection, and the retired backend's runtime wrapper); and the fleet's
// model credential Secret.
func keyedScope(t *testing.T, ctx context.Context, store client.Client, packageHash string) {
	t.Helper()
	const keyedRevision = "v1-keyed"
	managed := map[string]string{cellnplatform.ManagedByLabel: cellnplatform.ManagedByValue}
	profile := func(backend string) *api.CellnRuntimeProfile {
		return &api.CellnRuntimeProfile{
			ObjectMeta: metav1.ObjectMeta{Name: PlatformProfileName("trial", backend), Labels: map[string]string{"app.kubernetes.io/part-of": "sympozium", cellnplatform.BackendLabel: backend}, Annotations: map[string]string{packageAnnotation: packageHash}},
			Spec: api.CellnRuntimeProfileSpec{Revision: keyedRevision, ContractVersion: "celln.json-tools/v1", Lifecycles: []string{"disposable-one-shot", "enduring"}, Native: &api.CellnNativeProvisioning{
				ModelProfile: "blake3:" + strings.Repeat("e", 64), Parent: apiextensionsv1.JSON{Raw: []byte(`{}`)}, Worker: apiextensionsv1.JSON{Raw: []byte(`{}`)}, Template: apiextensionsv1.JSON{Raw: []byte(`{}`)}, SystemPrompt: "keyed",
			}},
		}
	}
	_, policyName, _ := PlatformCatalogueNames("trial")
	wrapper := func(backend, profileName string) *api.AgentRuntime {
		labels := map[string]string{cellnplatform.ManagedByLabel: cellnplatform.ManagedByValue, cellnplatform.BackendLabel: backend}
		return &api.AgentRuntime{
			ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: cellnplatform.WrapperNames(backend).Runtime, Labels: labels, Annotations: map[string]string{packageAnnotation: packageHash}},
			Spec:       api.AgentRuntimeSpec{CellnProfileRef: &api.CellnRuntimeProfileRef{Name: profileName, Revision: keyedRevision}, SupportOwner: "celln-platform"},
		}
	}
	names := cellnplatform.WrapperNames("native")
	for _, object := range []client.Object{
		profile("native"), profile("claude"),
		&api.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: policyName, Annotations: map[string]string{packageAnnotation: packageHash}}, Spec: api.CellnExecutionPolicySpec{
			RuntimeProfiles: []api.CellnExecutionPolicyRuntime{{Ref: api.CellnRuntimeProfileRef{Name: PlatformProfileName("trial", "native"), Revision: keyedRevision}}, {Ref: api.CellnRuntimeProfileRef{Name: PlatformProfileName("trial", "claude"), Revision: keyedRevision}}},
			DeniedOrigins:   []string{"evil.example"},
		}},
		wrapper("native", PlatformProfileName("trial", "native")),
		wrapper("claude", PlatformProfileName("trial", "claude")),
		&api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: names.Agent, Labels: managed}},
		&api.ModelConnection{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant-a", Name: names.Connection, Labels: managed}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: fleetNamespace}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: fleetNamespace, Name: FleetModelCredentialSecret}, Data: map[string][]byte{"native": []byte("sk-fleet-key")}},
	} {
		if err := store.Create(ctx, object); err != nil {
			t.Fatalf("seed %T %s: %v", object, object.GetName(), err)
		}
	}
}

// The 0.12 upgrade: a scope whose nodes published a fleet-held model and now
// publish model-free. Its profile is replaced, the retired backend's profile
// goes, the policy names the one profile and keeps its origin guardrail, the
// fleet credential Secret and the shared wrappers go, and the runtime wrapper
// follows the new profile. A rerun changes nothing further.
func TestInstallPlatformMigratesAKeyedScopeToModelFree(t *testing.T) {
	ctx := context.Background()
	dir, packageHash, principal := starterConfiguration(t)
	store := platformInstallStore(t)
	keyedScope(t, ctx, store, packageHash)
	options := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out"), Scope: "trial", ClusterID: "cluster-uid", PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
	for range 2 {
		if err := InstallPlatform(ctx, store, options); err != nil {
			t.Fatal(err)
		}
	}
	var profile api.CellnRuntimeProfile
	if err := store.Get(ctx, types.NamespacedName{Name: PlatformProfileName("trial", "native")}, &profile); err != nil || profile.Spec.Native.ModelProfile != "" || !cellnplatform.MediationOnly(&profile) || profile.Spec.Revision != "v1" || profile.Annotations[packageAnnotation] != packageHash {
		t.Fatalf("native profile not replaced: %v %+v", err, profile)
	}
	if err := store.Get(ctx, types.NamespacedName{Name: PlatformProfileName("trial", "claude")}, &api.CellnRuntimeProfile{}); err == nil {
		t.Fatal("the retired backend's profile survived")
	}
	_, policyName, _ := PlatformCatalogueNames("trial")
	var policy api.CellnExecutionPolicy
	if err := store.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil {
		t.Fatal(err)
	}
	if len(policy.Spec.RuntimeProfiles) != 1 || policy.Spec.RuntimeProfiles[0].Ref != (api.CellnRuntimeProfileRef{Name: profile.Name, Revision: profile.Spec.Revision}) || len(policy.Spec.Tools) != 3 || policy.Spec.Ceilings.MaxTurns != 12 || !strings.Contains(strings.Join(policy.Spec.DeniedOrigins, ","), "evil.example") {
		t.Fatalf("policy not rewritten: %+v", policy.Spec)
	}
	if err := store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: FleetModelCredentialSecret}, &corev1.Secret{}); err == nil {
		t.Fatal("the fleet credential Secret survived")
	}
	names := cellnplatform.WrapperNames("native")
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: names.Agent}, &api.Agent{}); err == nil {
		t.Fatal("the shared Agent survived")
	}
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: names.Connection}, &api.ModelConnection{}); err == nil {
		t.Fatal("the host-profile connection survived")
	}
	var runtime api.AgentRuntime
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: names.Runtime}, &runtime); err != nil || runtime.Spec.CellnProfileRef.Name != profile.Name || runtime.Spec.CellnProfileRef.Revision != profile.Spec.Revision {
		t.Fatalf("runtime wrapper not rebound: %v %+v", err, runtime.Spec)
	}
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: cellnplatform.WrapperNames("claude").Runtime}, &api.AgentRuntime{}); err == nil {
		t.Fatal("the retired backend's wrapper survived")
	}
	var profiles api.CellnRuntimeProfileList
	if err := store.List(ctx, &profiles); err != nil || len(profiles.Items) != 1 {
		t.Fatalf("the migrated scope holds %d profiles, want its one model-free profile (%v)", len(profiles.Items), err)
	}
}
