package roadmap

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/agent"
	domain "github.com/breakfix/breakfix/internal/domain/roadmap"
	roadmaptest "github.com/breakfix/breakfix/internal/testkit/roadmap"
)

func TestMaintenanceServiceRevisesRejectedChangeSetWithBothReviewers(t *testing.T) {
	now := time.Date(2026, time.August, 4, 15, 0, 0, 0, time.UTC)
	repository := newMaintenanceTestRepository(now)
	executor := &maintenanceTestExecutor{rejectFirstCurriculumReview: true}
	service := newMaintenanceTestService(t, repository, executor, now)

	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatalf("run roadmap maintenance: %v", err)
	}
	task := repository.taskSnapshot()
	if task.State != domain.TaskAccepted || task.Round != 1 || task.PlannerCalls != 2 || task.CurriculumCalls != 2 || task.SRECalls != 2 {
		t.Fatalf("completed roadmap task = %#v", task)
	}
	if executor.plannerCallCount() != 2 || executor.reviewerCallCount(domain.AgentCurriculumReviewer) != 2 || executor.reviewerCallCount(domain.AgentSREReviewer) != 2 {
		t.Fatalf("committee calls = %#v", executor.calls)
	}
}

func TestMaintenanceServiceFailsOnlyAfterTheRoleExhaustsItsFiveCalls(t *testing.T) {
	now := time.Date(2026, time.August, 4, 15, 15, 0, 0, time.UTC)
	repository := newMaintenanceTestRepository(now)
	executor := &maintenanceTestExecutor{plannerError: errors.New("model transport failed")}
	service := newMaintenanceTestService(t, repository, executor, now)

	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatalf("run failing roadmap maintenance: %v", err)
	}
	task := repository.taskSnapshot()
	if task.State != domain.TaskFailed || task.PlannerCalls != domain.MaxAgentCallsPerTask || task.CurriculumCalls != 0 || task.SRECalls != 0 {
		t.Fatalf("failed roadmap task = %#v", task)
	}
	if executor.plannerCallCount() != domain.MaxAgentCallsPerTask {
		t.Fatalf("planner calls = %d, want %d", executor.plannerCallCount(), domain.MaxAgentCallsPerTask)
	}
}

func newMaintenanceTestService(t *testing.T, repository *maintenanceTestRepository, executor *maintenanceTestExecutor, now time.Time) *MaintenanceService {
	t.Helper()
	service, err := NewMaintenanceService(MaintenanceConfig{
		Repository: repository, Executor: executor, ChallengeReader: maintenanceTestChallengeReader{},
		Model: "test-model", ServerID: "test-server", LeaseTTL: 5 * time.Second, PollInterval: time.Second,
	})
	if err != nil {
		t.Fatalf("create maintenance service: %v", err)
	}
	service.now = func() time.Time { return now }
	return service
}

type maintenanceTestChallengeReader struct{}

func (maintenanceTestChallengeReader) ReadRoadmapChallenge(context.Context, domain.ChallengeBinding) (ChallengeContent, error) {
	return ChallengeContent{Runtime: "node", Difficulty: "intermediate", Description: "测试内容", Problem: "测试题目", Solution: "测试解答"}, nil
}

type maintenanceTestExecutor struct {
	mu                          sync.Mutex
	calls                       []maintenanceTestCall
	rejectFirstCurriculumReview bool
	plannerError                error
}

type maintenanceTestCall struct {
	Role  domain.AgentRole
	Round int
}

func (e *maintenanceTestExecutor) Plan(_ context.Context, request PlannerRequest) (domain.ChangeSet, error) {
	e.mu.Lock()
	e.calls = append(e.calls, maintenanceTestCall{Role: domain.AgentPlanner, Round: request.Task.Round})
	err := e.plannerError
	e.mu.Unlock()
	if err != nil {
		return domain.ChangeSet{}, err
	}
	return domain.ChangeSet{}, nil
}

func (e *maintenanceTestExecutor) Review(_ context.Context, request ReviewerRequest) (domain.Review, error) {
	e.mu.Lock()
	e.calls = append(e.calls, maintenanceTestCall{Role: request.Role, Round: request.Task.Round})
	reject := e.rejectFirstCurriculumReview && request.Role == domain.AgentCurriculumReviewer && request.Task.Round == 0
	e.mu.Unlock()
	if reject {
		return domain.Review{Decision: domain.ReviewRejected, Feedback: "请重新检查这组关系是否有明确的课程依据。"}, nil
	}
	return domain.Review{Decision: domain.ReviewApproved}, nil
}

