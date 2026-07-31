package db

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/worklist"
)

func TestWorkItemConcurrentClaimHasOneWinner(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	item := enqueueTestWorkItem(t, database, "work-build-concurrent", worklist.KindBuild, now, time.Hour)

	claims := make(chan *worklist.Claim, 3)
	errs := make(chan error, 3)
	var wait sync.WaitGroup
	for _, workerID := range []string{"builder-a", "builder-b", "builder-c"} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			claim, err := database.ClaimWorkItem(ctx, worklist.KindBuild, workerID, time.Minute, now)
			if err != nil {
				errs <- err
				return
			}
			claims <- claim
		}()
	}
	wait.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		t.Fatalf("claim work item: %v", err)
	}

	var winner *worklist.Claim
	for claim := range claims {
		if claim == nil {
			continue
		}
		if winner != nil {
			t.Fatalf("two workers claimed one item: %#v and %#v", winner, claim)
		}
		winner = claim
	}
	if winner == nil || winner.Item.ID != item.ID || winner.Item.Attempt != 1 {
		t.Fatalf("winning claim = %#v", winner)
	}
}

func TestWorkItemExpiredLeaseCreatesFencedAttempt(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	enqueueTestWorkItem(t, database, "work-verify-recovery", worklist.KindVerify, now, time.Hour)

	first, err := database.ClaimWorkItem(ctx, worklist.KindVerify, "verifier-crashed", time.Second, now)
	if err != nil || first == nil {
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	second, err := database.ClaimWorkItem(ctx, worklist.KindVerify, "verifier-recovery", time.Minute, now.Add(2*time.Second))
	if err != nil || second == nil {
		t.Fatalf("recovery claim = %#v, %v", second, err)
	}
	if second.Item.ID != first.Item.ID || second.Item.Attempt != first.Item.Attempt+1 || second.LeaseOwner == first.LeaseOwner {
		t.Fatalf("recovery did not create a new fence: first=%#v second=%#v", first, second)
	}
	if err := database.CompleteWorkItem(ctx, *first, now.Add(3*time.Second)); !errors.Is(err, worklist.ErrLeaseLost) {
		t.Fatalf("late completion = %v, want lease lost", err)
	}
	if err := database.CompleteWorkItem(ctx, *second, now.Add(3*time.Second)); err != nil {
		t.Fatalf("complete current attempt: %v", err)
	}
}

func TestWorkItemRejectsResultAfterDeadline(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	enqueueTestWorkItem(t, database, "work-publish-deadline", worklist.KindArtifactPublish, now, time.Second)

	claim, err := database.ClaimWorkItem(ctx, worklist.KindArtifactPublish, "publisher", time.Minute, now)
	if err != nil || claim == nil {
		t.Fatalf("claim = %#v, %v", claim, err)
	}
	if err := database.CompleteWorkItem(ctx, *claim, now.Add(2*time.Second)); !errors.Is(err, worklist.ErrLeaseLost) {
		t.Fatalf("completion after deadline = %v, want lease lost", err)
	}
	if next, err := database.ClaimWorkItem(ctx, worklist.KindArtifactPublish, "publisher-next", time.Minute, now.Add(2*time.Second)); err != nil || next != nil {
		t.Fatalf("claim expired item = %#v, %v", next, err)
	}
	stored, err := database.GetWorkItem(ctx, claim.Item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != worklist.StateFailed || stored.ErrorCode != "deadline_exceeded" {
		t.Fatalf("expired work item = %#v", stored)
	}
}

func TestArtifactCleanupHasNoDeadlineAndCanFinish(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err := database.EnqueueWorkItem(ctx, worklist.CreateItem{
		ID:          "work-cleanup",
		Kind:        worklist.KindArtifactCleanup,
		SubjectType: worklist.SubjectCandidateRevision,
		SubjectID:   "candidate-cleanup",
		NextRunAt:   now,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := database.ClaimWorkItem(ctx, worklist.KindArtifactCleanup, "publisher", time.Minute, now)
	if err != nil || claim == nil {
		t.Fatalf("claim cleanup = %#v, %v", claim, err)
	}
	if err := database.CompleteWorkItem(ctx, *claim, now.Add(time.Second)); err != nil {
		t.Fatalf("complete cleanup: %v", err)
	}
}

func TestWorklistInspectionAndMetricsSnapshot(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	enqueueTestWorkItem(t, database, "work-build-observed", worklist.KindBuild, now, time.Hour)
	build, err := database.ClaimWorkItem(ctx, worklist.KindBuild, "builder-observed", time.Minute, now)
	if err != nil || build == nil {
		t.Fatalf("claim observed build = %#v, %v", build, err)
	}
	if err := database.CompleteWorkItem(ctx, *build, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	enqueueTestWorkItem(t, database, "work-verify-observed", worklist.KindVerify, now, time.Hour)
	verify, err := database.ClaimWorkItem(ctx, worklist.KindVerify, "verifier-observed", time.Minute, now)
	if err != nil || verify == nil {
		t.Fatalf("claim observed verify = %#v, %v", verify, err)
	}

	enqueueTestWorkItem(t, database, "work-publish-observed", worklist.KindArtifactPublish, now, time.Hour)
	publish, err := database.ClaimWorkItem(ctx, worklist.KindArtifactPublish, "publisher-observed", time.Minute, now)
	if err != nil || publish == nil {
		t.Fatalf("claim observed publication = %#v, %v", publish, err)
	}
	if err := database.FailWorkItem(ctx, *publish, "REGISTRY_UNAVAILABLE", "registry is unavailable", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	if _, err := database.EnqueueWorkItem(ctx, worklist.CreateItem{
		ID: "work-cleanup-observed", Kind: worklist.KindArtifactCleanup,
		SubjectType: worklist.SubjectCandidateRevision, SubjectID: "candidate-cleanup-observed", NextRunAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	items, err := database.InspectWorkItems(ctx, worklist.InspectionFilter{Kind: worklist.KindVerify, State: worklist.StateRunning, Limit: 10})
	if err != nil || len(items) != 1 || items[0].ID != verify.Item.ID || items[0].LeaseOwner != verify.LeaseOwner {
		t.Fatalf("inspected work items = %#v, %v", items, err)
	}
	snapshot, err := database.WorklistSnapshot(ctx, now.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	byKind := make(map[worklist.Kind]worklist.KindSnapshot, len(snapshot.Kinds))
	for _, item := range snapshot.Kinds {
		byKind[item.Kind] = item
	}
	if byKind[worklist.KindBuild].Succeeded != 1 || byKind[worklist.KindBuild].TerminalCount != 1 || byKind[worklist.KindBuild].TerminalDurationSeconds <= 0 {
		t.Fatalf("build snapshot = %#v", byKind[worklist.KindBuild])
	}
	if byKind[worklist.KindVerify].Running != 1 {
		t.Fatalf("verify snapshot = %#v", byKind[worklist.KindVerify])
	}
	if byKind[worklist.KindArtifactCleanup].Pending != 1 || byKind[worklist.KindArtifactCleanup].OldestPendingAgeSeconds <= 0 {
		t.Fatalf("cleanup snapshot = %#v", byKind[worklist.KindArtifactCleanup])
	}
	if byKind[worklist.KindArtifactPublish].Failed != 1 {
		t.Fatalf("publication snapshot = %#v", byKind[worklist.KindArtifactPublish])
	}
	foundInfrastructureFailure := false
	for _, failure := range snapshot.Failures {
		if failure.Kind == worklist.KindArtifactPublish && failure.Class == "infrastructure" && failure.Count == 1 {
			foundInfrastructureFailure = true
		}
	}
	if !foundInfrastructureFailure {
		t.Fatalf("failure snapshot = %#v", snapshot.Failures)
	}
}

func enqueueTestWorkItem(t *testing.T, database *DB, id string, kind worklist.Kind, nextRunAt time.Time, executionTimeout time.Duration) *worklist.Item {
	t.Helper()
	item, err := database.EnqueueWorkItem(context.Background(), worklist.CreateItem{
		ID:               id,
		Kind:             kind,
		SubjectType:      worklist.SubjectCandidateRevision,
		SubjectID:        "candidate-" + id,
		NextRunAt:        nextRunAt,
		ExecutionTimeout: executionTimeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	return item
}
