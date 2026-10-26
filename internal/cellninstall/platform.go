package cellninstall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// A new conversation's default session: a four-hour parent with 64 turns.
const (
	sessionLeaseSeconds = 14400
	sessionTurns        = 64
)

// SessionDefaults is the budget a new conversation asks for on a profile
// with the current starter package's per-turn allowance
// (api.TurnModelRequests, api.TurnOutputTokens); SessionDefaultsFor takes the
// allowance of one runtime profile.
func SessionDefaults(ceilings api.EnduringRunSpec) *api.EnduringRunSpec {
	return SessionDefaultsAt(ceilings, api.TurnModelRequests, api.TurnOutputTokens)
}

// SessionDefaultsAt is a working session that stays well inside the scope's
// ceilings: a four-hour parent with the largest turn count up to 64 whose
// model requests and output tokens the ceilings pay for, because every turn
// reserves the whole per-turn allowance from the totals. Ceilings that pay for
// less than one turn yield one turn capped by the ceilings themselves.
func SessionDefaultsAt(ceilings api.EnduringRunSpec, turnRequests, turnTokens int64) *api.EnduringRunSpec {
	if turnRequests < 1 || turnTokens < 1 {
		turnRequests, turnTokens = api.TurnModelRequests, api.TurnOutputTokens
	}
	turns := max(1, min(int64(sessionTurns), int64(ceilings.MaxTurns), TurnsAfforded(int64(ceilings.MaxModelRequests), ceilings.MaxOutputTokens, turnRequests, turnTokens)))
	return &api.EnduringRunSpec{LeaseSeconds: min(sessionLeaseSeconds, ceilings.LeaseSeconds), MaxTurns: int32(turns), MaxModelRequests: int32(min(turns*turnRequests, int64(ceilings.MaxModelRequests))), MaxOutputTokens: min(turns*turnTokens, ceilings.MaxOutputTokens)}
}

// ScopeLabel opts a namespace into a scope in strict ("labeled") mode; the
// default mode admits every namespace except the system exclusions.
const ScopeLabel = cellnplatform.ScopeLabel

const packageAnnotation = "celln.sympozium.ai/package"

// catalogueAnnotation records the starter catalogue a platform object was
// published from. The same package re-derived by another Celln binary (an
// upgraded celln-node-configure image) yields another catalogue: persona,
// tool limits, template; the cluster catalogue objects follow it in place.
const catalogueAnnotation = "celln.sympozium.ai/catalogue"

// PlatformOptions publishes one reviewed starter configuration as the
// cluster-scoped platform catalogue and wires one tenant namespace to it.
type PlatformOptions struct {
	Namespace, ConfigurationDir, OutputDir string
	Scope, ClusterID, PackageHash          string
	Principal                              string
	ControllerNamespace                    string
	// Authorise is "all" (default: every namespace except the system
	// exclusions and namespaces labeled excluded) or "labeled" (only
	// namespaces carrying ScopeLabel).
	Authorise string
	// Replacing is the publication this installation replaces, when the
	// operator approved moving the scope to a new package (or scope). Its
	// catalogue objects are replaced or removed and every namespace's
	// platform wrappers are rebound; zero means nothing is replaced.
	Replacing FleetPublication
}

// PlatformCatalogueNames are the cluster-scoped objects one scope publishes:
// its policy and its tool names. Profiles are named per backend by
// PlatformProfileName.
func PlatformCatalogueNames(scope string) (profile, policy string, tool func(string) string) {
	return PlatformProfileName(scope, cellnplatform.DefaultBackend), "celln-fleet-" + scope, func(name string) string { return "celln-" + scope + "-" + name }
}

// PlatformProfileName is the cluster-scoped runtime profile of one backend.
func PlatformProfileName(scope, backend string) string {
	if backend == cellnplatform.DefaultBackend {
		return "celln-native-" + scope
	}
	return "celln-native-" + scope + "-" + backend
}

