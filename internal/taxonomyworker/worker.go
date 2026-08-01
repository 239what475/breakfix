// Package taxonomyworker executes one complete TaxonomyWorkflow lease at a
// time. Server remains the only database and taxonomy-filesystem writer.
package taxonomyworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/llm"
	taxonomyapp "github.com/breakfix/breakfix/internal/application/taxonomy"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/domain/taxonomy"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type Store interface {
	Claim(context.Context, string, time.Duration) (*taxonomy.Claim, error)
	Renew(context.Context, taxonomy.Claim, time.Duration) error
	Context(context.Context, taxonomy.Claim) (*taxonomyapp.Context, error)
	StartAgentRun(context.Context, taxonomy.Claim, taxonomyapp.AgentRole, string) (*taxonomyapp.StartAgentRunResponse, error)
	Phase(context.Context, taxonomy.Claim, taxonomyapp.PhaseRequest) (*taxonomy.Claim, error)
}

// Committee executes the model-facing part of one taxonomy state. The worker
// owns persistence, ordering, retry, and lease fencing; this interface keeps
// those guarantees testable without replacing the model protocol with a fake
// prompt test.
type Committee interface {
	Map(context.Context, taxonomyapp.Context) (taxonomy.ChangeSet, error)
	Review(context.Context, taxonomyapp.Context) (taxonomy.Review, taxonomy.Review, error)
}

type Config struct {
	WorkerID  string
	LeaseTTL  time.Duration
	PollEvery time.Duration
}

type Worker struct {
	store     Store
	committee Committee
	agent     config.AgentConfig
	config    Config
	sleep     func(context.Context, time.Duration) error
}

func New(store Store, agent config.AgentConfig, worker Config) (*Worker, error) {
	return NewWithCommittee(store, newEinoCommittee(agent), agent, worker)
}

func NewWithCommittee(store Store, committee Committee, agent config.AgentConfig, worker Config) (*Worker, error) {
	if store == nil || committee == nil || strings.TrimSpace(agent.Model) == "" || strings.TrimSpace(worker.WorkerID) == "" {
		return nil, errors.New("taxonomy worker requires Server client, committee, model, and worker id")
	}
	if worker.LeaseTTL <= 0 {
		worker.LeaseTTL = 45 * time.Second
	}
	if worker.PollEvery <= 0 {
		worker.PollEvery = time.Second
	}
	return &Worker{store: store, committee: committee, agent: agent, config: worker, sleep: sleepContext}, nil
}

