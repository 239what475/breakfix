// Package runtimeworker executes one durable runtime action at a time. It has no
// model, OpenSandbox, PostgreSQL, or Server-volume dependency; Server exposes
// only lease-fenced immutable action inputs through its internal API.
package runtimeworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	domainexecution "github.com/breakfix/breakfix/internal/domain/execution"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
)

type Store interface {
	Claim(context.Context, string, time.Duration) (*runtime.Context, error)
	Renew(context.Context, runtime.Credential, time.Duration) error
	Archive(context.Context, runtime.Credential) ([]byte, string, error)
	CompleteBuild(context.Context, runtime.Credential, domainexecution.BuildOutput) error
	CompleteArtifactPublish(context.Context, runtime.Credential, domainexecution.ArtifactReference) error
	RecordVerificationEnvironment(context.Context, runtime.Credential, domainexecution.VerificationEnvironment) error
	CompleteVerification(context.Context, runtime.Credential, domainexecution.VerificationReport) error
	RecordFinalArtifact(context.Context, runtime.Credential, domainexecution.ArtifactReference) error
	ReportInfrastructureFailure(context.Context, runtime.Credential, runtime.Failure) error
	ReportArtifactFailure(context.Context, runtime.Credential, runtime.Failure, *domainexecution.VerificationReport) error
}

type BuilderExecutor interface {
	ExecuteWork(context.Context, domainexecution.Work, []byte) (domainexecution.BuildOutput, error)
}

type PublisherExecutor interface {
	PublishArtifactWork(context.Context, domainexecution.Work) (domainexecution.ArtifactReference, error)
	PublishFinalArtifactWork(context.Context, domainexecution.Work, string, string) (domainexecution.ArtifactReference, error)
	ReapResource(context.Context, runtime.Reap) error
}

type VerifierExecutor interface {
	ExecuteWork(context.Context, domainexecution.Work, func(context.Context, domainexecution.VerificationEnvironment) error) (domainexecution.VerificationReport, error)
	ReapVerificationEnvironment(context.Context, runtime.Reap) error
}

type ResourceReapStore interface {
	ClaimResourceReap(context.Context, string, time.Duration) (*runtime.ReapClaim, error)
	CompleteResourceReap(context.Context, runtime.ReapClaim, string) error
}

type Config struct {
	WorkerID  string
	LeaseTTL  time.Duration
	PollEvery time.Duration
}

type Worker struct {
	store     Store
	builder   BuilderExecutor
	publisher PublisherExecutor
	verifier  VerifierExecutor
	reapStore ResourceReapStore
	config    Config
	sleep     func(context.Context, time.Duration) error
}

func New(store Store, builder BuilderExecutor, publisher PublisherExecutor, verifier VerifierExecutor, config Config) (*Worker, error) {
	if store == nil || builder == nil || publisher == nil || verifier == nil || strings.TrimSpace(config.WorkerID) == "" {
		return nil, errors.New("runtime worker requires Server client, runtime executors, and worker id")
	}
	if config.LeaseTTL <= 0 {
		config.LeaseTTL = 45 * time.Second
	}
	if config.PollEvery <= 0 {
		config.PollEvery = time.Second
	}
	worker := &Worker{
		store: store, builder: builder, publisher: publisher, verifier: verifier,
		config: config, sleep: sleepContext,
	}
	if reapStore, ok := store.(ResourceReapStore); ok {
		worker.reapStore = reapStore
	}
	return worker, nil
}

func (w *Worker) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	loops := 1
	results := make(chan error, 2)
	go func() { results <- w.runActionLoop(runCtx) }()
	if w.reapStore != nil {
		loops++
		go func() { results <- w.runReaperLoop(runCtx) }()
	}

	var result error
	for completed := 0; completed < loops; completed++ {
		if err := <-results; err != nil && ctx.Err() == nil && result == nil {
			result = err
			cancel()
		}
	}
	return result
}