// sameJSON compares two JSON documents by value, not by formatting.
func sameJSON(a, b []byte) bool {
	var left, right any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

// workerTimeoutMs is the lifetime a native worker request gives one turn.
func workerTimeoutMs(worker []byte) int64 {
	var request struct {
		Capabilities struct {
			TimeoutMs int64 `json:"timeoutMs"`
		} `json:"capabilities"`
	}
	if json.Unmarshal(worker, &request) != nil {
		return 0
	}
	return request.Capabilities.TimeoutMs
}

// sameWorkerMaterial compares two native worker requests apart from the turn
// lifetime, which follows each backend's output-token cap.
func sameWorkerMaterial(a, b []byte) bool {
	strip := func(raw []byte) any {
		var request map[string]any
		if json.Unmarshal(raw, &request) != nil {
			return nil
		}
		if capabilities, ok := request["capabilities"].(map[string]any); ok {
			delete(capabilities, "timeoutMs")
		}
		return request
	}
	left, right := strip(a), strip(b)
	return left != nil && reflect.DeepEqual(left, right)
}

// nativeProvisionConfig is native-template.json as a node publishes it: the
// parent and worker requests, the per-turn allowance and the lifetime totals.
type nativeProvisionConfig struct {
	AdmissionWindowMs   uint64          `json:"admissionWindowMs"`
	Parent              json.RawMessage `json:"parent"`
	Worker              json.RawMessage `json:"worker"`
	Template            json.RawMessage `json:"template"`
	ModelProfile        string          `json:"modelProfile"`
	ReservedMemoryBytes uint64          `json:"reservedMemoryBytes"`
	MaxTurns            uint64          `json:"maxTurns"`
	TurnModelRequests   uint64          `json:"turnModelRequests"`
	TurnOutputTokens    uint64          `json:"turnOutputTokens"`
	TotalModelRequests  uint64          `json:"totalModelRequests"`
	TotalOutputTokens   uint64          `json:"totalOutputTokens"`
}

// backendConfiguration is one backend's reviewed starter configuration as a
// fleet node published it.
type backendConfiguration struct {
	name       string
	cat        catalogue
	configured receipt
	native     nativeProvisionConfig
	harness    struct {
		Contract      string `json:"contract"`
		System        string `json:"system"`
		Model         string `json:"model"`
		URL           string `json:"url"`
		AllowInsecure bool   `json:"allow_insecure"`
	}
}

func readBackendConfiguration(dir, backend, packageHash, principal string) (backendConfiguration, error) {
	b := backendConfiguration{name: backend}
	hashes := map[string]string{}
	for file, target := range map[string]any{"catalogue.json": &b.cat, "configured.json": &b.configured, "native-template.json": &b.native} {
		hash, err := read(filepath.Join(dir, backend, file), target)
		if err != nil {
			return b, fmt.Errorf("backend %s: %w", backend, err)
		}
		hashes[file] = hash
	}
	c := b.configured
	if c.PackageHash != packageHash || c.CatalogueHash != hashes["catalogue.json"] || c.NativeTemplateHash != hashes["native-template.json"] {
		return b, fmt.Errorf("backend %s: configuration differs from reviewed package/receipt", backend)
	}
	if c.APIVersion != "celln.native-starter-configured/v1" || c.ExecutionAuthorized || c.Readiness != "not_established" || b.native.ModelProfile != c.ModelProfile || c.Principal != principal || len(b.cat.Tools) < 3 || len(b.cat.Tools) > 24 || b.cat.Worker.ContractVersion != "celln.json-tools/v1" {
		return b, fmt.Errorf("backend %s: operator starter configuration mismatch", backend)
	}
	// A node that still calls a model itself (a fleet-held key, before 0.12)
	// is refused: the fleet holds no model, and the nodes reconfigure
	// themselves model-free when the chart rolls them.
	if c.ModelProfile != "" || c.Model.Provider != "" || c.Model.Model != "" || c.Model.BaseURL != "" {
		return b, fmt.Errorf("backend %s: the node published a model-bound starter configuration; since 0.12 no Celln node holds a model (the celln-node-configure pods reconfigure it; check their logs)", backend)
	}
	if json.Unmarshal(b.native.Template, &b.harness) != nil || b.harness.Contract != "celln.json-tools/v1" || b.harness.System != b.cat.SystemPrompt || b.harness.AllowInsecure || b.harness.Model == "" || b.harness.URL == "" {
		return b, fmt.Errorf("backend %s: native template does not match the starter catalogue", backend)
	}
	return b, nil
}

// InstallPlatform publishes the fleet's starter configuration as one
// CellnRuntimeProfile per backend (one, model-free), the scope's
// ClusterCellnTools and one CellnExecutionPolicy, then creates the target
// namespace's runtime wrappers. Existing catalogue
// objects are accepted only when they carry the same package; nothing is
// replaced and no run is submitted.
func InstallPlatform(ctx context.Context, store client.Client, o PlatformOptions) error {
	if store == nil || len(validation.IsDNS1123Label(o.Namespace)) != 0 || len(validation.IsDNS1123Label(o.Scope)) != 0 || o.ClusterID == "" || o.ControllerNamespace == "" || o.Principal == "" || !strings.HasPrefix(o.PackageHash, "blake3:") || len(o.PackageHash) != 71 {
		return fmt.Errorf("platform installation requires namespace, DNS-label scope, cluster identity, principal and package hash")
	}
	for _, path := range []string{o.ConfigurationDir, o.OutputDir} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("clean absolute installation paths required")
		}
	}
	if ready, err := ControllerRolledOut(ctx, store, o.ControllerNamespace); err != nil {
		return err
	} else if !ready {
		return fmt.Errorf("general controller must finish its rollout before platform installation")
	}
	names, err := ConfigurationBackends(o.ConfigurationDir)
	if err != nil {
		return err
	}
	backends := make([]backendConfiguration, 0, len(names))
	for _, name := range names {
		b, err := readBackendConfiguration(o.ConfigurationDir, name, o.PackageHash, o.Principal)
		if err != nil {
			return err
		}
		backends = append(backends, b)
	}
	first := backends[0]
	// The policy's turn ceiling admits the longest turn any backend grants;
	// each profile still holds its own runs to its own lifetime.
	var maxTurnMs int64
	for _, b := range backends {
		timeout := workerTimeoutMs(b.native.Worker)
		if timeout < 1000 || timeout != b.cat.Worker.Limits.TimeoutMillis {
			return fmt.Errorf("backend %s: native worker request lacks a bounded lifetime matching its catalogue", b.name)
		}
		maxTurnMs = max(maxTurnMs, timeout)
	}
	// One scope, one package: every backend must be the same reviewed
	// material with only its model route, credential and the turn lifetime
	// that follows its output-token cap differing.
	firstWorker := first.cat.Worker
	firstWorker.Limits.TimeoutMillis = 0
	for _, b := range backends[1:] {
		worker := b.cat.Worker
		worker.Limits.TimeoutMillis = 0
		if !reflect.DeepEqual(b.cat.Tools, first.cat.Tools) || !reflect.DeepEqual(worker, firstWorker) || !sameToolbox(b.cat.Toolbox, first.cat.Toolbox) || b.cat.SystemPrompt != first.cat.SystemPrompt || b.configured.HostLimits != first.configured.HostLimits || !sameJSON(b.native.Parent, first.native.Parent) || !sameWorkerMaterial(b.native.Worker, first.native.Worker) {
			return fmt.Errorf("backend %s differs from %s in reviewed package material; one scope carries one package", b.name, first.name)
		}
	}
	_, policyName, toolName := PlatformCatalogueNames(o.Scope)
	meta := func(name string, labels map[string]string) metav1.ObjectMeta {
		all := map[string]string{"app.kubernetes.io/part-of": "sympozium"}
		for k, v := range labels {
			all[k] = v
		}
		return metav1.ObjectMeta{Name: name, Labels: all, Annotations: map[string]string{packageAnnotation: first.configured.PackageHash, catalogueAnnotation: first.configured.CatalogueHash}}
	}
	var objects []client.Object
	profiles := map[string]*api.CellnRuntimeProfile{}
	toolboxes := map[string]*api.CellnRuntimeProfile{}
	runtimeRefs := make([]api.CellnExecutionPolicyRuntime, 0, len(backends))
	// The catalogue's tools in catalogue order: the order a toolbox closure
	// lends them in and a toolbox run must select them in.
	clusterRefs := make([]api.ClusterCellnToolRef, 0, len(first.cat.Tools))
	for _, entry := range first.cat.Tools {
		clusterRefs = append(clusterRefs, api.ClusterCellnToolRef{Name: toolName(entry.Name), Revision: entry.Spec.Revision})
	}
	for _, b := range backends {
		profileName := PlatformProfileName(o.Scope, b.name)
		cat, native := b.cat, b.native
		// Every profile serves Agents with their own connection only.
		profileMeta := meta(profileName, map[string]string{cellnplatform.BackendLabel: b.name, cellnplatform.MediationOnlyLabel: "true"})
		profile := &api.CellnRuntimeProfile{ObjectMeta: profileMeta, Spec: api.CellnRuntimeProfileSpec{
			Revision: cat.Worker.Revision, ContractVersion: cat.Worker.ContractVersion, Executable: cat.Worker.Executable, Closure: cat.Worker.Closure, Mote: cat.Worker.Mote, PublisherKey: cat.Worker.PublisherKey, EntryPoint: cat.Worker.EntryPoint, Platform: cat.Worker.Platform, Lane: cat.Worker.Lane,
			Lifecycles: []string{"disposable-one-shot", "enduring"}, Limits: cat.Worker.Limits, JSON: cat.Worker.JSON.DeepCopy(),
			Native: &api.CellnNativeProvisioning{AdmissionWindowMs: int64(native.AdmissionWindowMs), Parent: apiextensionsv1.JSON{Raw: native.Parent}, Worker: apiextensionsv1.JSON{Raw: native.Worker}, Template: apiextensionsv1.JSON{Raw: native.Template}, ReservedMemoryBytes: int64(native.ReservedMemoryBytes), TurnModelRequests: int64(native.TurnModelRequests), TurnOutputTokens: int64(native.TurnOutputTokens), SystemPrompt: cat.SystemPrompt},
		}}
		profiles[b.name] = profile
		objects = append(objects, profile)
		runtimeRefs = append(runtimeRefs, api.CellnExecutionPolicyRuntime{Ref: api.CellnRuntimeProfileRef{Name: profileName, Revision: cat.Worker.Revision}})
		if toolbox := toolboxProfile(profile, cat.Toolbox, clusterRefs); toolbox != nil {
			toolboxes[b.name] = toolbox
			objects = append(objects, toolbox)
			runtimeRefs = append(runtimeRefs, api.CellnExecutionPolicyRuntime{Ref: api.CellnRuntimeProfileRef{Name: toolbox.Name, Revision: toolbox.Spec.Revision}})
		}
	}
	policyTools := make([]api.CellnExecutionPolicyTool, 0, len(first.cat.Tools))
	for _, entry := range first.cat.Tools {
		objects = append(objects, &api.ClusterCellnTool{ObjectMeta: meta(toolName(entry.Name), nil), Spec: entry.Spec})
		policyTools = append(policyTools, api.CellnExecutionPolicyTool{Ref: api.ClusterCellnToolRef{Name: toolName(entry.Name), Revision: entry.Spec.Revision}})
	}
	limits := first.configured.HostLimits
	selector, err := cellnplatform.Selector(o.Authorise, o.Scope, cellnplatform.SystemNamespaces(o.ControllerNamespace, "celln-system"))
	if err != nil {
		return err
	}
	policy := &api.CellnExecutionPolicy{ObjectMeta: meta(policyName, nil), Spec: api.CellnExecutionPolicySpec{
		NamespaceSelector: selector,
		RuntimeProfiles:   runtimeRefs,
		Tools:             policyTools,
		Lifecycles:        []string{"direct-one-shot", "harness-one-shot", "enduring"},
		Ceilings:          api.CellnExecutionPolicyCeilings{MaxTurns: int64(limits.MaxTurns), MaxModelRequests: int64(limits.MaxModelRequests), MaxOutputTokens: limits.MaxOutputTokens, MaxParentLeaseSeconds: int64(limits.LeaseSeconds), MaxTurnSeconds: maxTurnMs / 1000},
	}}
	// Reserve the private output before any cluster change; a rerun that adds
	// a backend reuses the directory it made.
	if err := os.Mkdir(o.OutputDir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	replacing := o.Replacing.Replaces(o.Scope, first.configured.PackageHash)
	// A scope whose nodes moved from a fleet-held model to model-free (the
	// 0.12 upgrade) is migrated: its profiles are replaced, its policy loses
	// the host-profile routes, the backends it no longer configures and
	// their shared wrappers go, and runtime wrappers are rebound. Every live
	// parent was already lost when the nodes reconfigured.
	migrating, err := keyedToModelFree(ctx, store, backends, o.Scope)
	if err != nil {
		return err
	}
	if migrating {
		replacing = true
		if o.Replacing.Package == "" {
			o.Replacing = FleetPublication{Exists: true, Scope: o.Scope, Package: first.configured.PackageHash}
		}
	}
	for _, object := range objects {
		// The same package published model-free replaces the profiles it
		// published with a model; everything else of the same package is
		// accepted as it is.
		if _, isProfile := object.(*api.CellnRuntimeProfile); migrating && isProfile {
			existing := object.DeepCopyObject().(client.Object)
			switch err := store.Get(ctx, client.ObjectKeyFromObject(object), existing); {
			case err == nil:
				if err := replacePlatformObject(ctx, store, object, existing); err != nil {
					return err
				}
				continue
			case !apierrors.IsNotFound(err):
				return err
			}
		}
		if err := ensurePlatformObject(ctx, store, object, first.configured.PackageHash, first.configured.CatalogueHash, replacing); err != nil {
			return err
		}
	}
	// The scope's one policy grows with its backends: a rerun adds the new
	// backend's profile and route and never removes or rewrites the others.
	// The move to model-free rewrites it: the host-profile routes and the
	// retired profiles go, and the ceilings follow the model-free sizing.
	if err := ensurePlatformPolicy(ctx, store, policy, first.configured.PackageHash, replacing || migrating); err != nil {
		return err
	}
	if replacing {
		if err := retirePlatformCatalogue(ctx, store, o.Replacing, o.Scope, objects, policyName); err != nil {
			return err
		}
		if migrating {
			if err := retireKeyedWrappers(ctx, store, backends); err != nil {
				return err
			}
		}
		if err := rebindTenantWrappers(ctx, store, profiles, toolboxes); err != nil {
			return err
		}
	}
	var namespace corev1.Namespace
	if err := store.Get(ctx, types.NamespacedName{Name: o.Namespace}, &namespace); err != nil {
		return err
	}
	if o.Authorise == cellnplatform.AuthoriseLabeled && namespace.Labels[ScopeLabel] != o.Scope {
		patch := client.MergeFrom(namespace.DeepCopy())
		if namespace.Labels == nil {
			namespace.Labels = map[string]string{}
		}
		namespace.Labels[ScopeLabel] = o.Scope
		if err := store.Patch(ctx, &namespace, patch); err != nil {
			return err
		}
	}
	// The install namespace's runtime wrappers are the same objects the API
	// server creates on demand for any other authorised namespace.
	for _, b := range backends {
		wrappers := []client.Object{cellnplatform.RuntimeWrapper(o.Namespace, profiles[b.name])}
		if toolbox := toolboxes[b.name]; toolbox != nil {
			// An Agent lends the whole toolbox on this one.
			wrappers = append(wrappers, cellnplatform.RuntimeWrapper(o.Namespace, toolbox))
		}
		for _, object := range wrappers {
			annotations := object.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations[packageAnnotation] = first.configured.PackageHash
			object.SetAnnotations(annotations)
			if err := ensurePlatformObject(ctx, store, object, first.configured.PackageHash, first.configured.CatalogueHash, replacing); err != nil {
				return err
			}
		}
	}
	// installed.json records what this install published; it is rewritten on
	// every run.
	write := func(name string, value any) error {
		raw, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
		if name == "installed.json" {
			flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		}
		f, err := os.OpenFile(filepath.Join(o.OutputDir, name), flags, 0600)
		if os.IsExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := f.Write(raw); err != nil {
			return err
		}
		return f.Sync()
	}
	profileNames := make([]string, 0, len(backends))
	for _, b := range backends {
		profileNames = append(profileNames, PlatformProfileName(o.Scope, b.name))
		if toolbox := toolboxes[b.name]; toolbox != nil {
			profileNames = append(profileNames, toolbox.Name)
		}
	}
	return write("installed.json", map[string]any{"namespace": o.Namespace, "scope": o.Scope, "packageHash": first.configured.PackageHash, "backends": names, "profiles": profileNames, "policy": policyName, "runSubmitted": false, "readiness": "not_established"})
}

