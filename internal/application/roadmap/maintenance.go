// Package roadmap owns application-level Roadmap retrieval and maintenance.
// Its maintenance service is deliberately Server-owned: PostgreSQL carries
// durable workflow state while this package coordinates only one fixed task at
// a time and never creates a second worker queue.
package roadmap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/breakfix/breakfix/internal/domain/agent"
	domain "github.com/breakfix/breakfix/internal/domain/roadmap"
)

const (
	defaultMaintenancePollInterval = 2 * time.Second
	minimumMaintenanceLeaseTTL     = 5 * time.Second
)

// MaintenanceRepository is the narrow durable boundary used by the Server's
// Roadmap maintenance loop. It intentionally exposes only Roadmap workflow
// methods, not a general database or job-executor API.
type MaintenanceRepository interface {
	TryStartRoadmapWorkflow(context.Context, time.Time) (*domain.Workflow, error)
	GetRoadmapWorkflow(context.Context, string) (*domain.Workflow, error)
	RoadmapRevision(context.Context, string) (*domain.Revision, error)
	ClaimRoadmapTasks(context.Context, string, time.Duration, time.Time) ([]domain.TaskClaim, error)
	GetRoadmapTaskClaim(context.Context, string, domain.LeaseCredential, time.Time) (*domain.TaskClaim, error)
	RenewRoadmapTaskLease(context.Context, domain.TaskClaim, time.Duration, time.Time) error
	StartRoadmapTaskAgentRun(context.Context, domain.TaskClaim, domain.AgentRole, string, time.Time) (*agent.Run, error)
	FinalizeRoadmapTaskPlanner(context.Context, domain.TaskClaim, string, domain.ChangeSet, time.Time) (*domain.Task, error)
	FinalizeRoadmapTaskReview(context.Context, domain.TaskClaim, string, domain.AgentRole, domain.Review, time.Time) (*domain.Task, error)
	FailRoadmapTaskAgentRun(context.Context, domain.TaskClaim, string, domain.AgentRole, string, time.Time) (*domain.Task, error)
	FailRoadmapTask(context.Context, domain.TaskClaim, string, time.Time) (*domain.Task, error)
	ClaimRoadmapWorkflowPublication(context.Context, string, time.Duration, time.Time) (*domain.WorkflowClaim, error)
	RenewRoadmapWorkflowLease(context.Context, domain.WorkflowClaim, time.Duration, time.Time) error
	CompleteRoadmapWorkflow(context.Context, domain.WorkflowClaim, time.Time) (*domain.Revision, error)
	ReportRoadmapWorkflowInfrastructureFailure(context.Context, domain.WorkflowClaim, string, time.Time) (*domain.Workflow, error)
}

// CommitteeExecutor owns model execution only. The application service owns
// every durable transition and creates the AgentRun before calling it.
type CommitteeExecutor interface {
	Plan(context.Context, PlannerRequest) (domain.ChangeSet, error)
	Review(context.Context, ReviewerRequest) (domain.Review, error)
}

type PlannerRequest struct {
	Task      domain.Task
	Revision  domain.Revision
	Retrieval *Retrieval
}

type ReviewerRequest struct {
	Role      domain.AgentRole
	Task      domain.Task
	Revision  domain.Revision
	ChangeSet domain.ChangeSet
	Retrieval *Retrieval
}

type MaintenanceConfig struct {
	Repository      MaintenanceRepository
	Executor        CommitteeExecutor
	ChallengeReader ChallengeContentReader
	Model           string
	ServerID        string
	LeaseTTL        time.Duration
	PollInterval    time.Duration
}

// MaintenanceService runs the durable incremental Roadmap workflow directly
// in every Server replica. Leases make multiple replicas safe; no instance
// owns an in-memory worklist.
type MaintenanceService struct {
	repository      MaintenanceRepository
	executor        CommitteeExecutor
	challengeReader ChallengeContentReader
	model           string
	serverID        string
	leaseTTL        time.Duration
	pollInterval    time.Duration
	now             func() time.Time
}

