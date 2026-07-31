package candidateworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/worklist"
)

type Executor interface {
	Execute(context.Context, Claim) error
}

type ExecutorFunc func(context.Context, Claim) error

func (f ExecutorFunc) Execute(ctx context.Context, claim Claim) error { return f(ctx, claim) }

// Store is the narrow Server worklist protocol required by a fixed candidate
// Worker. Production uses Client; the interface keeps the scheduling loop
// independently testable from stage executors.
type Store interface {
	Claim(context.Context, worklist.Kind, string, time.Duration) (*Claim, error)
	Renew(context.Context, Claim, time.Duration) error
	Requeue(context.Context, Claim, time.Time, string, string) error
	FailArtifact(context.Context, Claim, candidate.Failure, *candidate.VerificationReport) error
}

type ArtifactError struct {
	Failure candidate.Failure
	Report  *candidate.VerificationReport
}

func (e *ArtifactError) Error() string { return e.Failure.Summary }

func ArtifactFailure(code, summary string, report *candidate.VerificationReport) error {
	return &ArtifactError{Failure: candidate.Failure{Class: candidate.FailureArtifact, Code: code, Summary: summary}, Report: report}
}

type Config struct {
	WorkerID  string
	Kinds     []worklist.Kind
	LeaseTTL  time.Duration
	PollEvery time.Duration
}

type Worker struct {
	client    Store
	executors map[worklist.Kind]Executor
	config    Config
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
	nextKind  int
}

func New(client Store, executors map[worklist.Kind]Executor, config Config) (*Worker, error) {
	if client == nil || strings.TrimSpace(config.WorkerID) == "" || len(config.Kinds) == 0 {
		return nil, errors.New("candidate worker requires a Server client, worker ID, and work kinds")
	}
	if config.LeaseTTL <= 0 {
		config.LeaseTTL = 45 * time.Second
	}
	if config.PollEvery <= 0 {
		config.PollEvery = time.Second
	}
	seen := make(map[worklist.Kind]struct{}, len(config.Kinds))
	for _, kind := range config.Kinds {
		if kind == worklist.KindAgent || !worklist.ValidKind(kind) || executors[kind] == nil {
			return nil, fmt.Errorf("candidate worker has invalid or unhandled kind %q", kind)
		}
		if _, duplicate := seen[kind]; duplicate {
			return nil, fmt.Errorf("candidate worker has duplicate kind %q", kind)
		}
		seen[kind] = struct{}{}
	}
	return &Worker{
		client: client, executors: executors, config: config,
		now: func() time.Time { return time.Now().UTC() }, sleep: sleepContext,
	}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	claimFailures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		claimed, err := w.ProcessOne(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			claimFailures++
			delay := worklist.ClaimRetryDelay(w.config.PollEvery, claimFailures)
			slog.Warn("candidate work claim failed; retrying", "worker_id", w.config.WorkerID,
				"failure_class", "infrastructure", "retry_in", delay, "error", err)
			if err := w.sleep(ctx, delay); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil
				}
				return err
			}
			continue
		}
		claimFailures = 0
		if claimed {
			continue
		}
		if err := w.sleep(ctx, w.config.PollEvery); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return err
		}
	}
}

func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	for offset := range w.config.Kinds {
		index := (w.nextKind + offset) % len(w.config.Kinds)
		kind := w.config.Kinds[index]
		claim, err := w.client.Claim(ctx, kind, w.config.WorkerID, w.config.LeaseTTL)
		if err != nil {
			return false, fmt.Errorf("claim %s work: %w", kind, err)
		}
		if claim == nil {
			continue
		}
		w.nextKind = (index + 1) % len(w.config.Kinds)
		w.processClaim(ctx, *claim)
		return true, nil
	}
	return false, nil
}

