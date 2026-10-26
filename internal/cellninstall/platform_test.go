package cellninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	"github.com/zeebo/blake3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// starterConfiguration materializes the reviewed testdata as the fleet nodes
// publish it since 0.12: one model-free backend, native, with a consistent
// receipt naming no model.
func starterConfiguration(t *testing.T) (dir, packageHash, principal string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "configuration")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeModelFreeConfiguration(t, dir, cellnplatform.DefaultBackend)
	var metadata receipt
	if _, err := read(filepath.Join(dir, cellnplatform.DefaultBackend, "configured.json"), &metadata); err != nil {
		t.Fatal(err)
	}
	return dir, metadata.PackageHash, metadata.Principal
}

func platformInstallStore(t *testing.T) client.Client {
	t.Helper()
	replicas := int32(1)
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "sympozium-controller-manager", Namespace: "sympozium-system", Generation: 1}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "manager"}}}}}, Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1}}
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: "cluster-uid"}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-a"}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tenant-b"}}).Build()
}

// One scope publishes one model-free profile, its tools and one policy
// without routes, sized from the nodes' host limits; a namespace gets its
// runtime wrapper only, never a shared Agent or ModelConnection, and nothing
// is written for a run.
func TestInstallPlatformPublishesCatalogueOncePerScopeAndWrapsNamespaces(t *testing.T) {
	ctx := context.Background()
	dir, packageHash, principal := starterConfiguration(t)
	store := platformInstallStore(t)
	clusterID, err := ClusterIdentity(ctx, store)
	if err != nil || clusterID != "cluster-uid" {
		t.Fatalf("cluster identity: %q %v", clusterID, err)
	}
	options := func(namespace string) PlatformOptions {
		return PlatformOptions{Namespace: namespace, ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out-"+namespace), Scope: "trial", ClusterID: clusterID, PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
	}
	first := options("tenant-a")
	if err := InstallPlatform(ctx, store, first); err != nil {
		t.Fatal(err)
	}
	profileName, policyName, toolName := PlatformCatalogueNames("trial")
	var profile api.CellnRuntimeProfile
	if err := store.Get(ctx, types.NamespacedName{Name: profileName}, &profile); err != nil {
		t.Fatal(err)
	}
	var profiles api.CellnRuntimeProfileList
	if err := store.List(ctx, &profiles); err != nil || len(profiles.Items) != 1 {
		t.Fatalf("a model-free scope publishes exactly one profile: %v %d", err, len(profiles.Items))
	}
	if !cellnplatform.MediationOnly(&profile) || cellnplatform.Backend(&profile) != "native" || profile.Annotations[packageAnnotation] != packageHash {
		t.Fatalf("profile is not the scope's mediation-only native profile: %+v", profile.ObjectMeta)
	}
	native := profile.Spec.Native
	if native == nil || native.ModelProfile != "" || native.SystemPrompt == "" || len(native.Parent.Raw) == 0 || len(native.Worker.Raw) == 0 || len(native.Template.Raw) == 0 || profile.Spec.Lifecycles[1] != "enduring" || profile.Spec.Limits.TimeoutMillis != 60000 {
		t.Fatalf("profile lacks model-free native provisioning material: %+v", profile.Spec)
	}
	var policy api.CellnExecutionPolicy
	if err := store.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil {
		t.Fatal(err)
	}
	exclusions := policy.Spec.NamespaceSelector.MatchExpressions
	if len(exclusions) != 2 || exclusions[0].Key != cellnplatform.NamespaceNameLabel || exclusions[0].Operator != metav1.LabelSelectorOpNotIn || !slices.Contains(exclusions[0].Values, "kube-system") || !slices.Contains(exclusions[0].Values, "sympozium-system") || !slices.Contains(exclusions[0].Values, "celln-system") || exclusions[1].Key != cellnplatform.ExcludedLabel || exclusions[1].Operator != metav1.LabelSelectorOpDoesNotExist {
		t.Fatalf("default policy is not open with system exclusions: %+v", policy.Spec.NamespaceSelector)
	}
	if len(policy.Spec.RuntimeProfiles) != 1 || policy.Spec.RuntimeProfiles[0].Ref != (api.CellnRuntimeProfileRef{Name: profileName, Revision: profile.Spec.Revision}) {
		t.Fatalf("policy must carry exactly the scope's profile: %+v", policy.Spec.RuntimeProfiles)
	}
	// The ceilings are the nodes' host limits; the turn ceiling is the worker's lifetime.
	c := policy.Spec.Ceilings
	if len(policy.Spec.Tools) != 3 || c.MaxTurns != 12 || c.MaxModelRequests != 36 || c.MaxOutputTokens != 18432 || c.MaxParentLeaseSeconds != 3600 || c.MaxTurnSeconds != 60 || policy.Annotations[packageAnnotation] != packageHash {
		t.Fatalf("policy does not bind the reviewed tools and ceilings: %+v", policy.Spec)
	}
	if !slices.Equal(policy.Spec.Lifecycles, []string{"direct-one-shot", "harness-one-shot", "enduring"}) {
		t.Fatalf("policy lifecycles: %v", policy.Spec.Lifecycles)
	}
	var tool api.ClusterCellnTool
	if err := store.Get(ctx, types.NamespacedName{Name: toolName("https-fetch")}, &tool); err != nil || tool.Spec.Revision != "v1" {
		t.Fatalf("cluster tool missing: %v", err)
	}
	var namespace corev1.Namespace
	if err := store.Get(ctx, types.NamespacedName{Name: "tenant-a"}, &namespace); err != nil || namespace.Labels[ScopeLabel] != "" {
		t.Fatalf("default mode labeled the namespace: %v %v", err, namespace.Labels)
	}
	names := cellnplatform.WrapperNames("native")
	var wrapper api.AgentRuntime
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: names.Runtime}, &wrapper); err != nil || wrapper.Spec.CellnProfileRef == nil || *wrapper.Spec.CellnProfileRef != (api.CellnRuntimeProfileRef{Name: profileName, Revision: profile.Spec.Revision}) || wrapper.Spec.Celln != nil || wrapper.Labels[cellnplatform.ManagedByLabel] != cellnplatform.ManagedByValue || wrapper.Annotations[packageAnnotation] != packageHash {
		t.Fatalf("tenant wrapper: %v %+v", err, wrapper)
	}
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: names.Connection}, &api.ModelConnection{}); err == nil {
		t.Fatal("a model-free scope published a shared ModelConnection")
	}
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: names.Agent}, &api.Agent{}); err == nil {
		t.Fatal("a model-free scope published a shared Agent")
	}
	var grants corev1.ConfigMapList
	if err := store.List(ctx, &grants, client.InNamespace("tenant-a")); err != nil || len(grants.Items) != 0 {
		t.Fatal("platform installation published namespace grant ConfigMaps")
	}
	var tools api.CellnToolList
	if err := store.List(ctx, &tools, client.InNamespace("tenant-a")); err != nil || len(tools.Items) != 0 {
		t.Fatal("platform installation copied namespaced tools")
	}
	// Only the installation record is written: no registration, no sample run.
	entries, err := os.ReadDir(first.OutputDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "installed.json" {
		t.Fatalf("output directory holds %v, want installed.json alone (%v)", entries, err)
	}
	var installed struct {
		Scope, PackageHash, Policy, Readiness string
		Backends, Profiles                    []string
		RunSubmitted                          bool
	}
	raw, _ := os.ReadFile(filepath.Join(first.OutputDir, "installed.json"))
	if err := json.Unmarshal(raw, &installed); err != nil || installed.Scope != "trial" || installed.PackageHash != packageHash || installed.Policy != policyName || installed.Readiness != "not_established" || installed.RunSubmitted || !slices.Equal(installed.Backends, []string{"native"}) || !slices.Equal(installed.Profiles, []string{profileName}) {
		t.Fatalf("installation record: %v %s", err, raw)
	}
	// A second namespace reuses the scope's catalogue and gets only its wrapper.
	if err := InstallPlatform(ctx, store, options("tenant-b")); err != nil {
		t.Fatalf("second namespace on the same scope: %v", err)
	}
	if err := store.List(ctx, &profiles); err != nil || len(profiles.Items) != 1 {
		t.Fatalf("second namespace changed the catalogue: %v %d", err, len(profiles.Items))
	}
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-b", Name: names.Runtime}, &wrapper); err != nil {
		t.Fatal(err)
	}
	// Strict mode publishes a scope-label selector and labels the namespace.
	strict := options("tenant-b")
	strict.Scope, strict.Authorise, strict.OutputDir = "strict", "labeled", filepath.Join(t.TempDir(), "strict")
	if err := InstallPlatform(ctx, store, strict); err != nil {
		t.Fatal(err)
	}
	_, strictPolicy, _ := PlatformCatalogueNames("strict")
	if err := store.Get(ctx, types.NamespacedName{Name: strictPolicy}, &policy); err != nil || policy.Spec.NamespaceSelector.MatchLabels[ScopeLabel] != "strict" {
		t.Fatalf("strict policy: %v %+v", err, policy.Spec.NamespaceSelector)
	}
	if err := store.Get(ctx, types.NamespacedName{Name: "tenant-b"}, &namespace); err != nil || namespace.Labels[ScopeLabel] != "strict" {
		t.Fatalf("strict mode did not label the namespace: %v %v", err, namespace.Labels)
	}
	bogus := options("tenant-b")
	bogus.Authorise, bogus.OutputDir = "sometimes", filepath.Join(t.TempDir(), "bogus")
	if err := InstallPlatform(ctx, store, bogus); err == nil {
		t.Fatal("unknown authorisation mode accepted")
	}
	// A different package under the same scope is refused before tenant changes.
	mismatch := options("tenant-b")
	mismatch.Principal = "someone-else"
	if err := InstallPlatform(ctx, store, mismatch); err == nil {
		t.Fatal("principal differing from the reviewed configuration accepted")
	}
}

