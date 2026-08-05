// Package generate executes the durable external runtime actions of one
// GenerationWorkflow. Model execution lives in Server; this process has no
// PostgreSQL dependency and receives only lease-fenced runtime inputs through
// the Server's internal API.
package generate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	app "github.com/breakfix/breakfix/internal/application/generation"
	domainexecution "github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

type Store interface {
	Claim(context.Context, string, time.Duration) (*generation.Claim, error)
	Renew(context.Context, generation.Claim, time.Duration) error
	Context(context.Context, generation.Claim) (*generation.Context, error)
	Phase(context.Context, generation.Claim, app.PhaseRequest) (*generation.Claim, error)
	CandidateArchive(context.Context, generation.Claim) ([]byte, string, error)
	K8sBase(context.Context, generation.Claim) ([]byte, string, error)
	BuildArchive(context.Context, generation.Claim) ([]byte, string, error)
}

type BuilderExecutor interface {
	Execute(context.Context, generation.Execution, []byte, []byte) (generation.BuildResult, error)
}

type PublisherExecutor interface {
	PublishArtifact(context.Context, generation.Execution, []byte) (generation.ArtifactReference, error)
	PublishChallenge(context.Context, generation.Execution) (generation.ArtifactReference, error)
}

type ResourceReapStore interface {
	ClaimResourceReap(context.Context, string, generation.ResourceReapKind, time.Duration) (*generation.ResourceReapClaim, error)
	CompleteResourceReap(context.Context, generation.ResourceReapClaim, string) error
}

type ResourceReapExecutor interface {
	ReapCandidate(context.Context, generation.ResourceReap) error
}

type VerifierExecutor interface {
	Execute(context.Context, generation.Execution, func(context.Context, generation.VerificationEnvironment) error) (generation.VerificationReport, error)
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
	reaper    ResourceReapExecutor
	config    Config
	sleep     func(context.Context, time.Duration) error
}

func New(store Store, builderExecutor BuilderExecutor, publisherExecutor PublisherExecutor, verifierExecutor VerifierExecutor, config Config) (*Worker, error) {
	if store == nil || builderExecutor == nil || publisherExecutor == nil || verifierExecutor == nil || strings.TrimSpace(config.WorkerID) == "" {
		return nil, errors.New("runtime worker requires Server client, runtime executors, and worker id")
	}
	if config.LeaseTTL <= 0 {
		config.LeaseTTL = 45 * time.Second
	}
	if config.PollEvery <= 0 {
		config.PollEvery = time.Second
	}
	worker := &Worker{
		store: store, builder: builderExecutor, publisher: publisherExecutor, verifier: verifierExecutor,
		config: config, sleep: sleepContext,
	}
	if reapStore, ok := store.(ResourceReapStore); ok {
		if reaper, ok := publisherExecutor.(ResourceReapExecutor); ok {
			worker.reapStore = reapStore
			worker.reaper = reaper
		}
	}
	return worker, nil
}

func (w *Worker) Run(ctx context.Context) error {
	if w.reapStore != nil && w.reaper != nil {
		go w.runResourceReaper(ctx)
	}
	claimFailures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		claim, err := w.store.Claim(ctx, w.config.WorkerID, w.config.LeaseTTL)
		if err != nil {
			claimFailures++
			delay := retryDelay(claimFailures)
			slog.Warn("claim generation workflow", "worker_id", w.config.WorkerID, "retry_in", delay, "err", err)
			if err := w.sleep(ctx, delay); err != nil && ctx.Err() == nil {
				return err
			}
			continue
		}
		claimFailures = 0
		if claim == nil {
			if err := w.sleep(ctx, w.config.PollEvery); err != nil && ctx.Err() == nil {
				return err
			}
			continue
		}
		w.processClaim(ctx, *claim)
	}
}

func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	claim, err := w.store.Claim(ctx, w.config.WorkerID, w.config.LeaseTTL)
	if err != nil || claim == nil {
		return claim != nil, err
	}
	w.processClaim(ctx, *claim)
	return true, nil
}