// toolboxProfile is the backend's toolbox profile: the backend's own profile
// with the catalogue's toolbox runtime artifacts, lending exactly tools in
// catalogue order. nil when the package exports no toolbox.
func toolboxProfile(profile *api.CellnRuntimeProfile, toolbox *api.AgentRuntimeCellnProfile, tools []api.ClusterCellnToolRef) *api.CellnRuntimeProfile {
	if toolbox == nil || len(tools) == 0 {
		return nil
	}
	out := profile.DeepCopy()
	out.Name = cellnplatform.ToolboxProfileName(profile.Name)
	out.Labels[cellnplatform.ToolboxLabel] = "true"
	out.Annotations[cellnplatform.ToolboxToolsAnnotation] = cellnplatform.ToolboxAnnotation(tools)
	out.Spec.Revision, out.Spec.Executable, out.Spec.Closure, out.Spec.Mote = toolbox.Revision, toolbox.Executable, toolbox.Closure, toolbox.Mote
	out.Spec.PublisherKey, out.Spec.EntryPoint = toolbox.PublisherKey, toolbox.EntryPoint
	return out
}

// sameToolbox compares two backends' toolbox runtimes apart from the turn
// lifetime, which follows each backend's output-token cap.
func sameToolbox(a, b *api.AgentRuntimeCellnProfile) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	left, right := *a, *b
	left.Limits.TimeoutMillis, right.Limits.TimeoutMillis = 0, 0
	return reflect.DeepEqual(left, right)
}

