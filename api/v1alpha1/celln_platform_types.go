package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CellnRuntimeProfileRef identifies one immutable operator-published runtime
// revision. Name alone is never sufficient authority.
type CellnRuntimeProfileRef struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$"
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Revision string `json:"revision"`
}

// ClusterCellnToolRef is an unambiguous reference to a cluster-scoped tool
// catalogue revision. It is intentionally distinct from CellnCatalogueToolRef,
// which continues to mean the legacy namespaced CellnTool API.
type ClusterCellnToolRef struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$"
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Revision string `json:"revision"`
}

// CellnRuntimeProfileSpec is executable platform catalogue authority. A
// revision is immutable; changing any executable identity or constraint requires
// publishing a new object/revision.
// +kubebuilder:validation:XValidation:rule="self.lifecycles.all(x, self.lifecycles.filter(y, y == x).size() == 1)",message="runtime lifecycles must be unique"
// +kubebuilder:validation:XValidation:rule="self.contractVersion == 'celln.json-tools/v1' ? has(self.json) : !has(self.json)",message="JSON loop ceilings must match the selected adapter contract"
type CellnRuntimeProfileSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Revision string `json:"revision"`
	// +kubebuilder:validation:Enum=celln.reference-functions/v1;celln.json-tools/v1
	ContractVersion string            `json:"contractVersion"`
	Executable      CellnImmutableRef `json:"executable"`
	Closure         CellnImmutableRef `json:"closure"`
	Mote            CellnImmutableRef `json:"mote"`
	// +kubebuilder:validation:Pattern="^[0-9a-f]{64}$"
	// +kubebuilder:validation:MaxLength=64
	PublisherKey string `json:"publisherKey"`
	// +kubebuilder:validation:Pattern="^/([A-Za-z0-9_-][A-Za-z0-9_.+-]*/)*[A-Za-z0-9_-][A-Za-z0-9_.+-]*$"
	// +kubebuilder:validation:MaxLength=256
	EntryPoint string `json:"entryPoint"`
	// +kubebuilder:validation:Enum=linux/amd64
	Platform string `json:"platform"`
	// +kubebuilder:validation:Enum=agent
	Lane string `json:"lane"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=2
	// +listType=atomic
	// +kubebuilder:validation:items:Enum=disposable-one-shot;enduring
	Lifecycles []string                `json:"lifecycles"`
	Limits     AgentRuntimeCellnLimits `json:"limits"`
	// +optional
	JSON *CellnHarnessJSONLimits `json:"json,omitempty"`

	// Native is the reviewed parent/worker provisioning material an owner
	// issues an enduring parent from. The fleet installer publishes it from
	// the starter configuration; every platform decision binds it through the
	// profile spec digest. It carries no credential.
	// +optional
	Native *CellnNativeProvisioning `json:"native,omitempty"`
}

// CellnNativeProvisioning mirrors the owner-side provision plan fields whose
// values are fixed by the admitted package rather than by the run.
type CellnNativeProvisioning struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=300000
	AdmissionWindowMs int64 `json:"admissionWindowMs"`
	// Parent and Worker are the complete owner-side ExecutionRequest objects;
	// Template is the native JSON-harness configuration bound into the model
	// profile. They are opaque here and verified by the owner at issuance.
	// +kubebuilder:pruning:PreserveUnknownFields
	Parent apiextensionsv1.JSON `json:"parent"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Worker apiextensionsv1.JSON `json:"worker"`
	// +kubebuilder:pruning:PreserveUnknownFields
	Template apiextensionsv1.JSON `json:"template"`
	// ModelProfile is the hash of the pinned model profile of a model the
	// fleet calls itself. Absent for a model-free profile: its nodes hold no
	// model and every run brings its own route through the gateway.
	// +kubebuilder:validation:Pattern=`^blake3:[0-9a-f]{64}$`
	// +optional
	ModelProfile string `json:"modelProfile,omitempty"`
	// +kubebuilder:validation:Minimum=1
	ReservedMemoryBytes int64 `json:"reservedMemoryBytes"`
	// +kubebuilder:validation:Minimum=1
	TurnModelRequests int64 `json:"turnModelRequests"`
	// +kubebuilder:validation:Minimum=1
	TurnOutputTokens int64 `json:"turnOutputTokens"`
	// SystemPrompt is the persona bound into the model profile; a run using
	// this runtime must carry it exactly.
	// +kubebuilder:validation:MaxLength=8192
	SystemPrompt string `json:"systemPrompt"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Revision",type=string,JSONPath=`.spec.revision`
// +kubebuilder:printcolumn:name="Contract",type=string,JSONPath=`.spec.contractVersion`
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="runtime profile revisions are immutable; publish a new revision"
type CellnRuntimeProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CellnRuntimeProfileSpec `json:"spec"`
	// +optional
	Status CellnPlatformCatalogueStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CellnRuntimeProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CellnRuntimeProfile `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Revision",type=string,JSONPath=`.spec.revision`