func (w *Worker) runResourceReaper(ctx context.Context) {
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		processed, err := w.reapOne(ctx)
		if err != nil {
			failures++
			delay := retryDelay(failures)
			slog.Warn("reap generation resources", "worker_id", w.config.WorkerID, "retry_in", delay, "err", err)
			if err := w.sleep(ctx, delay); err != nil && ctx.Err() == nil {
				slog.Error("wait to retry generation resource reap", "worker_id", w.config.WorkerID, "err", err)
			}
			continue
		}
		failures = 0
		if !processed {
			if err := w.sleep(ctx, w.config.PollEvery); err != nil && ctx.Err() == nil {
				slog.Error("wait for generation resource reap", "worker_id", w.config.WorkerID, "err", err)
			}
		}
	}
}

func (w *Worker) reapOne(ctx context.Context) (bool, error) {
	if w.reapStore == nil || w.reaper == nil {
		return false, nil
	}
	for _, kind := range []generation.ResourceReapKind{
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
		if err := w.reaper.ReapCandidate(ctx, claim.ResourceReap); err != nil {
			failure = err.Error()
		}
		if err := w.reapStore.CompleteResourceReap(ctx, *claim, failure); err != nil {
			return true, err
		}
		return true, nil
	}
	return false, nil
}

func (w *Worker) processClaim(parent context.Context, initial generation.Claim) {
	started := time.Now()
	lease := newLease(initial)
	execCtx, cancel := context.WithCancel(parent)
	defer cancel()
	done := make(chan struct{})
	go w.renew(execCtx, cancel, done, lease)

	for {
		claim := lease.current()
		if lease.lost.Load() || execCtx.Err() != nil {
			break
		}
		contextSnapshot, err := w.store.Context(execCtx, claim)
		if err != nil {
			next, reportErr := w.reportError(parent, lease, claim, err)
			if reportErr == nil && next != nil {
				lease.set(*next)
				continue
			}
			break
		}
		execution := generation.Execution{Claim: claim, Context: *contextSnapshot}
		if !execution.Valid() {
			next, reportErr := w.reportError(parent, lease, claim, errors.New("server returned inconsistent generation execution"))
			if reportErr == nil && next != nil {
				lease.set(*next)
				continue
			}
			break
		}
		// A runtime action has its own local timeout. It is never persisted on
		// the workflow and a replacement attempt receives a fresh budget.
		actionCtx, actionCancel := context.WithTimeout(execCtx, domainexecution.DefaultActionDeadline)
		next, err := w.executeState(actionCtx, execution, lease)
		actionCancel()
		if err != nil {
			next, reportErr := w.reportError(parent, lease, claim, err)
			if reportErr != nil {
				break
			}
			if next == nil {
				break
			}
			lease.set(*next)
			continue
		}
		if next == nil {
			break
		}
		lease.set(*next)
	}
	close(done)
	cancel()
	fields := []any{"workflow_id", initial.Workflow.ID, "worker_id", w.config.WorkerID, "duration_seconds", time.Since(started).Seconds()}
	if lease.lost.Load() {
		slog.Info("generation workflow lease ended", fields...)
	} else {
		slog.Info("generation workflow execution ended", fields...)
	}
}

