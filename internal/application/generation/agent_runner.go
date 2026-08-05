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
	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

const (
	defaultGenerationAgentLeaseTTL = 45 * time.Second
	defaultGenerationAgentPoll     = time.Second
)

// GenerationAgentStore is the workflow-owned persistence boundary for the
// Server's Generator, Judge, and Classifier roles. It deliberately contains
// no runtime build, registry, Incus, or Environment operation.
type GenerationAgentStore interface {
	ClaimGenerationAgentWorkflow(context.Context, string, time.Duration, time.Time) (*domain.Claim, error)
	GetGenerationClaim(context.Context, string, domain.LeaseCredential, time.Time) (*domain.Claim, error)
	LoadGenerationContext(context.Context, domain.Claim, time.Time) (*domain.Context, error)
	StartGenerationAgentRun(context.Context, domain.Claim, agent.CreateRun, time.Time) (*agent.Run, error)
	RetryGenerationAgentRun(context.Context, domain.Claim, string, string, time.Time) (*agent.Run, error)
	InterruptActiveGenerationAgentRuns(context.Context, string, time.Time) ([]domain.InterruptedAgentRun, error)
	GetCandidateRevision(context.Context, string) (*domain.Revision, error)
	FinalizeGeneratedCandidate(context.Context, domain.Claim, string, domain.Revision, time.Time) error
	FinalizeGenerationJudgement(context.Context, domain.Claim, string, bool, string, time.Time) error
	FinalizeGenerationClassification(context.Context, domain.Claim, string, domain.ClassificationOutput, time.Time) error
	FinalizeGenerationClassificationAdjustment(context.Context, domain.Claim, domain.ClassificationAdjustment, time.Time) error
	ReportGenerationArtifactFailure(context.Context, domain.Claim, domain.WorkflowState, domain.Failure, *domain.VerificationReport, time.Time) error
	RenewGenerationLease(context.Context, domain.Claim, time.Duration, time.Time) error
}

// GeneratorRoleExecutor is the role-specific Eino boundary. Generator and
// Judge intentionally share no mutable workflow implementation with
// Classifier: their prompts, typed results, and tools differ.
type GeneratorRoleExecutor interface {
	Generate(context.Context, domain.Execution) ([]byte, error)
	Judge(context.Context, authoring.Plan, *Candidate) (Judgement, error)
}

// ClassifierRoleExecutor owns only the independent classification role.
type ClassifierRoleExecutor interface {
	Classify(context.Context, domain.Execution, *Candidate) (ClassificationCompletion, error)
}

// ExecutionSnapshotter freezes the runtime facts that an accepted candidate
// will use later. It is injected from Server assembly so this package does not
// receive registry, Incus, or Kubernetes clients.
type ExecutionSnapshotter func(challenge.Entry) (domain.ExecutionSnapshot, error)

type AgentRunnerConfig struct {
	ServerID        string
	Model           string
	DataDir         string
	LeaseTTL        time.Duration
	PollEvery       time.Duration
	FreezeExecution ExecutionSnapshotter
}

// AgentRunner is the Server's durable background Agent Runtime for exactly
// Generating, Judging, and Classifying. It is not a worker pool or a generic
// task executor: the GenerationWorkflow remains the scheduling authority.
type AgentRunner struct {
	store      GenerationAgentStore
	generator  GeneratorRoleExecutor
	classifier ClassifierRoleExecutor
	workspace  *Manager
	config     AgentRunnerConfig
	now        func() time.Time
	sleep      func(context.Context, time.Duration) error
}

func NewAgentRunner(store GenerationAgentStore, generator GeneratorRoleExecutor, classifier ClassifierRoleExecutor, workspace *Manager, cfg AgentRunnerConfig) (*AgentRunner, error) {
	if store == nil || generator == nil || classifier == nil || workspace == nil || strings.TrimSpace(cfg.ServerID) == "" || strings.TrimSpace(cfg.Model) == "" ||
		strings.TrimSpace(cfg.DataDir) == "" || cfg.FreezeExecution == nil {
		return nil, errors.New("generation agent runner requires Server store, role executors, workspace manager, identity, model, data directory, and runtime snapshotter")
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = defaultGenerationAgentLeaseTTL
	}
	if cfg.PollEvery <= 0 {
		cfg.PollEvery = defaultGenerationAgentPoll
	}
	return &AgentRunner{
		store: store, generator: generator, classifier: classifier, workspace: workspace, config: cfg,
		now: func() time.Time { return time.Now().UTC() }, sleep: waitContext,
	}, nil
}

