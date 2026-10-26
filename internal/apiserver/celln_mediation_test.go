package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func mediationFixture() (profile *sympoziumv1alpha1.CellnRuntimeProfile, policy *sympoziumv1alpha1.CellnExecutionPolicy, namespaces []client.Object) {
	profile = &sympoziumv1alpha1.CellnRuntimeProfile{ObjectMeta: metav1.ObjectMeta{Name: "celln-native-trial"}, Spec: sympoziumv1alpha1.CellnRuntimeProfileSpec{Revision: "v1", Native: &sympoziumv1alpha1.CellnNativeProvisioning{Template: apiextensionsv1.JSON{Raw: []byte(`{"model":"deepseek-chat","url":"https://api.deepseek.com/chat/completions"}`)}}}}
	policy = &sympoziumv1alpha1.CellnExecutionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "celln-fleet-trial"}, Spec: sympoziumv1alpha1.CellnExecutionPolicySpec{
		NamespaceSelector: cellnplatform.OpenSelector(cellnplatform.SystemNamespaces("sympozium-system")),
		RuntimeProfiles:   []sympoziumv1alpha1.CellnExecutionPolicyRuntime{{Ref: sympoziumv1alpha1.CellnRuntimeProfileRef{Name: profile.Name, Revision: "v1"}}},
	}}
	for _, name := range []string{"team-a", "sympozium-system"} {
		namespaces = append(namespaces, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{cellnplatform.NamespaceNameLabel: name}}})
	}
	return profile, policy, namespaces
}

