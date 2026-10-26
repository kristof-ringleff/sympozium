package v1alpha1

import (
	"encoding/json"
	"fmt"
	"k8s.io/apimachinery/pkg/util/validation"
	"net"
	"net/url"
	"regexp"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ModelConnection describes a model route, not a grant to execute it.
// The independent host policy must admit the same endpoint and credential profile.
// +kubebuilder:object:root=true
type ModelConnection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ModelConnectionSpec `json:"spec"`
}

type ModelConnectionSpec struct {
	// Provider is a descriptive provider identifier, including custom providers.
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9_-]{1,64}$`
	Provider string `json:"provider"`
	// Protocol selects the host adapter. Compatible gateways may expose other providers.
	// +kubebuilder:validation:Enum=openai-chat;anthropic-messages
	Protocol string `json:"protocol"`
	// Endpoint is an HTTP(S) API base URL or full request URL. Native Celln requires a full HTTPS request URL.
	// +kubebuilder:validation:MaxLength=2048
	Endpoint string `json:"endpoint"`
	// SecretRef selects a Kubernetes credential Secret for container harnesses.
	// +optional
	SecretRef string `json:"secretRef,omitempty"`
	// SecretKey is the key of SecretRef that holds the credential. Omitted,
	// the protocol fixes it: OPENAI_API_KEY for openai-chat and
	// ANTHROPIC_API_KEY for anthropic-messages (DefaultSecretKey).
	// +kubebuilder:validation:Pattern=`^[A-Za-z_][A-Za-z0-9_.-]{0,252}$`
	// +optional
	SecretKey string `json:"secretKey,omitempty"`
	// Models lists the exact model identifiers selectable through this connection.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=128
	// +listType=set
	Models []string `json:"models"`
	// Disabled prevents new connection resolution. Host profile withdrawal stops live use.
	// +optional
	Disabled bool `json:"disabled,omitempty"`
	// AllowInsecure permits an HTTP or self-signed HTTPS endpoint on a private
	// address. This is an explicit operator opt-in that weakens the default
	// public-HTTPS transport contract. It never bypasses host admission.
	// +optional
	AllowInsecure bool `json:"allowInsecure,omitempty"`
	// Parameters is a bounded JSON object the model gateway merges into every
	// provider request made through this connection, for example
	// {"temperature":0.7} or {"chat_template_kwargs":{"enable_thinking":false}}.
	// It is operator policy pinned on the host side: the guest never sees it, a
	// guest request that carries one of its keys is refused, and request fields
	// the gateway owns (ReservedModelParameters) cannot be set. At most 16
	// top-level keys matching ^[a-z][a-z0-9_]{0,63}$, 3 levels deep and 2048
	// bytes (ValidateModelParameters). Only gateway-mediated connections
	// (secretRef or no credential) may set it.
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	// +optional
	Parameters *apiextensionsv1.JSON `json:"parameters,omitempty"`
	// MaxOutputTokens bounds the output tokens one model request through this
	// connection may ask for. Omitted means 512. The model gateway refuses a
	// request above it; a turn's own output-token cap still applies. Only
	// gateway-mediated connections (secretRef or no credential) may set it.
	// +kubebuilder:validation:Minimum=256
	// +kubebuilder:validation:Maximum=4096
	// +optional
	MaxOutputTokens int64 `json:"maxOutputTokens,omitempty"`
}

var modelConnectionIdentifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

var secretKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,252}$`)

// DefaultSecretKey is the Secret key a protocol's credential is read from
// when the connection names none.
func DefaultSecretKey(protocol string) string {
	if protocol == "anthropic-messages" {
		return "ANTHROPIC_API_KEY"
	}
	return "OPENAI_API_KEY"
}

// CredentialSecretKey is the Secret key this connection's credential is read
// from: the one it names, else the protocol's default.
func (s ModelConnectionSpec) CredentialSecretKey() string {
	if s.SecretKey != "" {
		return s.SecretKey
	}
	return DefaultSecretKey(s.Protocol)
}

// Origin is the connection's endpoint origin (scheme://host[:port]), the
// destination its credential is pinned to. It is the origin whatever the
// scheme; whether a key may cross plain HTTP is the resolver's and the
// gateway's rule, not the pin's.
func (s ModelConnectionSpec) Origin() (string, error) {
	return ModelEndpointOriginInsecure(s.Endpoint, true)
}

