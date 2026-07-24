package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
	// +kubebuilder:validation:Enum=Pending;Running;Verifying;Verified;Failed
	Phase            GenerationPhase `json:"phase,omitempty"`
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
