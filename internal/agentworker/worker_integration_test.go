package agentworker_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/agentworker"
	"github.com/breakfix/breakfix/internal/testpostgres"
	"github.com/breakfix/breakfix/internal/worklist"
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
		Model: "deepseek-v4-pro", PromptVersion: "assistant-v1", ExecutionTimeout: time.Hour,
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
	if run.Status != agentruntime.RunSucceeded {
		t.Fatalf("run = %#v", run)
	}
	item, err := database.GetWorkItemForSubject(ctx, worklist.KindAgent, worklist.SubjectAgentRun, run.ID)
	if err != nil || item.Attempt != 1 || item.State != worklist.StateSucceeded {
		t.Fatalf("work item = %#v, %v", item, err)
	}
	messages, err := database.ListMessages(ctx, "assistant-session")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Content != "durable response" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestWorkerTerminatesDomainManagedFailureWithoutGenericRequeue(t *testing.T) {
	database := testpostgres.New(t)
	ctx := context.Background()
	if _, err := database.CreateRun(ctx, agentruntime.CreateRun{
		ID: "taxonomy-run", Purpose: "taxonomy-mapper", OwnerKind: "taxonomy-work", OwnerRef: "work-one",
		Model: "deepseek-v4-pro", PromptVersion: "taxonomy-v2", ExecutionTimeout: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := agentworker.New(database, map[string]agentworker.Executor{
		"taxonomy-mapper": agentworker.ExecutorFunc(func(context.Context, agentruntime.Claim, agentworker.Emitter) (agentworker.ExecutionResult, error) {
			return agentworker.ExecutionResult{}, agentworker.Terminal(errors.New("typed result protocol failure"))
		}),
	}, nil, agentworker.Config{WorkerID: "worker-one", LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.ProcessOne(ctx)
	if err != nil || !processed {
		t.Fatalf("process run = %v, %v", processed, err)
	}
	run, err := database.GetRun(ctx, "taxonomy-run")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != agentruntime.RunFailed {
		t.Fatalf("terminal domain failure was requeued instead of failed: %#v", run)
	}
	item, err := database.GetWorkItemForSubject(ctx, worklist.KindAgent, worklist.SubjectAgentRun, run.ID)
	if err != nil || item.Attempt != 1 || item.State != worklist.StateFailed {
		t.Fatalf("terminal work item = %#v, %v", item, err)
	}
}

func TestWorkerCancelsExecutionAtRunDeadline(t *testing.T) {
	database := testpostgres.New(t)
	ctx := context.Background()
	if _, err := database.CreateRun(ctx, agentruntime.CreateRun{
		ID: "deadline-run", Purpose: "assistant", OwnerKind: "environment", OwnerRef: "environment-one",
		Model: "deepseek-v4-pro", PromptVersion: "assistant-v1", ExecutionTimeout: 200 * time.Millisecond,
	}); err != nil {
		t.Fatal(err)
	}
	worker, err := agentworker.New(database, map[string]agentworker.Executor{
		"assistant": agentworker.ExecutorFunc(func(ctx context.Context, _ agentruntime.Claim, _ agentworker.Emitter) (agentworker.ExecutionResult, error) {
			<-ctx.Done()
			return agentworker.ExecutionResult{}, ctx.Err()
		}),
	}, nil, agentworker.Config{WorkerID: "worker-one", LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.ProcessOne(ctx)
	if err != nil || !processed {
		t.Fatalf("process deadline run = %v, %v", processed, err)
	}
	run, err := database.GetRun(ctx, "deadline-run")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != agentruntime.RunFailed || run.CompletedAt == nil {
		t.Fatalf("deadline run = %#v", run)
	}
}

func TestWorkerDoesNotCancelACompletedDomainRunDuringLeaseRenewal(t *testing.T) {
	database := testpostgres.New(t)
	ctx := context.Background()
	if _, err := database.CreateSession(ctx, agentruntime.Session{
		ID: "generator-session", Purpose: "generator", OwnerKind: "authoring-session", OwnerRef: "authoring-one", UserRef: "user-one",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateMessageAndRun(ctx, agentruntime.Message{
		ID: "generator-message", SessionID: "generator-session", Role: "user", Content: "generate",
	}, agentruntime.CreateRun{
		ID: "generator-run", SessionID: "generator-session", Purpose: "generator", OwnerKind: "authoring-session", OwnerRef: "authoring-one",
		Model: "deepseek-v4-pro", PromptVersion: "generator-v1", ExecutionTimeout: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}

	var canceled atomic.Bool
	leaseTTL := 90 * time.Millisecond
	worker, err := agentworker.New(database, map[string]agentworker.Executor{
		"generator": agentworker.ExecutorFunc(func(ctx context.Context, claim agentruntime.Claim, _ agentworker.Emitter) (agentworker.ExecutionResult, error) {
			if err := database.CompleteWithMessage(ctx, claim, agentruntime.Message{
				ID: "generator-result", SessionID: claim.Run.SessionID, Role: "assistant", Content: "submitted",
			}, time.Now().UTC()); err != nil {
				return agentworker.ExecutionResult{}, err
			}
			select {
			case <-ctx.Done():
				canceled.Store(true)
				return agentworker.ExecutionResult{}, ctx.Err()
			case <-time.After(2 * leaseTTL):
				return agentworker.ExecutionResult{Finalized: true}, nil
			}
		}),
	}, nil, agentworker.Config{WorkerID: "worker-one", LeaseTTL: leaseTTL})
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.ProcessOne(ctx)
	if err != nil || !processed {
		t.Fatalf("process run = %v, %v", processed, err)
	}
	if canceled.Load() {
		t.Fatal("domain-finalized run was canceled by a stale lease renewal")
	}
	run, err := database.GetRun(ctx, "generator-run")
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != agentruntime.RunSucceeded {
		t.Fatalf("run status = %q, want succeeded", run.Status)
	}
}

func TestWorkerCancelsExecutionWhenLeaseRenewalRequestTimesOut(t *testing.T) {
	deadline := time.Now().UTC().Add(time.Minute)
	claim := agentruntime.Claim{
		WorkItemID: "work-renewal-timeout", Attempt: 1, LeaseOwner: "worker-one",
		Run: agentruntime.Run{
			ID: "run-renewal-timeout", Purpose: "assistant", DeadlineAt: &deadline,
		},
	}
	store := &blockingRenewalStore{claim: claim}
	leaseTTL := 150 * time.Millisecond
	worker, err := agentworker.New(store, map[string]agentworker.Executor{
		"assistant": agentworker.ExecutorFunc(func(ctx context.Context, _ agentruntime.Claim, _ agentworker.Emitter) (agentworker.ExecutionResult, error) {
			<-ctx.Done()
			return agentworker.ExecutionResult{}, ctx.Err()
		}),
	}, nil, agentworker.Config{WorkerID: "worker-one", LeaseTTL: leaseTTL})
	if err != nil {
		t.Fatal(err)
	}

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("process run = %v, %v", processed, err)
	}
	if !store.renewedWithDeadline.Load() {
		t.Fatal("lease renewal was not issued with a bounded context")
	}
}

type blockingRenewalStore struct {
	claim               agentruntime.Claim
	claimed             atomic.Bool
	renewedWithDeadline atomic.Bool
}

func (s *blockingRenewalStore) ClaimNext(context.Context, string, time.Duration, time.Time) (*agentruntime.Claim, error) {
	if s.claimed.Swap(true) {
		return nil, nil
	}
	claim := s.claim
	return &claim, nil
}

func (s *blockingRenewalStore) RenewLease(ctx context.Context, _ agentruntime.Claim, leaseTTL time.Duration, _ time.Time) error {
	_, hasDeadline := ctx.Deadline()
	s.renewedWithDeadline.Store(hasDeadline)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * leaseTTL):
		return errors.New("lease renewal request had no deadline")
	}
}

func (*blockingRenewalStore) Requeue(context.Context, agentruntime.Claim, time.Time, string, time.Time) error {
	return errors.New("lease-lost execution must not be requeued")
}

func (*blockingRenewalStore) CompleteWithMessage(context.Context, agentruntime.Claim, agentruntime.Message, time.Time) error {
	return errors.New("lease-lost execution must not be completed")
}

func (*blockingRenewalStore) Fail(context.Context, agentruntime.Claim, string, time.Time) error {
	return errors.New("lease-lost execution must not be failed")
}

func (*blockingRenewalStore) GetRun(context.Context, string) (*agentruntime.Run, error) {
	return nil, errors.New("not called for a timed out lease renewal")
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