// runActionLoop owns only normal runtime state transitions. Resource cleanup
// has its own loop so a full action backlog cannot starve reaping.
func (w *Worker) runActionLoop(ctx context.Context) error {
	actionClaimFailures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		processed, err := w.ProcessOne(ctx)
		if err != nil {
			actionClaimFailures++
			delay := retryDelay(actionClaimFailures)
			slog.Warn("claim runtime action", "worker_id", w.config.WorkerID, "retry_in", delay, "err", err)
			if err := w.sleep(ctx, delay); err != nil && ctx.Err() == nil {
				return err
			}
			continue
		}
		actionClaimFailures = 0
		if processed {
			continue
		}
		if err := w.sleep(ctx, w.config.PollEvery); err != nil && ctx.Err() == nil {
			return err
		}
	}
}

// runReaperLoop owns only idempotent provider cleanup. It intentionally does
// not participate in runtime action ordering or business-state transitions.
func (w *Worker) runReaperLoop(ctx context.Context) error {
	if w.reapStore == nil {
		return nil
	}
	reapFailures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		reaped, err := w.reapOne(ctx)
		if err != nil {
			reapFailures++
			delay := retryDelay(reapFailures)
			slog.Warn("reap runtime resources", "worker_id", w.config.WorkerID, "retry_in", delay, "err", err)
			if err := w.sleep(ctx, delay); err != nil && ctx.Err() == nil {
				return err
			}
			continue
		}
		reapFailures = 0
		if reaped {
			continue
		}
		if err := w.sleep(ctx, w.config.PollEvery); err != nil && ctx.Err() == nil {
			return err
		}
	}
}

// ProcessOne claims and executes exactly one runtime state. A state advance
// releases the lease, and a later loop iteration claims the next action.
func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	action, err := w.store.Claim(ctx, w.config.WorkerID, w.config.LeaseTTL)
	if err != nil || action == nil {
		return action != nil, err
	}
	if err := action.Valid(); err != nil {
		return true, fmt.Errorf("server returned invalid runtime action: %w", err)
	}
	w.processAction(ctx, *action)
	return true, nil
}

func (w *Worker) processAction(parent context.Context, action runtime.Context) {
	started := time.Now()
	credential := action.Credential()
	deadline := domainexecution.NewActionDeadline(started)
	execCtx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	done := make(chan struct{})
	lease := &actionLease{}
	go w.renew(execCtx, cancel, done, credential, lease)

	work, err := action.Work(deadline)
	if err == nil {
		err = w.executeAction(execCtx, action, work, credential, lease)
	}
	close(done)
	cancel()
	if err != nil && !lease.lost.Load() && parent.Err() == nil {
		if reportErr := w.reportError(parent, credential, err); reportErr != nil && !errors.Is(reportErr, runtime.ErrLeaseLost) {
			slog.Error("report runtime action failure", "action", action.Identity.String(), "err", reportErr)
		}
	}
	fields := []any{"action", action.Identity.String(), "worker_id", w.config.WorkerID, "duration_seconds", time.Since(started).Seconds()}
	if lease.lost.Load() {
		slog.Info("runtime action lease ended", fields...)
		return
	}
	if err != nil {
		slog.Info("runtime action execution ended", append(fields, "err", err)...)
		return
	}
	slog.Info("runtime action completed", fields...)
}

