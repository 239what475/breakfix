package taxonomy

import (
	"errors"
	"strings"

	"github.com/breakfix/breakfix/internal/agentruntime"
)

var (
	ErrNotFound  = errors.New("taxonomy workflow not found")
	ErrLeaseLost = errors.New("taxonomy workflow lease was lost")
)

const (
	MapperPurpose             = "taxonomy-mapper"
	CurriculumReviewerPurpose = "taxonomy-curriculum-reviewer"
	SREReviewerPurpose        = "taxonomy-sre-reviewer"
	MapperPromptVersion       = "taxonomy-mapper-v11"
	ReviewerPromptVersion     = "taxonomy-review-v8"
)

type AgentRole string

const (
	AgentRoleMapper             AgentRole = "mapper"
	AgentRoleCurriculumReviewer AgentRole = "curriculum_reviewer"
	AgentRoleSREReviewer        AgentRole = "sre_reviewer"
)

func (r AgentRole) Valid() bool {
	return r == AgentRoleMapper || r == AgentRoleCurriculumReviewer || r == AgentRoleSREReviewer
}

func (r AgentRole) Purpose() string {
	switch r {
	case AgentRoleMapper:
		return MapperPurpose
	case AgentRoleCurriculumReviewer:
		return CurriculumReviewerPurpose
	case AgentRoleSREReviewer:
		return SREReviewerPurpose
	default:
		return ""
	}
}

func (r AgentRole) PromptVersion() string {
	if r == AgentRoleMapper {
		return MapperPromptVersion
	}
	if r == AgentRoleCurriculumReviewer || r == AgentRoleSREReviewer {
		return ReviewerPromptVersion
	}
	return ""
}

func (r AgentRole) State() WorkflowState {
	if r == AgentRoleMapper {
		return WorkflowMapping
	}
	if r == AgentRoleCurriculumReviewer || r == AgentRoleSREReviewer {
		return WorkflowReviewing
	}
	return ""
}

// ModelInput is reconstructed by Server from immutable challenge and taxonomy
// content. Workers never receive a taxonomy path or direct database access.
type ModelInput struct {
	SystemPrompt string `json:"system_prompt"`
	Prompt       string `json:"prompt"`
}

type Context struct {
	Workflow         Workflow          `json:"workflow"`
	Mapper           ModelInput        `json:"mapper"`
	MapperValidation *MapperValidation `json:"mapper_validation,omitempty"`
	CurriculumReview ModelInput        `json:"curriculum_review"`
	SREReview        ModelInput        `json:"sre_review"`
}

// MapperValidation is Server-derived, model-independent context used to
// reject an invalid ChangeSet before the result is accepted by the Mapper.
// It travels to the Worker over the internal API but is not prompt content.
type MapperValidation struct {
	Challenge ChallengeRef `json:"challenge"`
	Base      Snapshot     `json:"base"`
}

func (c Context) ValidFor(claim Claim) bool {
	if !claim.Valid() || c.Workflow.ID != claim.Workflow.ID || c.Workflow.State != claim.Workflow.State {
		return false
	}
	switch claim.Workflow.State {
	case WorkflowMapping:
		return strings.TrimSpace(c.Mapper.SystemPrompt) != "" && strings.TrimSpace(c.Mapper.Prompt) != "" &&
			c.MapperValidation != nil && c.MapperValidation.Challenge.ID == claim.Workflow.ChallengeID &&
			c.MapperValidation.Challenge.Revision == claim.Workflow.ChallengeRevision
	case WorkflowReviewing:
		return strings.TrimSpace(c.CurriculumReview.SystemPrompt) != "" && strings.TrimSpace(c.CurriculumReview.Prompt) != "" &&
			strings.TrimSpace(c.SREReview.SystemPrompt) != "" && strings.TrimSpace(c.SREReview.Prompt) != ""
	default:
		return true
	}
}

type StartAgentRunRequest struct {
	LeaseCredential
	ExpectedState WorkflowState `json:"expected_state"`
	Role          AgentRole     `json:"role"`
	Model         string        `json:"model"`
}

func (r StartAgentRunRequest) Validate(workflowID string) error {
	if !r.Valid() || !r.ExpectedState.Valid() || !r.Role.Valid() || strings.TrimSpace(r.Model) == "" || strings.TrimSpace(workflowID) == "" {
		return errors.New("taxonomy agent run request is incomplete")
	}
	if r.Role.State() != r.ExpectedState {
		return errors.New("taxonomy agent role does not match workflow state")
	}
	return nil
}

type StartAgentRunResponse struct {
	Run agentruntime.Run `json:"run"`
}

type MapperResult struct {
	RunID     string    `json:"run_id"`
	ChangeSet ChangeSet `json:"changeset"`
}

type ReviewPairResult struct {
	CurriculumRunID string `json:"curriculum_run_id"`
	SRERunID        string `json:"sre_run_id"`
	Curriculum      Review `json:"curriculum"`
	SRE             Review `json:"sre"`
}

type PublicationResult struct{}

// TechnicalFailure reports transport, provider, timeout, or typed-result
// failures. It does not advance a semantic round.
type TechnicalFailure struct {
	Message string   `json:"message"`
	RunIDs  []string `json:"run_ids,omitempty"`
}

type PhaseRequest struct {
	LeaseCredential
	ExpectedState WorkflowState `json:"expected_state"`

	Mapper           *MapperResult      `json:"mapper,omitempty"`
	ReviewPair       *ReviewPairResult  `json:"review_pair,omitempty"`
	Publication      *PublicationResult `json:"publication,omitempty"`
	TechnicalFailure *TechnicalFailure  `json:"technical_failure,omitempty"`
}

func (r PhaseRequest) resultCount() int {
	count := 0
	if r.Mapper != nil {
		count++
	}
	if r.ReviewPair != nil {
		count++
	}
	if r.Publication != nil {
		count++
	}
	if r.TechnicalFailure != nil {
		count++
	}
	return count
}

func (r PhaseRequest) Validate() error {
	if !r.Valid() || !r.ExpectedState.Valid() || r.resultCount() != 1 {
		return errors.New("taxonomy phase request must contain one valid result")
	}
	if r.TechnicalFailure != nil {
		if strings.TrimSpace(r.TechnicalFailure.Message) == "" {
			return errors.New("taxonomy technical failure requires a message")
		}
		for _, id := range r.TechnicalFailure.RunIDs {
			if strings.TrimSpace(id) == "" {
				return errors.New("taxonomy technical failure contains an empty agent run id")
			}
		}
		return nil
	}
	switch r.ExpectedState {
	case WorkflowMapping:
		if r.Mapper == nil || strings.TrimSpace(r.Mapper.RunID) == "" || r.Mapper.ChangeSet.Empty() {
			return errors.New("Mapping phase requires a mapper result")
		}
	case WorkflowReviewing:
		if r.ReviewPair == nil || strings.TrimSpace(r.ReviewPair.CurriculumRunID) == "" || strings.TrimSpace(r.ReviewPair.SRERunID) == "" {
			return errors.New("reviewing phase requires both reviewer runs")
		}
		if err := ValidateReview(r.ReviewPair.Curriculum); err != nil {
			return err
		}
		if err := ValidateReview(r.ReviewPair.SRE); err != nil {
			return err
		}
	case WorkflowPublishing:
		if r.Publication == nil {
			return errors.New("Publishing phase requires publication completion")
		}
	default:
		return errors.New("taxonomy state cannot receive a phase result")
	}
	return nil
}
