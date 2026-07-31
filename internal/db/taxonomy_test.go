package db

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/taxonomy"
)

func TestTaxonomyMappingDeduplicatesAndPersistsDomainState(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	mapping := testTaxonomyMapping("mapping-one", "challenge-a", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	first, created, err := database.EnqueueTaxonomyMapping(ctx, mapping)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first taxonomy mapping was not created")
	}
	duplicate := mapping
	duplicate.ID = "mapping-duplicate"
	second, created, err := database.EnqueueTaxonomyMapping(ctx, duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("duplicate taxonomy mapping was reported as newly created")
	}
	if second.ID != first.ID {
		t.Fatalf("same challenge revision was not deduplicated: %q != %q", second.ID, first.ID)
	}

	first.Round = 1
	first.Candidate = &taxonomy.ChangeSet{}
	first.LastError = "review requested a semantic revision"
	if err := database.SaveTaxonomyMapping(ctx, *first); err != nil {
		t.Fatal(err)
	}
	persisted, err := database.GetTaxonomyMappingByChallenge(ctx, first.ChallengeID, first.ChallengeRevision)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Round != 1 || persisted.Candidate == nil || persisted.LastError != first.LastError {
		t.Fatalf("mapping domain state was not persisted: %#v", persisted)
	}
}

func TestTaxonomyMappingIsActionableOnlyAfterAgentRunTerminates(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	mapping, _, err := database.EnqueueTaxonomyMapping(ctx, testTaxonomyMapping("mapping-active", "challenge-active", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	if err != nil {
		t.Fatal(err)
	}
	run, err := database.ScheduleTaxonomyRun(ctx, *mapping, testTaxonomyRun(t, *mapping, "taxonomy-run-active"))
	if err != nil {
		t.Fatal(err)
	}
	if next, err := database.NextTaxonomyMapping(ctx); err != nil || next != nil {
		t.Fatalf("mapping with pending AgentRun is actionable: %#v, %v", next, err)
	}
	claim, err := database.ClaimNext(ctx, "agent-worker", time.Minute, time.Now().UTC())
	if err != nil || claim == nil || claim.Run.ID != run.ID {
		t.Fatalf("claim taxonomy AgentRun = %#v, %v", claim, err)
	}
	if err := database.Fail(ctx, *claim, "typed result protocol failure", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	next, err := database.NextTaxonomyMapping(ctx)
	if err != nil || next == nil || next.ID != mapping.ID {
		t.Fatalf("mapping with terminal AgentRun was not actionable: %#v, %v", next, err)
	}
}

func TestTaxonomyMappingSchedulesOnlyOneActiveAgentRun(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	mapping, _, err := database.EnqueueTaxonomyMapping(ctx, testTaxonomyMapping("mapping-race", "challenge-race", "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"))
	if err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"taxonomy-run-a", "taxonomy-run-b"} {
		wait.Add(1)
		go func(runID string) {
			defer wait.Done()
			_, err := database.ScheduleTaxonomyRun(ctx, *mapping, testTaxonomyRun(t, *mapping, runID))
			results <- err
		}(id)
	}
	wait.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful schedules = %d, want 1", succeeded)
	}
	runs, err := database.ListRunsForOwner(ctx, "taxonomy-mapping", mapping.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("scheduled runs = %#v, %v", runs, err)
	}
}

func testTaxonomyMapping(id, challengeID, revision string) taxonomy.TaxonomyMapping {
	return taxonomy.TaxonomyMapping{ID: id, ChallengeID: challengeID, ChallengeRevision: revision, State: taxonomy.MappingPending}
}

func testTaxonomyRun(t *testing.T, mapping taxonomy.TaxonomyMapping, id string) agentruntime.CreateRun {
	t.Helper()
	input, err := json.Marshal(taxonomy.RunInput{WorkID: mapping.ID, Stage: taxonomy.WorkStageMapper, Round: mapping.Round})
	if err != nil {
		t.Fatal(err)
	}
	return agentruntime.CreateRun{
		ID: id, Purpose: taxonomy.RuntimePurposeMapper, OwnerKind: "taxonomy-mapping", OwnerRef: mapping.ID,
		Input: input, Model: "test-model", PromptVersion: "test-v1", ExecutionTimeout: time.Hour,
	}
}
