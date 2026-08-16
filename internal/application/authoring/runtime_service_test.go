package authoring

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/agent"
	authoringdomain "github.com/breakfix/breakfix/internal/domain/authoring"
)

func TestRuntimeServiceDoesNotRebuildAuthoringRunAfterExecutorFailure(t *testing.T) {
	repository := &runtimeServiceRepository{run: agent.Run{
		ID: "authoring-run", Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: "authoring-session", Status: agent.RunRunning,
		Attempt: 1, DeadlineAt: time.Now().UTC().Add(time.Minute),
	}}
	executor := &failingRuntimeExecutor{}
	service := NewRuntimeService(repository, "test-model", time.Minute, executor)

	if _, err := service.RunTurn(context.Background(), repository.run.ID, nil); err == nil {
		t.Fatal("RunTurn unexpectedly succeeded")
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if len(repository.retryAttempts) != 0 {
		t.Fatalf("retry calls = %v", repository.retryAttempts)
	}
	if repository.run.Status != agent.RunRunning || repository.run.Attempt != 1 {
		t.Fatalf("authoring run changed after executor failure = %#v", repository.run)
	}
}

func TestRuntimeServiceLeavesRunForServerRecoveryOnLifecycleCancellation(t *testing.T) {
	repository := &runtimeServiceRepository{run: agent.Run{
		ID: "authoring-run", Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: "authoring-session", Status: agent.RunRunning,
		Attempt: 1, DeadlineAt: time.Now().UTC().Add(time.Minute),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := NewRuntimeService(repository, "test-model", time.Minute, contextRuntimeExecutor{})

	if _, err := service.RunTurn(ctx, repository.run.ID, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunTurn error = %v, want context cancellation", err)
	}
	if len(repository.retryAttempts) != 0 || repository.run.Status != agent.RunRunning {
		t.Fatalf("cancelled lifecycle changed durable run: retries=%v run=%#v", repository.retryAttempts, repository.run)
	}
}

type runtimeServiceRepository struct {
	run           agent.Run
	retryAttempts []int
}

func (r *runtimeServiceRepository) CreateAuthoringSession(context.Context, authoringdomain.Session, authoringdomain.Plan) (*authoringdomain.Session, error) {
	return nil, errors.New("unexpected CreateAuthoringSession")
}

func (r *runtimeServiceRepository) CreateChallengeRevisionSession(context.Context, string, string) (*authoringdomain.Session, error) {
	return nil, errors.New("unexpected CreateChallengeRevisionSession")
}

func (r *runtimeServiceRepository) GetAuthoringSession(context.Context, string, string) (*authoringdomain.Session, error) {
	return nil, errors.New("unexpected GetAuthoringSession")
}

func (r *runtimeServiceRepository) GetAuthoringSessionInternal(context.Context, string) (*authoringdomain.Session, error) {
	return &authoringdomain.Session{ID: r.run.OwnerRef, UserID: "authoring-user", RuntimeSessionID: r.run.SessionID}, nil
}

func (r *runtimeServiceRepository) GetLatestOpenAuthoringSession(context.Context, string) (*authoringdomain.Session, error) {
	return nil, errors.New("unexpected GetLatestOpenAuthoringSession")
}

func (r *runtimeServiceRepository) GetAuthoringRevision(context.Context, string, int64) (*authoringdomain.Revision, error) {
	return nil, errors.New("unexpected GetAuthoringRevision")
}

func (r *runtimeServiceRepository) StartAuthoringRun(context.Context, string, string, agent.Message, agent.CreateRun) (*authoringdomain.Stage, *agent.Run, error) {
	return nil, nil, errors.New("unexpected StartAuthoringRun")
}

func (r *runtimeServiceRepository) ListMessages(context.Context, string) ([]agent.Message, error) {
	return nil, errors.New("unexpected ListMessages")
}

func (r *runtimeServiceRepository) GetRun(context.Context, string) (*agent.Run, error) {
	copy := r.run
	return &copy, nil
}

func (r *runtimeServiceRepository) LoadAuthoringExecution(context.Context, string) (*authoringdomain.Stage, []agent.Message, error) {
	return &authoringdomain.Stage{RunID: r.run.ID, SessionID: r.run.OwnerRef, RunAttempt: r.run.Attempt}, []agent.Message{{Role: "user", Content: "继续完善题意"}}, nil
}

func (r *runtimeServiceRepository) UpdateAuthoringStage(context.Context, string, int, int64, authoringdomain.StageOperation, authoringdomain.Plan, authoringdomain.Change) (*authoringdomain.Stage, error) {
	return nil, errors.New("unexpected UpdateAuthoringStage")
}

func (r *runtimeServiceRepository) FinalizeAuthoringRun(context.Context, string, int, string, time.Time) (*authoringdomain.Revision, error) {
	return nil, errors.New("unexpected FinalizeAuthoringRun")
}

func (r *runtimeServiceRepository) RestartInterruptedAuthoringRun(context.Context, string, string, time.Time) (*agent.Run, error) {
	return nil, errors.New("unexpected RestartInterruptedAuthoringRun")
}

type failingRuntimeExecutor struct{ calls int }

func (e *failingRuntimeExecutor) Run(context.Context, Execution, StageUpdater, func(StreamEvent)) (string, error) {
	e.calls++
	return "", errors.New("model transport unavailable")
}

type contextRuntimeExecutor struct{}

func (contextRuntimeExecutor) Run(ctx context.Context, _ Execution, _ StageUpdater, _ func(StreamEvent)) (string, error) {
	return "", ctx.Err()
}
