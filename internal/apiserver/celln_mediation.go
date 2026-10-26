package apiserver

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/sympozium-ai/sympozium/internal/cellninstall"
	"github.com/sympozium-ai/sympozium/internal/cellnplatform"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

// How an Agent in a namespace may use its own model key. Read only, and
// nothing here is a credential. The Agent's connection is the route; what
// this reports is the operator's guardrail over it.

// CellnMediation is the answer of GET /api/v1/celln-platform/mediation.
type CellnMediation struct {
	// Enabled reports that the release runs the model gateway, which is how
	// every Celln Agent's model requests leave a cell.
	Enabled bool `json:"enabled"`
	// AllowedOrigins and DeniedOrigins are the host patterns the namespace's
	// policies place on an Agent's own connection: empty AllowedOrigins
	// means any origin the gateway reaches. Every policy that selects the
	// namespace applies, so these are their union (allowed: intersection
	// is what the resolver enforces; a client treats the lists as advice).
	AllowedOrigins []string `json:"allowedOrigins"`
	DeniedOrigins  []string `json:"deniedOrigins"`
	// Policies names the policies that apply.
	Policies []string `json:"policies"`
	// PrivateOrigins are the exact origins (scheme://host[:port]) on
	// private, loopback or link-local addresses the operator approved for the
	// gateway (modelGateway.privateOrigins). A connection to any other
	// private endpoint is refused at the gateway, so a console shows it as
	// awaiting approval rather than letting a run fail on its first call.
	PrivateOrigins []string `json:"privateOrigins"`
	// Routes and Pending are always empty: 0.11 consoles iterated them.
	Routes  []struct{} `json:"routes"`
	Pending []struct{} `json:"pending"`
}

func (s *Server) getCellnMediation(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}
	record, err := cellninstall.ReadMediationRecord(r.Context(), s.client)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	authorised, err := cellnplatform.AuthorisedProfiles(r.Context(), s.client, ns)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := CellnMediation{Enabled: record.Enabled, AllowedOrigins: []string{}, DeniedOrigins: []string{}, Policies: []string{}, PrivateOrigins: approvedPrivateOrigins(), Routes: []struct{}{}, Pending: []struct{}{}}
	seen := map[string]bool{}
	for _, a := range authorised {
		if seen[a.Policy.Name] {
			continue
		}
		seen[a.Policy.Name] = true
		out.Policies = append(out.Policies, a.Policy.Name)
		out.AllowedOrigins = append(out.AllowedOrigins, a.Policy.Spec.AllowedOrigins...)
		out.DeniedOrigins = append(out.DeniedOrigins, a.Policy.Spec.DeniedOrigins...)
	}
	writeJSON(w, out)
}

// privateOriginsEnv carries the gateway's approved private origins to the API
// server: a JSON array, or a comma-separated list, of exact origins. The chart
// renders it from the same value the gateway reads (modelGateway.privateOrigins).
const privateOriginsEnv = "SYMPOZIUM_PRIVATE_ORIGINS"

func approvedPrivateOrigins() []string {
	raw := strings.TrimSpace(os.Getenv(privateOriginsEnv))
	origins := []string{}
	if raw == "" {
		return origins
	}
	if strings.HasPrefix(raw, "[") {
		if err := json.Unmarshal([]byte(raw), &origins); err != nil {
			return []string{}
		}
	} else {
		origins = strings.Split(raw, ",")
	}
	out := make([]string, 0, len(origins))
	for _, o := range origins {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}
