package cellninstall

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/zeebo/blake3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func fleetStore() client.Client {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func validFleet() FleetOptions {
	return FleetOptions{Scope: "starter", Principal: "sympozium:celln", Publisher: "ed25519:operator", PackageImage: "registry.example/celln/starter@sha256:" + strings.Repeat("a", 64), PackageHash: "blake3:" + strings.Repeat("b", 64)}
}

func TestFleetValuesRefuseAmbiguousOrUnpinnedInputs(t *testing.T) {
	values, err := FleetValues(validFleet())
	if err != nil || !strings.Contains(strings.Join(values, "\n"), "celln.fleet.scope=starter") || !strings.Contains(strings.Join(values, "\n"), "celln.router.backends=null") {
		t.Fatalf("valid fleet refused: %v %v", err, values)
	}
	for name, change := range map[string]func(*FleetOptions){
		"scope":                func(o *FleetOptions) { o.Scope = "Starter" },
		"tag":                  func(o *FleetOptions) { o.PackageImage = "registry.example/celln/starter:latest" },
		"hash":                 func(o *FleetOptions) { o.PackageHash = "sha256:" + strings.Repeat("b", 64) },
		"publisher":            func(o *FleetOptions) { o.Publisher = "" },
		"comma":                func(o *FleetOptions) { o.Publisher = "a,b" },
		"principal":            func(o *FleetOptions) { o.Principal = "sympozium celln" },
		"uppercase https host": func(o *FleetOptions) { o.HTTPSHosts = []string{"Hooks.Example"} },
		"bare https host":      func(o *FleetOptions) { o.HTTPSHosts = []string{"localhost"} },
		"https host with port": func(o *FleetOptions) { o.HTTPSHosts = []string{"hooks.example:8080"} },
		"any host mixed":       func(o *FleetOptions) { o.HTTPSHosts = []string{"*", "hooks.example"} },
		"wildcard pattern":     func(o *FleetOptions) { o.HTTPSHosts = []string{"*.example.com"} },
	} {
		o := validFleet()
		change(&o)
		if _, err := FleetValues(o); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestFleetValuesCarryTheHTTPSHostsInOrder(t *testing.T) {
	o := validFleet()
	o.HTTPSHosts = []string{"hooks.example", "echo.tenant.svc.cluster.local"}
	values, err := FleetValues(o)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(values, "\n")
	if !strings.Contains(joined, "celln.fleet.httpsHosts[0]=hooks.example") || !strings.Contains(joined, "celln.fleet.httpsHosts[1]=echo.tenant.svc.cluster.local") {
		t.Fatalf("hosts not rendered: %v", values)
	}
	o.HTTPSHosts = []string{"*"}
	if values, err := FleetValues(o); err != nil || !strings.Contains(strings.Join(values, "\n"), "celln.fleet.httpsHosts[0]=*") {
		t.Fatalf("any public host not rendered: %v %v", err, values)
	}
	if values, err := FleetValues(validFleet()); err != nil || strings.Contains(strings.Join(values, "\n"), "httpsHosts") {
		t.Fatalf("empty hosts must leave the reviewed default: %v %v", err, values)
	}
}

func TestPrepareFleetTrustPublishesHashOnlyPolicyOnce(t *testing.T) {
	ctx := context.Background()
	store := fleetStore()
	if err := PrepareFleetTrust(ctx, store, "sympozium:celln"); err != nil {
		t.Fatal(err)
	}
	var secret corev1.Secret
	var policy corev1.ConfigMap
	if err := store.Get(ctx, types.NamespacedName{Namespace: "celln-system", Name: FleetParentTokenSecret}, &secret); err != nil {
		t.Fatal(err)
	}
	if err := store.Get(ctx, types.NamespacedName{Namespace: "celln-system", Name: FleetParentClientsConfigMap}, &policy); err != nil {
		t.Fatal(err)
	}
	token := string(secret.Data["token"])
	document := policy.Data["trusted-parent-clients.json"]
	if len(token) < 24 || strings.Contains(document, token) || !strings.Contains(document, fmt.Sprintf("blake3:%x", blake3.Sum256([]byte(token)))) || !strings.Contains(document, `"principal":"sympozium:celln"`) {
		t.Fatalf("policy must carry only the credential hash: %s", document)
	}
	if err := PrepareFleetTrust(ctx, store, "sympozium:celln"); err != nil {
		t.Fatalf("matching existing trust refused: %v", err)
	}
	if err := PrepareFleetTrust(ctx, store, "another:principal"); err == nil {
		t.Fatal("principal silently rotated")
	}
	if err := store.Delete(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	if err := PrepareFleetTrust(ctx, store, "sympozium:celln"); err == nil {
		t.Fatal("credential without policy accepted")
	}
}

func TestReadFleetConfigurationWaitsForNodesAndRefusesDrift(t *testing.T) {
	ctx := context.Background()
	store := fleetStore()
	dir := filepath.Join(t.TempDir(), "configuration")
	if published, err := ReadFleetConfiguration(ctx, store, dir); err != nil || published {
		t.Fatalf("unpublished configuration reported: %v %v", published, err)
	}
	data := map[string]string{"catalogue.json": `{"tools":[]}`, "configured.json": `{"apiVersion":"celln.native-starter-configured/v1"}`, "native-template.json": `{"maxTurns":12}`}
	if err := store.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: FleetConfigurationConfigMap, Namespace: "celln-system"}, Data: map[string]string{"catalogue.json": data["catalogue.json"]}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFleetConfiguration(ctx, store, dir); err == nil {
		t.Fatal("incomplete publication accepted")
	}
	var published corev1.ConfigMap
	_ = store.Get(ctx, types.NamespacedName{Namespace: "celln-system", Name: FleetConfigurationConfigMap}, &published)
	published.Data = data
	if err := store.Update(ctx, &published); err != nil {
		t.Fatal(err)
	}
	if ok, err := ReadFleetConfiguration(ctx, store, dir); err != nil || !ok {
		t.Fatalf("published configuration not materialized: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "native", "native-template.json")); err != nil || string(raw) != data["native-template.json"] {
		t.Fatal("materialized bytes differ")
	}
	if ok, err := ReadFleetConfiguration(ctx, store, dir); err != nil || !ok {
		t.Fatalf("identical rerun refused: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "native", "catalogue.json"), []byte(`{"tools":["drift"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFleetConfiguration(ctx, store, dir); err == nil {
		t.Fatal("drifted local configuration accepted")
	}
}

// A publication the nodes re-derived with another Celln binary (recorded in
// configured-by) replaces the operator's local copy for the same package; a
// local change under the same binary is still drift.
func TestReadFleetConfigurationFollowsARederivedPublication(t *testing.T) {
	ctx := context.Background()
	store := fleetStore()
	dir := filepath.Join(t.TempDir(), "configuration")
	data := func(persona string) map[string]string {
		return map[string]string{"native.catalogue.json": `{"systemPrompt":"` + persona + `","tools":[]}`, "native.configured.json": `{"apiVersion":"celln.native-starter-configured/v1"}`, "native.native-template.json": `{"maxTurns":12}`}
	}
	published := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: FleetConfigurationConfigMap, Namespace: "celln-system", Annotations: map[string]string{ConfiguredByAnnotation: "celln-old"}}, Data: data("terse")}
	if err := store.Create(ctx, published); err != nil {
		t.Fatal(err)
	}
	if ok, err := ReadFleetConfiguration(ctx, store, dir); err != nil || !ok {
		t.Fatalf("first materialization: %v %v", ok, err)
	}
	if marker, err := os.ReadFile(filepath.Join(dir, configuredByFile)); err != nil || string(marker) != "celln-old" {
		t.Fatalf("binary not recorded: %q %v", marker, err)
	}
	published.Annotations[ConfiguredByAnnotation] = "celln-new"
	published.Data = data("fetch first")
	if err := store.Update(ctx, published); err != nil {
		t.Fatal(err)
	}
	if ok, err := ReadFleetConfiguration(ctx, store, dir); err != nil || !ok {
		t.Fatalf("re-derived publication refused: %v %v", ok, err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "native", "catalogue.json")); !strings.Contains(string(raw), "fetch first") {
		t.Fatalf("local copy not replaced: %s", raw)
	}
	if marker, _ := os.ReadFile(filepath.Join(dir, configuredByFile)); string(marker) != "celln-new" {
		t.Fatalf("binary record not updated: %q", marker)
	}
	if err := os.WriteFile(filepath.Join(dir, "native", "catalogue.json"), []byte(`{"systemPrompt":"tampered","tools":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFleetConfiguration(ctx, store, dir); err == nil {
		t.Fatal("drift under the same binary accepted")
	}
}

func TestFleetLimitsDefaultToLongRunningAndAreBounded(t *testing.T) {
	got, err := FleetLimits{}.Resolve()
	if err != nil || got != DefaultFleetLimits || got.LeaseSeconds != 86400 {
		t.Fatalf("defaults: %+v %v", got, err)
	}
	got, err = FleetLimits{LeaseSeconds: 3600}.Resolve()
	if err != nil || got.LeaseSeconds != 3600 || got.MaxTurns != DefaultFleetLimits.MaxTurns {
		t.Fatalf("partial override: %+v %v", got, err)
	}
	// Every turn reserves 6 requests and 3072 tokens, so the defaults afford
	// exactly their 256 turns.
	if got.MaxTurns != 256 || got.MaxModelRequests != 1536 || got.MaxOutputTokens != 786432 || TurnsAfforded(got.MaxModelRequests, got.MaxOutputTokens, api.TurnModelRequests, api.TurnOutputTokens) != got.MaxTurns {
		t.Fatalf("defaults do not afford their turns: %+v", got)
	}
	for name, l := range map[string]FleetLimits{"lease too long": {LeaseSeconds: 86401}, "lease too short": {LeaseSeconds: 30}, "no turns": {MaxTurns: -1}, "tokens below one turn": {MaxOutputTokens: 3071}, "requests below one turn": {MaxModelRequests: 5}, "the old one-turn minimum": {MaxModelRequests: 3, MaxOutputTokens: 1536}, "too many requests": {MaxModelRequests: 6145}, "too many tokens": {MaxOutputTokens: 25165825}} {
		if _, err := l.Resolve(); err == nil || !strings.Contains(err.Error(), "model requests 6–6144, output tokens 3072–25165824") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if one, err := (FleetLimits{MaxTurns: 1, MaxModelRequests: 6, MaxOutputTokens: 3072}).Resolve(); err != nil || one.MaxModelRequests != 6 {
		t.Fatalf("one turn of the allowance refused: %+v %v", one, err)
	}
	o := validFleet()
	o.Limits = FleetLimits{LeaseSeconds: 7200}
	values, err := FleetValues(o)
	joined := strings.Join(values, "\n")
	if err != nil || !strings.Contains(joined, "celln.fleet.limits.leaseSeconds=7200") || !strings.Contains(joined, "celln.fleet.limits.maxTurns=256") {
		t.Fatalf("limits not rendered: %v %s", err, joined)
	}
}

// Nodes publish one configuration per backend; the installer materializes
// each under its own directory and accepts the legacy single-backend keys.
func TestReadFleetConfigurationMaterializesEveryBackend(t *testing.T) {
	ctx := context.Background()
	store := fleetStore()
	data := map[string]string{}
	for _, f := range fleetConfigurationFiles {
		data[f] = "{\"default\":\"" + f + "\"}"
		data["claude."+f] = "{\"claude\":\"" + f + "\"}"
	}
	backends, err := PublishedBackends(data)
	if err != nil || strings.Join(backends, ",") != "claude,native" {
		t.Fatalf("published backends: %v %v", backends, err)
	}
	delete(data, "claude.configured.json")
	if _, err := PublishedBackends(data); err == nil {
		t.Fatal("incomplete backend accepted")
	}
	data["claude.configured.json"] = "{}"
	data["stray.txt"] = "x"
	if _, err := PublishedBackends(data); err == nil {
		t.Fatal("unknown key accepted")
	}
	delete(data, "stray.txt")
	if err := store.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: FleetConfigurationConfigMap, Namespace: "celln-system"}, Data: data}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "configuration")
	if ok, err := ReadFleetConfiguration(ctx, store, dir); err != nil || !ok {
		t.Fatalf("read: %v %v", ok, err)
	}
	got, err := ConfigurationBackends(dir)
	if err != nil || strings.Join(got, ",") != "claude,native" {
		t.Fatalf("materialized backends: %v %v", got, err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "native", "catalogue.json")); string(raw) != data["catalogue.json"] {
		t.Fatalf("default backend file: %s", raw)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "claude", "configured.json")); string(raw) != "{}" {
		t.Fatalf("claude backend file: %s", raw)
	}
	// Drift between the operator's directory and the nodes is refused...
	if err := os.WriteFile(filepath.Join(dir, "native", "catalogue.json"), []byte(`{"edited":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFleetConfiguration(ctx, store, dir); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("drift accepted: %v", err)
	}
	// ...except the 0.12 upgrade: a backend materialized with a fleet-held
	// model is rewritten from the model-free publication, and backends the
	// nodes no longer publish are removed.
	if err := os.WriteFile(filepath.Join(dir, "native", "configured.json"), []byte(`{"modelProfile":"blake3:old","model":{"provider":"deepseek"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var cm corev1.ConfigMap
	if err := store.Get(ctx, types.NamespacedName{Namespace: "celln-system", Name: FleetConfigurationConfigMap}, &cm); err != nil {
		t.Fatal(err)
	}
	for key := range cm.Data {
		if strings.HasPrefix(key, "claude.") {
			delete(cm.Data, key)
		}
	}
	if err := store.Update(ctx, &cm); err != nil {
		t.Fatal(err)
	}
	if ok, err := ReadFleetConfiguration(ctx, store, dir); err != nil || !ok {
		t.Fatalf("model-free rewrite: %v %v", ok, err)
	}
	if got, err := ConfigurationBackends(dir); err != nil || strings.Join(got, ",") != "native" {
		t.Fatalf("stale backend kept: %v %v", got, err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "native", "catalogue.json")); string(raw) != data["catalogue.json"] {
		t.Fatalf("keyed files were not rewritten: %s", raw)
	}
}

// fleetStoreWith is fleetStore seeded with objects.
func fleetStoreWith(objects ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}
