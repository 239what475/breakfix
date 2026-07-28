package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"time"
)

type CheckpointResultStatus struct {
	ID            string       `json:"id"`
	Passed        bool         `json:"passed"`
	FirstPassedAt *metav1.Time `json:"firstPassedAt,omitempty"`
	Summary       string       `json:"summary"`
	Details       string       `json:"details,omitempty"`
}

// CheckpointStatus is controller-owned state from the most recent checkpoint run.
type CheckpointStatus struct {
	Results   []CheckpointResultStatus `json:"results,omitempty"`
	CheckedAt *metav1.Time             `json:"checkedAt,omitempty"`
	Error     string                   `json:"error,omitempty"`
}

type EnvironmentPhase string

const (
	EnvironmentPending      EnvironmentPhase = "Pending"
	EnvironmentProvisioning EnvironmentPhase = "Provisioning"
	EnvironmentReady        EnvironmentPhase = "Ready"
	EnvironmentDraining     EnvironmentPhase = "Draining"
	EnvironmentCompleted    EnvironmentPhase = "Completed"
	EnvironmentDestroyed    EnvironmentPhase = "Destroyed"
	EnvironmentFailed       EnvironmentPhase = "Failed"
)

const (
	ConditionProvisioned     = "Provisioned"
	ConditionWorkspaceReady  = "WorkspaceReady"
	ConditionKubeconfigReady = "KubeconfigReady"
	ConditionReady           = "Ready"
	ConditionDraining        = "Draining"
	ConditionCompleted       = "Completed"
	ConditionCleanedUp       = "CleanedUp"
	ConditionFailed          = "Failed"
)

type EnvironmentTimeoutsSpec struct {
	ReadyTimeoutSeconds     *int64 `json:"readyTimeoutSeconds,omitempty"`
	IdleTTLSeconds          *int64 `json:"idleTtlSeconds,omitempty"`
	DrainGracePeriodSeconds *int64 `json:"drainGracePeriodSeconds,omitempty"`
	DestroyTimeoutSeconds   *int64 `json:"destroyTimeoutSeconds,omitempty"`
}

type CleanupPolicySpec struct {
	AutoDestroyAfterIdle  *bool `json:"autoDestroyAfterIdle,omitempty"`
	ForceCleanupOnFailure *bool `json:"forceCleanupOnFailure,omitempty"`
}

type EnvironmentResourcesSpec struct {
	WorkspaceCPU              string `json:"workspaceCpu,omitempty"`
	WorkspaceMemory           string `json:"workspaceMemory,omitempty"`
	WorkspaceEphemeralStorage string `json:"workspaceEphemeralStorage,omitempty"`
}

type CommonEnvironmentSpec struct {
	ChallengeRef string `json:"challengeRef"`
	// These fields stay schema-optional so pre-split Environment CRDs can be
	// finalized after an upgrade. The Controller requires them before provision.
	ChallengeRevision string `json:"challengeRevision,omitempty"`
	UserRef           string `json:"userRef"`
	// +kubebuilder:validation:Enum=container;vcluster
	Runtime string `json:"runtime,omitempty"`
	Image   string `json:"image"`
	// +listType=set
	CheckpointIDs []string                 `json:"checkpointIDs,omitempty"`
	ActivityAt    *metav1.Time             `json:"activityAt,omitempty"`
	Timeouts      EnvironmentTimeoutsSpec  `json:"timeouts,omitempty"`
	CleanupPolicy CleanupPolicySpec        `json:"cleanupPolicy,omitempty"`
	Resources     EnvironmentResourcesSpec `json:"resources,omitempty"`
}

type EnvironmentErrorStatus struct {
	Component string       `json:"component,omitempty"`
	Code      string       `json:"code,omitempty"`
	Message   string       `json:"message,omitempty"`
	At        *metav1.Time `json:"at,omitempty"`
	Retriable bool         `json:"retriable,omitempty"`
}