func NewMaintenanceService(config MaintenanceConfig) (*MaintenanceService, error) {
	if config.Repository == nil || config.Executor == nil || config.ChallengeReader == nil || strings.TrimSpace(config.Model) == "" || strings.TrimSpace(config.ServerID) == "" {
		return nil, errors.New("roadmap maintenance requires repository, executor, challenge reader, model, and server identity")
	}
	if config.LeaseTTL == 0 {
		config.LeaseTTL = 2 * time.Minute
	}
	if config.LeaseTTL < minimumMaintenanceLeaseTTL {
		return nil, fmt.Errorf("roadmap maintenance lease ttl must be at least %s", minimumMaintenanceLeaseTTL)
	}
	if config.PollInterval == 0 {
		config.PollInterval = defaultMaintenancePollInterval
	}
	if config.PollInterval <= 0 {
		return nil, errors.New("roadmap maintenance poll interval must be positive")
	}
	return &MaintenanceService{
		repository: config.Repository, executor: config.Executor, challengeReader: config.ChallengeReader,
		model: strings.TrimSpace(config.Model), serverID: strings.TrimSpace(config.ServerID),
		leaseTTL: config.LeaseTTL, pollInterval: config.PollInterval,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// Run keeps attempting to start or resume work until the Server stops. A
// failed task is a durable expected result, so only infrastructure errors are
// logged here; those leave the lease/state available for a later pass.
func (s *MaintenanceService) Run(ctx context.Context) {
	if s == nil {
		return
	}
	for {
		if err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("run roadmap maintenance", "err", err)
		}
		timer := time.NewTimer(s.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// RunOnce is public for deterministic integration tests and performs one
// complete claim/pass. It first tries the idle-window start gate, runs all
// locally leased tasks concurrently, then publishes if every fixed task has a
// terminal result.
func (s *MaintenanceService) RunOnce(ctx context.Context) error {
	if s == nil || s.repository == nil {
		return errors.New("roadmap maintenance service is not configured")
	}
	now := s.currentTime()
	if _, err := s.repository.TryStartRoadmapWorkflow(ctx, now); err != nil {
		return fmt.Errorf("start roadmap workflow: %w", err)
	}
	claims, err := s.repository.ClaimRoadmapTasks(ctx, s.serverID, s.leaseTTL, now)
	if err != nil {
		return fmt.Errorf("claim roadmap tasks: %w", err)
	}
	var group sync.WaitGroup
	errs := make(chan error, len(claims))
	for _, claim := range claims {
		claim := claim
		group.Add(1)
		go func() {
			defer group.Done()
			if err := s.runTask(ctx, claim); err != nil {
				errs <- err
			}
		}()
	}
	group.Wait()
	close(errs)
	var combined error
	for err := range errs {
		combined = errors.Join(combined, err)
	}
	if combined != nil {
		return combined
	}
	if err := s.publishReadyWorkflow(ctx); err != nil {
		return err
	}
	return nil
}

func (s *MaintenanceService) runTask(parent context.Context, claim domain.TaskClaim) error {
	workflow, err := s.repository.GetRoadmapWorkflow(parent, claim.Task.WorkflowID)
	if errors.Is(err, domain.ErrWorkflowNotFound) || errors.Is(err, domain.ErrLeaseLost) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load roadmap task workflow: %w", err)
	}
	workflowContext, cancelWorkflow := context.WithDeadline(parent, workflow.DeadlineAt)
	defer cancelWorkflow()
	revision, err := s.repository.RoadmapRevision(parent, workflow.BaseRevision)
	if err != nil {
		return fmt.Errorf("load roadmap task revision: %w", err)
	}
	retrieval, err := NewPlannerRetrieval(*revision, claim.Task.Kind, claim.Task.Subject, s.challengeReader)
	if err != nil {
		return fmt.Errorf("create roadmap task retrieval: %w", err)
	}
	ctx, cancel := s.taskLeaseContext(workflowContext, claim)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		current, err := s.repository.GetRoadmapTaskClaim(ctx, claim.Task.ID, claim.LeaseCredential, s.currentTime())
		if errors.Is(err, domain.ErrLeaseLost) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("refresh roadmap task claim: %w", err)
		}
		if current.Task.State.Terminal() {
			return nil
		}
		if current.Task.ChangeSet == nil {
			if err := s.runPlanner(ctx, *current, *revision, retrieval); err != nil {
				return err
			}
			continue
		}
		if err := s.runOutstandingReviews(ctx, *current, *revision, retrieval); err != nil {
			return err
		}
	}
}

func (s *MaintenanceService) runPlanner(ctx context.Context, claim domain.TaskClaim, revision domain.Revision, retrieval *Retrieval) error {
	if claim.Task.PlannerCalls >= domain.MaxAgentCallsPerTask {
		return s.failTaskIfOwned(ctx, claim, "Roadmap Planner 已耗尽调用次数")
	}
	run, err := s.repository.StartRoadmapTaskAgentRun(ctx, claim, domain.AgentPlanner, s.model, s.currentTime())
	if errors.Is(err, domain.ErrAgentCallLimit) {
		return s.failTaskIfOwned(ctx, claim, "Roadmap Planner 已耗尽调用次数")
	}
	if errors.Is(err, domain.ErrLeaseLost) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("start roadmap planner run: %w", err)
	}
	changeSet, err := s.executor.Plan(ctx, PlannerRequest{Task: claim.Task, Revision: revision, Retrieval: retrieval})
	if err != nil {
		return s.recordAgentFailure(ctx, claim, run.ID, domain.AgentPlanner, err)
	}
	if err := changeSet.ValidateFor(claim.Task.Kind, claim.Task.Subject, revision); err != nil {
		return s.recordAgentFailure(ctx, claim, run.ID, domain.AgentPlanner, fmt.Errorf("Planner 返回的关系候选无效: %w", err))
	}
	if _, err := s.repository.FinalizeRoadmapTaskPlanner(ctx, claim, run.ID, changeSet, s.currentTime()); err != nil {
		if errors.Is(err, domain.ErrLeaseLost) {
			return nil
		}
		return fmt.Errorf("persist roadmap planner result: %w", err)
	}
	return nil
}

