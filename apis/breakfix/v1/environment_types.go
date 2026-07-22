package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"time"
)

type SubmitResult struct {
	Passed      bool         `json:"passed"`
	ExitCode    int          `json:"exitCode"`
	Output      string       `json:"output,omitempty"`
	SubmittedAt *metav1.Time `json:"submittedAt,omitempty"`
}

type EnvironmentPhase string

const (
	EnvironmentPending      EnvironmentPhase = "Pending"
	EnvironmentProvisioning EnvironmentPhase = "Provisioning"
	EnvironmentReady        EnvironmentPhase = "Ready"
	EnvironmentDraining     EnvironmentPhase = "Draining"
	EnvironmentSubmitted    EnvironmentPhase = "Submitted"
	EnvironmentDestroyed    EnvironmentPhase = "Destroyed"
	EnvironmentFailed       EnvironmentPhase = "Failed"
)

const (
	ConditionProvisioned     = "Provisioned"
	ConditionWorkspaceReady  = "WorkspaceReady"
	ConditionKubeconfigReady = "KubeconfigReady"
	ConditionReady           = "Ready"
	ConditionDraining        = "Draining"
	ConditionSubmitted       = "Submitted"
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
	AutoDestroyAfterSubmit *bool `json:"autoDestroyAfterSubmit,omitempty"`
	AutoDestroyAfterIdle   *bool `json:"autoDestroyAfterIdle,omitempty"`
	ForceCleanupOnFailure  *bool `json:"forceCleanupOnFailure,omitempty"`
}

type EnvironmentResourcesSpec struct {
	WorkspaceCPU              string `json:"workspaceCpu,omitempty"`
	WorkspaceMemory           string `json:"workspaceMemory,omitempty"`
	WorkspaceEphemeralStorage string `json:"workspaceEphemeralStorage,omitempty"`
}

type CommonEnvironmentSpec struct {
	ChallengeRef  string                   `json:"challengeRef"`
	UserRef       string                   `json:"userRef"`
	Image         string                   `json:"image"`
	Submit        bool                     `json:"submit,omitempty"`
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
	Phase              EnvironmentPhase        `json:"phase,omitempty"`
	ObservedGeneration int64                   `json:"observedGeneration,omitempty"`
	Namespace          string                  `json:"namespace,omitempty"`
	WorkspacePodName   string                  `json:"workspacePodName,omitempty"`
	StartedAt          *metav1.Time            `json:"startedAt,omitempty"`
	ReadyAt            *metav1.Time            `json:"readyAt,omitempty"`
	ExpiresAt          *metav1.Time            `json:"expiresAt,omitempty"`
	DestroyedAt        *metav1.Time            `json:"destroyedAt,omitempty"`
	Reason             string                  `json:"reason,omitempty"`
	LastError          *EnvironmentErrorStatus `json:"lastError,omitempty"`
	Conditions         []metav1.Condition      `json:"conditions,omitempty"`
	SubmitResult       *SubmitResult           `json:"submitResult,omitempty"`
	Message            string                  `json:"message,omitempty"`
}

// +genclient
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

// +genclient
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

func (in *ContainerEnvironment) DeepCopyInto(out *ContainerEnvironment) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *ContainerEnvironment) DeepCopy() *ContainerEnvironment {
	if in == nil {
		return nil
	}
	out := new(ContainerEnvironment)
	in.DeepCopyInto(out)
	return out
}

func (in *ContainerEnvironment) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *ContainerEnvironmentList) DeepCopyInto(out *ContainerEnvironmentList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		items := make([]ContainerEnvironment, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&items[i])
		}
		out.Items = items
	}
}

func (in *ContainerEnvironmentList) DeepCopy() *ContainerEnvironmentList {
	if in == nil {
		return nil
	}
	out := new(ContainerEnvironmentList)
	in.DeepCopyInto(out)
	return out
}

func (in *ContainerEnvironmentList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *VClusterEnvironment) DeepCopyInto(out *VClusterEnvironment) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *VClusterEnvironment) DeepCopy() *VClusterEnvironment {
	if in == nil {
		return nil
	}
	out := new(VClusterEnvironment)
	in.DeepCopyInto(out)
	return out
}