// Recover marks every in-flight Server AgentRun Interrupted before this Server
// starts claiming work. A Generator interruption retires its workflow-owned
// workspace so the replacement run creates a fresh PVC/Sandbox from durable
// candidate and Plan facts instead of resuming an unknown remote process.
func (r *AgentRunner) Recover(ctx context.Context) error {
	if r == nil {
		return errors.New("generation agent runner is required")
	}
	interrupted, err := r.store.InterruptActiveGenerationAgentRuns(ctx, "server restarted before agent completion", r.now())
	if err != nil {
		return fmt.Errorf("interrupt active generation agent runs: %w", err)
	}
	for _, value := range interrupted {
		if value.State != domain.StateGenerating {
			continue
		}
		if err := r.workspace.Retire(ctx, value.WorkflowID); err != nil {
			return fmt.Errorf("retire interrupted generator workspace %s: %w", value.WorkflowID, err)
		}
	}
	return nil
}

func (r *AgentRunner) Run(ctx context.Context) error {
	if r == nil {
		return errors.New("generation agent runner is required")
	}
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		processed, err := r.ProcessOne(ctx)
		if err != nil {
			failures++
			delay := domain.NextRetry(failures, r.now()).Sub(r.now())
			slog.Warn("run generation agent", "server_id", r.config.ServerID, "retry_in", delay, "err", err)
			if err := r.sleep(ctx, delay); err != nil && ctx.Err() == nil {
				return err
			}
			continue
		}
		failures = 0
		if !processed {
			if err := r.sleep(ctx, r.config.PollEvery); err != nil && ctx.Err() == nil {
				return err
			}
		}
	}
}

// ProcessOne is intentionally exposed for focused tests and local Server
// assembly. It performs at most one phase claim and never touches runtime
// states such as Build or Verify.
func (r *AgentRunner) ProcessOne(ctx context.Context) (bool, error) {
	if r == nil {
		return false, errors.New("generation agent runner is required")
	}
	claim, err := r.store.ClaimGenerationAgentWorkflow(ctx, r.config.ServerID, r.config.LeaseTTL, r.now())
	if err != nil || claim == nil {
		return claim != nil, err
	}
	return true, r.processClaim(ctx, *claim)
}

func (r *AgentRunner) processClaim(parent context.Context, claim domain.Claim) error {
	lease := newAgentLease(claim)
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	done := make(chan struct{})
	go r.renew(ctx, cancel, done, lease)
	defer close(done)

	err := r.executeClaim(ctx, lease)
	if errors.Is(err, domain.ErrLeaseLost) && claim.Workflow.State == domain.StateGenerating {
		if retireErr := r.workspace.Retire(context.Background(), claim.Workflow.ID); retireErr != nil {
			return fmt.Errorf("retire lost generator workspace: %w", retireErr)
		}
	}
	if err != nil && !errors.Is(err, domain.ErrLeaseLost) && ctx.Err() == nil {
		slog.Error("generation agent phase stopped", "workflow_id", claim.Workflow.ID, "state", claim.Workflow.State, "err", err)
	}
	return err
}

