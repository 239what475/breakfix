package generation

import (
	"errors"
	"strings"

	"github.com/breakfix/breakfix/internal/domain/agent"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

type StartAgentRunRequest struct {
	domain.LeaseCredential
	ExpectedState domain.WorkflowState `json:"expected_state"`
	Purpose       string `json:"purpose"`
	Model         string `json:"model"`
	PromptVersion string `json:"prompt_version"`
}

func (r StartAgentRunRequest) Validate(workflowID string) error {
	if !r.Valid() || !r.ExpectedState.Valid() || strings.TrimSpace(r.Purpose) == "" ||
		strings.TrimSpace(r.Model) == "" || strings.TrimSpace(r.PromptVersion) == "" || strings.TrimSpace(workflowID) == "" {
		return errors.New("generation agent run request is incomplete")
	}
	if r.Purpose == "generator" && r.ExpectedState != domain.StateGenerating {
		return errors.New("generator run requires Generating state")
	}
	if r.Purpose == "judge" && r.ExpectedState != domain.StateJudging {
		return errors.New("judge run requires Judging state")
	}
	if r.Purpose != "generator" && r.Purpose != "judge" {
		return errors.New("unknown generation agent purpose")
	}
	return nil
}

type StartAgentRunResponse struct {
	Run agent.Run `json:"run"`
}

// PhaseRequest is a closed, typed union. Exactly one result is legal for the
// expected state, so workers cannot route arbitrary JSON through the Server.
type PhaseRequest struct {
	domain.LeaseCredential
	ExpectedState domain.WorkflowState `json:"expected_state"`

	GeneratedCandidate      *domain.GeneratedCandidate               `json:"generated_candidate,omitempty"`
	Judgement               *domain.Judgement                        `json:"judgement,omitempty"`
	Build                   *domain.BuildResult                      `json:"build,omitempty"`
	ArtifactPublish         *domain.ArtifactPublishResult            `json:"artifact_publish,omitempty"`
	VerificationEnvironment *domain.VerificationEnvironmentResult    `json:"verification_environment,omitempty"`
	Verification            *domain.VerificationResult               `json:"verification,omitempty"`
	ChallengePublish        *domain.ChallengePublishResult           `json:"challenge_publish,omitempty"`
	Cleanup                 *domain.CleanupResult                    `json:"cleanup,omitempty"`
	InfrastructureFailure   *domain.InfrastructureFailureResult      `json:"infrastructure_failure,omitempty"`
	ArtifactFailure         *domain.ArtifactFailureResult            `json:"artifact_failure,omitempty"`
}

func (r PhaseRequest) ResultCount() int {
	count := 0
	if r.GeneratedCandidate != nil {
		count++
	}
	if r.Judgement != nil {
		count++
	}
	if r.Build != nil {
		count++
	}
	if r.ArtifactPublish != nil {
		count++
	}
	if r.VerificationEnvironment != nil {
		count++
	}
	if r.Verification != nil {
		count++
	}
	if r.ChallengePublish != nil {
		count++
	}
	if r.Cleanup != nil {
		count++
	}
	if r.InfrastructureFailure != nil {
		count++
	}
	if r.ArtifactFailure != nil {
		count++
	}
	return count
}

func (r PhaseRequest) Validate() error {
	if !r.Valid() || !r.ExpectedState.Valid() || r.ResultCount() != 1 {
		return errors.New("generation phase request must contain one valid result")
	}
	if r.InfrastructureFailure != nil {
		if r.InfrastructureFailure.Failure.Class != domain.FailureInfrastructure {
			return errors.New("generation infrastructure result has wrong failure class")
		}
		return r.InfrastructureFailure.Failure.Validate()
	}
	if r.ArtifactFailure != nil {
		if r.ArtifactFailure.Failure.Class != domain.FailureArtifact {
			return errors.New("generation artifact result has wrong failure class")
		}
		return r.ArtifactFailure.Failure.Validate()
	}
	switch r.ExpectedState {
	case domain.StateGenerating:
		if r.GeneratedCandidate == nil || strings.TrimSpace(r.GeneratedCandidate.RunID) == "" || len(r.GeneratedCandidate.Archive) == 0 {
			return errors.New("generating phase requires a generated candidate")
		}
	case domain.StateJudging:
		if r.Judgement == nil || strings.TrimSpace(r.Judgement.RunID) == "" || (!r.Judgement.Approved && strings.TrimSpace(r.Judgement.Feedback) == "") || (r.Judgement.Approved && r.Judgement.Feedback != "") {
			return errors.New("judging phase requires a valid judgement")
		}
	case domain.StateBuilding:
		if r.Build == nil {
			return errors.New("building phase requires build output")
		}
	case domain.StateArtifactPublishing:
		if r.ArtifactPublish == nil {
			return errors.New("ArtifactPublishing phase requires an artifact")
		}
	case domain.StateVerifying:
		if r.VerificationEnvironment == nil && r.Verification == nil {
			return errors.New("verifying phase requires environment or report")
		}
	case domain.StateChallengePublishing:
		if r.ChallengePublish == nil {
			return errors.New("ChallengePublishing phase requires a final artifact")
		}
	case domain.StateCleaningUp:
		if r.Cleanup == nil {
			return errors.New("CleaningUp phase requires cleanup completion")
		}
	default:
		return errors.New("generation state cannot receive a phase result")
	}
	return nil
}
