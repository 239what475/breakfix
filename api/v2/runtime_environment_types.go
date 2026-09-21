package v2

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

type EnvironmentPurpose string

const (
	PurposeLearning     EnvironmentPurpose = "learning"
	PurposeVerification EnvironmentPurpose = "verification"
)

type EnvironmentPhase string

const (
	PhasePending      EnvironmentPhase = "Pending"
	PhaseProvisioning EnvironmentPhase = "Provisioning"
	PhaseReady        EnvironmentPhase = "Ready"
	PhaseDraining     EnvironmentPhase = "Draining"
	PhaseReleased     EnvironmentPhase = "Released"
	PhaseFailed       EnvironmentPhase = "Failed"
)

type EnvironmentOperation string

const (
	OperationNone      EnvironmentOperation = "None"
	OperationResetting EnvironmentOperation = "Resetting"
)

type FailureClass string

const (
	FailureArtifact       FailureClass = "artifact"
	FailureInfrastructure FailureClass = "infrastructure"
)

type RunnableRevisionReference struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

// LeaseSpec is exclusively updated by Server. The API server enforces its
// monotonic fields while the Controller only observes it.
// +kubebuilder:validation:XValidation:rule="oldSelf == null || self.renewedAt >= oldSelf.renewedAt",message="renewedAt must be monotonic"
// +kubebuilder:validation:XValidation:rule="oldSelf == null || !has(oldSelf.releaseAt) || (has(self.releaseAt) && self.releaseAt >= oldSelf.releaseAt)",message="releaseAt must be monotonic once set"
type LeaseSpec struct {
	RenewedAt metav1.Time  `json:"renewedAt"`
	ReleaseAt *metav1.Time `json:"releaseAt,omitempty"`
}

// RuntimeEnvironmentSpec contains only Server-owned controls. Runtime
// profile, artifact, plan, and lifecycle policy are dereferenced from the
// immutable RunnableRevision and never copied into the CRD; a blank
// environment names no revision and the Controller resolves its installed
// runtime definition from configuration instead.
// +kubebuilder:validation:XValidation:rule="oldSelf == null || self.runnableRevisionRef == oldSelf.runnableRevisionRef",message="runnableRevisionRef is immutable"
// +kubebuilder:validation:XValidation:rule="oldSelf == null || self.blankRuntime == oldSelf.blankRuntime",message="blankRuntime is immutable"
// +kubebuilder:validation:XValidation:rule="!has(self.blankRuntime) || (self.runnableRevisionRef.id == ” && self.runnableRevisionRef.digest == ”)",message="blankRuntime excludes runnableRevisionRef"
// +kubebuilder:validation:XValidation:rule="has(self.blankRuntime) || (self.runnableRevisionRef.id != ” && self.runnableRevisionRef.digest != ”)",message="runnableRevisionRef requires id and digest when blankRuntime is absent"
// +kubebuilder:validation:XValidation:rule="oldSelf == null || self.purpose == oldSelf.purpose",message="purpose is immutable"
// +kubebuilder:validation:XValidation:rule="oldSelf == null || !has(oldSelf.resetNonce) || (has(self.resetNonce) && self.resetNonce >= oldSelf.resetNonce)",message="resetNonce must be monotonic"
type RuntimeEnvironmentSpec struct {
	// +optional
	RunnableRevisionRef RunnableRevisionReference `json:"runnableRevisionRef,omitempty"`
	// +optional
	BlankRuntime *BlankRuntimeSpec `json:"blankRuntime,omitempty"`
	// +kubebuilder:validation:Enum=learning;verification
	Purpose EnvironmentPurpose `json:"purpose"`
	Lease   LeaseSpec          `json:"lease"`
	// +optional
	// +kubebuilder:validation:Minimum=0
	ResetNonce int64 `json:"resetNonce,omitempty"`
}

// BlankRuntimeSpec selects a system blank runtime in place of a runnable
// revision. A blank environment exists for free-form hands-on practice: it
// provisions the installed runtime with no content, artifact, or plan.
type BlankRuntimeSpec struct {
	// +kubebuilder:validation:Enum=k8s
	Provider string `json:"provider"`
}

type ResourceReference struct {
	Provider string `json:"provider"`
	Kind     string `json:"kind"`
	ID       string `json:"id"`
}

type EndpointReference struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
}

type RuntimeStatus struct {
	// +kubebuilder:validation:Enum=node;k8s
	Provider      string              `json:"provider"`
	ProfileDigest string              `json:"profileDigest"`
	ResourceRefs  []ResourceReference `json:"resourceRefs,omitempty"`
	EndpointRefs  []EndpointReference `json:"endpointRefs,omitempty"`
}

type ReportReference struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

type EnvironmentProgress struct {
	PhaseID   string          `json:"phaseId,omitempty"`
	Attempt   int64           `json:"attempt,omitempty"`
	ReportRef ReportReference `json:"reportRef"`
}

type EnvironmentLifecycleStatus struct {
	ExpiresAt  *metav1.Time `json:"expiresAt,omitempty"`
	ReleasedAt *metav1.Time `json:"releasedAt,omitempty"`
}

type EnvironmentFailure struct {
	// +kubebuilder:validation:Enum=artifact;infrastructure
	Class     FailureClass `json:"class"`
	Component string       `json:"component"`
	Reason    string       `json:"reason"`
	// +kubebuilder:validation:MaxLength=1024
	Message string      `json:"message,omitempty"`
	At      metav1.Time `json:"at"`
}

// RuntimeEnvironmentStatus is exclusively Controller-owned. It projects
// lifecycle state and immutable result references without duplicating action
// or assertion trees.
type RuntimeEnvironmentStatus struct {
	// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Draining;Released;Failed
	Phase EnvironmentPhase `json:"phase,omitempty"`
	// +kubebuilder:validation:Enum=None;Resetting
	Operation          EnvironmentOperation       `json:"operation,omitempty"`
	ObservedGeneration int64                      `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition         `json:"conditions,omitempty"`
	Runtime            RuntimeStatus              `json:"runtime,omitempty"`
	Progress           EnvironmentProgress        `json:"progress,omitempty"`
	Lifecycle          EnvironmentLifecycleStatus `json:"lifecycle,omitempty"`
	Failure            *EnvironmentFailure        `json:"failure,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Purpose",type=string,JSONPath=`.spec.purpose`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type RuntimeEnvironment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RuntimeEnvironmentSpec   `json:"spec"`
	Status RuntimeEnvironmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type RuntimeEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RuntimeEnvironment `json:"items"`
}
