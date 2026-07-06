package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	EnvironmentDestroyed    EnvironmentPhase = "Destroyed"
	EnvironmentFailed       EnvironmentPhase = "Failed"
)

type CommonEnvironmentSpec struct {
	ChallengeRef string `json:"challengeRef"`
	UserRef      string `json:"userRef"`
	Image        string `json:"image"`
	Submit       bool   `json:"submit,omitempty"`
}

type CommonEnvironmentStatus struct {
	Phase            EnvironmentPhase `json:"phase,omitempty"`
	Namespace        string           `json:"namespace,omitempty"`
	WorkspacePodName string           `json:"workspacePodName,omitempty"`
	StartedAt        *metav1.Time     `json:"startedAt,omitempty"`
	ExpiresAt        *metav1.Time     `json:"expiresAt,omitempty"`
	SubmitResult     *SubmitResult    `json:"submitResult,omitempty"`
	Message          string           `json:"message,omitempty"`
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
}

type VClusterEnvironmentStatus struct {
	CommonEnvironmentStatus `json:",inline"`
	VClusterName            string `json:"vclusterName,omitempty"`
	KubeconfigSecretName    string `json:"kubeconfigSecretName,omitempty"`
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

func (in *CommonEnvironmentSpec) DeepCopyInto(out *CommonEnvironmentSpec) {
	*out = *in
}

func (in *CommonEnvironmentStatus) DeepCopyInto(out *CommonEnvironmentStatus) {
	*out = *in
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
