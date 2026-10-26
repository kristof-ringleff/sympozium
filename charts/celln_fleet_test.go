package charts

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func fleetValues() []string {
	return []string{
		"celln.enabled=true", "celln.allowInsecureHttp=true",
		"celln.tokenSecret=client", "celln.capabilityTokenSecret=discovery",
		"celln.router.clientTokenSecret=client", "celln.router.backendTokenSecret=backend",
		"celln.router.capabilityTokenSecret=discovery", "celln.router.parentTokenSecret=parent",
		"celln.router.ownershipClaim=ledger", "celln.router.allowInsecureBackends=true",
		"celln.router.image.tag=fleet-test",
		"celln.fleet.enabled=true", "celln.fleet.scope=starter",
		"celln.fleet.package.image=registry.example/celln/starter@sha256:" + strings.Repeat("a", 64),
		"celln.fleet.package.hash=blake3:" + strings.Repeat("b", 64),
		"celln.fleet.publisher=ed25519:operator", "celln.fleet.principal=sympozium:celln",
		"celln.fleet.parentClientsConfigMap=parent-clients",
	}
}

type fleetBackend struct {
	Name           string `json:"name"`
	Provider       string `json:"provider"`
	Protocol       string `json:"protocol"`
	Endpoint       string `json:"endpoint"`
	Model          string `json:"model"`
	AllowInsecure  bool   `json:"allowInsecure"`
	CredentialFile string `json:"credentialFile"`
}

// prepareBackends decodes the backend list the chart hands node configuration
// (the celln-node-configure DaemonSet's container).
func prepareBackends(t *testing.T, spec corev1.PodSpec) (map[string]string, []fleetBackend) {
	t.Helper()
	env := map[string]string{}
	for _, e := range spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	var backends []fleetBackend
	if err := json.Unmarshal([]byte(env["FLEET_BACKENDS"]), &backends); err != nil {
		t.Fatalf("FLEET_BACKENDS is not a JSON list: %v: %q", err, env["FLEET_BACKENDS"])
	}
	return env, backends
}

type fleetRender struct {
	daemonSets  map[string]appsv1.DaemonSet
	deployments map[string]appsv1.Deployment
	services    map[string]corev1.Service
	claims      map[string]corev1.PersistentVolumeClaim
}

func decodeFleet(t *testing.T, raw []byte) fleetRender {
	t.Helper()
	out := fleetRender{map[string]appsv1.DaemonSet{}, map[string]appsv1.Deployment{}, map[string]corev1.Service{}, map[string]corev1.PersistentVolumeClaim{}}
	for _, document := range bytes.Split(raw, []byte("\n---")) {
		var meta struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal(document, &meta); err != nil {
			t.Fatal(err)
		}
		switch meta.Kind {
		case "DaemonSet":
			var d appsv1.DaemonSet
			if err := yaml.Unmarshal(document, &d); err != nil {
				t.Fatal(err)
			}
			out.daemonSets[d.Name] = d
		case "Deployment":
			var d appsv1.Deployment
			if err := yaml.Unmarshal(document, &d); err != nil {
				t.Fatal(err)
			}
			out.deployments[d.Name] = d
		case "Service":
			var s corev1.Service
			if err := yaml.Unmarshal(document, &s); err != nil {
				t.Fatal(err)
			}
			out.services[s.Name] = s
		case "PersistentVolumeClaim":
			var c corev1.PersistentVolumeClaim
			if err := yaml.Unmarshal(document, &c); err != nil {
				t.Fatal(err)
			}
			out.claims[c.Name] = c
		}
	}
	return out
}

