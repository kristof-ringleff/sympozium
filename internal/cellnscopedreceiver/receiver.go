// Package cellnscopedreceiver is the TLS edge in front of a fleet node's
// scoped receiver. The router speaks plaintext HTTP on loopback and the
// controller only talks to an HTTPS receiver with a pinned CA; this listener
// terminates TLS and forwards nothing but the scoped protocol. The
// dispatcher's own operator bearer and signed permits remain the authority.
package cellnscopedreceiver

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// maxBody follows the receiver's bound (256 KiB).
const maxBody = 262144

// New exposes only the scoped receiver protocol (/v1/scoped/*) and read-only
// capability discovery of a loopback dispatcher.
func New(backend string) (http.Handler, func(), error) {
	u, err := url.Parse(backend)
	if err != nil || u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() || u.Port() == "" || u.User != nil || u.Opaque != "" || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, nil, fmt.Errorf("fixed literal loopback HTTP dispatcher origin required")
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: 90 * time.Second, IdleConnTimeout: 30 * time.Second, MaxConnsPerHost: 32, DisableCompression: true}
	proxy := &httputil.ReverseProxy{Transport: transport, Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(u)
		p.Out.Host = u.Host
		// Never turn a POST into a transport-retryable request based on caller headers.
		p.Out.Header.Del("Idempotency-Key")
		p.Out.Header.Del("X-Idempotency-Key")
		p.Out.Header.Del("Cookie")
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "scoped receiver unavailable; reconcile original identity", http.StatusBadGateway)
	}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.Header.Get("Upgrade") != "" || !scopedRoute(r.Method, r.URL.Path) {
			http.Error(w, "configured protocol route required", http.StatusNotFound)
			return
		}
		if r.ContentLength > maxBody {
			http.Error(w, "scoped request too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid or oversized scoped request", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.TransferEncoding = nil
		proxy.ServeHTTP(w, r)
	})
	return handler, transport.CloseIdleConnections, nil
}

func scopedRoute(method, path string) bool {
	// Read-only discovery: the controller preflights the contracts a run's
	// tools need, and the receiver (the router) answers for the whole fleet.
	if method == http.MethodGet && path == "/v1/capabilities" {
		return true
	}
	if method != http.MethodPost {
		return false
	}
	switch path {
	case "/v1/scoped/prepare", "/v1/scoped/start", "/v1/scoped/read", "/v1/scoped/cleanup":
		return true
	}
	return false
}