func (s *MaintenanceService) runOutstandingReviews(ctx context.Context, claim domain.TaskClaim, revision domain.Revision, retrieval *Retrieval) error {
	roles := make([]domain.AgentRole, 0, 2)
	if claim.Task.CurriculumReview == nil {
		roles = append(roles, domain.AgentCurriculumReviewer)
	}
	if claim.Task.SREReview == nil {
		roles = append(roles, domain.AgentSREReviewer)
	}
	if len(roles) == 0 {
		return nil
	}
	for _, role := range roles {
		if taskCallsForRole(claim.Task, role) >= domain.MaxAgentCallsPerTask {
			return s.failTaskIfOwned(ctx, claim, string(role)+" 已耗尽调用次数")
		}
	}
	var group sync.WaitGroup
	errs := make(chan error, len(roles))
	for _, role := range roles {
		role := role
		group.Add(1)
		go func() {
			defer group.Done()
			if err := s.runReviewer(ctx, claim, role, revision, retrieval); err != nil {
				errs <- err
			}
		}()
	}
	group.Wait()
	close(errs)
	var combined error
	for err := range errs {
		combined = errors.Join(combined, err)
	}
	return combined
}

func (s *MaintenanceService) runReviewer(ctx context.Context, claim domain.TaskClaim, role domain.AgentRole, revision domain.Revision, retrieval *Retrieval) error {
	run, err := s.repository.StartRoadmapTaskAgentRun(ctx, claim, role, s.model, s.currentTime())
	if errors.Is(err, domain.ErrAgentCallLimit) {
		return s.failTaskIfOwned(ctx, claim, string(role)+" 已耗尽调用次数")
	}
	if errors.Is(err, domain.ErrLeaseLost) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("start roadmap %s run: %w", role, err)
	}
	review, err := s.executor.Review(ctx, ReviewerRequest{
		Role: role, Task: claim.Task, Revision: revision, ChangeSet: claim.Task.ChangeSet.Clone(), Retrieval: retrieval,
	})
	if err != nil {
		return s.recordAgentFailure(ctx, claim, run.ID, role, err)
	}
	if err := review.Validate(); err != nil {
		return s.recordAgentFailure(ctx, claim, run.ID, role, fmt.Errorf("Reviewer 返回的结论无效: %w", err))
	}
	if _, err := s.repository.FinalizeRoadmapTaskReview(ctx, claim, run.ID, role, review, s.currentTime()); err != nil {
		if errors.Is(err, domain.ErrLeaseLost) {
			return nil
		}
		return fmt.Errorf("persist roadmap %s result: %w", role, err)
	}
	return nil
}