func (e *maintenanceTestExecutor) plannerCallCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	count := 0
	for _, call := range e.calls {
		if call.Role == domain.AgentPlanner {
			count++
		}
	}
	return count
}

func (e *maintenanceTestExecutor) reviewerCallCount(role domain.AgentRole) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	count := 0
	for _, call := range e.calls {
		if call.Role == role {
			count++
		}
	}
	return count
}

type maintenanceTestRepository struct {
	mu       sync.Mutex
	revision domain.Revision
	workflow domain.Workflow
	task     domain.Task
	claimed  bool
	runCount int
}

func newMaintenanceTestRepository(now time.Time) *maintenanceTestRepository {
	revision := roadmaptest.RuntimeRevision()
	revision.Revision = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	subject := revision.Topics[0]
	return &maintenanceTestRepository{
		revision: revision,
		workflow: domain.Workflow{
			ID: "roadmap-workflow-test", BaseRevision: revision.Revision, State: domain.WorkflowRunning,
			NextRunAt: now, CreatedAt: now, UpdatedAt: now,
		},
		task: domain.Task{
			ID: "roadmap-task-test", WorkflowID: "roadmap-workflow-test", EntryChallengeID: "challenge-node-runtime",
			SnapshotOrder: 0, Kind: domain.TaskTopic,
			Subject: domain.Subject{Ref: domain.Ref{ID: subject.ID, SourceRef: subject.SourceRef, Title: subject.Title}},
			State:   domain.TaskPending, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
		},
	}
}

func (r *maintenanceTestRepository) TryStartRoadmapWorkflow(context.Context, time.Time) (*domain.Workflow, error) {
	return nil, nil
}

func (r *maintenanceTestRepository) GetRoadmapWorkflow(_ context.Context, id string) (*domain.Workflow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != r.workflow.ID {
		return nil, domain.ErrWorkflowNotFound
	}
	value := r.workflow
	return &value, nil
}

func (r *maintenanceTestRepository) RoadmapRevision(_ context.Context, id string) (*domain.Revision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != r.revision.Revision {
		return nil, domain.ErrWorkflowNotFound
	}
	value := r.revision.Clone()
	return &value, nil
}

func (r *maintenanceTestRepository) ClaimRoadmapTasks(_ context.Context, _ string, leaseTTL time.Duration, now time.Time) ([]domain.TaskClaim, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimed {
		return nil, nil
	}
	r.claimed = true
	r.task.State = domain.TaskRunning
	r.task.LeaseOwner = "test-task-lease"
	r.task.LeaseVersion = 1
	expires := now.Add(leaseTTL)
	r.task.LeaseExpiresAt = &expires
	r.task.UpdatedAt = now
	return []domain.TaskClaim{{Task: cloneMaintenanceTestTask(r.task), LeaseCredential: domain.LeaseCredential{LeaseOwner: r.task.LeaseOwner, LeaseVersion: r.task.LeaseVersion}}}, nil
}

func (r *maintenanceTestRepository) GetRoadmapTaskClaim(_ context.Context, id string, credential domain.LeaseCredential, _ time.Time) (*domain.TaskClaim, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != r.task.ID || credential.LeaseOwner != r.task.LeaseOwner || credential.LeaseVersion != r.task.LeaseVersion {
		return nil, domain.ErrLeaseLost
	}
	return &domain.TaskClaim{Task: cloneMaintenanceTestTask(r.task), LeaseCredential: credential}, nil
}

func (r *maintenanceTestRepository) RenewRoadmapTaskLease(_ context.Context, claim domain.TaskClaim, leaseTTL time.Duration, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if claim.Task.ID != r.task.ID || claim.LeaseOwner != r.task.LeaseOwner || claim.LeaseVersion != r.task.LeaseVersion {
		return domain.ErrLeaseLost
	}
	expires := now.Add(leaseTTL)
	r.task.LeaseExpiresAt = &expires
	return nil
}

func (r *maintenanceTestRepository) StartRoadmapTaskAgentRun(_ context.Context, claim domain.TaskClaim, role domain.AgentRole, _ string, now time.Time) (*agent.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.requireTaskClaim(claim); err != nil {
		return nil, err
	}
	switch role {
	case domain.AgentPlanner:
		r.task.PlannerCalls++
	case domain.AgentCurriculumReviewer:
		r.task.CurriculumCalls++
	case domain.AgentSREReviewer:
		r.task.SRECalls++
	default:
		return nil, domain.ErrLeaseLost
	}
	r.runCount++
	r.task.UpdatedAt = now
	return &agent.Run{ID: fmt.Sprintf("roadmap-agent-run-%d", r.runCount), Status: agent.RunRunning}, nil
}

