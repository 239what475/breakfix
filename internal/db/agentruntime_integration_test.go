package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
)

func TestAgentRunClaimsFenceStaleAttemptsAndPersistOneFinalMessage(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	session := agentruntime.Session{
		ID:        "session-one",
		Purpose:   "assistant",
		OwnerKind: "environment",
		OwnerRef:  "environment-one",
		UserRef:   "user-one",
	}
	if _, err := database.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}
	_, err := database.CreateMessageAndRun(ctx, agentruntime.Message{
		ID: "message-user", SessionID: session.ID, Role: "user", Content: "help me", CreatedAt: now,
	}, agentruntime.CreateRun{
		ID:            "run-one",
		SessionID:     session.ID,
		Purpose:       "assistant",
		OwnerKind:     "environment",
		OwnerRef:      "environment-one",
		InputRevision: "environment-one",
		Model:         "deepseek-v4-pro",
		PromptVersion: "assistant-v1",
		DeadlineAt:    now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create message and run: %v", err)
	}
	claimNow := time.Now().UTC()

	claims := make(chan *agentruntime.Claim, 3)
	errs := make(chan error, 3)
	var workers sync.WaitGroup
	for _, worker := range []string{"worker-a", "worker-b", "worker-c"} {
		workers.Add(1)
		go func(worker string) {
			defer workers.Done()
			claim, err := database.ClaimNext(ctx, worker, time.Minute, claimNow)
			if err != nil {
				errs <- err
				return
			}
			claims <- claim
		}(worker)
	}
	workers.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		t.Fatalf("claim run: %v", err)
	}
	var first *agentruntime.Claim
	for claim := range claims {
		if claim == nil {
			continue
		}
		if first != nil {
			t.Fatalf("more than one worker claimed run: %#v and %#v", first, claim)
		}
		first = claim
	}
	if first == nil || first.Run.Attempt != 1 {
		t.Fatalf("first claim = %#v, want attempt one", first)
	}

	if err := database.Requeue(ctx, *first, claimNow.Add(time.Second), "transient model timeout", claimNow.Add(time.Millisecond)); err != nil {
		t.Fatalf("requeue first attempt: %v", err)
	}
	second, err := database.ClaimNext(ctx, "worker-next", time.Minute, claimNow.Add(2*time.Second))
	if err != nil {
		t.Fatalf("claim requeued run: %v", err)
	}
	if second == nil || second.Run.Attempt != 2 {
		t.Fatalf("second claim = %#v, want attempt two", second)
	}
	if err := database.CompleteWithMessage(ctx, *first, agentruntime.Message{
		ID: "message-stale", SessionID: session.ID, Role: "assistant", Content: "stale response",
	}, claimNow.Add(3*time.Second)); !errors.Is(err, agentruntime.ErrLeaseLost) {
		t.Fatalf("stale attempt completion error = %v, want lease lost", err)
	}
	if err := database.CompleteWithMessage(ctx, *second, agentruntime.Message{
		ID: "message-final", SessionID: session.ID, Role: "assistant", Content: "durable final response",
	}, claimNow.Add(3*time.Second)); err != nil {
		t.Fatalf("complete current attempt: %v", err)
	}

	run, err := database.GetRun(ctx, "run-one")
	if err != nil {
		t.Fatalf("get completed run: %v", err)
	}
	if run.Status != agentruntime.RunSucceeded || run.Attempt != 2 || run.LeaseOwner != "" || run.CompletedAt == nil {
		t.Fatalf("completed run = %#v", run)
	}
	messages, err := database.ListMessages(ctx, session.ID)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].ID != "message-final" || messages[1].Sequence != 2 {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestAgentRunExpiredLeaseIsClaimedAsNewAttempt(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := database.CreateRun(ctx, agentruntime.CreateRun{
		ID:            "run-expired-lease",
		Purpose:       "taxonomy-mapper",
		OwnerKind:     "taxonomy-work-item",
		OwnerRef:      "work-one",
		Model:         "deepseek-v4-pro",
		PromptVersion: "mapper-v1",
		DeadlineAt:    now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	claimNow := time.Now().UTC()
	first, err := database.ClaimNext(ctx, "worker-crashed", time.Second, claimNow)
	if err != nil || first == nil {
		t.Fatalf("claim first attempt = %#v, %v", first, err)
	}
	second, err := database.ClaimNext(ctx, "worker-recovery", time.Minute, claimNow.Add(2*time.Second))
	if err != nil || second == nil {
		t.Fatalf("claim expired lease = %#v, %v", second, err)
	}
	if second.Run.ID != first.Run.ID || second.Run.Attempt != first.Run.Attempt+1 || second.LeaseOwner == first.LeaseOwner {
		t.Fatalf("expired lease did not create a fenced next attempt: first=%#v second=%#v", first, second)
	}
	if err := database.Fail(ctx, *first, "late worker", claimNow.Add(3*time.Second)); !errors.Is(err, agentruntime.ErrLeaseLost) {
		t.Fatalf("expired attempt failure error = %v, want lease lost", err)
	}
}