func (s *MaintenanceService) recordAgentFailure(ctx context.Context, claim domain.TaskClaim, runID string, role domain.AgentRole, cause error) error {
	_, err := s.repository.FailRoadmapTaskAgentRun(ctx, claim, runID, role, maintenanceErrorSummary(cause), s.currentTime())
	if errors.Is(err, domain.ErrLeaseLost) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("record roadmap %s failure: %w", role, err)
	}
	return nil
}

func (s *MaintenanceService) failTaskIfOwned(ctx context.Context, claim domain.TaskClaim, message string) error {
	_, err := s.repository.FailRoadmapTask(ctx, claim, message, s.currentTime())
	if errors.Is(err, domain.ErrLeaseLost) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fail roadmap task: %w", err)
	}
	return nil
}

func (s *MaintenanceService) publishReadyWorkflow(parent context.Context) error {
	claim, err := s.repository.ClaimRoadmapWorkflowPublication(parent, s.serverID, s.leaseTTL, s.currentTime())
	if err != nil {
		return fmt.Errorf("claim roadmap workflow publication: %w", err)
	}
	if claim == nil {
		return nil
	}
	ctx, cancel := s.workflowLeaseContext(parent, *claim)
	defer cancel()
	if _, err := s.repository.CompleteRoadmapWorkflow(ctx, *claim, s.currentTime()); err == nil || errors.Is(err, domain.ErrLeaseLost) {
		return nil
	} else {
		_, reportErr := s.repository.ReportRoadmapWorkflowInfrastructureFailure(ctx, *claim, maintenanceErrorSummary(err), s.currentTime())
		if errors.Is(reportErr, domain.ErrLeaseLost) {
			return nil
		}
		if reportErr != nil {
			return fmt.Errorf("record roadmap publication failure: %w", reportErr)
		}
		return nil
	}
}

func (s *MaintenanceService) taskLeaseContext(parent context.Context, claim domain.TaskClaim) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(leaseRenewalInterval(s.leaseTTL))
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.repository.RenewRoadmapTaskLease(ctx, claim, s.leaseTTL, s.currentTime()); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

func (s *MaintenanceService) workflowLeaseContext(parent context.Context, claim domain.WorkflowClaim) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		ticker := time.NewTicker(leaseRenewalInterval(s.leaseTTL))
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.repository.RenewRoadmapWorkflowLease(ctx, claim, s.leaseTTL, s.currentTime()); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

func (s *MaintenanceService) currentTime() time.Time {
	return s.now().UTC()
}

func leaseRenewalInterval(leaseTTL time.Duration) time.Duration {
	interval := leaseTTL / 3
	if interval < time.Second {
		return time.Second
	}
	return interval
}

func taskCallsForRole(task domain.Task, role domain.AgentRole) int {
	switch role {
	case domain.AgentPlanner:
		return task.PlannerCalls
	case domain.AgentCurriculumReviewer:
		return task.CurriculumCalls
	case domain.AgentSREReviewer:
		return task.SRECalls
	default:
		return 0
	}
}

func maintenanceErrorSummary(err error) string {
	if err == nil {
		return "unknown roadmap maintenance failure"
	}
	const maxLength = 1024
	value := strings.TrimSpace(err.Error())
	if len(value) > maxLength {
		return value[:maxLength]
	}
	if value == "" {
		return "unknown roadmap maintenance failure"
	}
	return value
}
