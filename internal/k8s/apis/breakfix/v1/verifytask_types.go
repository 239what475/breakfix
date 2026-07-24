package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
	VerifyTaskFailed    VerifyTaskPhase = "Failed"
	VerifyTaskSucceeded VerifyTaskPhase = "Succeeded"
)

type VerifyIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type VerifyReport struct {
	BuildPassed       bool          `json:"buildPassed,omitempty"`
	AnswerPassed      bool          `json:"answerPassed,omitempty"`
	CheckpointsPassed bool          `json:"checkpointsPassed,omitempty"`
	Summary           string        `json:"summary,omitempty"`
	Issues            []VerifyIssue `json:"issues,omitempty"`
}

type VerifyTaskStatus struct {
	// +kubebuilder:validation:Enum=Pending;Running;Failed;Succeeded
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