func (w *Worker) Run(ctx context.Context) error {
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
			slog.Warn("claim taxonomy workflow", "worker_id", w.config.WorkerID, "retry_in", delay, "err", err)
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

func (w *Worker) processClaim(parent context.Context, initial taxonomy.Claim) {
	lease := newLease(initial)
	execCtx, cancel := context.WithCancel(parent)
	defer cancel()
	done := make(chan struct{})
	go w.renew(execCtx, cancel, done, lease)
	defer close(done)

	for {
		claim := lease.current()
		if lease.lost.Load() || execCtx.Err() != nil {
			return
		}
		contextSnapshot, err := w.store.Context(execCtx, claim)
		if err != nil {
			next, reportErr := w.reportFailure(parent, claim, nil, err)
			if reportErr == nil && next != nil {
				lease.set(*next)
				continue
			}
			return
		}
		next, err := w.executeState(execCtx, claim, *contextSnapshot)
		if err != nil {
			nextClaim, reportErr := w.reportFailure(parent, claim, failureRunIDs(err), unwrapExecutionFailure(err))
			if reportErr == nil && nextClaim != nil {
				lease.set(*nextClaim)
				continue
			}
			return
		}
		if next == nil {
			return
		}
		lease.set(*next)
	}
}

func (w *Worker) executeState(ctx context.Context, claim taxonomy.Claim, snapshot taxonomyapp.Context) (*taxonomy.Claim, error) {
	switch claim.Workflow.State {
	case taxonomy.WorkflowMapping:
		run, err := w.store.StartAgentRun(ctx, claim, taxonomyapp.AgentRoleMapper, w.agent.Model)
		if err != nil {
			return nil, err
		}
		changes, err := w.committee.Map(ctx, snapshot)
		if err != nil {
			return nil, &executionFailure{runIDs: []string{run.Run.ID}, cause: err}
		}
		return w.store.Phase(ctx, claim, taxonomyapp.PhaseRequest{Mapper: &taxonomyapp.MapperResult{RunID: run.Run.ID, ChangeSet: changes}})
	case taxonomy.WorkflowReviewing:
		curriculumRun, err := w.store.StartAgentRun(ctx, claim, taxonomyapp.AgentRoleCurriculumReviewer, w.agent.Model)
		if err != nil {
			return nil, err
		}
		sreRun, err := w.store.StartAgentRun(ctx, claim, taxonomyapp.AgentRoleSREReviewer, w.agent.Model)
		if err != nil {
			return nil, &executionFailure{runIDs: []string{curriculumRun.Run.ID}, cause: err}
		}
		curriculum, sre, err := w.committee.Review(ctx, snapshot)
		if err != nil {
			return nil, &executionFailure{runIDs: []string{curriculumRun.Run.ID, sreRun.Run.ID}, cause: err}
		}
		return w.store.Phase(ctx, claim, taxonomyapp.PhaseRequest{ReviewPair: &taxonomyapp.ReviewPairResult{
			CurriculumRunID: curriculumRun.Run.ID, SRERunID: sreRun.Run.ID, Curriculum: curriculum, SRE: sre,
		}})
	case taxonomy.WorkflowPublishing:
		return w.store.Phase(ctx, claim, taxonomyapp.PhaseRequest{Publication: &taxonomyapp.PublicationResult{}})
	default:
		return nil, fmt.Errorf("taxonomy worker cannot execute workflow state %s", claim.Workflow.State)
	}
}

func (w *Worker) reportFailure(ctx context.Context, claim taxonomy.Claim, runIDs []string, cause error) (*taxonomy.Claim, error) {
	if errors.Is(cause, taxonomy.ErrLeaseLost) {
		return nil, nil
	}
	select {
	case <-ctx.Done():
		return nil, nil
	default:
	}
	message := strings.TrimSpace(cause.Error())
	if message == "" {
		message = "taxonomy workflow technical failure"
	}
	next, err := w.store.Phase(ctx, claim, taxonomyapp.PhaseRequest{TechnicalFailure: &taxonomyapp.TechnicalFailure{Message: message, RunIDs: runIDs}})
	if err != nil && !errors.Is(err, taxonomy.ErrLeaseLost) {
		slog.Error("report taxonomy workflow technical failure", "workflow_id", claim.Workflow.ID, "state", claim.Workflow.State, "err", err)
	}
	if errors.Is(err, taxonomy.ErrLeaseLost) {
		return nil, nil
	}
	return next, err
}

type executionFailure struct {
	runIDs []string
	cause  error
}

func (e *executionFailure) Error() string { return e.cause.Error() }

func (e *executionFailure) Unwrap() error { return e.cause }

func failureRunIDs(err error) []string {
	var failure *executionFailure
	if errors.As(err, &failure) {
		return failure.runIDs
	}
	return nil
}

func unwrapExecutionFailure(err error) error {
	var failure *executionFailure
	if errors.As(err, &failure) {
		return failure.cause
	}
	return err
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
				lease.lost.Store(true)
				cancel()
				return
			}
		}
	}
}

type workflowLease struct {
	mu    sync.RWMutex
	claim taxonomy.Claim
	lost  atomic.Bool
}

func newLease(claim taxonomy.Claim) *workflowLease { return &workflowLease{claim: claim} }

func (l *workflowLease) current() taxonomy.Claim {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.claim
}

func (l *workflowLease) set(claim taxonomy.Claim) {
	l.mu.Lock()
	l.claim = claim
	l.mu.Unlock()
}

type reviewerResult struct {
	Approval  *reviewApproval  `json:"approval,omitempty" jsonschema:"oneof_required=approval" jsonschema_description:"候选通过"`
	Rejection *reviewRejection `json:"rejection,omitempty" jsonschema:"oneof_required=rejection" jsonschema_description:"候选需要修订"`
}

type reviewApproval struct{}

type reviewRejection struct {
	Feedback string `json:"feedback" jsonschema:"required" jsonschema_description:"具体且可执行的中文修订意见"`
}