func (w *Worker) executeAction(ctx context.Context, action runtime.Context, work domainexecution.Work, credential runtime.Credential, lease *actionLease) error {
	if lease.lost.Load() {
		return runtime.ErrLeaseLost
	}
	switch action.Identity.State {
	case runtime.StateBuilding:
		archive, digest, err := w.store.Archive(ctx, credential)
		if err != nil {
			return err
		}
		if digest != work.ArchiveSHA256 {
			return domainexecution.NewArtifactError("CANDIDATE_ARCHIVE_DIGEST_MISMATCH", "Server returned a candidate archive with a different immutable digest")
		}
		output, err := w.builder.ExecuteWork(ctx, work, archive)
		if err != nil {
			return err
		}
		if lease.lost.Load() {
			return runtime.ErrLeaseLost
		}
		return w.store.CompleteBuild(ctx, credential, output)

	case runtime.StateArtifactPublishing:
		artifact, err := w.publisher.PublishArtifactWork(ctx, work)
		if err != nil {
			return err
		}
		if lease.lost.Load() {
			return runtime.ErrLeaseLost
		}
		return w.store.CompleteArtifactPublish(ctx, credential, artifact)

	case runtime.StateVerifying:
		report, err := w.verifier.ExecuteWork(ctx, work, func(recordCtx context.Context, environment domainexecution.VerificationEnvironment) error {
			if lease.lost.Load() {
				return runtime.ErrLeaseLost
			}
			return w.store.RecordVerificationEnvironment(recordCtx, credential, environment)
		})
		if err != nil {
			return err
		}
		if lease.lost.Load() {
			return runtime.ErrLeaseLost
		}
		return w.store.CompleteVerification(ctx, credential, report)

	case runtime.StateArtifactFinalizing:
		artifact, err := w.publisher.PublishFinalArtifactWork(ctx, work, action.FinalArtifactTargetID, action.FinalArtifactTargetRevision)
		if err != nil {
			return err
		}
		if lease.lost.Load() {
			return runtime.ErrLeaseLost
		}
		return w.store.RecordFinalArtifact(ctx, credential, artifact)

	default:
		return fmt.Errorf("runtime worker cannot execute workflow state %s", action.Identity.State)
	}
}

func (w *Worker) reportError(ctx context.Context, credential runtime.Credential, executionErr error) error {
	if errors.Is(executionErr, runtime.ErrLeaseLost) {
		return runtime.ErrLeaseLost
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var artifact *domainexecution.ArtifactError
	if errors.As(executionErr, &artifact) {
		return w.store.ReportArtifactFailure(ctx, credential, runtime.Failure{
			Class: runtime.FailureArtifact, Code: artifact.Code, Summary: artifact.Summary,
		}, artifact.Report)
	}
	summary := strings.TrimSpace(executionErr.Error())
	if summary == "" {
		summary = "runtime action failed"
	}
	return w.store.ReportInfrastructureFailure(ctx, credential, runtime.Failure{
		Class: runtime.FailureInfrastructure, Code: "RUNTIME_ACTION_FAILED", Summary: summary,
	})
}

func (w *Worker) renew(ctx context.Context, cancel context.CancelFunc, done <-chan struct{}, credential runtime.Credential, lease *actionLease) {
	interval := w.config.LeaseTTL / 3
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
			requestCtx, requestCancel := context.WithTimeout(ctx, minDuration(w.config.LeaseTTL/2, 10*time.Second))
			err := w.store.Renew(requestCtx, credential, w.config.LeaseTTL)
			requestCancel()
			if err == nil {
				continue
			}
			lease.lost.Store(true)
			cancel()
			slog.Warn("runtime action lease renewal failed", "action", credential.Identity.String(), "err", err)
			return
		}
	}
}

func (w *Worker) reapOne(ctx context.Context) (bool, error) {
	if w.reapStore == nil {
		return false, nil
	}
	claim, err := w.reapStore.ClaimResourceReap(ctx, w.config.WorkerID, w.config.LeaseTTL)
	if err != nil {
		return false, err
	}
	if claim == nil {
		return false, nil
	}
	if err := claim.Valid(); err != nil {
		return true, fmt.Errorf("server returned invalid runtime resource reap claim: %w", err)
	}
	failure := ""
	switch claim.Kind {
	case runtime.ReapVerificationEnvironment:
		err = w.verifier.ReapVerificationEnvironment(ctx, claim.Reap)
	default:
		err = w.publisher.ReapResource(ctx, claim.Reap)
	}
	if err != nil {
		failure = err.Error()
	}
	if err := w.reapStore.CompleteResourceReap(ctx, *claim, failure); err != nil {
		return true, err
	}
	return true, nil
}

type actionLease struct{ lost atomic.Bool }

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

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func minDuration(left, right time.Duration) time.Duration {
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
