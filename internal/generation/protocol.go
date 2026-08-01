package generation

import (
	"errors"
	"strings"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/candidate"
)

// ArtifactError marks a deterministic defect in candidate content or its
// generated verification result. Generate Worker reports it as an artifact
// failure, which returns the workflow to the same generator session. A
// verifier can attach its complete structured report for the next repair turn.
type ArtifactError struct {
	Failure Failure
	Report  *candidate.VerificationReport
}

func (e *ArtifactError) Error() string { return e.Failure.Summary }

func NewArtifactError(code, summary string) error {
	return &ArtifactError{Failure: Failure{Class: FailureArtifact, Code: code, Summary: summary}}
}

func NewArtifactErrorWithReport(code, summary string, report candidate.VerificationReport) error {
	return &ArtifactError{
		Failure: Failure{Class: FailureArtifact, Code: code, Summary: summary},
		Report:  &report,
	}
}

// Feedback is the trusted, compact repair context passed to the generator.
// It never contains raw sandbox output or untrusted candidate instructions.
type Feedback struct {
	Summary string  `json:"summary"`
	Issues  []Issue `json:"issues"`
}

type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (f Feedback) Empty() bool {
	return strings.TrimSpace(f.Summary) == "" && len(f.Issues) == 0
}

func (f Feedback) Validate() error {
	if f.Empty() {
		return nil
	}
	if strings.TrimSpace(f.Summary) == "" || len(f.Issues) == 0 {
		return errors.New("generation feedback requires summary and issues")
	}
	for _, issue := range f.Issues {
		if strings.TrimSpace(issue.Code) == "" || strings.TrimSpace(issue.Message) == "" {
			return errors.New("generation feedback issue requires code and message")
		}
	}
	return nil
}

// Context is the complete Server-owned input for the current workflow state.
// Candidate bytes are intentionally absent and must be fetched through the
// corresponding lease-fenced artifact endpoint.
type Context struct {
	Workflow         Workflow              `json:"workflow"`
	Plan             authoring.Plan        `json:"plan"`
	Candidate        *candidate.WorkerView `json:"candidate,omitempty"`
	Feedback         Feedback              `json:"feedback"`
	GeneratorSession string                `json:"generator_session"`
}

type StartAgentRunRequest struct {
	LeaseCredential
	ExpectedState State  `json:"expected_state"`
	Purpose       string `json:"purpose"`
	Model         string `json:"model"`
	PromptVersion string `json:"prompt_version"`
}

func (r StartAgentRunRequest) Validate(workflowID string) error {
	if !r.Valid() || !r.ExpectedState.Valid() || strings.TrimSpace(r.Purpose) == "" ||
		strings.TrimSpace(r.Model) == "" || strings.TrimSpace(r.PromptVersion) == "" || strings.TrimSpace(workflowID) == "" {
		return errors.New("generation agent run request is incomplete")
	}
	if r.Purpose == "generator" && r.ExpectedState != StateGenerating {
		return errors.New("generator run requires Generating state")
	}
	if r.Purpose == "judge" && r.ExpectedState != StateJudging {
		return errors.New("judge run requires Judging state")
	}
	if r.Purpose != "generator" && r.Purpose != "judge" {
		return errors.New("unknown generation agent purpose")
	}
	return nil
}

type StartAgentRunResponse struct {
	Run agentruntime.Run `json:"run"`
}

type GeneratedCandidate struct {
	RunID         string `json:"run_id"`
	Archive       []byte `json:"archive"`
	ArchiveSHA256 string `json:"archive_sha256,omitempty"`
}

type Judgement struct {
	RunID    string `json:"run_id"`
	Approved bool   `json:"approved"`
	Feedback string `json:"feedback,omitempty"`
}

type BuildResult struct {
	Output  candidate.BuildOutput `json:"output"`
	Archive []byte                `json:"archive,omitempty"`
}

type ArtifactPublishResult struct {
	Artifact candidate.ArtifactReference `json:"artifact"`
}

type VerificationEnvironmentResult struct {
	Environment candidate.VerificationEnvironment `json:"environment"`
}

type VerificationResult struct {
	Report candidate.VerificationReport `json:"report"`
}

type ChallengePublishResult struct {
	Artifact candidate.ArtifactReference `json:"artifact"`
}

type InfrastructureFailureResult struct {
	Failure Failure `json:"failure"`
}

type ArtifactFailureResult struct {
	Failure Failure                       `json:"failure"`
	Report  *candidate.VerificationReport `json:"report,omitempty"`
}

// PhaseRequest is a closed, typed union. Exactly one result is legal for the
// expected state, so workers cannot route arbitrary JSON through the Server.
type PhaseRequest struct {
	LeaseCredential
	ExpectedState State `json:"expected_state"`

	GeneratedCandidate      *GeneratedCandidateResult      `json:"generated_candidate,omitempty"`
	Judgement               *Judgement                     `json:"judgement,omitempty"`
	Build                   *BuildResult                   `json:"build,omitempty"`
	ArtifactPublish         *ArtifactPublishResult         `json:"artifact_publish,omitempty"`
	VerificationEnvironment *VerificationEnvironmentResult `json:"verification_environment,omitempty"`
	Verification            *VerificationResult            `json:"verification,omitempty"`
	ChallengePublish        *ChallengePublishResult        `json:"challenge_publish,omitempty"`
	Cleanup                 *CleanupResult                 `json:"cleanup,omitempty"`
	InfrastructureFailure   *InfrastructureFailureResult   `json:"infrastructure_failure,omitempty"`
	ArtifactFailure         *ArtifactFailureResult         `json:"artifact_failure,omitempty"`
}

type GeneratedCandidateResult struct {
	RunID   string `json:"run_id"`
	Archive []byte `json:"archive"`
}

type CleanupResult struct{}

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
		if r.InfrastructureFailure.Failure.Class != FailureInfrastructure {
			return errors.New("generation infrastructure result has wrong failure class")
		}
		return r.InfrastructureFailure.Failure.Validate()
	}
	if r.ArtifactFailure != nil {
		if r.ArtifactFailure.Failure.Class != FailureArtifact {
			return errors.New("generation artifact result has wrong failure class")
		}
		return r.ArtifactFailure.Failure.Validate()
	}
	switch r.ExpectedState {
	case StateGenerating:
		if r.GeneratedCandidate == nil || strings.TrimSpace(r.GeneratedCandidate.RunID) == "" || len(r.GeneratedCandidate.Archive) == 0 {
			return errors.New("generating phase requires a generated candidate")
		}
	case StateJudging:
		if r.Judgement == nil || strings.TrimSpace(r.Judgement.RunID) == "" || (!r.Judgement.Approved && strings.TrimSpace(r.Judgement.Feedback) == "") || (r.Judgement.Approved && r.Judgement.Feedback != "") {
			return errors.New("judging phase requires a valid judgement")
		}
	case StateBuilding:
		if r.Build == nil {
			return errors.New("building phase requires build output")
		}
	case StateArtifactPublishing:
		if r.ArtifactPublish == nil {
			return errors.New("ArtifactPublishing phase requires an artifact")
		}
	case StateVerifying:
		if r.VerificationEnvironment == nil && r.Verification == nil {
			return errors.New("verifying phase requires environment or report")
		}
	case StateChallengePublishing:
		if r.ChallengePublish == nil {
			return errors.New("ChallengePublishing phase requires a final artifact")
		}
	case StateCleaningUp:
		if r.Cleanup == nil {
			return errors.New("CleaningUp phase requires cleanup completion")
		}
	default:
		return errors.New("generation state cannot receive a phase result")
	}
	return nil
}
