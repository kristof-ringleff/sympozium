package charts

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// nastyFleetParameters holds everything a values path could mangle: commas,
// quotes, braces, brackets, equals signs, backslashes, dots and nesting.
const nastyFleetParameters = `{"chat_template_kwargs":{"enable_thinking":false,"note":"a,b=c {x} [y] \"q\" \\ back.slash\nline é"},"stop":["</s>",",","a=b"],"temperature":0.7,"top_k":40,"deep":{"er":{"flag":true}}}`

// plan runs the node's plan builder (files/celln/fleet-plan.py) for one
// backend of a FLEET_BACKENDS list and returns its output.
func plan(t *testing.T, backends, backend string, extraEnv ...string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required for the plan builder")
	}
	cmd := exec.Command("python3", "sympozium/files/celln/fleet-plan.py", "/state/package-abc", "blake3:abc", "sympozium:celln", backend, "/state/.configure-abc")
	cmd.Env = append(append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1", "FLEET_SCOPE=starter", "FLEET_BACKENDS="+backends), extraEnv...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return stderr.String(), err
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

// A backend without parameters gets byte for byte the plan it always got (an
// older Celln refuses a plan naming parameters); one with parameters carries
// them under modelConnection.
func TestFleetPlanCarriesParametersOnlyWhenConfigured(t *testing.T) {
	backends := `[{"name":"native","provider":"deepseek","protocol":"openai-chat","endpoint":"https://api.deepseek.com/chat/completions","model":"deepseek-chat","allowInsecure":false,"credentialFile":"/etc/celln-native/credentials/native"},` +
		`{"name":"empty","provider":"deepseek","protocol":"openai-chat","endpoint":"https://api.deepseek.com/chat/completions","model":"deepseek-chat","allowInsecure":false,"credentialFile":"/etc/celln-native/credentials/empty","parameters":{}},` +
		`{"name":"local","provider":"llama-server","protocol":"openai-chat","endpoint":"http://10.0.0.1:8080/v1/chat/completions","model":"qwen.gguf","allowInsecure":true,"credentialFile":"/etc/celln-native/credentials/local","parameters":` + nastyFleetParameters + `},` +
		`{"name":"reviewed","credentialFile":"/etc/celln-native/credentials/reviewed"},` +
		`{"name":"stray","credentialFile":"/etc/celln-native/credentials/stray","parameters":{"temperature":1}},` +
		`{"name":"list","provider":"x","protocol":"openai-chat","endpoint":"https://x.example/v1/chat/completions","model":"m","credentialFile":"/c","parameters":[1]}]`
	got, err := plan(t, backends, "native", "FLEET_LIMIT_MAX_TURNS=256", `FLEET_HTTPS_HOSTS=["example.org"]`)
	// The exact bytes the inline builder printed before parameters existed.
	const before = `{"apiVersion": "celln.native-starter-config/v1", "package": "/state/package-abc", "packageHash": "blake3:abc", "principal": "sympozium:celln", "credentialFile": "/etc/celln-native/credentials/native", "output": "/state/.configure-abc", "modelConnection": {"provider": "deepseek", "protocol": "openai-chat", "endpoint": "https://api.deepseek.com/chat/completions", "model": "deepseek-chat", "credentialProfile": "starter", "allowInsecure": false}, "hostLimits": {"maxTurns": 256}, "httpsHosts": ["example.org"]}`
	if err != nil || got != before {
		t.Fatalf("plan without parameters changed:\n%s\n%s\n%v", got, before, err)
	}
	if got, err := plan(t, backends, "empty"); err != nil || strings.Contains(got, "parameters") {
		t.Fatalf("empty parameters reached the plan: %s %v", got, err)
	}
	// No endpoint is a model-free node: no connection, no credential file.
	// Celln reads such a plan as "runs bring their own route"; the old
	// DeepSeek default no longer exists.
	if got, err := plan(t, backends, "reviewed"); err != nil || strings.Contains(got, "modelConnection") || strings.Contains(got, "credentialFile") {
		t.Fatalf("model-free plan: %s %v", got, err)
	}
	got, err = plan(t, backends, "local")
	if err != nil {
		t.Fatalf("plan: %v: %s", err, got)
	}
	var built struct {
		ModelConnection map[string]any `json:"modelConnection"`
	}
	var want map[string]any
	if err := json.Unmarshal([]byte(got), &built); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal([]byte(nastyFleetParameters), &want)
	if !reflect.DeepEqual(built.ModelConnection["parameters"], want) || built.ModelConnection["credentialProfile"] != "starter-local" || built.ModelConnection["allowInsecure"] != true {
		t.Fatalf("plan model connection: %v", built.ModelConnection)
	}
	for _, refused := range []string{"stray", "list"} {
		if out, err := plan(t, backends, refused); err == nil || !strings.Contains(out, "parameters") {
			t.Fatalf("%s: plan built: %s", refused, out)
		}
	}
}

// The configure pods carry the plan builder next to prepare.sh, and
// prepare.sh uses it.
func TestFleetShipsThePlanBuilder(t *testing.T) {
	raw, err := renderNativeParent(t, fleetValues())
	if err != nil {
		t.Fatalf("render: %v: %s", err, raw)
	}
	if !strings.Contains(string(raw), "fleet-plan.py: |") || !strings.Contains(string(raw), "def build(package, package_hash, principal, name, output, environ)") {
		t.Fatal("celln-fleet-prepare does not carry fleet-plan.py")
	}
	mounted := false
	for _, m := range decodeFleet(t, raw).daemonSets["celln-node-configure"].Spec.Template.Spec.Containers[0].VolumeMounts {
		mounted = mounted || (m.MountPath == "/etc/celln-fleet/fleet-plan.py" && m.SubPath == "fleet-plan.py" && m.ReadOnly)
	}
	if !mounted {
		t.Fatal("fleet-plan.py is not mounted into the configure pod")
	}
	script, err := os.ReadFile("sympozium/files/celln/fleet-prepare.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), `python3 /etc/celln-fleet/fleet-plan.py "$package"`) || !strings.Contains(string(script), "newer than v0.5.22") {
		t.Fatal("prepare.sh does not build the plan with fleet-plan.py or explain a refused parameters plan")
	}
}
