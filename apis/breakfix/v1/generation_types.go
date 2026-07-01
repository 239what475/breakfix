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
	Draft *ChallengeDraft   `json:"draft,omitempty"`
	Image string            `json:"image"`
	Env   map[string]string `json:"env,omitempty"`
}

type GenerationStatus struct {
	Phase       GenerationPhase `json:"phase"`
	JobName     string          `json:"jobName,omitempty"`
	PodName     string          `json:"podName,omitempty"`
	Challenge   *ChallengeSpec  `json:"challenge,omitempty"`
	Message     string          `json:"message,omitempty"`
	StartedAt   *metav1.Time    `json:"startedAt,omitempty"`
	CompletedAt *metav1.Time    `json:"completedAt,omitempty"`
}

type ChallengeDraft struct {
	Title              string   `json:"title"`
	Difficulty         string   `json:"difficulty"`
	Tags               []string `json:"tags"`
	Description        string   `json:"description"`
	Goal               string   `json:"goal"`
	Symptoms           string   `json:"symptoms"`
	FaultMechanism     string   `json:"fault_mechanism"`
	EnvironmentShape   string   `json:"environment_shape"`
	AcceptanceCriteria string   `json:"acceptance_criteria"`
	DifficultyReason   string   `json:"difficulty_reason"`
	Notes              string   `json:"notes,omitempty"`
}

type GenerationPhase string

const (
	GenerationPending   GenerationPhase = "Pending"
	GenerationRunning   GenerationPhase = "Running"
	GenerationSucceeded GenerationPhase = "Succeeded"
	GenerationFailed    GenerationPhase = "Failed"
)

type ChallengeSpec struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Type        string   `json:"type"`
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
	if in.Draft != nil {
		d := *in.Draft
		d.Tags = append([]string{}, d.Tags...)
		out.Draft = &d
	}
	if in.Env != nil {
		out.Env = make(map[string]string, len(in.Env))
		for k, v := range in.Env {
			out.Env[k] = v
		}
	}
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
