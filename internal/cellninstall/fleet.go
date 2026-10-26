package cellninstall

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	"github.com/zeebo/blake3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Names shared between the chart's fleet mode and the installer.
const (
	FleetParentTokenSecret      = "celln-router-parent"
	FleetParentClientsConfigMap = "celln-fleet-parent-clients"
	FleetConfigurationConfigMap = "celln-fleet-configuration"
	// ConfiguredByAnnotation names the Celln binary (by content hash) that
	// derived a published fleet configuration; fleet-prepare.sh writes it.
	ConfiguredByAnnotation = "celln.sympozium.ai/configured-by"
	// configuredByFile records, in the operator's configuration directory,
	// which binary derived the materialized files.
	configuredByFile = "configured-by"
	// FleetCellsConfigMap is where every node reports its Celln cells.
	FleetCellsConfigMap = "celln-fleet-cells"
	// FleetModelCredentialSecret held fleet-keyed backends' provider keys before
	// 0.12; the migration deletes it. No node holds a model key.
	FleetModelCredentialSecret = "celln-fleet-model-credentials"

	fleetNamespace = "celln-system"
)

var (
	scopePattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	digestImagePattern = regexp.MustCompile(`^[^@:\s,]+(:[0-9]+)?(/[^@\s,]+)?@sha256:[a-f0-9]{64}$`)
	blake3Pattern      = regexp.MustCompile(`^blake3:[a-f0-9]{64}$`)
)

// FleetOptions describes one per-node native fleet: every node labeled
// celln.dev/kvm=true prepares the same reviewed package under
// /var/lib/sympozium-celln/<scope> and serves parents from it.
type FleetOptions struct {
	Scope, Principal, Publisher string
	PackageImage, PackageHash   string
	// Limits are the scope's parent ceilings; zero fields take DefaultFleetLimits.
	Limits FleetLimits
	// HTTPSHosts are the hosts the https-fetch and https-post-json starter
	// tools may reach. Empty keeps Celln's default, ["*"]: any public HTTPS
	// host (never a private, loopback or link-local address).
	HTTPSHosts []string
}

// StarterToolNames are Celln's own brokered tools, in the order the starter
// package bundles them. Every other catalogue tool is a command borrowed from
// a pinned image.
var StarterToolNames = []string{"workspace-read", "workspace-write", "https-fetch", "workspace-list", "workspace-append", "workspace-search", "workspace-delete", "https-post-json"}

var httpsHostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?)+$`)

// FleetBackend is one starter configuration of a scope, by DNS-label name.
// Since 0.12 there is exactly one, model-free: the fleet calls no provider.
type FleetBackend struct {
	Name string
}

var backendNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ResolvedBackends is the one configuration every node of the scope prepares:
// the model-free backend named native. It calls no provider and holds no key;
// every run brings its own route through the gateway.
func (o FleetOptions) ResolvedBackends() ([]FleetBackend, error) {
	return []FleetBackend{{Name: cellnplatform.DefaultBackend}}, nil
}

// FleetLimits bound one parent for its whole life. They become the policy
// ceilings every run in the scope is admitted under; Celln validates them
// again when configuring each node.
type FleetLimits struct {
	LeaseSeconds     int64
	MaxTurns         int64
	MaxModelRequests int64
	MaxOutputTokens  int64
}

// defaultFleetTurns is how many turns the default ceilings afford one parent.
const defaultFleetTurns = 256

// Bounds of the lifetime totals. The minima are one turn of the starter
// profile's allowance (Celln refuses less when configuring a node); the
// maxima are the CRD's: for output tokens, 1024 turns of the largest per-turn
// allowance a backend may have (api.MaxLifetimeOutputTokens).
const (
	MinFleetModelRequests = api.TurnModelRequests
	MaxFleetModelRequests = 6144
	MinFleetOutputTokens  = api.TurnOutputTokens
	MaxFleetOutputTokens  = api.MaxLifetimeOutputTokens
)

// DefaultFleetLimits favour long-running agents: a day-long parent with room
// for a working session of turns. Every turn reserves the starter profile's
// whole per-turn allowance (api.TurnModelRequests requests,
// api.TurnOutputTokens output tokens) from these totals, so they are sized as
// turns × allowance.
var DefaultFleetLimits = FleetLimits{LeaseSeconds: 86400, MaxTurns: defaultFleetTurns, MaxModelRequests: defaultFleetTurns * api.TurnModelRequests, MaxOutputTokens: defaultFleetTurns * api.TurnOutputTokens}

// CapFleetTotal bounds a suggested lifetime total by its maximum.
func CapFleetTotal(total, maximum int64) int64 { return min(total, maximum) }

// TurnsAfforded is how many turns lifetime totals pay for when every turn
// reserves turnRequests model requests and turnTokens output tokens.
func TurnsAfforded(maxModelRequests, maxOutputTokens, turnRequests, turnTokens int64) int64 {
	if turnRequests < 1 || turnTokens < 1 {
		return 0
	}
	return min(maxModelRequests/turnRequests, maxOutputTokens/turnTokens)
}

// ResolveModelFree sizes and checks the ceilings of a model-free scope. Its
// nodes are configured for the largest per-request cap an Agent's connection
// may bring (api.MaxRequestOutputTokens), so an unset lifetime output-token
// ceiling is sized for the default turns at that cap, and an explicit one
// must pay for at least one such turn. The note says when it was sized.
func (l FleetLimits) ResolveModelFree() (FleetLimits, string, error) {
	turnTokens := int64(api.TurnModelRequests * api.MaxRequestOutputTokens)
	note := ""
	if l.MaxOutputTokens == 0 {
		turns := DefaultFleetLimits.MaxTurns
		if l.MaxTurns != 0 {
			turns = l.MaxTurns
		}
		l.MaxOutputTokens = CapFleetTotal(turns*turnTokens, MaxFleetOutputTokens)
		note = fmt.Sprintf("Lifetime output-token ceiling sized to %d (%d turns × %d tokens, a turn of %d requests at the largest cap a connection may set, %d)", l.MaxOutputTokens, turns, turnTokens, api.TurnModelRequests, api.MaxRequestOutputTokens)
	}
	resolved, err := l.Resolve()
	if err != nil {
		return resolved, "", err
	}
	if resolved.MaxOutputTokens < turnTokens {
		return resolved, "", fmt.Errorf("fleet limits: %d lifetime output tokens do not pay for one turn at the largest per-request cap (%d requests × %d = %d); pass --celln-fleet-max-output-tokens %d or more", resolved.MaxOutputTokens, api.TurnModelRequests, api.MaxRequestOutputTokens, turnTokens, turnTokens)
	}
	return resolved, note, nil
}

// Resolve fills zero fields from the defaults and applies the CRD bounds, for
// backends with the default per-turn allowance; ResolveFor sizes and checks
// them for the backends a scope is installed with.
func (l FleetLimits) Resolve() (FleetLimits, error) {
	d := DefaultFleetLimits
	if l.LeaseSeconds == 0 {
		l.LeaseSeconds = d.LeaseSeconds
	}
	if l.MaxTurns == 0 {
		l.MaxTurns = d.MaxTurns
	}
	if l.MaxModelRequests == 0 {
		l.MaxModelRequests = d.MaxModelRequests
	}
	if l.MaxOutputTokens == 0 {
		l.MaxOutputTokens = d.MaxOutputTokens
	}
	if l.LeaseSeconds < 60 || l.LeaseSeconds > 86400 || l.MaxTurns < 1 || l.MaxTurns > 1024 || l.MaxModelRequests < MinFleetModelRequests || l.MaxModelRequests > MaxFleetModelRequests || l.MaxOutputTokens < MinFleetOutputTokens || l.MaxOutputTokens > MaxFleetOutputTokens {
		return l, fmt.Errorf("fleet limits out of range: lease 60–86400 s, turns 1–1024, model requests %d–%d, output tokens %d–%d", MinFleetModelRequests, MaxFleetModelRequests, MinFleetOutputTokens, MaxFleetOutputTokens)
	}
	return l, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// StatePath is the per-node authority location the chart derives from Scope.
func (o FleetOptions) StatePath() string { return "/var/lib/sympozium-celln/" + o.Scope }

func (o FleetOptions) validate() error {
	identities := o.Principal + o.Publisher
	if !scopePattern.MatchString(o.Scope) || !digestImagePattern.MatchString(o.PackageImage) || !blake3Pattern.MatchString(o.PackageHash) ||
		o.Principal == "" || o.Publisher == "" || strings.ContainsAny(identities, " \t\r\n,=") {
		return fmt.Errorf("fleet requires a DNS-label scope, a digest-pinned package image, a blake3 package hash, a publisher and a principal")
	}
	if len(o.HTTPSHosts) > 16 {
		return fmt.Errorf("at most 16 HTTPS hosts")
	}
	for _, host := range o.HTTPSHosts {
		if host == "*" {
			if len(o.HTTPSHosts) != 1 {
				return fmt.Errorf(`"*" (any public host) must be the only HTTPS host`)
			}
			continue
		}
		if len(host) > 253 || !httpsHostPattern.MatchString(host) {
			return fmt.Errorf("HTTPS host %q must be a lowercase DNS name", host)
		}
	}
	return nil
}

// FleetValues renders the node-preparation phase. The controller is wired by
// ConfigureFleet only after the nodes have published the starter configuration.
func FleetValues(o FleetOptions) ([]string, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	limits, err := o.Limits.Resolve()
	if err != nil {
		return nil, err
	}
	// The chart configures the one model-free backend itself; the values
	// carry no model, endpoint or credential.
	values := make([]string, 0, 16+len(o.HTTPSHosts))
	for i, host := range o.HTTPSHosts {
		values = append(values, fmt.Sprintf("celln.fleet.httpsHosts[%d]=%s", i, host))
	}
	return append(values,
		fmt.Sprintf("celln.fleet.limits.leaseSeconds=%d", limits.LeaseSeconds),
		fmt.Sprintf("celln.fleet.limits.maxTurns=%d", limits.MaxTurns),
		fmt.Sprintf("celln.fleet.limits.maxModelRequests=%d", limits.MaxModelRequests),
		fmt.Sprintf("celln.fleet.limits.maxOutputTokens=%d", limits.MaxOutputTokens),
		"celln.dispatcher.enabled=false",
		"celln.router.backends=null",
		"celln.router.parentTokenSecret="+FleetParentTokenSecret,
		"celln.fleet.enabled=true",
		"celln.fleet.scope="+o.Scope,
		"celln.fleet.package.image="+o.PackageImage,
		"celln.fleet.package.hash="+o.PackageHash,
		"celln.fleet.publisher="+o.Publisher,
		"celln.fleet.principal="+o.Principal,
		"celln.fleet.parentClientsConfigMap="+FleetParentClientsConfigMap,
		"celln.fleet.configurationConfigMap="+FleetConfigurationConfigMap,
		"celln.fleet.cellsConfigMap="+FleetCellsConfigMap,
	), nil
}

// PrepareFleetTrust publishes the shared parent principal: the gateway's
// bearer credential and the hash-only client policy every owner installs. An
// existing pair is accepted only when the policy still matches the credential;
// a live principal is never replaced.
func PrepareFleetTrust(ctx context.Context, store client.Client, principal string) error {
	if principal == "" || strings.ContainsAny(principal, " \t\r\n") {
		return fmt.Errorf("bounded parent principal required")
	}
	var secret corev1.Secret
	err := store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: FleetParentTokenSecret}, &secret)
	switch {
	case apierrors.IsNotFound(err):
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		token := base64.RawURLEncoding.EncodeToString(raw)
		policy, err := parentClientPolicy(principal, token)
		if err != nil {
			return err
		}
		for _, object := range []client.Object{
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: FleetParentTokenSecret, Namespace: fleetNamespace, Labels: fleetLabels()}, Data: map[string][]byte{"token": []byte(token)}},
			&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: FleetParentClientsConfigMap, Namespace: fleetNamespace, Labels: fleetLabels()}, Data: map[string]string{"trusted-parent-clients.json": policy}},
		} {
			if err := store.Create(ctx, object); err != nil {
				return fmt.Errorf("publish %s: %w; partial fleet trust retained, never replaced", object.GetName(), err)
			}
		}
		return nil
	case err != nil:
		return err
	}
	var policy corev1.ConfigMap
	if err := store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: FleetParentClientsConfigMap}, &policy); err != nil {
		return fmt.Errorf("fleet credential exists without its client policy; reconcile %s manually", FleetParentClientsConfigMap)
	}
	expected, err := parentClientPolicy(principal, string(secret.Data["token"]))
	if err != nil || policy.Data["trusted-parent-clients.json"] != expected {
		return fmt.Errorf("existing fleet client policy does not match the gateway credential and principal; never rotate by replacement")
	}
	return nil
}

func parentClientPolicy(principal, token string) (string, error) {
	if len(token) < 24 || len(token) > 4096 || strings.ContainsAny(token, " \t\r\n") {
		return "", fmt.Errorf("invalid parent credential")
	}
	raw, err := json.Marshal(map[string]any{"apiVersion": "celln.parent-clients/v1", "clients": []map[string]string{{"principal": principal, "tokenHash": fmt.Sprintf("blake3:%x", blake3.Sum256([]byte(token)))}}})
	return string(raw), err
}

func fleetLabels() map[string]string {
	return map[string]string{"app.kubernetes.io/part-of": "sympozium"}
}

var fleetConfigurationFiles = []string{"catalogue.json", "configured.json", "native-template.json"}

// PublishedBackends lists the backends a fleet configuration ConfigMap
// carries: keys are "<backend>.<file>"; unprefixed keys belong to the default
// backend. Every backend must carry every file.
func PublishedBackends(data map[string]string) ([]string, error) {
	files := map[string]map[string]bool{}
	for key, content := range data {
		backend, file := "", ""
		for _, f := range fleetConfigurationFiles {
			switch {
			case key == f:
				backend, file = cellnplatform.DefaultBackend, f
			case strings.HasSuffix(key, "."+f):
				backend, file = strings.TrimSuffix(key, "."+f), f
			}
		}
		if file == "" {
			return nil, fmt.Errorf("published fleet configuration carries an unknown key %q", key)
		}
		if len(content) == 0 || len(content) > 1<<20 {
			return nil, fmt.Errorf("published fleet configuration is incomplete: %s", key)
		}
		if !backendNamePattern.MatchString(backend) {
			return nil, fmt.Errorf("published fleet configuration names an invalid backend %q", backend)
		}
		if files[backend] == nil {
			files[backend] = map[string]bool{}
		}
		files[backend][file] = true
	}
	backends := make([]string, 0, len(files))
	for backend, have := range files {
		for _, f := range fleetConfigurationFiles {
			if !have[f] {
				return nil, fmt.Errorf("published fleet configuration is incomplete: %s.%s", backend, f)
			}
		}
		backends = append(backends, backend)
	}
	sort.Strings(backends)
	return backends, nil
}

func publishedKey(backend, file string, data map[string]string) string {
	if backend == cellnplatform.DefaultBackend {
		if _, ok := data[file]; ok {
			return file
		}
	}
	return backend + "." + file
}

// ReadFleetConfiguration materializes the starter configuration a fleet node
// published into dir/<backend>/ for every backend, for InstallPlatform. It
// reports false until a node has published; existing files must be identical.
func ReadFleetConfiguration(ctx context.Context, store client.Client, dir string) (bool, error) {
	return ReadFleetConfigurationFor(ctx, store, dir, "", nil)
}

// ReadFleetConfigurationFor is ReadFleetConfiguration that also waits until
// every expected backend has been published, so adding a backend to a
// running scope blocks until a node has configured it. With packageHash set
// it also waits until the nodes have published that package, so moving a
// scope to a new package never reads the previous package's files.
func ReadFleetConfigurationFor(ctx context.Context, store client.Client, dir, packageHash string, expected []string) (bool, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return false, fmt.Errorf("clean absolute configuration directory required")
	}
	var published corev1.ConfigMap
	if err := store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: FleetConfigurationConfigMap}, &published); apierrors.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if packageHash != "" && published.Annotations[packageAnnotation] != packageHash {
		return false, nil
	}
	backends, err := PublishedBackends(published.Data)
	if err != nil {
		return false, err
	}
	if len(backends) == 0 {
		return false, fmt.Errorf("published fleet configuration carries no backend")
	}
	for _, name := range expected {
		if !slices.Contains(backends, name) {
			return false, nil
		}
	}
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return false, err
	}
	// A backend an earlier materialization of this package held but the
	// nodes no longer publish (the fleet-held backends a model-free scope
	// dropped) is removed, so the installer never installs it again.
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() && backendNamePattern.MatchString(e.Name()) && !slices.Contains(backends, e.Name()) {
				if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
					return false, err
				}
			}
		}
	}
	// The nodes record which Celln binary derived the publication. A
	// publication derived by another binary than the one this directory was
	// materialized from (an upgraded celln-node-configure image) legitimately
	// differs for the same package and replaces the local copy.
	marker := filepath.Join(dir, configuredByFile)
	configuredBy := published.Annotations[ConfiguredByAnnotation]
	rederived := false
	if existing, err := os.ReadFile(marker); err == nil {
		rederived = string(existing) != configuredBy
	} else if configuredBy != "" {
		// Materialized before the nodes recorded their binary.
		rederived = true
	}
	for _, backend := range backends {
		if err := os.Mkdir(filepath.Join(dir, backend), 0700); err != nil && !os.IsExist(err) {
			return false, err
		}
		// A backend materialized with a fleet-held model whose nodes now
		// publish it model-free (the 0.12 upgrade) is rewritten, as is one
		// the nodes re-derived with another Celln binary; any other
		// difference is drift between the operator's directory and the
		// nodes, which is refused.
		receipt := filepath.Join(dir, backend, "configured.json")
		migrating := rederived
		if existing, err := os.ReadFile(receipt); err == nil && !migrating {
			migrating = receiptNamesModel(existing) && !receiptNamesModel([]byte(published.Data[publishedKey(backend, "configured.json", published.Data)]))
		}
		for _, name := range fleetConfigurationFiles {
			content := published.Data[publishedKey(backend, name, published.Data)]
			path := filepath.Join(dir, backend, name)
			if existing, err := os.ReadFile(path); err == nil && !migrating {
				if string(existing) != content {
					return false, fmt.Errorf("existing %s/%s differs from the published fleet configuration", backend, name)
				}
				continue
			}
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				return false, err
			}
		}
	}
	if configuredBy == "" {
		if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
			return false, err
		}
	} else if err := os.WriteFile(marker, []byte(configuredBy), 0600); err != nil {
		return false, err
	}
	return true, nil
}

// receiptNamesModel reports whether a configured.json names a model profile,
// which only a node that calls a model itself (a fleet-held key) writes.
func receiptNamesModel(raw []byte) bool {
	var receipt struct {
		ModelProfile string `json:"modelProfile"`
	}
	return json.Unmarshal(raw, &receipt) == nil && receipt.ModelProfile != ""
}

// ConfigurationBackends lists the backend directories a materialized
// configuration holds, sorted.
func ConfigurationBackends(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && backendNamePattern.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("configuration directory %s holds no backend", dir)
	}
	return out, nil
}

// FleetConfigureDaemonSet prepares every node and carries the scope's facts.
const FleetConfigureDaemonSet = "celln-node-configure"

// FleetFacts are the scope's identities the configure DaemonSet carries, so
// a caller that did not install the fleet can act on it.
type FleetFacts struct {
	Scope, Principal, PackageHash string
}

// ReadFleetFacts reads them from the configure DaemonSet's environment.
func ReadFleetFacts(ctx context.Context, store client.Reader) (FleetFacts, error) {
	var ds appsv1.DaemonSet
	if err := store.Get(ctx, types.NamespacedName{Namespace: fleetNamespace, Name: FleetConfigureDaemonSet}, &ds); err != nil {
		return FleetFacts{}, fmt.Errorf("no Celln fleet is installed: %w", err)
	}
	var facts FleetFacts
	for _, c := range ds.Spec.Template.Spec.Containers {
		for _, env := range c.Env {
			switch env.Name {
			case "FLEET_SCOPE":
				facts.Scope = env.Value
			case "FLEET_PRINCIPAL":
				facts.Principal = env.Value
			case "FLEET_PACKAGE_HASH":
				facts.PackageHash = env.Value
			}
		}
	}
	if facts.Scope == "" || facts.Principal == "" || facts.PackageHash == "" {
		return FleetFacts{}, fmt.Errorf("configure DaemonSet does not carry the fleet's scope, principal and package hash")
	}
	return facts, nil
}
