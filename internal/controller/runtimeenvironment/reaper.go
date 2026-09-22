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
	// Failed retries share the platform backoff curve (runnable.RetryBackoff):
	// doubling from the base, capped at five minutes, then constant forever.
	// There is no attempt ceiling and no terminal give-up: the Custom Resource
	// holds the goal, so the reaper keeps retrying a stuck teardown until the
	// operator fixes or removes the underlying resource (a resource that is
	// already gone counts as success).
)

type ReapRequest = runnable.ReapRequest
type ReapRecord = runnable.ReapRecord
type ReapClaim = runnable.ReapClaim
type ReapQueue = runnable.ReapQueue
type ReapState = runnable.ReapState

const (
	ReapQueued    = runnable.ReapQueued
	ReapClaimed   = runnable.ReapClaimed
	ReapSucceeded = runnable.ReapSucceeded
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
		retry = runnable.RetryBackoff(retry, claim.Record.Attempt)
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
