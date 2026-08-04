// Package generation owns the durable lifecycle of one authored challenge.
// A workflow is the scheduling authority; candidate revisions only retain
// immutable content and the outputs produced for that content.
package generation

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ExecutionDeadline     = time.Hour
	MaxStateAttempts      = 10
	MaxCandidateRevisions = 10
)

var (
	ErrWorkflowNotFound           = errors.New("generation workflow not found")
	ErrLeaseLost                  = errors.New("generation workflow lease was lost")
	ErrClassificationConflict     = errors.New("classification proposal conflicts with the current roadmap")
	ErrChallengeSourceRefConflict = errors.New("challenge source reference conflicts with the current roadmap")
)

type WorkflowState string

const (
	StateGenerating                WorkflowState = "Generating"
	StateJudging                   WorkflowState = "Judging"
	StateBuilding                  WorkflowState = "Building"
	StateArtifactPublishing        WorkflowState = "ArtifactPublishing"
	StateVerifying                 WorkflowState = "Verifying"
	StateNeedsAuthorReview         WorkflowState = "NeedsAuthorReview"
	StateClassifying               WorkflowState = "Classifying"
	StateNeedsClassificationReview WorkflowState = "NeedsClassificationReview"
	StateChallengePublishing       WorkflowState = "ChallengePublishing"
	StatePublished                 WorkflowState = "Published"
	StateFailed                    WorkflowState = "Failed"
	StateCancelled                 WorkflowState = "Cancelled"
	StateSuperseded                WorkflowState = "Superseded"
)