type CommonEnvironmentStatus struct {
	// +kubebuilder:validation:Enum=Pending;Provisioning;Ready;Draining;Completed;Destroyed;Failed
	Phase              EnvironmentPhase        `json:"phase,omitempty"`
	ObservedGeneration int64                   `json:"observedGeneration,omitempty"`
	Namespace          string                  `json:"namespace,omitempty"`
	WorkspacePodName   string                  `json:"workspacePodName,omitempty"`
	StartedAt          *metav1.Time            `json:"startedAt,omitempty"`
	ReadyAt            *metav1.Time            `json:"readyAt,omitempty"`
	LastActivityAt     *metav1.Time            `json:"lastActivityAt,omitempty"`
	CompletedAt        *metav1.Time            `json:"completedAt,omitempty"`
	ExpiresAt          *metav1.Time            `json:"expiresAt,omitempty"`
	DestroyedAt        *metav1.Time            `json:"destroyedAt,omitempty"`
	Reason             string                  `json:"reason,omitempty"`
	LastError          *EnvironmentErrorStatus `json:"lastError,omitempty"`
	Conditions         []metav1.Condition      `json:"conditions,omitempty"`
	Checkpoints        *CheckpointStatus       `json:"checkpoints,omitempty"`
	Message            string                  `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
type ContainerEnvironment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CommonEnvironmentSpec   `json:"spec"`
	Status CommonEnvironmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ContainerEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ContainerEnvironment `json:"items"`
}

type VClusterEnvironmentSpec struct {
	CommonEnvironmentSpec `json:",inline"`
	VCluster              VClusterRuntimeSpec `json:"vcluster,omitempty"`
}

type VClusterRuntimeSpec struct {
	Profile               string `json:"profile,omitempty"`
	Version               string `json:"version,omitempty"`
	ControlPlaneCPU       string `json:"controlPlaneCpu,omitempty"`
	ControlPlaneMemory    string `json:"controlPlaneMemory,omitempty"`
	QuotaCPU              string `json:"quotaCpu,omitempty"`
	QuotaMemory           string `json:"quotaMemory,omitempty"`
	QuotaEphemeralStorage string `json:"quotaEphemeralStorage,omitempty"`
}

type VClusterEnvironmentStatus struct {
	CommonEnvironmentStatus `json:",inline"`
	VClusterName            string `json:"vclusterName,omitempty"`
	KubeconfigSecretName    string `json:"kubeconfigSecretName,omitempty"`
	KubeconfigReady         bool   `json:"kubeconfigReady,omitempty"`
	Profile                 string `json:"profile,omitempty"`
	Version                 string `json:"version,omitempty"`
}

func (in *ContainerEnvironment) CommonSpec() *CommonEnvironmentSpec {
	return &in.Spec
}

func (in *ContainerEnvironment) CommonStatus() *CommonEnvironmentStatus {
	return &in.Status
}

func (in *VClusterEnvironment) CommonSpec() *CommonEnvironmentSpec {
	return &in.Spec.CommonEnvironmentSpec
}

func (in *VClusterEnvironment) CommonStatus() *CommonEnvironmentStatus {
	return &in.Status.CommonEnvironmentStatus
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
type VClusterEnvironment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VClusterEnvironmentSpec   `json:"spec"`
	Status VClusterEnvironmentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type VClusterEnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VClusterEnvironment `json:"items"`
}

func (in *CommonEnvironmentSpec) ReadyTimeoutOr(fallback time.Duration) time.Duration {
	if in == nil || in.Timeouts.ReadyTimeoutSeconds == nil || *in.Timeouts.ReadyTimeoutSeconds <= 0 {
		return fallback
	}
	return time.Duration(*in.Timeouts.ReadyTimeoutSeconds) * time.Second
}

func (in *CommonEnvironmentSpec) IdleTTLOr(fallback time.Duration) time.Duration {
	if in == nil || in.Timeouts.IdleTTLSeconds == nil || *in.Timeouts.IdleTTLSeconds <= 0 {
		return fallback
	}
	return time.Duration(*in.Timeouts.IdleTTLSeconds) * time.Second
}

func (in *CommonEnvironmentSpec) DrainGracePeriodOr(fallback time.Duration) time.Duration {
	if in == nil || in.Timeouts.DrainGracePeriodSeconds == nil || *in.Timeouts.DrainGracePeriodSeconds <= 0 {
		return fallback
	}
	return time.Duration(*in.Timeouts.DrainGracePeriodSeconds) * time.Second
}

func (in *CommonEnvironmentSpec) DestroyTimeoutOr(fallback time.Duration) time.Duration {
	if in == nil || in.Timeouts.DestroyTimeoutSeconds == nil || *in.Timeouts.DestroyTimeoutSeconds <= 0 {
		return fallback
	}
	return time.Duration(*in.Timeouts.DestroyTimeoutSeconds) * time.Second
}

func (in *CommonEnvironmentSpec) AutoDestroyAfterIdleOr(fallback bool) bool {
	if in == nil || in.CleanupPolicy.AutoDestroyAfterIdle == nil {
		return fallback
	}
	return *in.CleanupPolicy.AutoDestroyAfterIdle
}

func (in *CommonEnvironmentSpec) ForceCleanupOnFailureOr(fallback bool) bool {
	if in == nil || in.CleanupPolicy.ForceCleanupOnFailure == nil {
		return fallback
	}
	return *in.CleanupPolicy.ForceCleanupOnFailure
}