func TestSessionDefaultsStayInsideCeilings(t *testing.T) {
	wide := SessionDefaults(api.EnduringRunSpec{LeaseSeconds: 86400, MaxTurns: 256, MaxModelRequests: 1536, MaxOutputTokens: 786432})
	if wide.LeaseSeconds != 14400 || wide.MaxTurns != 64 || wide.MaxModelRequests != 384 || wide.MaxOutputTokens != 196608 {
		t.Fatalf("session defaults: %+v", wide)
	}
	// Ceilings that pay for two turns yield two, not the four the policy names.
	narrow := SessionDefaults(api.EnduringRunSpec{LeaseSeconds: 600, MaxTurns: 4, MaxModelRequests: 12, MaxOutputTokens: 6144})
	if narrow.LeaseSeconds != 600 || narrow.MaxTurns != 2 || narrow.MaxModelRequests != 12 || narrow.MaxOutputTokens != 6144 {
		t.Fatalf("session defaults exceed narrow ceilings: %+v", narrow)
	}
}

// repackage turns a materialized configuration into the one a newer package
// publishes: another package hash, a new worker revision and its last tool
// renamed (the old name is dropped, a new one added).
func repackage(t *testing.T, dir, packageHash string) {
	t.Helper()
	backends, err := ConfigurationBackends(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range backends {
		sub := filepath.Join(dir, backend)
		var cat map[string]any
		raw, _ := os.ReadFile(filepath.Join(sub, "catalogue.json"))
		if err := json.Unmarshal(raw, &cat); err != nil {
			t.Fatal(err)
		}
		tools := cat["tools"].([]any)
		last := tools[len(tools)-1].(map[string]any)
		last["name"] = fmt.Sprint(last["name"]) + "-next"
		worker := cat["worker"].(map[string]any)
		worker["revision"] = fmt.Sprint(worker["revision"]) + "-next"
		raw, _ = json.Marshal(cat)
		if err := os.WriteFile(filepath.Join(sub, "catalogue.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		var configured map[string]any
		receiptRaw, _ := os.ReadFile(filepath.Join(sub, "configured.json"))
		if err := json.Unmarshal(receiptRaw, &configured); err != nil {
			t.Fatal(err)
		}
		configured["packageHash"] = packageHash
		configured["catalogueHash"] = fmt.Sprintf("blake3:%x", blake3.Sum256(raw))
		receiptRaw, _ = json.Marshal(configured)
		if err := os.WriteFile(filepath.Join(sub, "configured.json"), receiptRaw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// rederive rewrites the configuration as a newer Celln binary would derive it
// from the same package: another persona and a wider fetch limit, with the
// template and receipt hashes following.
func rederive(t *testing.T, dir, persona string, fetchBytes int) {
	t.Helper()
	sub := filepath.Join(dir, cellnplatform.DefaultBackend)
	var cat map[string]any
	raw, _ := os.ReadFile(filepath.Join(sub, "catalogue.json"))
	if err := json.Unmarshal(raw, &cat); err != nil {
		t.Fatal(err)
	}
	cat["systemPrompt"] = persona
	for _, tool := range cat["tools"].([]any) {
		entry := tool.(map[string]any)
		if entry["name"] == "https-fetch" {
			entry["spec"].(map[string]any)["limits"].(map[string]any)["https"].(map[string]any)["maxResponseBytes"] = fetchBytes
		}
	}
	raw, _ = json.Marshal(cat)
	if err := os.WriteFile(filepath.Join(sub, "catalogue.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	var native map[string]any
	nativeRaw, _ := os.ReadFile(filepath.Join(sub, "native-template.json"))
	if err := json.Unmarshal(nativeRaw, &native); err != nil {
		t.Fatal(err)
	}
	native["template"].(map[string]any)["system"] = persona
	nativeRaw, _ = json.Marshal(native)
	if err := os.WriteFile(filepath.Join(sub, "native-template.json"), nativeRaw, 0600); err != nil {
		t.Fatal(err)
	}
	var configured map[string]any
	receiptRaw, _ := os.ReadFile(filepath.Join(sub, "configured.json"))
	if err := json.Unmarshal(receiptRaw, &configured); err != nil {
		t.Fatal(err)
	}
	configured["catalogueHash"] = fmt.Sprintf("blake3:%x", blake3.Sum256(raw))
	configured["nativeTemplateHash"] = fmt.Sprintf("blake3:%x", blake3.Sum256(nativeRaw))
	receiptRaw, _ = json.Marshal(configured)
	if err := os.WriteFile(filepath.Join(sub, "configured.json"), receiptRaw, 0600); err != nil {
		t.Fatal(err)
	}
}

// The same package re-derived by another Celln binary (another persona, a
// wider fetch limit) updates the scope's profile and tools in place without
// a package replacement; namespace wrappers keep working unchanged.
func TestInstallPlatformFollowsARederivedCatalogue(t *testing.T) {
	ctx := context.Background()
	dir, packageHash, principal := starterConfiguration(t)
	store := platformInstallStore(t)
	o := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out"), Scope: "trial", ClusterID: "cluster-uid", PackageHash: packageHash, Principal: principal, ControllerNamespace: "sympozium-system"}
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatal(err)
	}
	profileName, _, toolName := PlatformCatalogueNames("trial")
	var before api.CellnRuntimeProfile
	if err := store.Get(ctx, types.NamespacedName{Name: profileName}, &before); err != nil {
		t.Fatal(err)
	}
	rederive(t, dir, "You have tools; fetch a live source first.", 16384)
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatalf("re-derived catalogue refused: %v", err)
	}
	var profile api.CellnRuntimeProfile
	if err := store.Get(ctx, types.NamespacedName{Name: profileName}, &profile); err != nil || profile.Spec.Native.SystemPrompt != "You have tools; fetch a live source first." || profile.Annotations[packageAnnotation] != packageHash || profile.Annotations[catalogueAnnotation] == before.Annotations[catalogueAnnotation] {
		t.Fatalf("profile did not follow the re-derived catalogue: %v %+v", err, profile.ObjectMeta)
	}
	var tool api.ClusterCellnTool
	if err := store.Get(ctx, types.NamespacedName{Name: toolName("https-fetch")}, &tool); err != nil || tool.Spec.Limits.HTTPS == nil || tool.Spec.Limits.HTTPS.MaxResponseBytes != 16384 {
		t.Fatalf("tool limits did not follow: %v %+v", err, tool.Spec.Limits)
	}
	var runtime api.AgentRuntime
	if err := store.Get(ctx, types.NamespacedName{Namespace: "tenant-a", Name: "celln-native"}, &runtime); err != nil || runtime.Spec.CellnProfileRef.Name != profileName {
		t.Fatalf("namespace wrapper lost: %v %+v", err, runtime.Spec.CellnProfileRef)
	}
	// A rerun is idempotent.
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatalf("rerun refused: %v", err)
	}
}

// Moving a scope to a new package is refused unless approved; approved, the
// catalogue is replaced, dropped tools are retired and every namespace's
// platform wrappers follow the new profile.
func TestInstallPlatformReplacesPackageOnlyWhenApproved(t *testing.T) {
	ctx := context.Background()
	dir, oldPackage, principal := starterConfiguration(t)
	store := platformInstallStore(t)
	o := PlatformOptions{Namespace: "tenant-a", ConfigurationDir: dir, OutputDir: filepath.Join(t.TempDir(), "out"), Scope: "trial", ClusterID: "cluster-uid", PackageHash: oldPackage, Principal: principal, ControllerNamespace: "sympozium-system"}
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatal(err)
	}
	profileName, policyName, toolName := PlatformCatalogueNames("trial")
	if name, err := cellnplatform.EnsureRuntimeWrapper(ctx, store, "tenant-b", profileName); err != nil || name != "celln-native" {
		t.Fatalf("on-demand wrapper: %v %q", err, name)
	}
	var oldCat catalogue
	if _, err := read(filepath.Join(dir, "native", "catalogue.json"), &oldCat); err != nil {
		t.Fatal(err)
	}
	dropped := toolName(oldCat.Tools[len(oldCat.Tools)-1].Name)

	newPackage := "blake3:" + strings.Repeat("b", 64)
	repackage(t, dir, newPackage)
	o.PackageHash = newPackage
	if err := InstallPlatform(ctx, store, o); err == nil || !strings.Contains(err.Error(), "another package") {
		t.Fatalf("unapproved package change accepted: %v", err)
	}
	o.Replacing = FleetPublication{Exists: true, Package: oldPackage, Scope: "trial"}
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatalf("approved replacement refused: %v", err)
	}

	var profile api.CellnRuntimeProfile
	if err := store.Get(ctx, types.NamespacedName{Name: profileName}, &profile); err != nil || profile.Annotations[packageAnnotation] != newPackage || !strings.HasSuffix(profile.Spec.Revision, "-next") || !cellnplatform.MediationOnly(&profile) {
		t.Fatalf("profile not replaced: %v %+v", err, profile.ObjectMeta)
	}
	var tool api.ClusterCellnTool
	if err := store.Get(ctx, types.NamespacedName{Name: dropped}, &tool); err == nil {
		t.Fatalf("tool %s dropped by the new package was kept", dropped)
	}
	var policy api.CellnExecutionPolicy
	if err := store.Get(ctx, types.NamespacedName{Name: policyName}, &policy); err != nil || policy.Annotations[packageAnnotation] != newPackage || len(policy.Spec.Tools) != len(oldCat.Tools) || !slices.ContainsFunc(policy.Spec.Tools, func(t api.CellnExecutionPolicyTool) bool { return t.Ref.Name == dropped+"-next" }) {
		t.Fatalf("policy not replaced: %v %+v", err, policy.Spec.Tools)
	}
	if len(policy.Spec.RuntimeProfiles) != 1 || policy.Spec.RuntimeProfiles[0].Ref != (api.CellnRuntimeProfileRef{Name: profileName, Revision: profile.Spec.Revision}) {
		t.Fatalf("policy still names the old profile revision: %+v", policy.Spec.RuntimeProfiles)
	}
	for _, key := range []types.NamespacedName{{Namespace: "tenant-a", Name: "celln-native"}, {Namespace: "tenant-b", Name: "celln-native"}} {
		var runtime api.AgentRuntime
		if err := store.Get(ctx, key, &runtime); err != nil || runtime.Spec.CellnProfileRef.Revision != profile.Spec.Revision {
			t.Fatalf("%s wrapper not rebound: %v %+v", key, err, runtime.Spec.CellnProfileRef)
		}
	}
	// A rerun of the new package is an ordinary idempotent install.
	o.Replacing = FleetPublication{}
	if err := InstallPlatform(ctx, store, o); err != nil {
		t.Fatalf("rerun after replacement refused: %v", err)
	}
}

func TestFleetPublicationReplaces(t *testing.T) {
	p := FleetPublication{Exists: true, Package: "blake3:a", Scope: "starter"}
	for _, c := range []struct {
		scope, pkg string
		want       bool
	}{{"starter", "blake3:a", false}, {"starter", "blake3:b", true}, {"other", "blake3:a", true}} {
		if got := p.Replaces(c.scope, c.pkg); got != c.want {
			t.Fatalf("%+v: got %v", c, got)
		}
	}
	if (FleetPublication{}).Replaces("starter", "blake3:b") {
		t.Fatal("a fresh cluster replaces nothing")
	}
	if (FleetPublication{Exists: true, Package: "blake3:a"}).Replaces("other", "blake3:a") {
		t.Fatal("an unrecorded scope with the same package is not a replacement")
	}
	if err := p.ReplacementRefusal("starter", "blake3:b"); !strings.Contains(err.Error(), "--celln-fleet-replace-package") {
		t.Fatalf("refusal does not name the approval flag: %v", err)
	}
}
