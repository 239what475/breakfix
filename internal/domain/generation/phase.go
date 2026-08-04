package generation

import (
	"errors"
	"strings"
)

// GeneratedCandidate is the immutable archive emitted by a generator run.
type GeneratedCandidate struct {
	RunID         string `json:"run_id"`
	Archive       []byte `json:"archive"`
	ArchiveSHA256 string `json:"archive_sha256,omitempty"`
}

// Judgement is the Judge's decision over one generated candidate.
type Judgement struct {
	RunID    string `json:"run_id"`
	Approved bool   `json:"approved"`
	Feedback string `json:"feedback,omitempty"`
}

type Classification struct {
	RunID  string               `json:"run_id"`
	Output ClassificationOutput `json:"output"`
}

// ClassificationChangeScope is a typed routing decision for feedback received
// while an author reviews a private classification proposal. The model may
// only alter the proposal for Classification; Content is handed back to the
// Authoring flow, while Clarify keeps both proposal and content unchanged.
type ClassificationChangeScope string

const (
	ClassificationChangeContent        ClassificationChangeScope = "content"
	ClassificationChangeClassification ClassificationChangeScope = "classification"
	ClassificationChangeClarify        ClassificationChangeScope = "clarify"
)

// ClassificationAdjustment is the typed terminal result of a classification
// adjustment run. For classification changes Output is the proposal assembled
// through private setters; it never carries a global Roadmap mutation.
type ClassificationAdjustment struct {
	RunID         string                    `json:"run_id"`
	ChangeScope   ClassificationChangeScope `json:"change_scope"`
	Output        *ClassificationOutput     `json:"output,omitempty"`
	Clarification string                    `json:"clarification,omitempty"`
}

// ClassificationContentFeedback is returned to the Server after a typed
// content-scope decision. The Server then sends the original author message
// through the existing Authoring Agent; it is not a Roadmap write.
type ClassificationContentFeedback struct {
	SessionID string
	UserID    string
	Content   string
}

func (r ClassificationAdjustment) Validate() error {
	if strings.TrimSpace(r.RunID) == "" {
		return errors.New("classification adjustment requires an agent run")
	}
	switch r.ChangeScope {
	case ClassificationChangeClassification:
		if r.Output == nil || r.Output.Result != ClassificationProposed || r.Output.Validate() != nil || strings.TrimSpace(r.Clarification) != "" {
			return errors.New("classification adjustment requires one proposed output")
		}
	case ClassificationChangeContent:
		if r.Output != nil || strings.TrimSpace(r.Clarification) != "" {
			return errors.New("content adjustment must not mutate classification")
		}
	case ClassificationChangeClarify:
		if r.Output != nil || strings.TrimSpace(r.Clarification) == "" {
			return errors.New("classification clarification requires a message")
		}
	default:
		return errors.New("classification adjustment change scope is invalid")
	}
	return nil
}

type BuildResult struct {
	Output  BuildOutput `json:"output"`
	Archive []byte      `json:"archive,omitempty"`
}

type ArtifactPublishResult struct {
	Artifact ArtifactReference `json:"artifact"`
}

type VerificationEnvironmentResult struct {
	Environment VerificationEnvironment `json:"environment"`
}

type VerificationResult struct {
	Report VerificationReport `json:"report"`
}

type ChallengePublishResult struct {
	Artifact ArtifactReference `json:"artifact"`
}

type InfrastructureFailureResult struct {
	Failure Failure `json:"failure"`
}

type ArtifactFailureResult struct {
	Failure Failure             `json:"failure"`
	Report  *VerificationReport `json:"report,omitempty"`
}
