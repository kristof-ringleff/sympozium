package v1alpha1

// CellnCatalogueSelection keeps tenant intent separate from operator grant and
// route configuration. Ordered tools are explicitly lent;
// an empty list lends none, never all installed tools.
//
// toolRefs always means the legacy namespaced CellnTool API. clusterToolRefs
// always means the shared ClusterCellnTool catalogue. The two scopes are never
// inferred from object existence and a friendly name may not appear in both
// lists in one selection.
// +kubebuilder:validation:XValidation:rule="self.toolRefs.all(t, self.toolRefs.filter(x, x.name == t.name).size() == 1)",message="catalogue tool names must be unique"
// +kubebuilder:validation:XValidation:rule="!has(self.clusterToolRefs) || self.clusterToolRefs.all(t, self.clusterToolRefs.filter(x, x.name == t.name).size() == 1)",message="cluster catalogue tool names must be unique"
// +kubebuilder:validation:XValidation:rule="!has(self.clusterToolRefs) || self.clusterToolRefs.all(t, self.toolRefs.filter(x, x.name == t.name).size() == 0)",message="a tool name cannot resolve through both namespaced and cluster catalogues"
type CellnCatalogueSelection struct {
	// RuntimeRef preserves the legacy namespaced AgentRuntime reference. It is
	// never reinterpreted as a CellnRuntimeProfile name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$"
	// +optional
	RuntimeRef string `json:"runtimeRef,omitempty"`
	// +kubebuilder:validation:MaxItems=24
	// +listType=atomic
	ToolRefs []CellnCatalogueToolRef `json:"toolRefs"`
	// ClusterToolRefs selects exact shared catalogue revisions. Empty means no
	// shared tools, not every installed tool.
	// +optional
	// +kubebuilder:validation:MaxItems=24
	// +listType=atomic
	ClusterToolRefs []ClusterCellnToolRef `json:"clusterToolRefs,omitempty"`
}

type CellnCatalogueToolRef struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$"
	Name string `json:"name"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Revision string `json:"revision"`
}

// CellnHarnessJSONLimits are explicit bounded loop ceilings, not model grants.
type CellnHarnessJSONLimits struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=6
	MaxTurns int64 `json:"maxTurns"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=16
	MaxCalls int64 `json:"maxCalls"`
}

type CellnImmutableRef struct {
	// +kubebuilder:validation:Pattern="^blake3:[0-9a-f]{64}$"
	// +kubebuilder:validation:MaxLength=71
	Hash string `json:"hash"`
}