func (s ModelConnectionSpec) Validate() error {
	if !modelConnectionIdentifier.MatchString(s.Provider) {
		return fmt.Errorf("provider must be an identifier of 1–64 letters, digits, underscores or hyphens")
	}
	if s.SecretRef != "" && len(validation.IsDNS1123Subdomain(s.SecretRef)) != 0 {
		return fmt.Errorf("secretRef must name a Kubernetes Secret in this namespace")
	}
	if s.SecretKey != "" && (s.SecretRef == "" || !secretKeyPattern.MatchString(s.SecretKey)) {
		return fmt.Errorf("secretKey names the key of secretRef and must be a valid Secret key")
	}
	if s.Protocol != "openai-chat" && s.Protocol != "anthropic-messages" {
		return fmt.Errorf("protocol must be openai-chat or anthropic-messages")
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || len(s.Endpoint) > 2048 || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(s.Endpoint, "\r\n\x00") {
		return fmt.Errorf("endpoint must be an HTTP(S) API URL without credentials, query or fragment")
	}
	if _, err := s.RequestParameters(); err != nil {
		return err
	}
	if err := ValidateRequestOutputTokens(s.MaxOutputTokens); err != nil {
		return err
	}
	if len(s.Models) == 0 || len(s.Models) > 128 {
		return fmt.Errorf("connection requires 1–128 model identifiers")
	}
	seen := map[string]bool{}
	for _, model := range s.Models {
		if !ValidModelIdentifier(model) || seen[model] {
			return fmt.Errorf("model identifiers must be unique, nonempty and at most 128 bytes")
		}
		seen[model] = true
	}
	return nil
}

// ValidModelIdentifier is the shape of one model name: nonempty, at most 128
// bytes, no surrounding whitespace and no NUL, CR or LF.
func ValidModelIdentifier(model string) bool {
	return len(model) != 0 && len(model) <= 128 && strings.TrimSpace(model) == model && !strings.ContainsAny(model, "\x00\r\n")
}

// RequestParameters is the validated parameters object, numbers in their
// written form; nil when the connection sets none.
func (s ModelConnectionSpec) RequestParameters() (map[string]any, error) {
	if s.Parameters == nil {
		return nil, nil
	}
	if len(s.Parameters.Raw) == 0 {
		return nil, fmt.Errorf("model parameters: a JSON object is required")
	}
	return ParseModelParameters(s.Parameters.Raw)
}

// RequestOutputTokens is the output-token bound of one request through this
// connection: maxOutputTokens, or the default when omitted.
func (s ModelConnectionSpec) RequestOutputTokens() int64 {
	return EffectiveRequestOutputTokens(s.MaxOutputTokens)
}

// DigestView is the value every authority digests as this connection's spec
// (route.modelConnectionSpecSha256). Decision digests use an integer-only
// canonical JSON profile, which cannot carry a fractional parameter such as
// {"temperature":0.7}; parameters are therefore bound as one string, their
// key-sorted compact JSON text with numbers as written. A spec without
// parameters is returned unchanged, so its digest is what it always was.
func (s ModelConnectionSpec) DigestView() (any, error) {
	if s.Parameters == nil {
		return s, nil
	}
	parameters, err := s.RequestParameters()
	if err != nil {
		return nil, err
	}
	text := []byte("{}")
	if len(parameters) != 0 {
		if text, err = json.Marshal(parameters); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields["parameters"], err = json.Marshal(string(text)); err != nil {
		return nil, err
	}
	return fields, nil
}

// ModelEndpointOrigin matches the native host HTTPS transport: no userinfo,
// query credentials, fragments, alternate ports or redirect-based endpoints.
func ModelEndpointOrigin(endpoint string) (string, error) {
	return ModelEndpointOriginInsecure(endpoint, false)
}

// ModelEndpointOriginInsecure returns the egress origin for a model endpoint.
// The default keeps the HTTPS transport contract (an explicit port is fine:
// the origin, not the port, is what a credential is pinned to); allowInsecure
// is an explicit opt-in that also permits plain HTTP for a private endpoint.
func ModelEndpointOriginInsecure(endpoint string, allowInsecure bool) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || len(endpoint) > 2048 || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path == "" || strings.ContainsAny(endpoint, "\r\n\x00") {
		return "", fmt.Errorf("model endpoint must be an HTTP(S) URL without credentials, query or fragment")
	}
	if allowInsecure {
		if u.Scheme != "http" && u.Scheme != "https" {
			return "", fmt.Errorf("model endpoint must be an HTTP(S) URL")
		}
	} else if u.Scheme != "https" {
		return "", fmt.Errorf("model endpoint must be a complete HTTPS URL without credentials, query or fragment")
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), nil
}

// OriginHost is the host[:port] of an origin or endpoint, lowercased, for
// matching against an operator's host patterns.
func OriginHost(origin string) string {
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

var originPatternRegexp = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$`)

// ValidOriginPattern reports whether pattern is an exact lowercase host, with
// an optional port, or a leading "*." wildcard over one or more labels.
func ValidOriginPattern(pattern string) bool {
	return len(pattern) <= 255 && originPatternRegexp.MatchString(pattern)
}

// OriginMatches reports whether an origin's host matches pattern: the exact
// host (with its port when the pattern names one), or any subdomain of a
// "*." pattern. "*.example.com" matches "api.example.com" and "example.com";
// it never matches "notexample.com".
func OriginMatches(origin, pattern string) bool {
	host := OriginHost(origin)
	if host == "" || !ValidOriginPattern(pattern) {
		return false
	}
	hostname, port := host, ""
	if h, p, err := net.SplitHostPort(host); err == nil {
		hostname, port = h, p
	}
	patternHost, patternPort := pattern, ""
	if h, p, err := net.SplitHostPort(pattern); err == nil {
		patternHost, patternPort = h, p
	}
	if patternPort != "" && patternPort != port {
		return false
	}
	if suffix, ok := strings.CutPrefix(patternHost, "*."); ok {
		return hostname == suffix || strings.HasSuffix(hostname, "."+suffix)
	}
	return hostname == patternHost
}

// +kubebuilder:object:root=true
type ModelConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelConnection `json:"items"`
}

func init() { SchemeBuilder.Register(&ModelConnection{}, &ModelConnectionList{}) }
