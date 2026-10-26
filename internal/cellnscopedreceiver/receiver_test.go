package cellnscopedreceiver

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestEdgeRefusesRemoteOrMutableBackend(t *testing.T) {
	for _, backend := range []string{"https://127.0.0.1:8788", "http://celln-node:8788", "http://127.0.0.1", "http://127.0.0.1:8788/v1", "http://127.0.0.1:8788?x=1", "http://user@127.0.0.1:8788"} {
		if _, _, err := New(backend); err == nil {
			t.Fatalf("accepted %q", backend)
		}
	}
}

func TestEdgeForwardsOnlyTheScopedProtocol(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		{"POST", "/v1/scoped/prepare", true}, {"POST", "/v1/scoped/start", true},
		{"POST", "/v1/scoped/read", true}, {"POST", "/v1/scoped/cleanup", true},
		{"GET", "/v1/scoped/read", false}, {"POST", "/v1/scoped/", false},
		{"POST", "/v1/scoped/start/extra", false}, {"POST", "/v1/scoped/../parents", false},
		{"POST", "/v1/parents", false}, {"POST", "/v1/artifacts/prewarm", false},
		{"POST", "/v1/drain", false}, {"GET", "/v1/health", false},
		{"GET", "/v1/capabilities", true}, {"POST", "/v1/capabilities", false},
	} {
		if scopedRoute(tc.method, tc.path) != tc.allowed {
			t.Fatalf("unexpected scoped route %s %s", tc.method, tc.path)
		}
	}
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer operator-only" || r.Header.Get("X-Celln-Execution-Permit") != "execution-only" || r.Header.Get("X-Celln-Model-Permit") != "model-only" {
			t.Error("operator bearer or permits did not reach the receiver unchanged")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	handler, closeTransport, err := New(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTransport()
	edge := httptest.NewUnstartedServer(handler)
	edge.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	edge.StartTLS()
	defer edge.Close()
	// The receiver accepts 256 KiB; a prepared operation well above 64 KiB
	// must pass this edge.
	req, _ := http.NewRequest("POST", edge.URL+"/v1/scoped/start", strings.NewReader(strings.Repeat("x", 200000)))
	req.Header.Set("Authorization", "Bearer operator-only")
	req.Header.Set("X-Celln-Execution-Permit", "execution-only")
	req.Header.Set("X-Celln-Model-Permit", "model-only")
	response, err := edge.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || calls.Load() != 1 {
		t.Fatalf("scoped request was not forwarded: %d", response.StatusCode)
	}
	for _, tc := range []struct {
		path string
		body int
		want int
	}{{"/v1/parents", 2, 404}, {"/v1/drain", 2, 404}, {"/v1/scoped/start?owner=other", 2, 404}, {"/v1/scoped/prepare", 262145, 413}} {
		req, _ := http.NewRequest("POST", edge.URL+tc.path, strings.NewReader(strings.Repeat("x", tc.body)))
		response, err := edge.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != tc.want {
			t.Fatalf("unexpected status for %s: %d", tc.path, response.StatusCode)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("forbidden or oversized request reached the receiver")
	}
}