func (w *Worker) processClaim(parent context.Context, claim Claim) {
	started := time.Now()
	slog.Info("candidate work started", w.logFields(claim)...)
	deadline := claim.Work.Item.DeadlineAt
	var execCtx context.Context
	var cancel context.CancelFunc
	if deadline != nil {
		execCtx, cancel = context.WithDeadline(parent, *deadline)
	} else {
		execCtx, cancel = context.WithCancel(parent)
	}
	defer cancel()

	done := make(chan struct{})
	var leaseLost atomic.Bool
	go w.renew(execCtx, claim, cancel, done, &leaseLost)
	err := w.executors[claim.Work.Item.Kind].Execute(execCtx, claim)
	close(done)
	cancel()
	if err == nil {
		slog.Info("candidate work succeeded", w.logFields(claim, "duration_seconds", time.Since(started).Seconds())...)
		return
	}
	slog.Warn("candidate work failed", w.logFields(claim,
		"duration_seconds", time.Since(started).Seconds(), "failure_class", candidateWorkErrorClass(err), "error", err)...)
	if leaseLost.Load() || errors.Is(err, worklist.ErrLeaseLost) || parent.Err() != nil {
		return
	}
	var artifact *ArtifactError
	if errors.As(err, &artifact) {
		if failureErr := artifact.Failure.Validate(); failureErr != nil {
			err = fmt.Errorf("invalid artifact failure: %w", failureErr)
		} else if failErr := w.client.FailArtifact(parent, claim, artifact.Failure, artifact.Report); failErr == nil || errors.Is(failErr, worklist.ErrLeaseLost) {
			return
		} else {
			err = fmt.Errorf("submit artifact failure: %w", failErr)
		}
	}
	w.requeue(parent, claim, err)
}

func (w *Worker) renew(ctx context.Context, claim Claim, cancel context.CancelFunc, done <-chan struct{}, lost *atomic.Bool) {
	interval := worklist.LeaseRenewalInterval(w.config.LeaseTTL)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			requestCtx, requestCancel := context.WithTimeout(ctx, worklist.LeaseRenewalRequestTimeout(w.config.LeaseTTL))
			err := w.client.Renew(requestCtx, claim, w.config.LeaseTTL)
			requestCancel()
			if err != nil {
				select {
				case <-done:
					return
				default:
				}
				lost.Store(true)
				cancel()
				slog.Warn("candidate work lease renewal failed", w.logFields(claim, "error", err)...)
				return
			}
		}
	}
}

func (w *Worker) requeue(ctx context.Context, claim Claim, executionErr error) {
	now := w.now()
	deadline := claim.Work.Item.DeadlineAt
	if deadline != nil && !now.Before(*deadline) {
		return
	}
	next := now.Add(retryDelay(claim.Work.Item.Attempt))
	if deadline != nil && next.After(*deadline) {
		next = *deadline
	}
	summary := strings.TrimSpace(executionErr.Error())
	if summary == "" {
		summary = "candidate stage failed"
	}
	if err := w.client.Requeue(ctx, claim, next, "INFRASTRUCTURE", summary); err != nil && !errors.Is(err, worklist.ErrLeaseLost) {
		slog.Error("requeue candidate work", w.logFields(claim, "error", err)...)
	}
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second * time.Duration(1<<min(attempt-1, 6))
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}

func (w *Worker) logFields(claim Claim, extra ...any) []any {
	fields := []any{
		"work_item_id", claim.Work.Item.ID, "kind", claim.Work.Item.Kind,
		"subject_id", claim.Candidate.ID, "attempt", claim.Work.Item.Attempt,
		"worker_id", w.config.WorkerID,
	}
	return append(fields, extra...)
}

func candidateWorkErrorClass(err error) string {
	var artifact *ArtifactError
	switch {
	case errors.As(err, &artifact):
		return string(candidate.FailureArtifact)
	case errors.Is(err, worklist.ErrLeaseLost):
		return "lease_lost"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "cancelled"
	default:
		return string(candidate.FailureInfrastructure)
	}
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