// ClusterIdentity is the stable cluster identity every platform decision and
// scoped parent incarnation binds: the kube-system namespace UID.
func ClusterIdentity(ctx context.Context, store client.Reader) (string, error) {
	var system corev1.Namespace
	if err := store.Get(ctx, types.NamespacedName{Name: "kube-system"}, &system); err != nil {
		return "", fmt.Errorf("cluster identity unavailable: %w", err)
	}
	if system.UID == "" {
		return "", fmt.Errorf("cluster identity unavailable")
	}
	return string(system.UID), nil
}

// ensurePlatformPolicy creates the scope's policy, or extends an existing one
// from the same package with any runtime profile or route it lacks. Existing
// entries, the selector and the ceilings are never rewritten here.
//
// With replace, a policy from another package is rewritten to this one.
func ensurePlatformPolicy(ctx context.Context, store client.Client, policy *api.CellnExecutionPolicy, packageHash string, replace bool) error {
	err := store.Create(ctx, policy)
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create %s: %w; partial installation retained, no run was submitted", policy.Name, err)
	}
	var existing api.CellnExecutionPolicy
	if err := store.Get(ctx, client.ObjectKeyFromObject(policy), &existing); err != nil {
		return err
	}
	if existing.Annotations[packageAnnotation] != packageHash || replace {
		if !replace {
			return fmt.Errorf("%s exists from another package; one scope carries exactly one package", policy.Name)
		}
		// An operator's origin guardrail is kept across a rewrite.
		policy.Spec.AllowedOrigins, policy.Spec.DeniedOrigins = existing.Spec.AllowedOrigins, existing.Spec.DeniedOrigins
		policy.ResourceVersion = existing.ResourceVersion
		return store.Update(ctx, policy)
	}
	patch := client.MergeFrom(existing.DeepCopy())
	changed := false
	for _, ref := range policy.Spec.RuntimeProfiles {
		if !slices.ContainsFunc(existing.Spec.RuntimeProfiles, func(r api.CellnExecutionPolicyRuntime) bool { return r.Ref == ref.Ref }) {
			existing.Spec.RuntimeProfiles = append(existing.Spec.RuntimeProfiles, ref)
			changed = true
		}
	}
	// An added backend may grant a longer turn than any before it; the
	// ceiling follows it up and is never lowered under running agents.
	if policy.Spec.Ceilings.MaxTurnSeconds > existing.Spec.Ceilings.MaxTurnSeconds {
		existing.Spec.Ceilings.MaxTurnSeconds = policy.Spec.Ceilings.MaxTurnSeconds
		changed = true
	}
	if !changed {
		return nil
	}
	return store.Patch(ctx, &existing, patch)
}

