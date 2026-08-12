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

	"github.com/breakfix/breakfix/internal/domain/publication"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
)

const (
	MaxRuntimeAttempts = runtime.MaxAttempts
)

var (
	ErrWorkflowNotFound           = errors.New("generation workflow not found")
	ErrLeaseLost                  = runtime.ErrLeaseLost
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

// ContentChangeRequest returns one reviewed candidate to Generating. The
// request keeps the existing workspace and records the author's concrete
// change request as the next Generator turn's feedback.
type ContentChangeRequest struct {
	WorkflowID          string `json:"workflow_id"`
	CandidateRevisionID string `json:"candidate_revision_id"`
	Feedback            string `json:"feedback"`
	IdempotencyKey      string `json:"idempotency_key"`
}

func (c ContentChangeRequest) Valid() bool {
	return strings.TrimSpace(c.WorkflowID) != "" && strings.TrimSpace(c.CandidateRevisionID) != "" &&
		strings.TrimSpace(c.Feedback) != "" && validIdempotencyKey(c.IdempotencyKey)
}

// Cancellation is the explicit, idempotent request to stop one unfinished
// workflow. Cleanup remains asynchronous; this request only changes the
// durable workflow authority.
type Cancellation struct {
	WorkflowID     string `json:"workflow_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (c Cancellation) Valid() bool {
	return strings.TrimSpace(c.WorkflowID) != "" && validIdempotencyKey(c.IdempotencyKey)
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
		StateChallengePublishing, StatePublished, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

func (s WorkflowState) Terminal() bool {
	return s == StatePublished || s == StateFailed || s == StateCancelled
}

// AgentState reports states whose only active executor is the Server-owned
// Agent Runtime. Runtime Worker identities can never claim these states.
func (s WorkflowState) AgentState() bool {
	switch s {
	case StateJudging, StateClassifying:
		return true
	default:
		return false
	}
}

// RuntimeState reports states whose external side effects are performed by
// Runtime Worker. A Runtime state always has one Server-managed attempt.
func (s WorkflowState) RuntimeState() bool {
	switch s {
	case StateBuilding, StateArtifactPublishing, StateVerifying, StateChallengePublishing:
		return true
	default:
		return false
	}
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
// publication. StateVersion changes only when State changes. RuntimeAttempt
// is meaningful only while State is a RuntimeState and is incremented by
// Server after an infrastructure failure or expired Runtime Worker lease.
// LeaseOwner is randomized for every claim and fences late reports.
type Workflow struct {
	ID                            string        `json:"id"`
	Source                        Source        `json:"source"`
	SourceRevision                string        `json:"source_revision"`
	State                         WorkflowState `json:"state"`
	ClassificationRoadmapRevision string        `json:"classification_roadmap_revision,omitempty"`
	// ClassificationFeedback is the one pending author message for a resumed
	// Classifying run. It is private workflow input, never Roadmap content.
	ClassificationFeedback   string               `json:"classification_feedback,omitempty"`
	CandidateRevisionID      string               `json:"candidate_revision_id,omitempty"`
	ActiveAgentRunID         string               `json:"active_agent_run_id,omitempty"`
	StateVersion             int64                `json:"state_version"`
	RuntimeAttempt           int                  `json:"runtime_attempt"`
	LeaseOwner               string               `json:"-"`
	LeaseExpiresAt           *time.Time           `json:"lease_expires_at,omitempty"`
	NextRunAt                time.Time            `json:"next_run_at"`
	LastError                string               `json:"last_error,omitempty"`
	FinalizerErrorCategory   publication.Category `json:"finalizer_error_category,omitempty"`
	FinalizerLastError       string               `json:"finalizer_last_error,omitempty"`
	FinalizerLastAttemptedAt *time.Time           `json:"finalizer_last_attempted_at,omitempty"`
	FinalizerNextRetryAt     *time.Time           `json:"finalizer_next_retry_at,omitempty"`
	CreatedAt                time.Time            `json:"created_at"`
	UpdatedAt                time.Time            `json:"updated_at"`
}

func (w Workflow) Valid() bool {
	if strings.TrimSpace(w.ID) == "" || !w.Source.Valid() || strings.TrimSpace(w.SourceRevision) == "" || !w.State.Valid() ||
		w.StateVersion < 1 || w.RuntimeAttempt < 0 || w.RuntimeAttempt > MaxRuntimeAttempts {
		return false
	}
	if w.State.RuntimeState() {
		return w.RuntimeAttempt >= 1
	}
	if w.RuntimeAttempt != 0 {
		return false
	}
	if w.FinalizerErrorCategory == publication.CategoryUnknown {
		return w.FinalizerLastError == "" && w.FinalizerLastAttemptedAt == nil && w.FinalizerNextRetryAt == nil
	}
	diagnostic := publication.Diagnostic{
		Category:        w.FinalizerErrorCategory,
		LastError:       w.FinalizerLastError,
		LastAttemptedAt: valueOrZero(w.FinalizerLastAttemptedAt),
		NextRetryAt:     w.FinalizerNextRetryAt,
	}
	return diagnostic.Validate() == nil
}

func valueOrZero(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

type LeaseCredential struct {
	StateVersion int64  `json:"state_version"`
	LeaseOwner   string `json:"lease_owner"`
}

func (c LeaseCredential) Valid() bool {
	return c.StateVersion >= 1 && strings.TrimSpace(c.LeaseOwner) != ""
}

type Claim struct {
	Workflow Workflow `json:"workflow"`
	LeaseCredential
}

func (c Claim) Valid() bool {
	return c.Workflow.Valid() && c.LeaseCredential.Valid() && c.StateVersion == c.Workflow.StateVersion
}

// Execution is the complete leased view consumed by one fixed state owner.
// The workflow remains the scheduling authority; Context only carries
// immutable authoring input and recorded candidate outputs for its current
// state.
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
