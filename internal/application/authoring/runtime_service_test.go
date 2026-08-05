package authoring

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/agent"
	authoringdomain "github.com/breakfix/breakfix/internal/domain/authoring"
)

func TestRuntimeServiceRetriesOneRunFiveTimes(t *testing.T) {
	repository := &runtimeServiceRepository{run: agent.Run{
		ID: "authoring-run", Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: "authoring-session", Status: agent.RunRunning,
		Attempt: 1, DeadlineAt: time.Now().UTC().Add(time.Minute),
	}}
	executor := &failingRuntimeExecutor{}
	service := NewRuntimeService(repository, "test-model", executor)

	if _, err := service.RunTurn(context.Background(), repository.run.ID, nil); err == nil {
		t.Fatal("RunTurn unexpectedly succeeded")
	}
	if executor.calls != agent.MaxAttempts {
		t.Fatalf("executor calls = %d, want %d", executor.calls, agent.MaxAttempts)
	}
	if len(repository.retryAttempts) != agent.MaxAttempts {
		t.Fatalf("retry calls = %v", repository.retryAttempts)
	}
	for index, attempt := range repository.retryAttempts {
		if want := index + 1; attempt != want {
			t.Fatalf("retry attempt %d = %d, want %d", index, attempt, want)
		}
	}
	if repository.run.Status != agent.RunFailed || repository.run.Attempt != agent.MaxAttempts {
		t.Fatalf("terminal authoring run = %#v", repository.run)
	}
}

func TestRuntimeServiceLeavesRunForServerRecoveryOnLifecycleCancellation(t *testing.T) {
	repository := &runtimeServiceRepository{run: agent.Run{
		ID: "authoring-run", Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: "authoring-session", Status: agent.RunRunning,
		Attempt: 1, DeadlineAt: time.Now().UTC().Add(time.Minute),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := NewRuntimeService(repository, "test-model", contextRuntimeExecutor{})

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

func (r *runtimeServiceRepository) GetAuthoringSession(context.Context, string, string) (*authoringdomain.Session, error) {
	return nil, errors.New("unexpected GetAuthoringSession")
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

func (r *runtimeServiceRepository) UpdateAuthoringStage(context.Context, string, int, int64, authoringdomain.Plan, authoringdomain.Change) (*authoringdomain.Stage, error) {
	return nil, errors.New("unexpected UpdateAuthoringStage")
}

func (r *runtimeServiceRepository) FinalizeAuthoringRun(context.Context, string, int, string, time.Time) (*authoringdomain.Revision, error) {
	return nil, errors.New("unexpected FinalizeAuthoringRun")
}

func (r *runtimeServiceRepository) RetryAuthoringRun(_ context.Context, _ string, expectedAttempt int, _ string, _ time.Time) (*agent.Run, error) {
	r.retryAttempts = append(r.retryAttempts, expectedAttempt)
	if expectedAttempt != r.run.Attempt {
		return nil, agent.ErrRunActive
	}
	if expectedAttempt == agent.MaxAttempts {
		r.run.Status = agent.RunFailed
		return nil, nil
	}
	r.run.Attempt++
	copy := r.run
	return &copy, nil
}

func (r *runtimeServiceRepository) RestartInterruptedAuthoringRun(context.Context, string, string, time.Time) (*agent.Run, error) {
	return nil, errors.New("unexpected RestartInterruptedAuthoringRun")
}

type failingRuntimeExecutor struct{ calls int }

func (e *failingRuntimeExecutor) Run(context.Context, string, authoringdomain.Stage, []agent.Message, StageUpdater, func(StreamEvent)) (string, error) {
	e.calls++
	return "", errors.New("model transport unavailable")
}

type contextRuntimeExecutor struct{}

func (contextRuntimeExecutor) Run(ctx context.Context, _ string, _ authoringdomain.Stage, _ []agent.Message, _ StageUpdater, _ func(StreamEvent)) (string, error) {
	return "", ctx.Err()
}
