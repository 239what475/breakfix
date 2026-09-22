package runtimeenvironment

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

const (
	defaultReaperLeaseTTL = 30 * time.Second
	defaultReaperRetry    = 5 * time.Second
	// Failed retries back off exponentially from the base delay, capped so a
	// stuck provider cannot hold the queue hostage; attempts are bounded and
	// the record retires to the dead terminal state instead of retrying
	// forever (5s..5m over 20 attempts is roughly 75 minutes end to end).
	reaperRetryCap       = 5 * time.Minute
	reaperMaxAttempts    = 20
	reaperDeadDiagnostic = "reap attempts exhausted"
)

// reaperRetryDelay doubles the base delay after each failed attempt and caps
// it, so the queue drains fast for transient errors and slowly for stuck ones.
func reaperRetryDelay(base time.Duration, attempt int64) time.Duration {
	if base <= 0 {
		base = defaultReaperRetry
	}
	if attempt < 1 {
		attempt = 1
	}
	delay := base
	for range attempt - 1 {
		delay *= 2
		if delay >= reaperRetryCap {
			return reaperRetryCap
		}
	}
	return delay
}

type ReapRequest = runnable.ReapRequest
type ReapRecord = runnable.ReapRecord
type ReapClaim = runnable.ReapClaim
type ReapQueue = runnable.ReapQueue
type ReapState = runnable.ReapState

const (
	ReapQueued    = runnable.ReapQueued
	ReapClaimed   = runnable.ReapClaimed
	ReapSucceeded = runnable.ReapSucceeded
	ReapDead      = runnable.ReapDead
)

var (
	ErrReapNotFound   = runnable.ErrReapNotFound
	ErrReapLeaseLost  = runnable.ErrReapLeaseLost
	ErrResourceAbsent = runnable.ErrResourceAbsent
)

// ReapProvider is the only provider capability Reaper needs.
type ReapProvider interface {
	Stop(context.Context, Binding) (bool, error)
	Release(context.Context, Binding) (bool, error)
}

// Reaper claims cleanup work under a time-bounded fence. A provider error is
// recorded for diagnostics and retried; it never mutates environment reports.
type Reaper struct {
	Queue    runnable.ReapQueue
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
		operationCtx, cancel := withLifecycleTimeout(ctx, claim.Record.Request.Binding.Lifecycle().ReapTimeoutSeconds)
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
	if !success {
		if claim.Record.Attempt >= reaperMaxAttempts {
			if err := r.Queue.Deadletter(ctx, *claim, reaperDeadDiagnostic+": "+diagnostic, r.now()); err != nil {
				return true, err
			}
			return true, nil
		}
		retry = reaperRetryDelay(retry, claim.Record.Attempt)
	}
	if err := r.Queue.Complete(ctx, *claim, success, diagnostic, r.now(), r.now().Add(retry)); err != nil {
		return true, err
	}
	return true, nil
}

func (r *Reaper) stop(ctx context.Context, binding Binding) (bool, error) {
	operationCtx, cancel := withLifecycleTimeout(ctx, binding.Lifecycle().StopTimeoutSeconds)
	defer cancel()
	return r.Provider.Stop(operationCtx, binding)
}

func (r *Reaper) now() time.Time {
	if r.Now == nil {
		return time.Now().UTC()
	}
	return r.Now().UTC()
}

func NewInMemoryReapQueue() *runnable.InMemoryReapQueue { return runnable.NewInMemoryReapQueue() }