func TestFleetRendersPerNodeOwnersBehindOneGateway(t *testing.T) {
	raw, err := renderNativeParent(t, fleetValues())
	if err != nil {
		t.Fatalf("render: %v: %s", err, raw)
	}
	r := decodeFleet(t, raw)
	if _, ok := r.deployments["celln-dispatcher"]; ok {
		t.Fatal("single pinned dispatcher rendered alongside the fleet")
	}
	node, ok := r.daemonSets["celln-node"]
	if !ok {
		t.Fatal("celln-node DaemonSet missing")
	}
	spec := node.Spec.Template.Spec
	if spec.NodeSelector["celln.dev/kvm"] != "true" || spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Fatal("fleet nodes must be label-selected and never automount an API token")
	}
	if len(spec.InitContainers) != 1 || spec.InitContainers[0].Name != "wait-prepared" || len(spec.Containers) != 1 || spec.Containers[0].Name != "dispatcher" {
		t.Fatalf("unexpected fleet pod shape: %+v", spec)
	}
	configure, ok := r.daemonSets["celln-node-configure"]
	if !ok || len(configure.Spec.Template.Spec.Containers) != 1 || configure.Spec.Template.Spec.Containers[0].Name != "configure" || configure.Spec.Template.Spec.NodeSelector["celln.dev/kvm"] != "true" {
		t.Fatalf("node configuration DaemonSet missing or misshaped: %+v", configure.Spec.Template.Spec)
	}
	configureSpec := configure.Spec.Template.Spec
	const state = "/var/lib/sympozium-celln/starter"
	args := strings.Join(spec.Containers[0].Args, " ")
	for _, want := range []string{"--root " + state + "/authority", "--node-name $(NODE_NAME)"} {
		if !strings.Contains(args, want) {
			t.Fatalf("dispatcher args lack %q: %s", want, args)
		}
	}
	// Default capacity is sized on the node; the flags come from the wrapper.
	command := strings.Join(spec.Containers[0].Command, " ")
	if strings.Contains(args, "--max-cells") || !strings.HasPrefix(command, "/bin/sh -ec") || !strings.Contains(command, "/proc/meminfo") || !strings.Contains(command, `--max-cells "$cells"`) || !strings.Contains(command, `--egress-slots "$cells"`) || !strings.Contains(command, "* 75 ))") {
		t.Fatalf("dispatcher does not size capacity on the node: command=%s args=%s", command, args)
	}
	// One model-free backend named native: no endpoint, no credential file,
	// nothing for a node to hold.
	prepareEnv, backends := prepareBackends(t, configureSpec)
	if len(backends) != 1 || backends[0].Name != "native" || backends[0].Endpoint != "" || backends[0].CredentialFile != "" || backends[0].Provider != "" || prepareEnv["FLEET_SCOPE"] != "starter" {
		t.Fatalf("the fleet must configure exactly one model-free backend named native: %+v", backends)
	}
	if raw := mustJSON(t, configureSpec); strings.Contains(raw, "model-credentials") || strings.Contains(raw, "backends-extra") {
		t.Fatal("the configure pod mounts model credentials or an extra backend list")
	}
	if prepareEnv["FLEET_LIMIT_LEASE_SECONDS"] != "0" {
		t.Fatalf("default fleet must keep Celln's reviewed host limits: %v", prepareEnv)
	}
	limited, err := renderNativeParent(t, append(fleetValues(), "celln.fleet.limits.leaseSeconds=86400", "celln.fleet.limits.maxTurns=256"))
	if err != nil {
		t.Fatalf("limits render: %v: %s", err, limited)
	}
	for _, e := range decodeFleet(t, limited).daemonSets["celln-node-configure"].Spec.Template.Spec.Containers[0].Env {
		prepareEnv[e.Name] = e.Value
	}
	if prepareEnv["FLEET_LIMIT_LEASE_SECONDS"] != "86400" || prepareEnv["FLEET_LIMIT_MAX_TURNS"] != "256" {
		t.Fatalf("host limits not passed to node preparation: %v", prepareEnv)
	}
	fixed, err := renderNativeParent(t, append(fleetValues(), "celln.fleet.capacity=fixed", "celln.fleet.maxCells=6", "celln.fleet.egressSlots="))
	if err != nil {
		t.Fatalf("fixed capacity render: %v: %s", err, fixed)
	}
	fixedSpec := decodeFleet(t, fixed).daemonSets["celln-node"].Spec.Template.Spec
	if got := strings.Join(fixedSpec.Containers[0].Args, " "); !strings.Contains(got, "--max-cells 6") || !strings.Contains(got, "--egress-slots 6") || len(fixedSpec.Containers[0].Command) != 1 {
		t.Fatalf("fixed capacity must pass explicit flags with egress slots defaulting to cells: %v %s", fixedSpec.Containers[0].Command, got)
	}
	mounts := map[string]corev1.VolumeMount{}
	for _, m := range spec.Containers[0].VolumeMounts {
		mounts[m.Name] = m
	}
	if _, ok := mounts["model-credentials"]; ok {
		t.Fatal("the dispatcher mounts model credentials; no Celln node holds a model key")
	}
	if _, ok := mounts["publisher-credentials"]; ok {
		t.Fatal("dispatcher must not receive the configuration publishing credential")
	}
	// The owner's template never mentions backends, so adding one rolls only
	// the configuration DaemonSet and running parents survive.
	if raw := mustJSON(t, node.Spec.Template); strings.Contains(raw, "FLEET_BACKENDS") || strings.Contains(raw, "celln.sympozium.ai/backends") || strings.Contains(raw, "publisher-credentials") {
		t.Fatal("owner template carries backend or publishing material")
	}
	if a := configure.Spec.Template.Annotations["celln.sympozium.ai/backends"]; len(a) != 64 {
		t.Fatalf("configuration DaemonSet must roll on backend changes: %q", a)
	}
	init := map[string]corev1.VolumeMount{}
	for _, m := range configureSpec.Containers[0].VolumeMounts {
		init[m.Name] = m
	}
	if _, ok := init["publisher-credentials"]; !ok || init["state"].MountPath != state {
		t.Fatal("configure step lacks publishing credential or node state")
	}
	if wait := spec.InitContainers[0]; len(wait.VolumeMounts) != 1 || wait.VolumeMounts[0].Name != "state" || !wait.VolumeMounts[0].ReadOnly || !strings.Contains(strings.Join(wait.Command, " "), "admitted-$FLEET_PACKAGE_HEX") {
		t.Fatalf("owner must only wait for the node's admission: %+v", wait)
	}
	env := map[string]string{}
	for _, e := range configureSpec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	if _, legacy := env["FLEET_MODEL_CREDENTIAL_FILE"]; legacy || env["FLEET_PACKAGE_HASH"] != "blake3:"+strings.Repeat("b", 64) || env["FLEET_PUBLISHER"] != "ed25519:operator" {
		t.Fatalf("configure step configuration drifted: %v", env)
	}
	for _, v := range spec.Volumes {
		if v.Name == "state" && (v.HostPath == nil || v.HostPath.Path != state || *v.HostPath.Type != corev1.HostPathDirectoryOrCreate) {
			t.Fatal("node state must be created on first use without host ceremony")
		}
	}
	if svc := r.services["celln-node"]; svc.Spec.ClusterIP != "None" {
		t.Fatal("owners must be discoverable individually through a headless Service")
	}
	router := strings.Join(r.deployments["celln-router"].Spec.Template.Spec.Containers[0].Args, " ")
	if !strings.Contains(router, "--backends-srv celln-node.celln-system.svc.cluster.local:8787") || strings.Contains(router, "--backends ") || !strings.Contains(router, "--parent-token-file") {
		t.Fatalf("gateway must discover the fleet and route parents: %s", router)
	}
	controller := r.deployments["sympozium-controller-manager"].Spec.Template.Spec
	if _, pinned := controller.NodeSelector["kubernetes.io/hostname"]; pinned {
		t.Fatal("fleet controller must not be pinned to an owner node")
	}
	for _, v := range controller.Volumes {
		if v.HostPath != nil {
			t.Fatal("fleet controller must not mount host state")
		}
	}
	// The fleet's parents are issued and journaled by the node that owns them;
	// the controller keeps no parent journal, claim or registrations of its own.
	for _, v := range controller.Volumes {
		if v.PersistentVolumeClaim != nil {
			t.Fatalf("fleet controller must not claim storage: %s", v.Name)
		}
	}
	if _, ok := r.claims["celln-parent-journal"]; ok {
		t.Fatal("the controller journal claim was removed with the parent plane")
	}
	for _, e := range controller.Containers[0].Env {
		if strings.HasPrefix(e.Name, "CELLN_PARENT_") {
			t.Fatalf("parent plane wiring remains: %s", e.Name)
		}
	}
}