func (in *VClusterEnvironment) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *VClusterEnvironmentList) DeepCopyInto(out *VClusterEnvironmentList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		items := make([]VClusterEnvironment, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&items[i])
		}
		out.Items = items
	}
}

func (in *VClusterEnvironmentList) DeepCopy() *VClusterEnvironmentList {
	if in == nil {
		return nil
	}
	out := new(VClusterEnvironmentList)
	in.DeepCopyInto(out)
	return out
}

func (in *VClusterEnvironmentList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *EnvironmentTimeoutsSpec) DeepCopyInto(out *EnvironmentTimeoutsSpec) {
	*out = *in
	if in.ReadyTimeoutSeconds != nil {
		v := *in.ReadyTimeoutSeconds
		out.ReadyTimeoutSeconds = &v
	}
	if in.IdleTTLSeconds != nil {
		v := *in.IdleTTLSeconds
		out.IdleTTLSeconds = &v
	}
	if in.DrainGracePeriodSeconds != nil {
		v := *in.DrainGracePeriodSeconds
		out.DrainGracePeriodSeconds = &v
	}
	if in.DestroyTimeoutSeconds != nil {
		v := *in.DestroyTimeoutSeconds
		out.DestroyTimeoutSeconds = &v
	}
}

func (in *CleanupPolicySpec) DeepCopyInto(out *CleanupPolicySpec) {
	*out = *in
	if in.AutoDestroyAfterSubmit != nil {
		v := *in.AutoDestroyAfterSubmit
		out.AutoDestroyAfterSubmit = &v
	}
	if in.AutoDestroyAfterIdle != nil {
		v := *in.AutoDestroyAfterIdle
		out.AutoDestroyAfterIdle = &v
	}
	if in.ForceCleanupOnFailure != nil {
		v := *in.ForceCleanupOnFailure
		out.ForceCleanupOnFailure = &v
	}
}

func (in *CommonEnvironmentSpec) DeepCopyInto(out *CommonEnvironmentSpec) {
	*out = *in
	in.Timeouts.DeepCopyInto(&out.Timeouts)
	in.CleanupPolicy.DeepCopyInto(&out.CleanupPolicy)
}

func (in *CommonEnvironmentStatus) DeepCopyInto(out *CommonEnvironmentStatus) {
	*out = *in
	if in.LastError != nil {
		e := *in.LastError
		out.LastError = &e
	}
	if in.LastError != nil && in.LastError.At != nil {
		t := *in.LastError.At
		out.LastError.At = &t
	}
	if in.Conditions != nil {
		out.Conditions = make([]metav1.Condition, len(in.Conditions))
		copy(out.Conditions, in.Conditions)
	}
	if in.SubmitResult != nil {
		r := *in.SubmitResult
		out.SubmitResult = &r
	}
	if in.StartedAt != nil {
		t := *in.StartedAt
		out.StartedAt = &t
	}
	if in.ExpiresAt != nil {
		t := *in.ExpiresAt
		out.ExpiresAt = &t
	}
	if in.ReadyAt != nil {
		t := *in.ReadyAt
		out.ReadyAt = &t
	}
	if in.DestroyedAt != nil {
		t := *in.DestroyedAt
		out.DestroyedAt = &t
	}
	if in.SubmitResult != nil && in.SubmitResult.SubmittedAt != nil {
		t := *in.SubmitResult.SubmittedAt
		out.SubmitResult.SubmittedAt = &t
	}
}

func (in *VClusterEnvironmentSpec) DeepCopyInto(out *VClusterEnvironmentSpec) {
	*out = *in
	in.CommonEnvironmentSpec.DeepCopyInto(&out.CommonEnvironmentSpec)
}

func (in *VClusterEnvironmentStatus) DeepCopyInto(out *VClusterEnvironmentStatus) {
	*out = *in
	in.CommonEnvironmentStatus.DeepCopyInto(&out.CommonEnvironmentStatus)
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

func (in *CommonEnvironmentSpec) AutoDestroyAfterSubmitOr(fallback bool) bool {
	if in == nil || in.CleanupPolicy.AutoDestroyAfterSubmit == nil {
		return fallback
	}
	return *in.CleanupPolicy.AutoDestroyAfterSubmit
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
