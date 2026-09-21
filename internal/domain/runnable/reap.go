package runnable

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrReapNotFound   = errors.New("runnable reap request not found")
	ErrReapLeaseLost  = errors.New("runnable reap lease lost")
	ErrResourceAbsent = errors.New("runnable resource absent")
)

type ReapState string

const (
	ReapQueued    ReapState = "queued"
	ReapClaimed   ReapState = "claimed"
	ReapSucceeded ReapState = "succeeded"
)

// EnvironmentPurpose controls the public lifecycle use of an environment.
// It is intentionally a runtime concern: it carries no content semantics.
type EnvironmentPurpose string

const (
	PurposeLearning     EnvironmentPurpose = "learning"
	PurposeVerification EnvironmentPurpose = "verification"
)

func (p EnvironmentPurpose) Valid() bool {
	return p == PurposeLearning || p == PurposeVerification
}

// EnvironmentBinding names one concrete provider resource set. It contains a
// complete immutable runnable revision so lifecycle consumers never receive a
// mutable artifact reference from an Environment object. A blank environment
// carries no revision: BlankRuntime supplies the installed runtime definition
// instead and RunnableRevision stays zero-valued.
type EnvironmentBinding struct {
	Namespace        string             `json:"namespace"`
	Name             string             `json:"name"`
	UID              string             `json:"uid"`
	Purpose          EnvironmentPurpose `json:"purpose"`
	RunnableRevision RunnableRevision   `json:"runnable_revision"`
	BlankRuntime     *BlankRuntimePlan  `json:"blank_runtime,omitempty"`
}

// Digest is the revision fence for one binding: the runnable revision digest
// for content-bound environments, the blank plan digest for blank ones.
func (b EnvironmentBinding) Digest() (string, error) {
	if b.BlankRuntime != nil {
		return b.BlankRuntime.Digest()
	}
	return b.RunnableRevision.Digest()
}

// Lifecycle is the frozen policy of whichever plan arm the binding carries.
// A blank binding has a zero runnable revision; reading its policy directly
// would yield zero timeouts and expire every operation immediately.
func (b EnvironmentBinding) Lifecycle() LifecyclePolicy {
	if b.BlankRuntime != nil {
		return b.BlankRuntime.Lifecycle
	}
	return b.RunnableRevision.Spec.LifecyclePolicy
}

// ReapRequest is immutable once enqueued. UID and revision digest fence the
// request to one concrete provider resource set, even when a name is reused.
type ReapRequest struct {
	Namespace string             `json:"namespace"`
	Name      string             `json:"name"`
	UID       string             `json:"uid"`
	Revision  string             `json:"runnable_revision_digest"`
	Binding   EnvironmentBinding `json:"binding"`
}

func (r ReapRequest) Key() string {
	return r.Namespace + "/" + r.Name + "/" + r.UID
}

func (r ReapRequest) Valid() error {
	if strings.TrimSpace(r.Namespace) == "" || strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.UID) == "" || !ValidDigest(r.Revision) {
		return errors.New("runnable reap request identity is incomplete")
	}
	if r.Namespace != r.Binding.Namespace || r.Name != r.Binding.Name || r.UID != r.Binding.UID || !r.Binding.Purpose.Valid() {
		return errors.New("runnable reap request binding does not match identity")
	}
	digest, err := r.Binding.Digest()
	if err != nil {
		return fmt.Errorf("runnable reap revision: %w", err)
	}
	if r.Revision != digest {
		return errors.New("runnable reap request revision does not match binding")
	}
	return nil
}

// ReapRecord is internal cleanup state. It is deliberately separate from
// verification reports and any content publication state.
type ReapRecord struct {
	Request       ReapRequest
	State         ReapState
	Attempt       int64
	LeaseOwner    string
	LeaseExpires  time.Time
	NextAttemptAt time.Time
	LastError     string
	CompletedAt   *time.Time
}

type ReapClaim struct {
	Record ReapRecord
}

// ReapQueue is the durable handoff between an Environment controller and a
// cleanup worker. Completion must fence the exact claimed attempt and lease.
type ReapQueue interface {
	Enqueue(context.Context, ReapRequest) error
	Get(context.Context, string) (ReapRecord, error)
	Claim(context.Context, string, time.Duration, time.Time) (*ReapClaim, error)
	Complete(context.Context, ReapClaim, bool, string, time.Time, time.Time) error
}

// InMemoryReapQueue is only suitable for tests and local development.
// Production code must use a durable ReapQueue implementation.
type InMemoryReapQueue struct {
	mu      sync.Mutex
	records map[string]ReapRecord
}

func NewInMemoryReapQueue() *InMemoryReapQueue {
	return &InMemoryReapQueue{records: make(map[string]ReapRecord)}
}

func (q *InMemoryReapQueue) Enqueue(_ context.Context, request ReapRequest) error {
	if err := request.Valid(); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	key := request.Key()
	if existing, found := q.records[key]; found {
		if existing.Request.Revision != request.Revision {
			return errors.New("runnable reap request fence differs from existing request")
		}
		return nil
	}
	q.records[key] = ReapRecord{Request: request, State: ReapQueued}
	return nil
}

func (q *InMemoryReapQueue) Get(_ context.Context, key string) (ReapRecord, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	record, found := q.records[key]
	if !found {
		return ReapRecord{}, ErrReapNotFound
	}
	return record, nil
}

func (q *InMemoryReapQueue) Claim(_ context.Context, owner string, ttl time.Duration, now time.Time) (*ReapClaim, error) {
	if strings.TrimSpace(owner) == "" || ttl <= 0 || now.IsZero() {
		return nil, errors.New("runnable reap claim is invalid")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for key, record := range q.records {
		eligible := record.State == ReapQueued && !record.NextAttemptAt.After(now)
		if record.State == ReapClaimed && !record.LeaseExpires.After(now) {
			eligible = true
		}
		if !eligible {
			continue
		}
		record.State = ReapClaimed
		record.Attempt++
		record.LeaseOwner = owner
		record.LeaseExpires = now.Add(ttl)
		q.records[key] = record
		return &ReapClaim{Record: record}, nil
	}
	return nil, nil
}

func (q *InMemoryReapQueue) Complete(_ context.Context, claim ReapClaim, succeeded bool, diagnostic string, now, retryAt time.Time) error {
	if now.IsZero() {
		return errors.New("runnable reap completion time is required")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	key := claim.Record.Request.Key()
	record, found := q.records[key]
	if !found {
		return ErrReapNotFound
	}
	if record.State != ReapClaimed || !now.Before(record.LeaseExpires) || record.Attempt != claim.Record.Attempt || record.LeaseOwner != claim.Record.LeaseOwner || record.Request.Revision != claim.Record.Request.Revision || !record.LeaseExpires.Equal(claim.Record.LeaseExpires) {
		return ErrReapLeaseLost
	}
	if succeeded {
		record.State = ReapSucceeded
		record.LastError = ""
		completed := now.UTC()
		record.CompletedAt = &completed
	} else {
		record.State = ReapQueued
		record.LeaseOwner = ""
		record.LeaseExpires = time.Time{}
		record.LastError = boundedReapDiagnostic(diagnostic)
		record.NextAttemptAt = retryAt.UTC()
	}
	q.records[key] = record
	return nil
}

func boundedReapDiagnostic(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > MaxSummaryLength {
		return value[:MaxSummaryLength]
	}
	return value
}
