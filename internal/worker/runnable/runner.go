package runnableworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

// ActionStore is the narrow Server boundary used by the polling worker. It
// contains only lease-fenced public runnable values, never content workflow or
// publication state.
type ActionStore interface {
	Claim(context.Context, string, time.Duration) (*runnable.ActionContext, error)
	Renew(context.Context, runnable.LeaseCredential, time.Duration) error
	CompleteMaterialization(context.Context, runnable.LeaseCredential, runnable.StoredRevision) error
	CompleteVerification(context.Context, runnable.LeaseCredential, runnable.StoredVerificationReport) error
	RequestVerificationEnvironmentRelease(context.Context, runnable.LeaseCredential, runnable.EnvironmentIdentity) error
	ReportFailure(context.Context, runnable.LeaseCredential, runnable.FailureClass, string, string) error
}

// ActionExecutor makes the scheduler independently testable from source and
// provider adapters. Worker implements this interface.
type ActionExecutor interface {
	Materialize(context.Context, runnable.MaterializeRequest) (runnable.RunnableRevision, error)
	Verify(context.Context, runnable.VerifyRequest) (runnable.VerificationReport, error)
}

type RunnerConfig struct {
	WorkerID  string
	LeaseTTL  time.Duration
	PollEvery time.Duration
}

// Runner owns one-at-a-time public runnable action execution. Provider work
// is fenced by a renewable Server lease, while result IDs are deterministically
// derived from the action identity so retry or takeover cannot create another
// immutable record for the same completed state transition.
type Runner struct {
	store    ActionStore
	executor ActionExecutor
	config   RunnerConfig
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
}

func NewRunner(store ActionStore, executor ActionExecutor, config RunnerConfig) (*Runner, error) {
	if store == nil || executor == nil || strings.TrimSpace(config.WorkerID) == "" {
		return nil, errors.New("runnable action runner requires a Server store, executor, and worker id")
	}
	if config.LeaseTTL <= 0 {
		config.LeaseTTL = 45 * time.Second
	}
	if config.PollEvery <= 0 {
		config.PollEvery = time.Second
	}
	return &Runner{store: store, executor: executor, config: config, now: time.Now, sleep: sleepContext}, nil
}

func (r *Runner) Run(ctx context.Context) error {
	failures := 0
	for {
		if ctx.Err() != nil {
			// A cancelled context is an orderly shutdown, not a worker failure.
			return nil //nolint:nilerr
		}
		processed, err := r.ProcessOne(ctx)
		if err != nil {
			failures++
			delay := runnerRetryDelay(failures)
			slog.Warn("claim runnable action", "worker_id", r.config.WorkerID, "retry_in", delay, "err", err)
			if err := r.sleep(ctx, delay); err != nil && ctx.Err() == nil {
				return err
			}
			continue
		}
		failures = 0
		if processed {
			continue
		}
		if err := r.sleep(ctx, r.config.PollEvery); err != nil && ctx.Err() == nil {
			return err
		}
	}
}

// ProcessOne claims and handles exactly one public action. An execution error
// is persisted through ReportFailure and does not terminate the polling loop;
// only an inability to claim or validate a claimed action is returned.
func (r *Runner) ProcessOne(ctx context.Context) (bool, error) {
	action, err := r.store.Claim(ctx, r.config.WorkerID, r.config.LeaseTTL)
	if err != nil || action == nil {
		return action != nil, err
	}
	if err := action.Validate(); err != nil {
		return true, fmt.Errorf("server returned invalid runnable action: %w", err)
	}
	r.process(ctx, *action)
	return true, nil
}

func (r *Runner) process(parent context.Context, action runnable.ActionContext) {
	timeout, err := runnerActionTimeout(action)
	if err == nil {
		execCtx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()
		lease := &runnerLease{}
		done := make(chan struct{})
		go r.renew(execCtx, cancel, done, action.Credential, lease)
		err = r.execute(execCtx, action)
		close(done)
		cancel()
		if lease.lost.Load() {
			return
		}
	}
	if err == nil || parent.Err() != nil || errors.Is(err, runnable.ErrActionLeaseLost) {
		return
	}
	if reportErr := r.reportFailure(parent, action.Credential, err); reportErr != nil && !errors.Is(reportErr, runnable.ErrActionLeaseLost) {
		slog.Error("report runnable action failure", "action", action.Credential.Identity.Key(), "err", reportErr)
	}
}