// +kubebuilder:printcolumn:name="Lane",type=string,JSONPath=`.spec.lane`
// +kubebuilder:validation:XValidation:rule="self.spec == oldSelf.spec",message="cluster tool revisions are immutable; publish a new revision"
type ClusterCellnTool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CellnToolSpec `json:"spec"`
	// +optional
	Status CellnPlatformCatalogueStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ClusterCellnToolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterCellnTool `json:"items"`
}

type CellnPlatformCatalogueStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// Conditions may describe publication/distribution observations but never
	// constitute execution admission for a tenant run.
	// +optional
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// CellnExecutionPolicyRuntime permits an exact shared runtime revision and may
// narrow its numeric ceilings. The resolver enforces that the override cannot
// raise the referenced profile's ceilings.
type CellnExecutionPolicyRuntime struct {
	Ref CellnRuntimeProfileRef `json:"ref"`
	// +optional
	Limits *AgentRuntimeCellnLimits `json:"limits,omitempty"`
}

// CellnExecutionPolicyTool permits an exact shared tool revision and may narrow
// that tool's declared constraints.
type CellnExecutionPolicyTool struct {
	Ref ClusterCellnToolRef `json:"ref"`
	// +optional
	Limits *CellnToolLimits `json:"limits,omitempty"`
}

// CellnAnyModel is the token a route-era policy used for "any model". A run
// still never binds it as a concrete model name.
const CellnAnyModel = "*"

type CellnExecutionPolicyCeilings struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1024
	MaxTurns int64 `json:"maxTurns"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=6144
	MaxModelRequests int64 `json:"maxModelRequests"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=25165824
	MaxOutputTokens int64 `json:"maxOutputTokens"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=86400
	MaxParentLeaseSeconds int64 `json:"maxParentLeaseSeconds"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=3600
	MaxTurnSeconds int64 `json:"maxTurnSeconds"`
}

// CellnExecutionPolicySpec maps protected namespace labels to an intersection of
// exact catalogue revisions, lifecycle modes, and hard ceilings. An Agent's
// ModelConnection is its model route; AllowedOrigins/DeniedOrigins narrow it.
// Empty tools means no tool authority; it does not make an otherwise valid
// tool-free direct request invalid.
// +kubebuilder:validation:XValidation:rule="self.runtimeProfiles.all(x, self.runtimeProfiles.filter(y, y.ref.name == x.ref.name).size() == 1)",message="runtime profile names must be unique within a policy"
// +kubebuilder:validation:XValidation:rule="self.tools.all(x, self.tools.filter(y, y.ref.name == x.ref.name).size() == 1)",message="cluster tool names must be unique within a policy"
type CellnExecutionPolicySpec struct {
	NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	RuntimeProfiles []CellnExecutionPolicyRuntime `json:"runtimeProfiles"`
	// +kubebuilder:validation:MaxItems=64
	// +listType=atomic
	Tools []CellnExecutionPolicyTool `json:"tools"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=3
	// +listType=set
	// +kubebuilder:validation:items:Enum=direct-one-shot;harness-one-shot;enduring
	Lifecycles []string                     `json:"lifecycles"`
	Ceilings   CellnExecutionPolicyCeilings `json:"ceilings"`
	// AllowedOrigins narrows where an Agent's own key (or a keyless
	// connection) may send model requests: host patterns, each an exact
	// lowercase host with an optional port or a "*." wildcard over
	// subdomains. Empty admits any origin the gateway can reach. The
	// Agent's connection is the route; this is the operator's one-line
	// guardrail over it.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:Pattern=`^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$`
	// +listType=set
	// +optional
	AllowedOrigins []string `json:"allowedOrigins,omitempty"`
	// DeniedOrigins refuses matching origins even when AllowedOrigins admits
	// them; same patterns.
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:Pattern=`^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*(:[0-9]{1,5})?$`
	// +listType=set
	// +optional
	DeniedOrigins []string `json:"deniedOrigins,omitempty"`
}

// AdmitsOrigin reports whether the policy's origin guardrail admits a model
// endpoint origin for an Agent's own connection: not denied, and allowed when
// an allow-list is declared. It returns the pattern that decided a refusal.
func (s CellnExecutionPolicySpec) AdmitsOrigin(origin string) (bool, string) {
	for _, pattern := range s.DeniedOrigins {
		if OriginMatches(origin, pattern) {
			return false, pattern
		}
	}
	if len(s.AllowedOrigins) == 0 {
		return true, ""
	}
	for _, pattern := range s.AllowedOrigins {
		if OriginMatches(origin, pattern) {
			return true, ""
		}
	}
	return false, ""
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
type CellnExecutionPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CellnExecutionPolicySpec `json:"spec"`
	// +optional
	Status CellnExecutionPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CellnExecutionPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CellnExecutionPolicy `json:"items"`
}

type CellnExecutionPolicyStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

func init() {
	SchemeBuilder.Register(
		&CellnRuntimeProfile{}, &CellnRuntimeProfileList{},
		&ClusterCellnTool{}, &ClusterCellnToolList{},
		&CellnExecutionPolicy{}, &CellnExecutionPolicyList{},
	)
}
