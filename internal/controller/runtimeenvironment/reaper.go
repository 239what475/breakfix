package runtimeenvironment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	defaultReaperLeaseTTL = 30 * time.Second
	defaultReaperRetry    = 5 * time.Second
)

var (
	ErrReapNotFound   = errors.New("runtime environment reap request not found")
	ErrReapLeaseLost  = errors.New("runtime environment reap lease lost")
	ErrResourceAbsent = errors.New("runtime environment resource absent")
)

type ReapState string

const (
	ReapQueued    ReapState = "queued"
	ReapClaimed   ReapState = "claimed"
	ReapSucceeded ReapState = "succeeded"
)

// ReapRequest is immutable once enqueued. UID and revision digest fence the
// request to one concrete provider resource set, even when a name is reused.
type ReapRequest struct {
	Namespace string
	Name      string
	UID       string
	Revision  string
	Binding   Binding
}

func (r ReapRequest) Key() string {
	return r.Namespace + "/" + r.Name + "/" + r.UID
}

func (r ReapRequest) Valid() error {
	if strings.TrimSpace(r.Namespace) == "" || strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.UID) == "" || strings.TrimSpace(r.Revision) == "" {
		return errors.New("runtime environment reap request identity is incomplete")
	}
	if r.Namespace != r.Binding.Namespace || r.Name != r.Binding.Name || r.UID != r.Binding.UID {
		return errors.New("runtime environment reap request binding does not match identity")
	}
	digest, err := r.Binding.RunnableRevision.Digest()
	if err != nil {
		return fmt.Errorf("runtime environment reap revision: %w", err)
	}
	if r.Revision != digest {
		return errors.New("runtime environment reap request revision does not match binding")
	}
	return nil
}

// ReapRecord is an internal cleanup projection. It is deliberately separate
// from VerificationReport and content publication state.
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

// ReapQueue is the durable handoff between the Controller and Reaper. Its
// implementation is intentionally isolated so task 6 can back it with the
// runtime persistence schema without changing lifecycle behavior.
type ReapQueue interface {
	Enqueue(context.Context, ReapRequest) error
	Get(context.Context, string) (ReapRecord, error)
	Claim(context.Context, string, time.Duration, time.Time) (*ReapClaim, error)
	Complete(context.Context, ReapClaim, bool, string, time.Time, time.Time) error
}

// ReapProvider is the only provider capability Reaper needs.
type ReapProvider interface {
	Stop(context.Context, Binding) (bool, error)
	Release(context.Context, Binding) (bool, error)
}

// Reaper claims cleanup work under a time-bounded fence. A provider error is
// recorded for diagnostics and retried; it never mutates environment reports.
type Reaper struct {
	Queue    ReapQueue
	Provider ReapProvider
	Owner    string
	LeaseTTL time.Duration
	Retry    time.Duration
	Now      func() time.Time
}

func (r *Reaper) RunOnce(ctx context.Context) (bool, error) {
	if r.Queue == nil || r.Provider == nil || strings.TrimSpace(r.Owner) == "" {
		return false, errors.New("runtime environment reaper requires queue, provider, and owner")
	}
	now := r.now()
	leaseTTL := r.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = defaultReaperLeaseTTL
	}
	claim, err := r.Queue.Claim(ctx, r.Owner, leaseTTL, now)
	if err != nil || claim == nil {
		return false, err
	}
	stopped, stopErr := r.stop(ctx, claim.Record.Request.Binding)
	success := errors.Is(stopErr, ErrResourceAbsent)
	diagnostic := ""
	if stopErr != nil && !success {
		diagnostic = bounded(stopErr.Error())
	}
	if stopErr == nil && !stopped {
		diagnostic = "runtime environment stop is still in progress"
	}
	if stopErr == nil && stopped {
		operationCtx, cancel := withLifecycleTimeout(ctx, claim.Record.Request.Binding.RunnableRevision.Spec.LifecyclePolicy.ReapTimeoutSeconds)
		done, releaseErr := r.Provider.Release(operationCtx, claim.Record.Request.Binding)
		cancel()
		success = done || errors.Is(releaseErr, ErrResourceAbsent)
		if releaseErr != nil && !success {
			diagnostic = bounded(releaseErr.Error())
		}
	}
	retry := r.Retry
	if retry <= 0 {
		retry = defaultReaperRetry
	}
	if err := r.Queue.Complete(ctx, *claim, success, diagnostic, r.now(), r.now().Add(retry)); err != nil {
		return true, err
	}
	return true, nil
}

func (r *Reaper) stop(ctx context.Context, binding Binding) (bool, error) {
	operationCtx, cancel := withLifecycleTimeout(ctx, binding.RunnableRevision.Spec.LifecyclePolicy.StopTimeoutSeconds)
	defer cancel()
	return r.Provider.Stop(operationCtx, binding)
}

func (r *Reaper) now() time.Time {
	if r.Now == nil {
		return time.Now().UTC()
	}
	return r.Now().UTC()
}

// InMemoryReapQueue is suitable for controller tests and local development.
// Production wiring must provide the task-6 durable ReapQueue implementation.
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
			return errors.New("runtime environment reap request fence differs from existing request")
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
		return nil, errors.New("runtime environment reap claim is invalid")
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
		return errors.New("runtime environment reap completion time is required")
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
		record.LastError = bounded(diagnostic)
		record.NextAttemptAt = retryAt.UTC()
	}
	q.records[key] = record
	return nil
}
