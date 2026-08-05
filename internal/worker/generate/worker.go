// Package generate executes one durable runtime action at a time. It has no
// model, OpenSandbox, PostgreSQL, or Server-volume dependency; Server exposes
// only lease-fenced immutable action inputs through its internal API.
package generate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	domainexecution "github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

type Store interface {
	Claim(context.Context, string, time.Duration) (*generation.RuntimeActionContext, error)
	Renew(context.Context, generation.RuntimeActionCredential, time.Duration) error
	CandidateArchive(context.Context, generation.RuntimeActionCredential) ([]byte, string, error)
	CompleteBuild(context.Context, generation.RuntimeActionCredential, generation.BuildOutput) error
	CompleteArtifactPublish(context.Context, generation.RuntimeActionCredential, generation.ArtifactReference) error
	RecordVerificationEnvironment(context.Context, generation.RuntimeActionCredential, generation.VerificationEnvironment) error
	CompleteVerification(context.Context, generation.RuntimeActionCredential, generation.VerificationReport) error
	RecordChallengePublication(context.Context, generation.RuntimeActionCredential, generation.ArtifactReference) error
	ReportInfrastructureFailure(context.Context, generation.RuntimeActionCredential, generation.Failure) error
	ReportArtifactFailure(context.Context, generation.RuntimeActionCredential, generation.Failure, *generation.VerificationReport) error
}

type BuilderExecutor interface {
	ExecuteWork(context.Context, domainexecution.Work, []byte) (domainexecution.BuildOutput, error)
}

type PublisherExecutor interface {
	PublishArtifactWork(context.Context, domainexecution.Work) (domainexecution.ArtifactReference, error)
	PublishChallengeWork(context.Context, domainexecution.Work, string) (domainexecution.ArtifactReference, error)
	ReapCandidate(context.Context, generation.ResourceReap) error
}

type VerifierExecutor interface {
	ExecuteWork(context.Context, domainexecution.Work, func(context.Context, domainexecution.VerificationEnvironment) error) (domainexecution.VerificationReport, error)
	ReapVerificationEnvironment(context.Context, generation.ResourceReap) error
}

type ResourceReapStore interface {
	ClaimResourceReap(context.Context, string, generation.ResourceReapKind, time.Duration) (*generation.ResourceReapClaim, error)
	CompleteResourceReap(context.Context, generation.ResourceReapClaim, string) error
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
	actionClaimFailures := 0
	reapFailures := 0
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
		if w.reapStore != nil {
			reaped, reapErr := w.reapOne(ctx)
			if reapErr != nil {
				reapFailures++
				delay := retryDelay(reapFailures)
				slog.Warn("reap runtime resources", "worker_id", w.config.WorkerID, "retry_in", delay, "err", reapErr)
				if err := w.sleep(ctx, delay); err != nil && ctx.Err() == nil {
					return err
				}
				continue
			}
			reapFailures = 0
			if reaped {
				continue
			}
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
		return true, fmt.Errorf("Server returned invalid runtime action: %w", err)
	}
	w.processAction(ctx, *action)
	return true, nil
}

func (w *Worker) processAction(parent context.Context, action generation.RuntimeActionContext) {
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
		if reportErr := w.reportError(parent, credential, err); reportErr != nil && !errors.Is(reportErr, generation.ErrLeaseLost) {
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

func (w *Worker) executeAction(ctx context.Context, action generation.RuntimeActionContext, work domainexecution.Work, credential generation.RuntimeActionCredential, lease *actionLease) error {
	if lease.lost.Load() {
		return generation.ErrLeaseLost
	}
	switch action.Identity.State {
	case generation.StateBuilding:
		archive, digest, err := w.store.CandidateArchive(ctx, credential)
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
			return generation.ErrLeaseLost
		}
		return w.store.CompleteBuild(ctx, credential, output)

	case generation.StateArtifactPublishing:
		artifact, err := w.publisher.PublishArtifactWork(ctx, work)
		if err != nil {
			return err
		}
		if lease.lost.Load() {
			return generation.ErrLeaseLost
		}
		return w.store.CompleteArtifactPublish(ctx, credential, artifact)

	case generation.StateVerifying:
		report, err := w.verifier.ExecuteWork(ctx, work, func(recordCtx context.Context, environment domainexecution.VerificationEnvironment) error {
			if lease.lost.Load() {
				return generation.ErrLeaseLost
			}
			return w.store.RecordVerificationEnvironment(recordCtx, credential, environment)
		})
		if err != nil {
			return err
		}
		if lease.lost.Load() {
			return generation.ErrLeaseLost
		}
		return w.store.CompleteVerification(ctx, credential, report)

	case generation.StateChallengePublishing:
		artifact, err := w.publisher.PublishChallengeWork(ctx, work, action.Candidate.Publication.ChallengeID)
		if err != nil {
			return err
		}
		if lease.lost.Load() {
			return generation.ErrLeaseLost
		}
		return w.store.RecordChallengePublication(ctx, credential, artifact)

	default:
		return fmt.Errorf("runtime worker cannot execute workflow state %s", action.Identity.State)
	}
}

func (w *Worker) reportError(ctx context.Context, credential generation.RuntimeActionCredential, executionErr error) error {
	if errors.Is(executionErr, generation.ErrLeaseLost) || ctx.Err() != nil {
		return nil
	}
	var artifact *domainexecution.ArtifactError
	if errors.As(executionErr, &artifact) {
		return w.store.ReportArtifactFailure(ctx, credential, generation.Failure{
			Class: generation.FailureArtifact, Code: artifact.Code, Summary: artifact.Summary,
		}, artifact.Report)
	}
	summary := strings.TrimSpace(executionErr.Error())
	if summary == "" {
		summary = "runtime action failed"
	}
	return w.store.ReportInfrastructureFailure(ctx, credential, generation.Failure{
		Class: generation.FailureInfrastructure, Code: "RUNTIME_ACTION_FAILED", Summary: summary,
	})
}

func (w *Worker) renew(ctx context.Context, cancel context.CancelFunc, done <-chan struct{}, credential generation.RuntimeActionCredential, lease *actionLease) {
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
	for _, kind := range []generation.ResourceReapKind{
		generation.ResourceReapVerificationEnvironment,
		generation.ResourceReapNodeBuildImage,
		generation.ResourceReapCandidateArtifact,
	} {
		claim, err := w.reapStore.ClaimResourceReap(ctx, w.config.WorkerID, kind, w.config.LeaseTTL)
		if err != nil {
			return false, err
		}
		if claim == nil {
			continue
		}
		failure := ""
		switch claim.Kind {
		case generation.ResourceReapVerificationEnvironment:
			err = w.verifier.ReapVerificationEnvironment(ctx, claim.ResourceReap)
		default:
			err = w.publisher.ReapCandidate(ctx, claim.ResourceReap)
		}
		if err != nil {
			failure = err.Error()
		}
		if err := w.reapStore.CompleteResourceReap(ctx, *claim, failure); err != nil {
			return true, err
		}
		return true, nil
	}
	return false, nil
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
