package v1

// This file: the ModelDeployment spec/status exactly as spec §5.2 sketches it,
// plus status.message (design §4). Validation lives in the markers so the API
// server rejects bad objects before the controller sees them.

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Phases of the §5.3 state machine. "" (Declared) is not a phase value.
const (
	PhaseSyncing            = "Syncing"
	PhaseConformanceRunning = "ConformanceRunning"
	PhaseDeployable         = "Deployable"
	PhaseDeployed           = "Deployed"
	PhaseRejected           = "Rejected"
)

// ModelRef names a model image at a source registry. Digest is written once
// by the controller after resolution and is immutable afterwards (D5, D8).
// The type-level rule closes the hole the field-level one leaves: a rule on an
// optional field does not fire when the field is removed, so removing digest
// would otherwise unpin the object.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.digest) || has(self.digest)",message="spec.modelRef.digest cannot be removed once set"
type ModelRef struct {
	// The one source Phase 1 supports; --model-center-registry is its address.
	// A source -> registry map replaces this enum when a second source exists (design D14).
	// +kubebuilder:validation:Enum=model-center
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.modelRef.source is immutable"
	Source string `json:"source"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.modelRef.model is immutable"
	Model string `json:"model"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.modelRef.version is immutable"
	Version string `json:"version"`
	// +optional
	// +kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.modelRef.digest is immutable once set"
	Digest string `json:"digest,omitempty"`
}

// +kubebuilder:validation:XValidation:rule="self.max >= self.min",message="replicas.max must be >= replicas.min"
type Replicas struct {
	// +kubebuilder:validation:Minimum=1
	Min int32 `json:"min"`
	// +kubebuilder:validation:Minimum=1
	Max int32 `json:"max"`
}

type ModelDeploymentSpec struct {
	ModelRef ModelRef `json:"modelRef"`
	// Logical target, validated against the object's namespace (D14).
	// +kubebuilder:validation:Enum=blue;green
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.cluster is immutable"
	Cluster  string   `json:"cluster"`
	Replicas Replicas `json:"replicas"`
}

// ConformanceStatus lists stable check IDs (C1…C7) by verdict.
type ConformanceStatus struct {
	Passed []string `json:"passed,omitempty"`
	Failed []string `json:"failed,omitempty"`
}

type ModelDeploymentStatus struct {
	Phase        string            `json:"phase,omitempty"`
	PinnedDigest string            `json:"pinnedDigest,omitempty"`
	Conformance  ConformanceStatus `json:"conformance,omitempty"`
	// Human-readable reason; populated on Rejected.
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=md
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Digest",type=string,JSONPath=`.status.pinnedDigest`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ModelDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ModelDeploymentSpec   `json:"spec"`
	Status            ModelDeploymentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ModelDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ModelDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ModelDeployment{}, &ModelDeploymentList{})
}
