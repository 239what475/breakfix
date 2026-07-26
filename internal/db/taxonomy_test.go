package db

import (
	"context"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/taxonomy"
)

func TestTaxonomyWorkDeduplicatesAndUsesExpiringLeases(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	item := testTaxonomyWork("mapping-one", "challenge-a", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	first, err := database.EnqueueTaxonomyWork(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := item
	duplicate.ID = "mapping-duplicate"
	second, err := database.EnqueueTaxonomyWork(ctx, duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("same challenge revision was not deduplicated: %q != %q", second.ID, first.ID)
	}

	claimed, err := database.ClaimTaxonomyWork(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != first.ID || claimed.LeaseOwner != "worker-a" {
		t.Fatalf("unexpected claimed work: %#v", claimed)
	}
	next, err := database.ClaimTaxonomyWork(ctx, "worker-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if next != nil {
		t.Fatalf("active lease was stolen: %#v", next)
	}
	claimed.State = taxonomy.WorkPending
	claimed.Round = 1
	claimed.Candidate = &taxonomy.ChangeSet{}
	if err := database.SaveClaimedTaxonomyWork(ctx, *claimed); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := database.ClaimTaxonomyWork(ctx, "worker-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed == nil || reclaimed.LeaseOwner != "worker-b" || reclaimed.Round != 1 || reclaimed.Candidate == nil {
		t.Fatalf("released work was not durably requeued: %#v", reclaimed)
	}
}

func TestTaxonomyWorkPersistsDelayedRetryBeforeItCanBeClaimed(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	item, err := database.EnqueueTaxonomyWork(ctx, testTaxonomyWork("mapping-delayed", "challenge-delayed", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimTaxonomyWork(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != item.ID {
		t.Fatalf("unexpected claimed work: %#v", claimed)
	}
	delayedUntil := time.Now().UTC().Add(time.Minute)
	claimed.TechnicalFailures = 0
	claimed.ExecutionFailures = 2
	claimed.NextRunAt = delayedUntil
	claimed.LastError = "taxonomy agent temporarily unavailable"
	if err := database.SaveClaimedTaxonomyWork(ctx, *claimed); err != nil {
		t.Fatal(err)
	}
	persisted, err := database.GetTaxonomyWorkByChallenge(ctx, claimed.Kind, claimed.ChallengeID, claimed.ChallengeRevision)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ExecutionFailures != 2 || persisted.NextRunAt.IsZero() || persisted.NextRunAt.Before(delayedUntil.Add(-time.Second)) || persisted.LastError != claimed.LastError {
		t.Fatalf("delayed retry state was not persisted: %#v", persisted)
	}
	next, err := database.ClaimTaxonomyWork(ctx, "worker-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if next != nil {
		t.Fatalf("work with a future next_run_at was claimed: %#v", next)
	}
}

func TestTaxonomyPublisherLeaseIsExclusive(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	first, err := database.AcquireTaxonomyLease(ctx, "publisher", "server-a", time.Minute)
	if err != nil || !first {
		t.Fatalf("first acquire = %v, %v", first, err)
	}
	second, err := database.AcquireTaxonomyLease(ctx, "publisher", "server-b", time.Minute)
	if err != nil || second {
		t.Fatalf("second acquire = %v, %v", second, err)
	}
	if err := database.ReleaseTaxonomyLease(ctx, "publisher", "server-a"); err != nil {
		t.Fatal(err)
	}
	third, err := database.AcquireTaxonomyLease(ctx, "publisher", "server-b", time.Minute)
	if err != nil || !third {
		t.Fatalf("acquire after release = %v, %v", third, err)
	}
}

func testTaxonomyWork(id, challengeID, revision string) taxonomy.WorkItem {
	return taxonomy.WorkItem{
		ID: id, Kind: taxonomy.WorkKindMapping, ChallengeID: challengeID, ChallengeRevision: revision,
		MapperSessionID: "mapper", CurriculumSession: "curriculum", SRESession: "sre", State: taxonomy.WorkPending,
	}
}