// ensurePlatformObject creates the object, or accepts an existing one that was
// published from the same package and catalogue. A cluster catalogue object
// of the same package but another catalogue (the nodes re-derived the
// configuration with another Celln binary) is replaced in place. Anything
// else is a conflict, replaced only when the operator approved moving the
// scope to this package.
func ensurePlatformObject(ctx context.Context, store client.Client, object client.Object, packageHash, catalogueHash string, replace bool) error {
	err := store.Create(ctx, object)
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create %s: %w; partial installation retained, no run was submitted", object.GetName(), err)
	}
	existing := object.DeepCopyObject().(client.Object)
	if err := store.Get(ctx, client.ObjectKeyFromObject(object), existing); err != nil {
		return err
	}
	if existing.GetAnnotations()[packageAnnotation] != packageHash {
		if !replace {
			return fmt.Errorf("%s exists from another package; one scope carries exactly one package", object.GetName())
		}
		if object.GetNamespace() != "" {
			// A namespace's wrappers are rebound in place (rebindTenantWrappers):
			// deleting its Agent would take the runs that name it with it. Only
			// the package record moves.
			patch := client.MergeFrom(existing.DeepCopyObject().(client.Object))
			annotations := existing.GetAnnotations()
			if annotations == nil {
				annotations = map[string]string{}
			}
			annotations[packageAnnotation] = packageHash
			existing.SetAnnotations(annotations)
			return store.Patch(ctx, existing, patch)
		}
		return replacePlatformObject(ctx, store, object, existing)
	}
	if object.GetNamespace() == "" && catalogueHash != "" && existing.GetAnnotations()[catalogueAnnotation] != catalogueHash {
		return replacePlatformObject(ctx, store, object, existing)
	}
	return nil
}