func (w *Worker) executeState(ctx context.Context, execution generation.Execution, lease *workflowLease) (*generation.Claim, error) {
	claim := execution.Claim
	switch claim.Workflow.State {
	case generation.StateBuilding:
		archive, _, err := w.store.CandidateArchive(ctx, claim)
		if err != nil {
			return nil, err
		}
		var base []byte
		if execution.Context.Candidate != nil && execution.Context.Candidate.Snapshot.Runtime == "k8s" {
			base, _, err = w.store.K8sBase(ctx, claim)
			if err != nil {
				return nil, err
			}
		}
		result, err := w.builder.Execute(ctx, execution, archive, base)
		if err != nil {
			return nil, err
		}
		return w.store.Phase(ctx, claim, app.PhaseRequest{Build: &result})

	case generation.StateArtifactPublishing:
		var archive []byte
		var err error
		if execution.Context.Candidate != nil && execution.Context.Candidate.Snapshot.Runtime == "k8s" {
			archive, _, err = w.store.BuildArchive(ctx, claim)
			if err != nil {
				return nil, err
			}
		}
		artifact, err := w.publisher.PublishArtifact(ctx, execution, archive)
		if err != nil {
			return nil, err
		}
		return w.store.Phase(ctx, claim, app.PhaseRequest{ArtifactPublish: &generation.ArtifactPublishResult{Artifact: artifact}})

	case generation.StateVerifying:
		report, err := w.verifier.Execute(ctx, execution, func(recordCtx context.Context, environment generation.VerificationEnvironment) error {
			next, phaseErr := w.store.Phase(recordCtx, lease.current(), app.PhaseRequest{
				VerificationEnvironment: &generation.VerificationEnvironmentResult{Environment: environment},
			})
			if phaseErr == nil && next != nil {
				lease.set(*next)
			}
			return phaseErr
		})
		if err != nil {
			return nil, err
		}
		return w.store.Phase(ctx, lease.current(), app.PhaseRequest{Verification: &generation.VerificationResult{Report: report}})

	case generation.StateChallengePublishing:
		artifact, err := w.publisher.PublishChallenge(ctx, execution)
		if err != nil {
			return nil, err
		}
		return w.store.Phase(ctx, claim, app.PhaseRequest{ChallengePublish: &generation.ChallengePublishResult{Artifact: artifact}})

	default:
		return nil, fmt.Errorf("generate worker cannot execute workflow state %s", claim.Workflow.State)
	}
}

func (w *Worker) reportError(ctx context.Context, lease *workflowLease, claim generation.Claim, executionErr error) (*generation.Claim, error) {
	if lease.lost.Load() || errors.Is(executionErr, generation.ErrLeaseLost) {
		return nil, nil
	}
	select {
	case <-ctx.Done():
		return nil, nil
	default:
	}
	request := app.PhaseRequest{}
	var artifact *generation.ArtifactError
	var sharedArtifact *domainexecution.ArtifactError
	if errors.As(executionErr, &artifact) {
		request.ArtifactFailure = &generation.ArtifactFailureResult{Failure: artifact.Failure, Report: artifact.Report}
	} else if errors.As(executionErr, &sharedArtifact) {
		request.ArtifactFailure = &generation.ArtifactFailureResult{Failure: generation.Failure{
			Class: generation.FailureArtifact, Code: sharedArtifact.Code, Summary: sharedArtifact.Summary,
		}, Report: sharedArtifact.Report}
	} else {
		summary := strings.TrimSpace(executionErr.Error())
		if summary == "" {
			summary = "generation workflow phase failed"
		}
		request.InfrastructureFailure = &generation.InfrastructureFailureResult{Failure: generation.Failure{
			Class: generation.FailureInfrastructure, Code: "PHASE_EXECUTION_FAILED", Summary: summary,
		}}
	}
	next, err := w.store.Phase(ctx, claim, request)
	if err != nil && !errors.Is(err, generation.ErrLeaseLost) {
		slog.Error("report generation workflow failure", "workflow_id", claim.Workflow.ID, "state", claim.Workflow.State, "err", err)
	}
	if errors.Is(err, generation.ErrLeaseLost) {
		return nil, nil
	}
	return next, err
}

func (w *Worker) renew(ctx context.Context, cancel context.CancelFunc, done <-chan struct{}, lease *workflowLease) {
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
			err := w.store.Renew(requestCtx, lease.current(), w.config.LeaseTTL)
			requestCancel()
			if err != nil {
				select {
				case <-done:
					return
				default:
				}
				lease.lost.Store(true)
				cancel()
				slog.Warn("generation workflow lease renewal failed", "workflow_id", lease.current().Workflow.ID, "err", err)
				return
			}
		}
	}
}

type workflowLease struct {
	mu    sync.RWMutex
	claim generation.Claim
	lost  atomic.Bool
}

func newLease(claim generation.Claim) *workflowLease { return &workflowLease{claim: claim} }

func (l *workflowLease) current() generation.Claim {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.claim
}

func (l *workflowLease) set(claim generation.Claim) {
	l.mu.Lock()
	l.claim = claim
	l.mu.Unlock()
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