func TestFleetRefusesUnsafeConfiguration(t *testing.T) {
	for _, override := range []string{
		"celln.fleet.scope=", "celln.fleet.scope=Starter/../x",
		"celln.fleet.package.image=registry.example/celln/starter:latest",
		"celln.fleet.package.hash=sha256:" + strings.Repeat("b", 64),
		"celln.fleet.publisher=", "celln.fleet.principal=", "celln.fleet.parentClientsConfigMap=",
		"celln.fleet.modelCredential.secret=legacy",
		"celln.fleet.capacity=fixed,celln.fleet.maxCells=1", "celln.fleet.capacity=sometimes",
		"celln.fleet.memoryPercent=99", "celln.fleet.cellMemoryBytes=1048576", "celln.router.parentTokenSecret=",
		"celln.fleet.model.endpoint=https://api.openai.com/v1/chat/completions,celln.fleet.model.provider=openai,celln.fleet.model.protocol=openai-chat",
		"celln.fleet.model.endpoint=http://10.0.0.5:8080/v1/chat/completions,celln.fleet.model.provider=llama-server,celln.fleet.model.protocol=openai-chat,celln.fleet.model.name=q",
		"celln.fleet.limits.leaseSeconds=90000", "celln.fleet.limits.leaseSeconds=30",
		"celln.fleet.backends[0].name=Native",
		"celln.fleet.backends[0].name=native,celln.fleet.backends[0].credentialSecret=s",
		"celln.fleet.backends[0].name=native,celln.fleet.backends[1].name=native",
		"celln.fleet.backends[0].name=local,celln.fleet.backends[0].provider=llama-server,celln.fleet.backends[0].protocol=openai-chat,celln.fleet.backends[0].model=q,celln.fleet.backends[0].endpoint=http://10.0.0.5:8080/v1/chat/completions",
		"celln.fleet.backends[0].name=openai,celln.fleet.backends[0].provider=openai,celln.fleet.backends[0].protocol=openai-chat,celln.fleet.backends[0].endpoint=https://api.openai.com/v1/chat/completions",
		"celln.router.external=true",
	} {
		t.Run(override, func(t *testing.T) {
			if raw, err := renderNativeParent(t, append(fleetValues(), strings.Split(override, ",")...)); err == nil {
				t.Fatalf("unsafe fleet rendered: %s", raw)
			}
		})
	}
}

func mustRender(t *testing.T, values []string) []byte {
	t.Helper()
	raw, err := renderNativeParent(t, values)
	if err != nil {
		t.Fatalf("render: %v: %s", err, raw)
	}
	return raw
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