func (r reviewerResult) Validate() error {
	if (r.Approval == nil) == (r.Rejection == nil) {
		return errors.New("review result must contain exactly one of approval or rejection")
	}
	if r.Rejection != nil && strings.TrimSpace(r.Rejection.Feedback) == "" {
		return errors.New("rejection requires feedback")
	}
	return nil
}

func (r reviewerResult) Review() taxonomy.Review {
	if r.Approval != nil {
		return taxonomy.Review{Decision: taxonomy.ReviewApprove}
	}
	return taxonomy.Review{Decision: taxonomy.ReviewReject, Feedback: strings.TrimSpace(r.Rejection.Feedback)}
}

func runMapper(ctx context.Context, cfg config.AgentConfig, input taxonomyapp.ModelInput, validation taxonomyapp.MapperValidation) (taxonomy.ChangeSet, error) {
	var accepted taxonomy.ChangeSet
	_, err := invokeTypedResult(ctx, cfg, "taxonomy_mapper", input, "submit_mapping", "提交本轮最终 taxonomy mapping。仅在完成分类判断后调用；所有字段以工具 schema 为准。", func(value mapperResult) error {
		changes, err := value.ChangeSet(validation)
		if err != nil {
			return err
		}
		if _, err := taxonomy.ValidateWorkflowChangeSet(changes, validation.Challenge, validation.Base); err != nil {
			return err
		}
		accepted = changes
		return nil
	})
	if err != nil {
		return taxonomy.ChangeSet{}, err
	}
	return accepted, nil
}

func runReviewPair(ctx context.Context, cfg config.AgentConfig, curriculumInput, sreInput taxonomyapp.ModelInput) (taxonomy.Review, taxonomy.Review, error) {
	type outcome struct {
		review taxonomy.Review
		err    error
	}
	var curriculum, sre outcome
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		value, err := invokeTypedResult(ctx, cfg, "taxonomy_curriculum_reviewer", curriculumInput, "submit_curriculum_review", "提交最终教学审查结论。没有实质问题时批准；否则拒绝并提供可执行的中文反馈。", func(value reviewerResult) error {
			return value.Validate()
		})
		if err != nil {
			curriculum.err = err
			return
		}
		curriculum.review = value.Review()
	}()
	go func() {
		defer wait.Done()
		value, err := invokeTypedResult(ctx, cfg, "taxonomy_sre_reviewer", sreInput, "submit_sre_review", "提交最终 SRE 审查结论。没有实质问题时批准；否则拒绝并提供可执行的中文反馈。", func(value reviewerResult) error {
			return value.Validate()
		})
		if err != nil {
			sre.err = err
			return
		}
		sre.review = value.Review()
	}()
	wait.Wait()
	if curriculum.err != nil {
		return taxonomy.Review{}, taxonomy.Review{}, fmt.Errorf("run curriculum reviewer: %w", curriculum.err)
	}
	if sre.err != nil {
		return taxonomy.Review{}, taxonomy.Review{}, fmt.Errorf("run SRE reviewer: %w", sre.err)
	}
	return curriculum.review, sre.review, nil
}

func invokeTypedResult[T any](ctx context.Context, cfg config.AgentConfig, name string, input taxonomyapp.ModelInput, toolName, toolDescription string, validate func(T) error) (T, error) {
	var zero T
	chat, err := llm.NewChatModel(ctx, cfg)
	if err != nil {
		return zero, err
	}
	resultTool, err := llm.NewResultTool[T](toolName, toolDescription, validate)
	if err != nil {
		return zero, err
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          name,
		Description:   "Breakfix taxonomy committee agent",
		Instruction:   input.SystemPrompt,
		Model:         chat,
		MaxIterations: 8,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{resultTool}},
		},
		ModelRetryConfig: &adk.ModelRetryConfig{MaxRetries: 3, IsRetryAble: func(_ context.Context, err error) bool {
			return llm.IsTransientTransportError(err)
		}},
	})
	if err != nil {
		return zero, fmt.Errorf("create taxonomy agent: %w", err)
	}
	runner := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	events := runner.Run(ctx, []adk.Message{schema.UserMessage(input.Prompt)})
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return zero, event.Err
		}
		if event.Action != nil && event.Action.Exit {
			break
		}
	}
	value, called := resultTool.Value()
	if !called {
		return zero, errors.New("taxonomy agent did not submit its typed result")
	}
	return value, nil
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