func (r *AgentRunner) executeClaim(ctx context.Context, lease *agentLease) error {
	claim := lease.current()
	workflowContext, err := r.store.LoadGenerationContext(ctx, claim, r.now())
	if err != nil {
		return err
	}
	execution := domain.Execution{Claim: claim, Context: *workflowContext}
	if !execution.Valid() {
		return errors.New("generation agent runner received inconsistent workflow context")
	}
	if claim.Workflow.State == domain.StateGenerating && strings.TrimSpace(claim.Workflow.ActiveAgentRunID) != "" {
		if err := r.workspace.Retire(ctx, claim.Workflow.ID); err != nil {
			return fmt.Errorf("retire interrupted generator workspace: %w", err)
		}
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
		var artifact *domain.ArtifactError
		if errors.As(err, &artifact) {
			return r.store.ReportGenerationArtifactFailure(context.Background(), lease.current(), lease.current().Workflow.State, artifact.Failure, artifact.Report, r.now())
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
	execution := domain.Execution{Claim: claim, Context: workflowContext}
	if !execution.Valid() {
		return errors.New("generation agent run has inconsistent execution context")
	}
	switch claim.Workflow.State {
	case domain.StateGenerating:
		archive, err := r.generator.Generate(ctx, execution)
		if err != nil {
			return err
		}
		return r.finalizeGeneratedCandidate(ctx, claim, run, archive)
	case domain.StateJudging:
		candidateValue, err := r.readCandidate(ctx, claim.Workflow.CandidateRevisionID)
		if err != nil {
			return err
		}
		judgement, err := r.generator.Judge(ctx, workflowContext.Plan, candidateValue)
		if err != nil {
			return err
		}
		if err := judgement.Validate(); err != nil {
			return err
		}
		return r.store.FinalizeGenerationJudgement(ctx, claim, run.ID, judgement.Approved, judgement.Feedback, r.now())
	case domain.StateClassifying:
		candidateValue, err := r.readCandidate(ctx, claim.Workflow.CandidateRevisionID)
		if err != nil {
			return err
		}
		completion, err := r.classifier.Classify(ctx, execution, candidateValue)
		if err != nil {
			return err
		}
		if err := completion.Validate(); err != nil {
			return err
		}
		if completion.Initial != nil {
			return r.store.FinalizeGenerationClassification(ctx, claim, run.ID, *completion.Initial, r.now())
		}
		adjustment := *completion.Adjustment
		adjustment.RunID = run.ID
		return r.store.FinalizeGenerationClassificationAdjustment(ctx, claim, adjustment, r.now())
	default:
		return fmt.Errorf("generation agent runner cannot execute %s", claim.Workflow.State)
	}
}

func (r *AgentRunner) finalizeGeneratedCandidate(ctx context.Context, claim domain.Claim, run agent.Run, archive []byte) error {
	inspected, err := InspectCandidateArchive(archive)
	if err != nil {
		return domain.NewArtifactError("CANDIDATE_INVALID", err.Error())
	}
	snapshot, err := r.config.FreezeExecution(inspected.Entry)
	if err != nil {
		return domain.NewArtifactError("CANDIDATE_RUNTIME_INVALID", err.Error())
	}
	id := domain.IDForGeneratorRun(run.ID)
	path, digest, err := candidate.SaveArchiveAtomic(r.config.DataDir, id, inspected.Archive)
	if err != nil {
		return fmt.Errorf("persist generated candidate archive: %w", err)
	}
	revision := domain.Revision{
		ID: id, Source: claim.Workflow.Source, SourceRevision: claim.Workflow.SourceRevision,
		GeneratorRunID: run.ID, ArchivePath: path, ArchiveSHA256: digest, Snapshot: snapshot,
	}
	return r.store.FinalizeGeneratedCandidate(ctx, claim, run.ID, revision, r.now())
}

func (r *AgentRunner) readCandidate(ctx context.Context, id string) (*Candidate, error) {
	if strings.TrimSpace(id) == "" {
		return nil, domain.NewArtifactError("CANDIDATE_INVALID", "generation workflow has no candidate revision")
	}
	revision, err := r.store.GetCandidateRevision(ctx, id)
	if err != nil {
		return nil, err
	}
	archive, err := candidate.ReadArchive(revision.ArchivePath, revision.ArchiveSHA256)
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
	case domain.StateGenerating:
		return GeneratorPurpose
	case domain.StateJudging:
		return JudgePurpose
	case domain.StateClassifying:
		return ClassifierPurpose
	default:
		return ""
	}
}

func generationPromptVersion(state domain.WorkflowState) string {
	switch state {
	case domain.StateGenerating:
		return GeneratorPromptVersion
	case domain.StateJudging:
		return JudgePromptVersion
	case domain.StateClassifying:
		return ClassifierPromptVersion
	default:
		return ""
	}
}

func generationInputRevision(workflow domain.Workflow) string {
	parts := []string{workflow.SourceRevision, workflow.CandidateRevisionID, workflow.ClassificationRoadmapRevision}
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
