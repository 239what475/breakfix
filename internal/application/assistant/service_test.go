package assistant

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/agent"
)

func TestServiceRetriesOneAssistantRunFiveTimes(t *testing.T) {
	repository := &assistantServiceRepository{run: agent.Run{
		ID: "assistant-run", SessionID: "assistant-session", Purpose: "assistant", OwnerKind: "environment", OwnerRef: "environment-one",
		Status: agent.RunRunning, Attempt: 1, DeadlineAt: time.Now().UTC().Add(time.Minute),
	}}
	executor := &failingAssistantExecutor{}
	service := NewService(repository, "test-model", executor)

	if _, err := service.RunTurn(context.Background(), repository.run.SessionID, repository.run.ID, Request{}, nil); err == nil {
		t.Fatal("RunTurn unexpectedly succeeded")
	}
	if executor.calls != agent.MaxAttempts {
		t.Fatalf("executor calls = %d, want %d", executor.calls, agent.MaxAttempts)
	}
	if len(repository.retryAttempts) != agent.MaxAttempts {
		t.Fatalf("retry calls = %v", repository.retryAttempts)
	}
	if repository.run.Status != agent.RunFailed || repository.run.Attempt != agent.MaxAttempts {
		t.Fatalf("terminal assistant run = %#v", repository.run)
	}
}

type assistantServiceRepository struct {
	run           agent.Run
	retryAttempts []int
}

func (r *assistantServiceRepository) CreateSession(context.Context, agent.Session) (*agent.Session, error) {
	return nil, errors.New("unexpected CreateSession")
}

func (r *assistantServiceRepository) FindOrCreateSession(context.Context, agent.Session) (*agent.Session, error) {
	return nil, errors.New("unexpected FindOrCreateSession")
}

func (r *assistantServiceRepository) FindSession(context.Context, string, string, string, string) (*agent.Session, error) {
	return nil, errors.New("unexpected FindSession")
}

func (r *assistantServiceRepository) GetSession(context.Context, string) (*agent.Session, error) {
	return nil, errors.New("unexpected GetSession")
}

func (r *assistantServiceRepository) ListMessages(context.Context, string) ([]agent.Message, error) {
	return []agent.Message{{Role: "user", Content: "请给出排障建议"}}, nil
}

func (r *assistantServiceRepository) CreateMessageAndRun(context.Context, agent.Message, agent.CreateRun) (*agent.Run, error) {
	return nil, errors.New("unexpected CreateMessageAndRun")
}

func (r *assistantServiceRepository) CreateRun(context.Context, agent.CreateRun) (*agent.Run, error) {
	return nil, errors.New("unexpected CreateRun")
}

func (r *assistantServiceRepository) GetRun(context.Context, string) (*agent.Run, error) {
	copy := r.run
	return &copy, nil
}

func (r *assistantServiceRepository) GetActiveRunForSession(context.Context, string) (*agent.Run, error) {
	return nil, errors.New("unexpected GetActiveRunForSession")
}

func (r *assistantServiceRepository) ListRunsForOwner(context.Context, string, string) ([]agent.Run, error) {
	return nil, errors.New("unexpected ListRunsForOwner")
}

func (r *assistantServiceRepository) CompleteRunWithMessage(context.Context, string, agent.Message, time.Time) error {
	return errors.New("unexpected CompleteRunWithMessage")
}

func (r *assistantServiceRepository) CompleteRun(context.Context, string, time.Time) error {
	return errors.New("unexpected CompleteRun")
}

func (r *assistantServiceRepository) FailRun(context.Context, string, string, time.Time) error {
	return errors.New("unexpected FailRun")
}

func (r *assistantServiceRepository) RetryRun(_ context.Context, _ string, expectedAttempt int, _ string, _ time.Time) (*agent.Run, error) {
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

func (r *assistantServiceRepository) InterruptRun(context.Context, string, string, time.Time) error {
	return errors.New("unexpected InterruptRun")
}

func (r *assistantServiceRepository) RestartRun(context.Context, string, string, time.Time) (*agent.Run, error) {
	return nil, errors.New("unexpected RestartRun")
}

func (r *assistantServiceRepository) CancelRunsForOwner(context.Context, string, string, string, string, time.Time) (int64, error) {
	return 0, errors.New("unexpected CancelRunsForOwner")
}

type failingAssistantExecutor struct{ calls int }

func (e *failingAssistantExecutor) Run(context.Context, Request, []agent.Message, func(StreamEvent)) (EngineResult, error) {
	e.calls++
	return EngineResult{}, errors.New("model transport unavailable")
}
