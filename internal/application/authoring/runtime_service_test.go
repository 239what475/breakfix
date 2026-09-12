package authoring

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/agent"
	authoringdomain "github.com/breakfix/breakfix/internal/domain/authoring"
)

func TestRuntimeServiceTerminatesAuthoringRunAfterExecutorFailure(t *testing.T) {
	repository := &runtimeServiceRepository{run: agent.Run{
		ID: "authoring-run", Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: "authoring-session", Status: agent.RunRunning,
		Attempt: 1, DeadlineAt: time.Now().UTC().Add(time.Minute),
	}, terminationFailures: 1}
	executor := &failingRuntimeExecutor{}
	service := NewRuntimeService(repository, "test-model", time.Minute, executor)

	if _, err := service.RunTurn(context.Background(), repository.run.ID, nil); err == nil {
		t.Fatal("RunTurn unexpectedly succeeded")
	}
	if executor.calls != 1 {
		t.Fatalf("executor calls = %d, want 1", executor.calls)
	}
	if repository.run.Status != agent.RunFailed || repository.run.Attempt != 1 {
		t.Fatalf("authoring run status = %#v", repository.run)
	}
	if repository.terminationReason != authoringdomain.RunTerminationPermanentExecutorError {
		t.Fatalf("termination reason = %q", repository.terminationReason)
	}
	if repository.terminationCalls != 2 {
		t.Fatalf("termination persistence calls = %d, want 2", repository.terminationCalls)
	}
}

func TestRuntimeServiceRetriesFinalizationWithoutReexecutingAgent(t *testing.T) {
	repository := &runtimeServiceRepository{run: agent.Run{
		ID: "authoring-run", Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: "authoring-session", Status: agent.RunRunning,
		Attempt: 1, DeadlineAt: time.Now().UTC().Add(time.Minute),
	}, finalizationFailures: 1}
	executor := &successfulRuntimeExecutor{}
	service := NewRuntimeService(repository, "test-model", time.Minute, executor)

	content, err := service.RunTurn(context.Background(), repository.run.ID, nil)
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if content != "本轮已经完成。" || executor.calls != 1 {
		t.Fatalf("authoring execution = content=%q calls=%d", content, executor.calls)
	}
	if repository.finalizationCalls != 2 || repository.run.Status != agent.RunSucceeded {
		t.Fatalf("finalization persistence = calls=%d run=%#v", repository.finalizationCalls, repository.run)
	}
}

func TestRuntimeServiceTerminatesRunForServerLifecycleCancellation(t *testing.T) {
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
	if repository.run.Status != agent.RunInterrupted || repository.terminationReason != authoringdomain.RunTerminationServerStopping {
		t.Fatalf("cancelled lifecycle termination = reason=%q run=%#v", repository.terminationReason, repository.run)
	}
}

func TestProjectRuntimeMessagesPreservesAuthoringEvents(t *testing.T) {
	event, err := authoringdomain.NewRunEvent("run-one", authoringdomain.RunTerminationDeadlineExceeded, "workspace")
	if err != nil {
		t.Fatalf("create authoring event: %v", err)
	}
	content, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("encode authoring event: %v", err)
	}
	messages, err := projectRuntimeMessages([]agent.Message{{
		ID: "event-one", Role: "event", Content: string(content), CreatedAt: time.Now().UTC(),
	}})
	if err != nil {
		t.Fatalf("project authoring event: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "event" || messages[0].Event == nil || messages[0].Event.Reason != authoringdomain.RunTerminationDeadlineExceeded {
		t.Fatalf("projected authoring event = %#v", messages)
	}
}

type runtimeServiceRepository struct {
	run                  agent.Run
	finalizationCalls    int
	finalizationFailures int
	terminationCalls     int
	terminationFailures  int
	terminationReason    authoringdomain.RunTerminationReason
	terminationError     string
}

func (r *runtimeServiceRepository) CreateAuthoringSession(context.Context, authoringdomain.Session, authoringdomain.Plan) (*authoringdomain.Session, error) {
	return nil, errors.New("unexpected CreateAuthoringSession")
}

func (r *runtimeServiceRepository) CreateScenarioRevisionSession(context.Context, string, string) (*authoringdomain.Session, error) {
	return nil, errors.New("unexpected CreateScenarioRevisionSession")
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

func (r *runtimeServiceRepository) StartAuthoringRun(context.Context, string, string, string, agent.Message, agent.CreateRun) (*authoringdomain.Stage, *agent.Run, bool, error) {
	return nil, nil, false, errors.New("unexpected StartAuthoringRun")
}

func (r *runtimeServiceRepository) ListMessages(context.Context, string) ([]agent.Message, error) {
	return nil, errors.New("unexpected ListMessages")
}

func (r *runtimeServiceRepository) GetRun(context.Context, string) (*agent.Run, error) {
	copy := r.run
	return &copy, nil
}

func (r *runtimeServiceRepository) LoadAuthoringExecution(context.Context, string) (*authoringdomain.Stage, []agent.Message, error) {
	return &authoringdomain.Stage{RunID: r.run.ID, SessionID: r.run.OwnerRef, RunAttempt: r.run.Attempt}, []agent.Message{{Role: "user", Content: "继续完善现场说明"}}, nil
}

func (r *runtimeServiceRepository) UpdateAuthoringStage(context.Context, string, int, int64, authoringdomain.StageOperation, authoringdomain.Plan, authoringdomain.Change) (*authoringdomain.Stage, error) {
	return nil, errors.New("unexpected UpdateAuthoringStage")
}

func (r *runtimeServiceRepository) FinalizeAuthoringRun(_ context.Context, runID string, attempt int, _ string, _ time.Time) (*authoringdomain.Revision, error) {
	r.finalizationCalls++
	if r.finalizationFailures > 0 {
		r.finalizationFailures--
		return nil, errors.New("temporary persistence failure")
	}
	if runID != r.run.ID || attempt != r.run.Attempt || r.run.Status != agent.RunRunning {
		return nil, agent.ErrRunActive
	}
	r.run.Status = agent.RunSucceeded
	return &authoringdomain.Revision{}, nil
}

func (r *runtimeServiceRepository) TerminateAuthoringRun(_ context.Context, runID string, reason authoringdomain.RunTerminationReason, diagnostic string, _ time.Time) error {
	r.terminationCalls++
	if r.terminationFailures > 0 {
		r.terminationFailures--
		return errors.New("temporary persistence failure")
	}
	if runID != r.run.ID {
		return errors.New("unexpected run id")
	}
	r.terminationReason = reason
	r.terminationError = diagnostic
	if reason.Kind() == authoringdomain.RunTerminationFailed {
		r.run.Status = agent.RunFailed
	} else {
		r.run.Status = agent.RunInterrupted
	}
	return nil
}

type failingRuntimeExecutor struct{ calls int }

func (e *failingRuntimeExecutor) Run(context.Context, Execution, StageUpdater, func(StreamEvent)) (string, error) {
	e.calls++
	return "", errors.New("model transport unavailable")
}

type successfulRuntimeExecutor struct{ calls int }

func (e *successfulRuntimeExecutor) Run(context.Context, Execution, StageUpdater, func(StreamEvent)) (string, error) {
	e.calls++
	return "本轮已经完成。", nil
}

type contextRuntimeExecutor struct{}

func (contextRuntimeExecutor) Run(ctx context.Context, _ Execution, _ StageUpdater, _ func(StreamEvent)) (string, error) {
	return "", ctx.Err()
}