func (r *maintenanceTestRepository) FinalizeRoadmapTaskPlanner(_ context.Context, claim domain.TaskClaim, _ string, changes domain.ChangeSet, now time.Time) (*domain.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.requireTaskClaim(claim); err != nil {
		return nil, err
	}
	value := changes.Clone()
	r.task.ChangeSet = &value
	r.task.CurriculumReview = nil
	r.task.SREReview = nil
	r.task.UpdatedAt = now
	result := cloneMaintenanceTestTask(r.task)
	return &result, nil
}

func (r *maintenanceTestRepository) FinalizeRoadmapTaskReview(_ context.Context, claim domain.TaskClaim, _ string, role domain.AgentRole, review domain.Review, now time.Time) (*domain.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.requireTaskClaim(claim); err != nil {
		return nil, err
	}
	value := review
	switch role {
	case domain.AgentCurriculumReviewer:
		r.task.CurriculumReview = &value
	case domain.AgentSREReviewer:
		r.task.SREReview = &value
	default:
		return nil, domain.ErrLeaseLost
	}
	if r.task.CurriculumReview != nil && r.task.SREReview != nil {
		if r.task.CurriculumReview.Decision == domain.ReviewRejected || r.task.SREReview.Decision == domain.ReviewRejected {
			r.task.Round++
			r.task.ChangeSet = nil
			r.task.CurriculumReview = nil
			r.task.SREReview = nil
		} else {
			r.task.State = domain.TaskAccepted
		}
	}
	r.task.UpdatedAt = now
	result := cloneMaintenanceTestTask(r.task)
	return &result, nil
}

func (r *maintenanceTestRepository) FailRoadmapTaskAgentRun(_ context.Context, claim domain.TaskClaim, _ string, role domain.AgentRole, message string, now time.Time) (*domain.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.requireTaskClaim(claim); err != nil {
		return nil, err
	}
	if taskCallsForRole(r.task, role) >= domain.MaxAgentCallsPerTask {
		r.task.State = domain.TaskFailed
	}
	r.task.LastError = message
	r.task.UpdatedAt = now
	result := cloneMaintenanceTestTask(r.task)
	return &result, nil
}

func (r *maintenanceTestRepository) FailRoadmapTask(_ context.Context, claim domain.TaskClaim, message string, now time.Time) (*domain.Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.requireTaskClaim(claim); err != nil {
		return nil, err
	}
	r.task.State = domain.TaskFailed
	r.task.LastError = message
	r.task.UpdatedAt = now
	result := cloneMaintenanceTestTask(r.task)
	return &result, nil
}

func (r *maintenanceTestRepository) ClaimRoadmapWorkflowPublication(context.Context, string, time.Duration, time.Time) (*domain.WorkflowClaim, error) {
	return nil, nil
}

func (r *maintenanceTestRepository) RenewRoadmapWorkflowLease(context.Context, domain.WorkflowClaim, time.Duration, time.Time) error {
	return nil
}

func (r *maintenanceTestRepository) CompleteRoadmapWorkflow(context.Context, domain.WorkflowClaim, time.Time) (*domain.Revision, error) {
	value := r.revision.Clone()
	return &value, nil
}

func (r *maintenanceTestRepository) ReportRoadmapWorkflowInfrastructureFailure(context.Context, domain.WorkflowClaim, string, time.Time) (*domain.Workflow, error) {
	value := r.workflow
	return &value, nil
}

func (r *maintenanceTestRepository) requireTaskClaim(claim domain.TaskClaim) error {
	if claim.Task.ID != r.task.ID || claim.LeaseOwner != r.task.LeaseOwner || claim.LeaseVersion != r.task.LeaseVersion || r.task.State != domain.TaskRunning {
		return domain.ErrLeaseLost
	}
	return nil
}

func (r *maintenanceTestRepository) taskSnapshot() domain.Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneMaintenanceTestTask(r.task)
}

func cloneMaintenanceTestTask(value domain.Task) domain.Task {
	result := value
	if value.ChangeSet != nil {
		changeSet := value.ChangeSet.Clone()
		result.ChangeSet = &changeSet
	}
	if value.CurriculumReview != nil {
		review := *value.CurriculumReview
		result.CurriculumReview = &review
	}
	if value.SREReview != nil {
		review := *value.SREReview
		result.SREReview = &review
	}
	if value.LeaseExpiresAt != nil {
		expires := *value.LeaseExpiresAt
		result.LeaseExpiresAt = &expires
	}
	return result
}
