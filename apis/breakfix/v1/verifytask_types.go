package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced

type VerifyTask struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VerifyTaskSpec   `json:"spec"`
	Status VerifyTaskStatus `json:"status,omitempty"`
}

type VerifyTaskSpec struct {
	Source      VerifyTaskSource     `json:"source"`
	ChallengeID string               `json:"challengeID"`
	Submission  VerifyTaskSubmission `json:"submission"`
}

type VerifyTaskSource struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref,omitempty"`
}

type VerifyTaskSubmission struct {
	ID string `json:"id"`
}

type VerifyTaskPhase string

const (
	VerifyTaskPending   VerifyTaskPhase = "Pending"
	VerifyTaskRunning   VerifyTaskPhase = "Running"
	VerifyTaskVerified  VerifyTaskPhase = "Verified"
	VerifyTaskFailed    VerifyTaskPhase = "Failed"
	VerifyTaskSucceeded VerifyTaskPhase = "Succeeded"
)

type VerifyIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type VerifyReport struct {
	BuildPassed  bool          `json:"buildPassed,omitempty"`
	AnswerPassed bool          `json:"answerPassed,omitempty"`
	CheckpointsPassed bool          `json:"checkpointsPassed,omitempty"`
	Summary      string        `json:"summary,omitempty"`
	Issues       []VerifyIssue `json:"issues,omitempty"`
}

type VerifyTaskStatus struct {
	Phase       VerifyTaskPhase `json:"phase,omitempty"`
	Message     string          `json:"message,omitempty"`
	JobName     string          `json:"jobName,omitempty"`
	PodName     string          `json:"podName,omitempty"`
	TempImage   string          `json:"tempImage,omitempty"`
	StartedAt   *metav1.Time    `json:"startedAt,omitempty"`
	CompletedAt *metav1.Time    `json:"completedAt,omitempty"`
	Report      *VerifyReport   `json:"report,omitempty"`
}

// +kubebuilder:object:root=true
type VerifyTaskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VerifyTask `json:"items"`
}

func (in *VerifyTask) DeepCopyInto(out *VerifyTask) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *VerifyTask) DeepCopy() *VerifyTask {
	if in == nil {
		return nil
	}
	out := new(VerifyTask)
	in.DeepCopyInto(out)
	return out
}

func (in *VerifyTask) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *VerifyTaskList) DeepCopyInto(out *VerifyTaskList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		items := make([]VerifyTask, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&items[i])
		}
		out.Items = items
	}
}

func (in *VerifyTaskList) DeepCopy() *VerifyTaskList {
	if in == nil {
		return nil
	}
	out := new(VerifyTaskList)
	in.DeepCopyInto(out)
	return out
}

func (in *VerifyTaskList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *VerifyTaskSpec) DeepCopyInto(out *VerifyTaskSpec) {
	*out = *in
}

func (in *VerifyTaskStatus) DeepCopyInto(out *VerifyTaskStatus) {
	*out = *in
	if in.StartedAt != nil {
		t := *in.StartedAt
		out.StartedAt = &t
	}
	if in.CompletedAt != nil {
		t := *in.CompletedAt
		out.CompletedAt = &t
	}
	if in.Report != nil {
		report := *in.Report
		if in.Report.Issues != nil {
			report.Issues = append([]VerifyIssue{}, in.Report.Issues...)
		}
		out.Report = &report
	}
}
