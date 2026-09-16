package generation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

const (
	defaultGenerationAgentLeaseTTL = 45 * time.Second
	defaultGenerationAgentPoll     = time.Second
)

// GenerationAgentStore is the workflow-owned persistence boundary for the
// Server's Judge role. It deliberately contains no Generator
// workspace, runtime build, registry, Incus, or Environment operation.
type GenerationAgentStore interface {
	ClaimGenerationAgentWorkflow(context.Context, string, time.Duration, time.Time) (*domain.Claim, error)
	LoadGenerationContext(context.Context, domain.Claim, time.Time) (*domain.Context, error)
	StartGenerationAgentRun(context.Context, domain.Claim, agent.CreateRun, time.Time) (*agent.Run, error)
	RetryGenerationAgentRun(context.Context, domain.Claim, string, string, time.Time) (*agent.Run, error)
	InterruptActiveGenerationAgentRuns(context.Context, string, time.Time) error
	GetCandidateRevision(context.Context, string) (*domain.Revision, error)
	FinalizeGenerationJudgement(context.Context, domain.Claim, string, bool, string, time.Time) error
	ReportGenerationArtifactFailure(context.Context, domain.Claim, domain.WorkflowState, domain.Failure, time.Time) error
	RenewGenerationLease(context.Context, domain.Claim, time.Duration, time.Time) error
}

// JudgeRoleExecutor owns the Server-internal review of a submitted candidate.
// Generator clients submit candidates through GeneratorService instead.
type JudgeRoleExecutor interface {
	Judge(context.Context, authoring.Plan, *Candidate) (Judgement, error)
}

type AgentRunnerConfig struct {
	ServerID  string
	Model     string
	LeaseTTL  time.Duration
	PollEvery time.Duration
}

// AgentRunner is the Server's durable background Agent Runtime for exactly
// Judging. Generating is user-directed workspace activity and
// is never claimed by this runner. Each claimed phase is dispatched
// independently; the database workflow remains the scheduling authority.
type AgentRunner struct {
	store  GenerationAgentStore
	judge  JudgeRoleExecutor
	config AgentRunnerConfig
	now    func() time.Time
	sleep  func(context.Context, time.Duration) error
}

func NewAgentRunner(store GenerationAgentStore, judge JudgeRoleExecutor, cfg AgentRunnerConfig) (*AgentRunner, error) {
	if store == nil || judge == nil || strings.TrimSpace(cfg.ServerID) == "" || strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("generation agent runner requires Server store, Judge executor, identity, and model")
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = defaultGenerationAgentLeaseTTL
	}
	if cfg.PollEvery <= 0 {
		cfg.PollEvery = defaultGenerationAgentPoll
	}
	return &AgentRunner{
		store: store, judge: judge, config: cfg,
		now: func() time.Time { return time.Now().UTC() }, sleep: waitContext,
	}, nil
}

// Recover marks every in-flight Server AgentRun Interrupted before this Server
// starts claiming work. Generator workspaces are retired separately by
// WorkspaceReaper because a user-directed turn has no Server AgentRun.
func (r *AgentRunner) Recover(ctx context.Context) error {
	if r == nil {
		return errors.New("generation agent runner is required")
	}
	if err := r.store.InterruptActiveGenerationAgentRuns(ctx, "server restarted before agent completion", r.now()); err != nil {
		return fmt.Errorf("interrupt active generation agent runs: %w", err)
	}
	return nil
}