func (r *Runner) execute(ctx context.Context, action runnable.ActionContext) error {
	switch action.Credential.Identity.Phase {
	case runnable.ActionMaterializeArtifact:
		revision, err := r.executor.Materialize(ctx, runnable.MaterializeRequest{Credential: action.Credential, Spec: *action.Spec})
		if err != nil {
			return err
		}
		digest, err := revision.Digest()
		if err != nil {
			return err
		}
		stored := runnable.StoredRevision{
			Reference: runnable.RevisionReference{ID: runnable.RecordID("rr", action.Credential.Identity), Digest: digest},
			Revision:  revision,
			CreatedAt: r.now().UTC(),
		}
		return r.store.CompleteMaterialization(ctx, action.Credential, stored)
	case runnable.ActionVerify:
		revision := *action.RunnableRevision
		report, err := r.executor.Verify(ctx, runnable.VerifyRequest{
			Credential: action.Credential, RunnableRevision: revision, RunnableRevisionRef: action.RunnableRevisionRef, RunnableRevisionDigest: action.RunnableRevisionDigest, Attempt: action.Attempt,
		})
		if err != nil {
			return err
		}
		if report.Failure != nil && report.Failure.Class == runnable.FailureInfrastructure {
			r.requestVerificationEnvironmentRelease(ctx, action.Credential, report.Environment)
			return r.store.ReportFailure(ctx, action.Credential, report.Failure.Class, report.Failure.Reason, report.Failure.Message)
		}
		digest, err := report.Digest(revision)
		if err != nil {
			return err
		}
		stored := runnable.StoredVerificationReport{
			Reference:        runnable.VerificationReportReference{ID: runnable.RecordID("vr", action.Credential.Identity), Digest: digest},
			Report:           report,
			RunnableRevision: revision,
			CreatedAt:        r.now().UTC(),
		}
		if err := r.store.CompleteVerification(ctx, action.Credential, stored); err != nil {
			return err
		}
		return nil
	default:
		return runnable.NewArtifactFailure("action-phase", "runnable action has an unsupported phase")
	}
}

func (r *Runner) requestVerificationEnvironmentRelease(ctx context.Context, credential runnable.LeaseCredential, environment runnable.EnvironmentIdentity) {
	if err := r.store.RequestVerificationEnvironmentRelease(ctx, credential, environment); err != nil {
		// Cleanup handoff is intentionally independent of the verification retry.
		slog.Warn("request verification environment release", "environment", environment.ID, "err", err)
	}
}

func (r *Runner) reportFailure(ctx context.Context, credential runnable.LeaseCredential, executionErr error) error {
	class, code := runnable.FailureInfrastructure, "runnable-action-failed"
	var artifact *runnable.ArtifactFailure
	if errors.As(executionErr, &artifact) {
		class, code = runnable.FailureArtifact, artifact.Code
	}
	summary := strings.TrimSpace(executionErr.Error())
	if summary == "" {
		summary = "runnable action failed"
	}
	if len(summary) > runnable.MaxSummaryLength {
		summary = summary[:runnable.MaxSummaryLength]
	}
	if code == "" {
		code = "runnable-action-failed"
	}
	return r.store.ReportFailure(ctx, credential, class, code, summary)
}

func (r *Runner) renew(ctx context.Context, cancel context.CancelFunc, done <-chan struct{}, credential runnable.LeaseCredential, lease *runnerLease) {
	interval := r.config.LeaseTTL / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			requestCtx, requestCancel := context.WithTimeout(ctx, runnerMinDuration(r.config.LeaseTTL/2, 10*time.Second))
			err := r.store.Renew(requestCtx, credential, r.config.LeaseTTL)
			requestCancel()
			if err == nil {
				continue
			}
			lease.lost.Store(true)
			cancel()
			slog.Warn("runnable action lease renewal failed", "action", credential.Identity.Key(), "err", err)
			return
		}
	}
}

type runnerLease struct{ lost atomic.Bool }

func runnerActionTimeout(action runnable.ActionContext) (time.Duration, error) {
	var seconds int64
	switch action.Credential.Identity.Phase {
	case runnable.ActionMaterializeArtifact:
		seconds = action.Spec.LifecyclePolicy.CreateTimeoutSeconds
	case runnable.ActionVerify:
		seconds = action.RunnableRevision.Spec.LifecyclePolicy.MaxLifetimeSeconds
	default:
		return 0, runnable.NewArtifactFailure("action-phase", "runnable action has an unsupported phase")
	}
	if seconds <= 0 {
		return 0, runnable.NewArtifactFailure("action-timeout", "runnable action has no approved deadline")
	}
	return time.Duration(seconds) * time.Second, nil
}

// Kept package-local for existing executor tests; production callers use the
// public domain helper so content coordinators share the same stable ID.
func actionRecordID(prefix string, identity runnable.ActionIdentity) string {
	return runnable.RecordID(prefix, identity)
}

func runnerRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second * time.Duration(1<<runnerMin(attempt-1, 6))
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}

func runnerMin(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func runnerMinDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
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