// keyedToModelFree reports whether the scope's installed profile for a
// model-free backend still carries a fleet-held model (its node material
// named a model profile): the 0.12 upgrade, which replaces it.
func keyedToModelFree(ctx context.Context, store client.Reader, backends []backendConfiguration, scope string) (bool, error) {
	for _, b := range backends {
		var existing api.CellnRuntimeProfile
		err := store.Get(ctx, types.NamespacedName{Name: PlatformProfileName(scope, b.name)}, &existing)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if existing.Spec.Native != nil && existing.Spec.Native.ModelProfile != "" {
			return true, nil
		}
	}
	return false, nil
}

// retireKeyedWrappers removes what a fleet-held model left in every
// namespace: the shared Agents and host-profile ModelConnections the platform
// managed, and the profiles and wrappers of backends the model-free scope no
// longer configures. Wrappers a namespace made itself are left alone.
func retireKeyedWrappers(ctx context.Context, store client.Client, backends []backendConfiguration) error {
	current := map[string]bool{}
	for _, b := range backends {
		current[b.name] = true
	}
	managed := client.MatchingLabels{cellnplatform.ManagedByLabel: cellnplatform.ManagedByValue}
	var agents api.AgentList
	if err := store.List(ctx, &agents, managed); err != nil {
		return err
	}
	for i := range agents.Items {
		if err := deleteIgnoringMissing(ctx, store, &agents.Items[i]); err != nil {
			return err
		}
	}
	var connections api.ModelConnectionList
	if err := store.List(ctx, &connections, managed); err != nil {
		return err
	}
	for i := range connections.Items {
		if err := deleteIgnoringMissing(ctx, store, &connections.Items[i]); err != nil {
			return err
		}
	}
	var runtimes api.AgentRuntimeList
	if err := store.List(ctx, &runtimes, managed); err != nil {
		return err
	}
	for i := range runtimes.Items {
		if backend := runtimes.Items[i].Labels[cellnplatform.BackendLabel]; backend != "" && !current[backend] {
			if err := deleteIgnoringMissing(ctx, store, &runtimes.Items[i]); err != nil {
				return err
			}
		}
	}
	var profiles api.CellnRuntimeProfileList
	if err := store.List(ctx, &profiles); err != nil {
		return err
	}
	for i := range profiles.Items {
		p := &profiles.Items[i]
		if backend := p.Labels[cellnplatform.BackendLabel]; backend != "" && !current[backend] && p.Annotations[packageAnnotation] != "" {
			if err := deleteIgnoringMissing(ctx, store, p); err != nil {
				return err
			}
		}
	}
	// No node mounts the fleet's model credentials any more.
	return deleteIgnoringMissing(ctx, store, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: fleetNamespace, Name: FleetModelCredentialSecret}})
}