// The mediation answer names no provider: the Agent's connection is the
// route. It reports whether the release runs the gateway and the origin
// guardrail the namespace's policies place on an Agent's own connection.
func TestCellnMediationReportsTheOriginGuardrail(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = sympoziumv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	record := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "celln-system", Name: cellninstall.MediationRecordConfigMap}, Data: map[string]string{"mediation.json": `{"enabled":true}`}}
	for _, tc := range []struct {
		name      string
		namespace string
		record    *corev1.ConfigMap
		allowed   []string
		denied    []string
		status    int
		want      CellnMediation
	}{
		{name: "gateway off", namespace: "team-a", status: http.StatusOK, want: CellnMediation{AllowedOrigins: []string{}, DeniedOrigins: []string{}, Policies: []string{"celln-fleet-trial"}}},
		{name: "any origin", namespace: "team-a", record: record, status: http.StatusOK, want: CellnMediation{Enabled: true, AllowedOrigins: []string{}, DeniedOrigins: []string{}, Policies: []string{"celln-fleet-trial"}}},
		{name: "narrowed", namespace: "team-a", record: record, allowed: []string{"*.openai.com", "llm.corp.internal:8443"}, denied: []string{"evil.example"}, status: http.StatusOK,
			want: CellnMediation{Enabled: true, AllowedOrigins: []string{"*.openai.com", "llm.corp.internal:8443"}, DeniedOrigins: []string{"evil.example"}, Policies: []string{"celln-fleet-trial"}}},
		{name: "a namespace no policy admits", namespace: "sympozium-system", record: record, allowed: []string{"*.openai.com"}, status: http.StatusOK,
			want: CellnMediation{Enabled: true, AllowedOrigins: []string{}, DeniedOrigins: []string{}, Policies: []string{}}},
		{name: "unknown namespace", namespace: "nowhere", status: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile, policy, objects := mediationFixture()
			policy.Spec.AllowedOrigins, policy.Spec.DeniedOrigins = tc.allowed, tc.denied
			objects = append(objects, profile, policy)
			if tc.record != nil {
				objects = append(objects, tc.record)
			}
			srv := NewServer(fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(), nil, nil, logr.Discard())
			res := httptest.NewRecorder()
			srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/celln-platform/mediation?namespace="+tc.namespace, nil))
			if res.Code != tc.status {
				t.Fatalf("status %d: %s", res.Code, res.Body.String())
			}
			if tc.status != http.StatusOK {
				return
			}
			var got CellnMediation
			tc.want.Routes, tc.want.Pending, tc.want.PrivateOrigins = []struct{}{}, []struct{}{}, []string{}
			if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("mediation = %s (%v), want %+v", res.Body.String(), err, tc.want)
			}
			for _, field := range []string{`"allowedOrigins":[`, `"deniedOrigins":[`} {
				if !strings.Contains(res.Body.String(), field) {
					t.Fatalf("clients iterate %s; got %s", field, res.Body.String())
				}
			}
		})
	}
	// The gateway's approved private origins reach the console from the same
	// value the gateway reads, as a JSON array or a comma-separated list.
	for raw, want := range map[string][]string{
		"": {},
		`["http://10.0.0.5:8080", "http://llm.corp.internal"]`:      {"http://10.0.0.5:8080", "http://llm.corp.internal"},
		" http://10.0.0.5:8080 ,, http://ollama.default.svc:11434 ": {"http://10.0.0.5:8080", "http://ollama.default.svc:11434"},
		`[not json`: {},
	} {
		t.Setenv(privateOriginsEnv, raw)
		if got := approvedPrivateOrigins(); !reflect.DeepEqual(got, want) {
			t.Fatalf("%q: approved private origins %v, want %v", raw, got, want)
		}
	}
	t.Setenv(privateOriginsEnv, `["http://10.0.0.5:8080"]`)
	{
		profile, policy, objects := mediationFixture()
		objects = append(objects, profile, policy, record)
		srv := NewServer(fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(), nil, nil, logr.Discard())
		res := httptest.NewRecorder()
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/celln-platform/mediation?namespace=team-a", nil))
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"privateOrigins":["http://10.0.0.5:8080"]`) {
			t.Fatalf("private origins not reported: %d %s", res.Code, res.Body.String())
		}
	}
	// Read only: no verb of this path changes anything.
	profile, policy, objects := mediationFixture()
	srv := NewServer(fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objects, profile, policy)...).Build(), nil, nil, logr.Discard())
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		res := httptest.NewRecorder()
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(method, "/api/v1/celln-platform/mediation", strings.NewReader(`{}`)))
		if res.Code == http.StatusOK || res.Code == http.StatusCreated {
			t.Fatalf("%s accepted: %d", method, res.Code)
		}
	}
}

// An Agent with its own Secret-backed connection needs the AgentRuntime only:
// the backend's shared Agent and host-profile connection stay out of its
// namespace, and the authorisation is the full set's.
func TestCellnPlatformRuntimeOnlyWrapper(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = sympoziumv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	profile, policy, objects := mediationFixture()
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objects, profile, policy)...).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	post := func(namespace, body string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		srv.Handler(nil).ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/celln-platform/wrappers?namespace="+namespace, strings.NewReader(body)))
		return res
	}
	for _, tc := range []struct {
		name, namespace, body string
		status                int
		created               []string
	}{
		{name: "first use creates the runtime", namespace: "team-a", body: `{"profile":"celln-native-trial","runtimeOnly":true}`, status: http.StatusOK, created: []string{"celln-native"}},
		{name: "second use keeps it", namespace: "team-a", body: `{"profile":"celln-native-trial","runtimeOnly":true}`, status: http.StatusOK, created: []string{}},
		{name: "excluded namespace", namespace: "sympozium-system", body: `{"profile":"celln-native-trial","runtimeOnly":true}`, status: http.StatusForbidden},
		{name: "unknown profile", namespace: "team-a", body: `{"profile":"someone-elses","runtimeOnly":true}`, status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := post(tc.namespace, tc.body)
			if res.Code != tc.status {
				t.Fatalf("status %d: %s", res.Code, res.Body.String())
			}
			if tc.status != http.StatusOK {
				return
			}
			var got cellnplatform.Wrappers
			if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil || got.Runtime != "celln-native" || got.Backend != "native" || got.Agent != "" || got.Connection != "" || !reflect.DeepEqual(got.Created, tc.created) {
				t.Fatalf("wrappers = %s", res.Body.String())
			}
		})
	}
	var runtimeWrapper sympoziumv1alpha1.AgentRuntime
	if err := cl.Get(t.Context(), types.NamespacedName{Namespace: "team-a", Name: "celln-native"}, &runtimeWrapper); err != nil || runtimeWrapper.Spec.CellnProfileRef == nil || runtimeWrapper.Spec.CellnProfileRef.Name != "celln-native-trial" {
		t.Fatalf("runtime wrapper: %+v %v", runtimeWrapper.Spec, err)
	}
	var agents sympoziumv1alpha1.AgentList
	var connections sympoziumv1alpha1.ModelConnectionList
	if err := cl.List(t.Context(), &agents); err != nil || len(agents.Items) != 0 {
		t.Fatalf("runtimeOnly created an Agent: %+v", agents.Items)
	}
	if err := cl.List(t.Context(), &connections); err != nil || len(connections.Items) != 0 {
		t.Fatalf("runtimeOnly created a ModelConnection: %+v", connections.Items)
	}
}