func (r *AgentRunner) Run(ctx context.Context) error {
	if r == nil {
		return errors.New("generation agent runner is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var active sync.WaitGroup
	defer active.Wait()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		claim, err := r.claimOne(ctx)
		if err != nil {
			failures++
			delay := domain.NextRetry(failures, r.now()).Sub(r.now())
			slog.Warn("run generation agent", "server_id", r.config.ServerID, "retry_in", delay, "err", err)
			if waitErr := r.sleep(ctx, delay); waitErr != nil {
				if ctx.Err() != nil {
					return nil
				}
				return waitErr
			}
			continue
		}
		failures = 0
		if claim == nil {
			if waitErr := r.sleep(ctx, r.config.PollEvery); waitErr != nil {
				if ctx.Err() != nil {
					return nil
				}
				return waitErr
			}
			continue
		}
		active.Add(1)
		go func(value domain.Claim) {
			defer active.Done()
			_ = r.processClaim(ctx, value)
		}(*claim)
	}
}

// ProcessOne is intentionally exposed for focused tests and local Server
// assembly. It performs one claim and waits for that phase; the background
// Run method dispatches the same phase asynchronously.
func (r *AgentRunner) ProcessOne(ctx context.Context) (bool, error) {
	if r == nil {
		return false, errors.New("generation agent runner is required")
	}
	claim, err := r.claimOne(ctx)
	if err != nil || claim == nil {
		return claim != nil, err
	}
	return true, r.processClaim(ctx, *claim)
}

func (r *AgentRunner) claimOne(ctx context.Context) (*domain.Claim, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return r.store.ClaimGenerationAgentWorkflow(ctx, r.config.ServerID, r.config.LeaseTTL, r.now())
}

func (r *AgentRunner) processClaim(parent context.Context, claim domain.Claim) error {
	lease := newAgentLease(claim)
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	done := make(chan struct{})
	go r.renew(ctx, cancel, done, lease)
	defer close(done)

	err := r.executeClaim(ctx, lease)
	if err != nil && !errors.Is(err, domain.ErrLeaseLost) && ctx.Err() == nil {
		slog.Error("generation agent phase stopped", "workflow_id", claim.Workflow.ID, "state", claim.Workflow.State, "err", err)
	}
	return err
}

func (r *AgentRunner) executeClaim(ctx context.Context, lease *agentLease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	claim := lease.current()
	workflowContext, err := r.store.LoadGenerationContext(ctx, claim, r.now())
	if err != nil {
		return err
	}
	if !(domain.Execution{Claim: claim, Context: *workflowContext}).Valid() {
		return errors.New("generation agent runner received inconsistent workflow context")
	}
	run, err := r.store.StartGenerationAgentRun(ctx, claim, agent.CreateRun{
		ID:            agent.NewID("generation-agent-run"),
		Purpose:       generationPurpose(claim.Workflow.State),
		OwnerKind:     "generation-workflow",
		OwnerRef:      claim.Workflow.ID,
		InputRevision: generationInputRevision(claim.Workflow),
		Model:         r.config.Model,
		PromptVersion: generationPromptVersion(claim.Workflow.State),
	}, r.now())
	if err != nil {
		return err
	}
	for run != nil && !lease.lost.Load() {
		attemptCtx, cancel := context.WithDeadline(ctx, run.DeadlineAt)
		err := r.executeRun(attemptCtx, lease.current(), *workflowContext, *run)
		cancel()
		if err == nil || errors.Is(err, domain.ErrLeaseLost) || lease.lost.Load() {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var artifact *domain.ArtifactError
		if errors.As(err, &artifact) {
			return r.store.ReportGenerationArtifactFailure(context.Background(), lease.current(), lease.current().Workflow.State, artifact.Failure, r.now())
		}
		next, retryErr := r.store.RetryGenerationAgentRun(context.Background(), lease.current(), run.ID, err.Error(), r.now())
		if retryErr != nil {
			return retryErr
		}
		if next == nil {
			return nil
		}
		run = next
	}
	return domain.ErrLeaseLost
}

func (r *AgentRunner) executeRun(ctx context.Context, claim domain.Claim, workflowContext domain.Context, run agent.Run) error {
	claim.Workflow.ActiveAgentRunID = run.ID
	workflowContext.Workflow.ActiveAgentRunID = run.ID
	if !(domain.Execution{Claim: claim, Context: workflowContext}).Valid() {
		return errors.New("generation agent run has inconsistent execution context")
	}
	switch claim.Workflow.State {
	case domain.StateJudging:
		candidateValue, err := r.readCandidate(ctx, claim.Workflow.CandidateRevisionID)
		if err != nil {
			return err
		}
		judgement, err := r.judge.Judge(ctx, workflowContext.Plan, candidateValue)
		if err != nil {
			return err
		}
		if err := judgement.Validate(); err != nil {
			return err
		}
		return r.store.FinalizeGenerationJudgement(ctx, claim, run.ID, judgement.Approved, judgement.Feedback, r.now())
	default:
		return fmt.Errorf("generation agent runner cannot execute %s", claim.Workflow.State)
	}
}

func (r *AgentRunner) readCandidate(ctx context.Context, id string) (*Candidate, error) {
	if strings.TrimSpace(id) == "" {
		return nil, domain.NewArtifactError("CANDIDATE_INVALID", "generation workflow has no candidate revision")
	}
	revision, err := r.store.GetCandidateRevision(ctx, id)
	if err != nil {
		return nil, err
	}
	archive, err := candidate.ReadArchive(revision.ArchivePath, revision.ArchiveDigest)
	if err != nil {
		return nil, fmt.Errorf("read candidate archive: %w", err)
	}
	value, err := InspectCandidateArchive(archive)
	if err != nil {
		return nil, domain.NewArtifactError("CANDIDATE_INVALID", err.Error())
	}
	return value, nil
}

func (r *AgentRunner) renew(ctx context.Context, cancel context.CancelFunc, done <-chan struct{}, lease *agentLease) {
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
			requestCtx, requestCancel := context.WithTimeout(ctx, minDuration(r.config.LeaseTTL/2, 10*time.Second))
			err := r.store.RenewGenerationLease(requestCtx, lease.current(), r.config.LeaseTTL, r.now())
			requestCancel()
			if err == nil {
				continue
			}
			lease.lost.Store(true)
			cancel()
			slog.Warn("generation agent lease renewal failed", "workflow_id", lease.current().Workflow.ID, "err", err)
			return
		}
	}
}

type agentLease struct {
	mu    sync.RWMutex
	claim domain.Claim
	lost  atomic.Bool
}

func newAgentLease(claim domain.Claim) *agentLease { return &agentLease{claim: claim} }

func (l *agentLease) current() domain.Claim {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.claim
}

func generationPurpose(state domain.WorkflowState) string {
	switch state {
	case domain.StateJudging:
		return JudgePurpose
	default:
		return ""
	}
}

func generationPromptVersion(state domain.WorkflowState) string {
	switch state {
	case domain.StateJudging:
		return JudgePromptVersion
	default:
		return ""
	}
}

func generationInputRevision(workflow domain.Workflow) string {
	parts := []string{workflow.SourceRevision, workflow.CandidateRevisionID}
	return strings.Join(parts, ":")
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