// StartConfirmation is the explicit, idempotent confirmation of one author
// plan revision. The workflow does not exist before this request.
type StartConfirmation struct {
	PlanRevision   int64  `json:"plan_revision"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (c StartConfirmation) Valid() bool {
	return c.PlanRevision >= 0 && validIdempotencyKey(c.IdempotencyKey)
}

// ContentConfirmation freezes one verified CandidateRevision and starts the
// independent classification lifecycle.
type ContentConfirmation struct {
	WorkflowID          string `json:"workflow_id"`
	CandidateRevisionID string `json:"candidate_revision_id"`
	IdempotencyKey      string `json:"idempotency_key"`
}

func (c ContentConfirmation) Valid() bool {
	return strings.TrimSpace(c.WorkflowID) != "" && strings.TrimSpace(c.CandidateRevisionID) != "" && validIdempotencyKey(c.IdempotencyKey)
}

// ClassificationAdjustmentConfirmation requests one new private Classifying
// run for the reviewed proposal. The proposal revision is the optimistic
// concurrency fence; the author message itself is classified by the Agent.
type ClassificationAdjustmentConfirmation struct {
	WorkflowID          string `json:"workflow_id"`
	CandidateRevisionID string `json:"candidate_revision_id"`
	ProposalRevision    int    `json:"proposal_revision"`
	Feedback            string `json:"feedback"`
	IdempotencyKey      string `json:"idempotency_key"`
}

func (c ClassificationAdjustmentConfirmation) Valid() bool {
	return strings.TrimSpace(c.WorkflowID) != "" && strings.TrimSpace(c.CandidateRevisionID) != "" &&
		c.ProposalRevision > 0 && strings.TrimSpace(c.Feedback) != "" && validIdempotencyKey(c.IdempotencyKey)
}

// PublicationConfirmation makes a reviewed private classification proposal
// public. The proposal revision is the optimistic-concurrency fence.
type PublicationConfirmation struct {
	WorkflowID          string `json:"workflow_id"`
	CandidateRevisionID string `json:"candidate_revision_id"`
	ProposalRevision    int    `json:"proposal_revision"`
	IdempotencyKey      string `json:"idempotency_key"`
}

func (c PublicationConfirmation) Valid() bool {
	return strings.TrimSpace(c.WorkflowID) != "" && strings.TrimSpace(c.CandidateRevisionID) != "" && c.ProposalRevision > 0 && validIdempotencyKey(c.IdempotencyKey)
}

func validIdempotencyKey(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 200
}

func (s WorkflowState) Valid() bool {
	switch s {
	case StateGenerating, StateJudging, StateBuilding, StateArtifactPublishing,
		StateVerifying, StateNeedsAuthorReview, StateClassifying, StateNeedsClassificationReview,
		StateChallengePublishing, StatePublished, StateFailed, StateCancelled, StateSuperseded:
		return true
	default:
		return false
	}
}

func (s WorkflowState) Terminal() bool {
	return s == StatePublished || s == StateFailed || s == StateCancelled || s == StateSuperseded
}

func (s WorkflowState) Leaseable() bool {
	switch s {
	case StateGenerating, StateJudging, StateBuilding, StateArtifactPublishing,
		StateVerifying, StateClassifying, StateChallengePublishing:
		return true
	default:
		return false
	}
}

func (s WorkflowState) DeadlineActive() bool {
	return s.Leaseable()
}

func (s WorkflowState) Review() bool {
	return s == StateNeedsAuthorReview || s == StateNeedsClassificationReview
}

type FailureClass string

const (
	FailureArtifact       FailureClass = "artifact"
	FailureInfrastructure FailureClass = "infrastructure"
	FailureCancelled      FailureClass = "cancelled"
)

type Failure struct {
	Class   FailureClass `json:"class"`
	Code    string       `json:"code"`
	Summary string       `json:"summary"`
}

func (f Failure) Validate() error {
	if (f.Class != FailureArtifact && f.Class != FailureInfrastructure && f.Class != FailureCancelled) || strings.TrimSpace(f.Code) == "" || strings.TrimSpace(f.Summary) == "" {
		return errors.New("workflow failure requires class, code, and summary")
	}
	return nil
}

// Workflow is the sole durable state machine for candidate generation and
// publication. StateAttempt counts continuous technical failures of State;
// LeaseOwner is randomized for every claim and fences late worker reports.
type Workflow struct {
	ID                            string        `json:"id"`
	Source                        Source        `json:"source"`
	SourceRevision                string        `json:"source_revision"`
	State                         WorkflowState `json:"state"`
	ClassificationRoadmapRevision string        `json:"classification_roadmap_revision,omitempty"`
	// ClassificationFeedback is the one pending author message for a resumed
	// Classifying run. It is private workflow input, never Roadmap content.
	ClassificationFeedback string     `json:"classification_feedback,omitempty"`
	SupersededByWorkflowID string     `json:"superseded_by_workflow_id,omitempty"`
	CandidateRevisionID    string     `json:"candidate_revision_id,omitempty"`
	ActiveAgentRunID       string     `json:"active_agent_run_id,omitempty"`
	StateAttempt           int        `json:"state_attempt"`
	LeaseOwner             string     `json:"-"`
	LeaseExpiresAt         *time.Time `json:"lease_expires_at,omitempty"`
	NextRunAt              time.Time  `json:"next_run_at"`
	DeadlineAt             *time.Time `json:"deadline_at,omitempty"`
	DeadlinePausedAt       *time.Time `json:"deadline_paused_at,omitempty"`
	LastError              string     `json:"last_error,omitempty"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

func (w Workflow) Valid() bool {
	if strings.TrimSpace(w.ID) == "" || !w.Source.Valid() || strings.TrimSpace(w.SourceRevision) == "" || !w.State.Valid() || w.StateAttempt < 0 {
		return false
	}
	return true
}

type LeaseCredential struct {
	StateAttempt int    `json:"state_attempt"`
	LeaseOwner   string `json:"lease_owner"`
}

func (c LeaseCredential) Valid() bool {
	return c.StateAttempt >= 0 && strings.TrimSpace(c.LeaseOwner) != ""
}

type Claim struct {
	Workflow Workflow `json:"workflow"`
	LeaseCredential
}

func (c Claim) Valid() bool {
	return c.Workflow.Valid() && c.LeaseCredential.Valid()
}

// Execution is the complete leased view used by a Generate Worker. The
// workflow remains the scheduling authority; Context only carries immutable
// authoring input and recorded candidate outputs for its current state.
type Execution struct {
	Claim   Claim   `json:"claim"`
	Context Context `json:"context"`
}

func (e Execution) Valid() bool {
	return e.Claim.Valid() && e.Context.Workflow.ID == e.Claim.Workflow.ID &&
		e.Context.Workflow.State == e.Claim.Workflow.State
}

func NewID(prefix string) string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(value[:])
}

func NextRetry(attempt int, now time.Time) time.Time {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second * time.Duration(1<<min(attempt-1, 6))
	if delay > time.Minute {
		delay = time.Minute
	}
	return now.UTC().Add(delay)
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
