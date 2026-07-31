// Package worklist defines the single durable scheduling envelope used by all
// Breakfix workers. Domain payloads remain with their owning aggregate.
package worklist

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrNotFound  = errors.New("work item not found")
	ErrLeaseLost = errors.New("work item lease was lost")
)

const (
	maxLeaseRenewalRequestTimeout = 10 * time.Second
	maxClaimRetryDelay            = 30 * time.Second
)

// LeaseRenewalInterval schedules the first renewal after one third of a lease.
func LeaseRenewalInterval(leaseTTL time.Duration) time.Duration {
	if leaseTTL <= 0 {
		return time.Second
	}
	interval := leaseTTL / 3
	if interval <= 0 {
		return time.Nanosecond
	}
	return interval
}

// LeaseRenewalRequestTimeout keeps a stalled Server call within the remaining
// lease budget while capping the timeout for long leases.
func LeaseRenewalRequestTimeout(leaseTTL time.Duration) time.Duration {
	timeout := LeaseRenewalInterval(leaseTTL)
	if timeout > maxLeaseRenewalRequestTimeout {
		return maxLeaseRenewalRequestTimeout
	}
	return timeout
}

// ClaimRetryDelay backs off a Worker after its request to claim work cannot
// reach Server. It intentionally belongs to the common worklist protocol so
// all fixed Worker pools recover from a Server restart at the same rate.
func ClaimRetryDelay(pollEvery time.Duration, failures int) time.Duration {
	if pollEvery <= 0 {
		pollEvery = time.Second
	}
	if pollEvery >= maxClaimRetryDelay {
		return maxClaimRetryDelay
	}
	if failures < 1 {
		failures = 1
	}
	delay := pollEvery
	for retry := 1; retry < failures && delay < maxClaimRetryDelay; retry++ {
		if delay > maxClaimRetryDelay/2 {
			return maxClaimRetryDelay
		}
		delay *= 2
	}
	if delay > maxClaimRetryDelay {
		return maxClaimRetryDelay
	}
	return delay
}

type Kind string

const (
	KindAgent            Kind = "agent"
	KindBuild            Kind = "build"
	KindArtifactPublish  Kind = "artifact_publish"
	KindVerify           Kind = "verify"
	KindArtifactCleanup  Kind = "artifact_cleanup"
	KindChallengePublish Kind = "challenge_publish"
)

type SubjectType string

const (
	SubjectAgentRun          SubjectType = "agent_run"
	SubjectCandidateRevision SubjectType = "candidate_revision"
)

type State string

const (
	StatePending   State = "pending"
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

type Item struct {
	ID             string      `json:"id"`
	Kind           Kind        `json:"kind"`
	SubjectType    SubjectType `json:"subject_type"`
	SubjectID      string      `json:"subject_id"`
	State          State       `json:"state"`
	Attempt        int         `json:"attempt"`
	LeaseOwner     string      `json:"-"`
	LeaseExpiresAt *time.Time  `json:"lease_expires_at,omitempty"`
	NextRunAt      time.Time   `json:"next_run_at"`
	// ExecutionTimeout is fixed when the item is created. DeadlineAt remains
	// unset while queued and is assigned by the first successful claim.
	ExecutionTimeout time.Duration `json:"-"`
	DeadlineAt       *time.Time    `json:"deadline_at,omitempty"`
	ErrorCode        string        `json:"error_code,omitempty"`
	ErrorSummary     string        `json:"error_summary,omitempty"`
	CreatedAt        time.Time     `json:"created_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
}

type InspectionItem struct {
	Item
	LeaseOwner string `json:"lease_owner,omitempty"`
}

type InspectionFilter struct {
	Kind  Kind  `json:"kind,omitempty"`
	State State `json:"state,omitempty"`
	Limit int   `json:"limit,omitempty"`
}

type KindSnapshot struct {
	Kind                    Kind    `json:"kind"`
	Pending                 int64   `json:"pending"`
	Running                 int64   `json:"running"`
	Succeeded               int64   `json:"succeeded"`
	Failed                  int64   `json:"failed"`
	Cancelled               int64   `json:"cancelled"`
	OldestPendingAgeSeconds float64 `json:"oldest_pending_age_seconds"`
	TerminalDurationSeconds float64 `json:"terminal_duration_seconds"`
	TerminalCount           int64   `json:"terminal_count"`
}

type FailureSnapshot struct {
	Kind  Kind   `json:"kind"`
	Class string `json:"class"`
	Count int64  `json:"count"`
}

type Snapshot struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Kinds       []KindSnapshot    `json:"kinds"`
	Failures    []FailureSnapshot `json:"failures"`
}

type CreateItem struct {
	ID               string
	Kind             Kind
	SubjectType      SubjectType
	SubjectID        string
	NextRunAt        time.Time
	ExecutionTimeout time.Duration
}

type Claim struct {
	Item       Item   `json:"item"`
	LeaseOwner string `json:"lease_owner"`
}

func (c Claim) Valid() bool {
	return strings.TrimSpace(c.Item.ID) != "" && c.Item.Attempt > 0 && strings.TrimSpace(c.LeaseOwner) != ""
}

type Credential struct {
	WorkItemID string `json:"work_item_id"`
	Attempt    int    `json:"attempt"`
	LeaseOwner string `json:"lease_owner"`
}

func (c Claim) Credential() Credential {
	return Credential{WorkItemID: c.Item.ID, Attempt: c.Item.Attempt, LeaseOwner: c.LeaseOwner}
}

func (c Credential) Valid() bool {
	return strings.TrimSpace(c.WorkItemID) != "" && c.Attempt > 0 && strings.TrimSpace(c.LeaseOwner) != ""
}

func (i CreateItem) Validate() error {
	if strings.TrimSpace(i.ID) == "" || strings.TrimSpace(i.SubjectID) == "" {
		return errors.New("work item requires id and subject id")
	}
	if !ValidKind(i.Kind) || !ValidSubject(i.Kind, i.SubjectType) {
		return errors.New("work item kind and subject type do not match")
	}
	if i.NextRunAt.IsZero() {
		return errors.New("work item next run time is required")
	}
	if i.Kind == KindArtifactCleanup {
		if i.ExecutionTimeout != 0 {
			return errors.New("artifact cleanup work item must not have an execution timeout")
		}
		return nil
	}
	if i.ExecutionTimeout <= 0 {
		return errors.New("work item execution timeout is required")
	}
	return nil
}

func ValidKind(kind Kind) bool {
	switch kind {
	case KindAgent, KindBuild, KindArtifactPublish, KindVerify, KindArtifactCleanup, KindChallengePublish:
		return true
	default:
		return false
	}
}

func Kinds() []Kind {
	return []Kind{KindAgent, KindBuild, KindArtifactPublish, KindVerify, KindArtifactCleanup, KindChallengePublish}
}

func ValidState(state State) bool {
	switch state {
	case StatePending, StateRunning, StateSucceeded, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

func ValidSubject(kind Kind, subject SubjectType) bool {
	if kind == KindAgent {
		return subject == SubjectAgentRun
	}
	return ValidKind(kind) && subject == SubjectCandidateRevision
}

func Terminal(state State) bool {
	return state == StateSucceeded || state == StateFailed || state == StateCancelled
}

func NewID(prefix string) string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(raw[:])
}
