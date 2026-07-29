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
	ID             string
	Kind           Kind
	SubjectType    SubjectType
	SubjectID      string
	State          State
	Attempt        int
	LeaseOwner     string
	LeaseExpiresAt *time.Time
	NextRunAt      time.Time
	DeadlineAt     *time.Time
	ErrorCode      string
	ErrorSummary   string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type CreateItem struct {
	ID          string
	Kind        Kind
	SubjectType SubjectType
	SubjectID   string
	NextRunAt   time.Time
	DeadlineAt  *time.Time
}

type Claim struct {
	Item       Item
	LeaseOwner string
}

func (c Claim) Valid() bool {
	return strings.TrimSpace(c.Item.ID) != "" && c.Item.Attempt > 0 && strings.TrimSpace(c.LeaseOwner) != ""
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
	if i.Kind != KindArtifactCleanup && (i.DeadlineAt == nil || i.DeadlineAt.IsZero()) {
		return errors.New("work item deadline is required")
	}
	if i.Kind == KindArtifactCleanup && i.DeadlineAt != nil {
		return errors.New("artifact cleanup work item must not have a deadline")
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
