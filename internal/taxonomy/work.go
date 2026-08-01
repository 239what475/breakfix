package taxonomy

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

type WorkflowState string

const (
	WorkflowQueued     WorkflowState = "Queued"
	WorkflowMapping    WorkflowState = "Mapping"
	WorkflowReviewing  WorkflowState = "Reviewing"
	WorkflowPublishing WorkflowState = "Publishing"
	WorkflowCompleted  WorkflowState = "Completed"
	WorkflowFailed     WorkflowState = "Failed"
	WorkflowCancelled  WorkflowState = "Cancelled"
)

func (s WorkflowState) Valid() bool {
	switch s {
	case WorkflowQueued, WorkflowMapping, WorkflowReviewing, WorkflowPublishing, WorkflowCompleted, WorkflowFailed, WorkflowCancelled:
		return true
	default:
		return false
	}
}

func (s WorkflowState) Terminal() bool {
	return s == WorkflowCompleted || s == WorkflowFailed || s == WorkflowCancelled
}

type ReviewDecision string

const (
	ReviewApprove ReviewDecision = "approve"
	ReviewReject  ReviewDecision = "reject"
)

type Review struct {
	Decision ReviewDecision `json:"decision"`
	Feedback string         `json:"feedback,omitempty"`
}

func ValidateReview(value Review) error {
	switch value.Decision {
	case ReviewApprove:
		if strings.TrimSpace(value.Feedback) != "" {
			return errors.New("approved taxonomy review must have empty feedback")
		}
	case ReviewReject:
		if strings.TrimSpace(value.Feedback) == "" {
			return errors.New("rejected taxonomy review requires feedback")
		}
	default:
		return fmt.Errorf("unknown taxonomy review decision %q", value.Decision)
	}
	return nil
}

// Workflow is a complete, lease-protected taxonomy maintenance unit for one
// immutable published challenge revision. It is not a generic task record.
type Workflow struct {
	ID                       string        `json:"id"`
	ChallengeID              string        `json:"challenge_id"`
	ChallengeRevision        string        `json:"challenge_revision"`
	State                    WorkflowState `json:"state"`
	BaseTaxonomyRevision     string        `json:"base_taxonomy_revision"`
	Round                    int           `json:"round"`
	StateAttempt             int           `json:"state_attempt"`
	CandidateChangeSet       *ChangeSet    `json:"candidate_changeset,omitempty"`
	CurriculumReview         *Review       `json:"curriculum_review,omitempty"`
	SREReview                *Review       `json:"sre_review,omitempty"`
	ExpectedSnapshotRevision string        `json:"expected_snapshot_revision,omitempty"`
	PublishedRevision        string        `json:"published_revision,omitempty"`
	LeaseOwner               string        `json:"-"`
	LeaseExpiresAt           *time.Time    `json:"lease_expires_at,omitempty"`
	NextRunAt                time.Time     `json:"next_run_at"`
	LastError                string        `json:"last_error,omitempty"`
	CreatedAt                time.Time     `json:"created_at"`
	UpdatedAt                time.Time     `json:"updated_at"`
}

func (w Workflow) Valid() bool {
	return strings.TrimSpace(w.ID) != "" && strings.TrimSpace(w.ChallengeID) != "" && strings.TrimSpace(w.ChallengeRevision) != "" &&
		w.State.Valid() && w.Round >= 0 && w.StateAttempt >= 0
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

func (c Claim) Valid() bool { return c.Workflow.Valid() && c.LeaseCredential.Valid() }

func NewWorkflowID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("taxonomy-workflow-%d", time.Now().UnixNano())
	}
	return "taxonomy-workflow-" + hex.EncodeToString(raw[:])
}

func RetryAt(attempt int, now time.Time) time.Time {
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
