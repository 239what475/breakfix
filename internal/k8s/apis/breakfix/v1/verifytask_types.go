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
	Source     VerifyTaskSource     `json:"source"`
	Submission VerifyTaskSubmission `json:"submission"`
	Execution  VerifyTaskExecution  `json:"execution"`
}

type VerifyTaskSource struct {
	Ref string `json:"ref"`
}

type VerifyTaskSubmission struct {
	ID string `json:"id"`
}

// VerifyTaskExecution is the small immutable execution contract extracted by
// the Server from the candidate before it creates the task. The verifier must
// never re-read the untrusted submission archive to decide what to execute.
type VerifyTaskExecution struct {
	Runtime       string   `json:"runtime"`
	CheckpointIDs []string `json:"checkpointIds"`
}

type VerifyTaskPhase string

const (
	VerifyTaskPending   VerifyTaskPhase = "Pending"
	VerifyTaskRunning   VerifyTaskPhase = "Running"
	VerifyTaskFailed    VerifyTaskPhase = "Failed"
	VerifyTaskSucceeded VerifyTaskPhase = "Succeeded"
)

// +kubebuilder:validation:Enum=Building;Publishing;Verifying
type VerifyTaskStage string

const (
	VerifyTaskBuilding   VerifyTaskStage = "Building"
	VerifyTaskPublishing VerifyTaskStage = "Publishing"
	VerifyTaskVerifying  VerifyTaskStage = "Verifying"
)

type VerifyIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// +kubebuilder:validation:Enum=artifact;infrastructure
type VerifyFailureClass string

const (
	VerifyFailureArtifact       VerifyFailureClass = "artifact"
	VerifyFailureInfrastructure VerifyFailureClass = "infrastructure"
)

type VerifyReport struct {
	Class             VerifyFailureClass `json:"class,omitempty"`
	BuildPassed       bool               `json:"buildPassed,omitempty"`
	AnswerPassed      bool               `json:"answerPassed,omitempty"`
	CheckpointsPassed bool               `json:"checkpointsPassed,omitempty"`
	Summary           string             `json:"summary,omitempty"`
	Issues            []VerifyIssue      `json:"issues,omitempty"`
}

type VerifyTaskStatus struct {
	// +kubebuilder:validation:Enum=Pending;Running;Failed;Succeeded
	Phase            VerifyTaskPhase `json:"phase,omitempty"`
	Message          string          `json:"message,omitempty"`
	Stage            VerifyTaskStage `json:"stage,omitempty"`
	BuildJobName     string          `json:"buildJobName,omitempty"`
	PublisherJobName string          `json:"publisherJobName,omitempty"`
	VerifierJobName  string          `json:"verifierJobName,omitempty"`
	StagingImage     string          `json:"stagingImage,omitempty"`
	Image            string          `json:"image,omitempty"`
	StartedAt        *metav1.Time    `json:"startedAt,omitempty"`
	CompletedAt      *metav1.Time    `json:"completedAt,omitempty"`
	Report           *VerifyReport   `json:"report,omitempty"`
}

// +kubebuilder:object:root=true
type VerifyTaskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VerifyTask `json:"items"`
}
