package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced

type Generation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GenerationSpec   `json:"spec"`
	Status GenerationStatus `json:"status,omitempty"`
}

type GenerationSpec struct {
	Image               string `json:"image"`
	EnvSecretRef        string `json:"envSecretRef"`
	AuthoringSessionRef string `json:"authoringSessionRef,omitempty"`
	AuthoringRevision   int64  `json:"authoringRevision,omitempty"`
}

type GenerationStatus struct {
	Phase            GenerationPhase `json:"phase"`
	JobName          string          `json:"jobName,omitempty"`
	PodName          string          `json:"podName,omitempty"`
	Challenge        *ChallengeSpec  `json:"challenge,omitempty"`
	VerifyTaskRef    string          `json:"verifyTaskRef,omitempty"`
	SubmissionID     string          `json:"submissionID,omitempty"`
	ArtifactRevision int64           `json:"artifactRevision,omitempty"`
	Attempt          int64           `json:"attempt,omitempty"`
	Message          string          `json:"message,omitempty"`
	StartedAt        *metav1.Time    `json:"startedAt,omitempty"`
	CompletedAt      *metav1.Time    `json:"completedAt,omitempty"`
}

type GenerationPhase string

const (
	GenerationPending   GenerationPhase = "Pending"
	GenerationRunning   GenerationPhase = "Running"
	GenerationVerifying GenerationPhase = "Verifying"
	GenerationVerified  GenerationPhase = "Verified"
	GenerationFailed    GenerationPhase = "Failed"
)

type ChallengeSpec struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Type        string   `json:"type"`
	Runtime     string   `json:"runtime,omitempty"`
	Difficulty  string   `json:"difficulty"`
	Tags        []string `json:"tags"`
	Description string   `json:"description"`
	Image       string   `json:"image"`
}

// +kubebuilder:object:root=true
type GenerationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Generation `json:"items"`
}

// DeepCopy methods
func (in *Generation) DeepCopyInto(out *Generation) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *Generation) DeepCopy() *Generation {
	if in == nil {
		return nil
	}
	out := new(Generation)
	in.DeepCopyInto(out)
	return out
}

func (in *Generation) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *GenerationList) DeepCopyInto(out *GenerationList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		l := make([]Generation, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&l[i])
		}
		out.Items = l
	}
}

func (in *GenerationList) DeepCopy() *GenerationList {
	if in == nil {
		return nil
	}
	out := new(GenerationList)
	in.DeepCopyInto(out)
	return out
}

func (in *GenerationList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *GenerationSpec) DeepCopyInto(out *GenerationSpec) {
	*out = *in
}

func (in *GenerationStatus) DeepCopyInto(out *GenerationStatus) {
	*out = *in
	if in.Challenge != nil {
		c := *in.Challenge
		c.Tags = append([]string{}, c.Tags...)
		out.Challenge = &c
	}
	if in.StartedAt != nil {
		t := *in.StartedAt
		out.StartedAt = &t
	}
	if in.CompletedAt != nil {
		t := *in.CompletedAt
		out.CompletedAt = &t
	}
}
