package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced

type Instance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstanceSpec   `json:"spec"`
	Status InstanceStatus `json:"status,omitempty"`
}

type InstanceSpec struct {
	ChallengeRef string `json:"challengeRef"`
	UserRef      string `json:"userRef"`
	Image        string `json:"image"`
	Submit       bool   `json:"submit,omitempty"`
}

type SubmitResult struct {
	Passed      bool         `json:"passed"`
	ExitCode    int          `json:"exitCode"`
	Output      string       `json:"output,omitempty"`
	SubmittedAt *metav1.Time `json:"submittedAt,omitempty"`
}

type InstanceStatus struct {
	Phase         InstancePhase `json:"phase"`
	PodName       string        `json:"podName,omitempty"`
	Namespace     string        `json:"namespace,omitempty"`
	StartedAt     *metav1.Time  `json:"startedAt,omitempty"`
	CooldownUntil *metav1.Time  `json:"cooldownUntil,omitempty"`
	SubmitResult  *SubmitResult `json:"submitResult,omitempty"`
}

type InstancePhase string

const (
	InstancePending   InstancePhase = "Pending"
	InstanceRunning   InstancePhase = "Running"
	InstanceDraining  InstancePhase = "Draining"
	InstanceDestroyed InstancePhase = "Destroyed"
)

// +kubebuilder:object:root=true
type InstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Instance `json:"items"`
}

// DeepCopy methods
func (in *Instance) DeepCopyInto(out *Instance) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *Instance) DeepCopy() *Instance {
	if in == nil {
		return nil
	}
	out := new(Instance)
	in.DeepCopyInto(out)
	return out
}

func (in *Instance) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *InstanceList) DeepCopyInto(out *InstanceList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		l := make([]Instance, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&l[i])
		}
		out.Items = l
	}
}

func (in *InstanceList) DeepCopy() *InstanceList {
	if in == nil {
		return nil
	}
	out := new(InstanceList)
	in.DeepCopyInto(out)
	return out
}

func (in *InstanceList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *InstanceSpec) DeepCopyInto(out *InstanceSpec) {
	*out = *in
}

func (in *InstanceStatus) DeepCopyInto(out *InstanceStatus) {
	*out = *in
	if in.SubmitResult != nil {
		r := *in.SubmitResult
		out.SubmitResult = &r
	}
	if in.StartedAt != nil {
		t := *in.StartedAt
		out.StartedAt = &t
	}
	if in.CooldownUntil != nil {
		t := *in.CooldownUntil
		out.CooldownUntil = &t
	}
	if in.SubmitResult != nil && in.SubmitResult.SubmittedAt != nil {
		t := *in.SubmitResult.SubmittedAt
		out.SubmitResult.SubmittedAt = &t
	}
}
