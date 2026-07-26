package agentworker_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/agentworker"
	"github.com/breakfix/breakfix/internal/testpostgres"
)

func TestWorkerCompletesReadOnlyRunAfterBestEffortDeltaFailure(t *testing.T) {
	database := testpostgres.New(t)
	ctx := context.Background()
	if _, err := database.CreateSession(ctx, agentruntime.Session{
		ID: "assistant-session", Purpose: "assistant", OwnerKind: "environment", OwnerRef: "environment-one", UserRef: "user-one",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateMessageAndRun(ctx, agentruntime.Message{
		ID: "user-message", SessionID: "assistant-session", Role: "user", Content: "what should I do",
	}, agentruntime.CreateRun{
		ID: "assistant-run", SessionID: "assistant-session", Purpose: "assistant", OwnerKind: "environment", OwnerRef: "environment-one",
		Model: "deepseek-v4-pro", PromptVersion: "assistant-v1", DeadlineAt: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := agentworker.New(database, map[string]agentworker.Executor{
		"assistant": agentworker.ExecutorFunc(func(ctx context.Context, claim agentruntime.Claim, emit agentworker.Emitter) (agentworker.ExecutionResult, error) {
			emit.EmitDelta(ctx, "draft response")
			return agentworker.ExecutionResult{Message: &agentruntime.Message{
				ID: "assistant-message", SessionID: claim.Run.SessionID, Role: "assistant", Content: "durable response",
			}}, nil
		}),
	}, failingDeltaSink{}, agentworker.Config{WorkerID: "worker-one", LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.ProcessOne(ctx)
	if err != nil || !processed {
		t.Fatalf("process run = %v, %v", processed, err)
	}
	run, err := database.GetRun(ctx, "assistant-run")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != agentruntime.RunSucceeded || run.Attempt != 1 {
		t.Fatalf("run = %#v", run)
	}
	messages, err := database.ListMessages(ctx, "assistant-session")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Content != "durable response" {
		t.Fatalf("messages = %#v", messages)
	}
}

type failingDeltaSink struct{}

func (failingDeltaSink) EmitDelta(context.Context, agentruntime.Claim, string) error {
	return errors.New("server stream unavailable")
}

func (failingDeltaSink) EmitTool(context.Context, agentruntime.Claim, string) error {
	return errors.New("server stream unavailable")
}

func (failingDeltaSink) EmitReset(context.Context, agentruntime.Claim) error {
	return errors.New("server stream unavailable")
}
