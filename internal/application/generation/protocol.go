package generation

import (
	"errors"

	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

// PhaseRequest is a closed, typed union. Exactly one result is legal for the
// expected state, so workers cannot route arbitrary JSON through the Server.
type PhaseRequest struct {
	domain.LeaseCredential
	ExpectedState domain.WorkflowState `json:"expected_state"`

	Build                   *domain.BuildResult                   `json:"build,omitempty"`
	ArtifactPublish         *domain.ArtifactPublishResult         `json:"artifact_publish,omitempty"`
	VerificationEnvironment *domain.VerificationEnvironmentResult `json:"verification_environment,omitempty"`
	Verification            *domain.VerificationResult            `json:"verification,omitempty"`
	ChallengePublish        *domain.ChallengePublishResult        `json:"challenge_publish,omitempty"`
	InfrastructureFailure   *domain.InfrastructureFailureResult   `json:"infrastructure_failure,omitempty"`
	ArtifactFailure         *domain.ArtifactFailureResult         `json:"artifact_failure,omitempty"`
}

func (r PhaseRequest) ResultCount() int {
	count := 0
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
	default:
		return errors.New("generation state cannot receive a phase result")
	}
	return nil
}
