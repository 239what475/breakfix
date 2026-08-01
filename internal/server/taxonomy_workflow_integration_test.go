package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/taxonomy"
	"github.com/breakfix/breakfix/internal/testpostgres"
)

func TestTaxonomyWorkflowCancelsWhenPublishedArtifactIsStale(t *testing.T) {
	database := testpostgres.New(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	workflow, _, err := database.CreateOrGetTaxonomyWorkflow(ctx, "chal-stale", "sha256:"+strings.Repeat("a", 64), "", now)
	if err != nil {
		t.Fatalf("create taxonomy workflow: %v", err)
	}
	handler := &Handler{db: database, challengesDir: t.TempDir(), taxonomy: taxonomy.NewStore(t.TempDir())}
	if _, err := handler.taxonomyChallenge(ctx, *workflow); !errors.Is(err, taxonomy.ErrLeaseLost) {
		t.Fatalf("read stale taxonomy challenge error = %v, want lease loss", err)
	}
	stored, err := database.GetTaxonomyWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load stale taxonomy workflow: %v", err)
	}
	if stored.State != taxonomy.WorkflowCancelled {
		t.Fatalf("stale taxonomy workflow state = %s, want Cancelled", stored.State)
	}
}

func TestTaxonomyPublicationRecoveryCompletesFilesystemPublishedSnapshot(t *testing.T) {
	database := testpostgres.New(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 1, 13, 0, 0, 0, time.UTC)
	challengeID := "chal-recovery"
	challengeRevision := "sha256:" + strings.Repeat("b", 64)
	workflow, _, err := database.CreateOrGetTaxonomyWorkflow(ctx, challengeID, challengeRevision, "", now)
	if err != nil {
		t.Fatalf("create taxonomy workflow: %v", err)
	}
	claim, err := database.ClaimTaxonomyWorkflow(ctx, "taxonomy-worker", time.Minute, now)
	if err != nil || claim == nil {
		t.Fatalf("claim taxonomy workflow = %#v, %v", claim, err)
	}
	mapper, err := database.StartTaxonomyAgentRun(ctx, *claim, taxonomy.AgentRoleMapper, "test-model", now)
	if err != nil {
		t.Fatalf("start mapper: %v", err)
	}
	changes := taxonomy.ChangeSet{ChallengeMappings: []taxonomy.ChallengeMappingChange{{
		Operation: taxonomy.ChangeUpsert,
		Value:     &taxonomy.ChallengeMapping{Challenge: taxonomy.ChallengeRef{ID: challengeID, Title: "Recovery challenge", Revision: challengeRevision}},
	}}}
	if err := database.FinalizeTaxonomyMapper(ctx, *claim, mapper.ID, changes, now); err != nil {
		t.Fatalf("finalize mapper: %v", err)
	}
	claim, err = database.RefreshTaxonomyClaim(ctx, workflow.ID, claim.LeaseOwner, now)
	if err != nil {
		t.Fatalf("refresh reviewer claim: %v", err)
	}
	curriculum, err := database.StartTaxonomyAgentRun(ctx, *claim, taxonomy.AgentRoleCurriculumReviewer, "test-model", now)
	if err != nil {
		t.Fatalf("start curriculum review: %v", err)
	}
	sre, err := database.StartTaxonomyAgentRun(ctx, *claim, taxonomy.AgentRoleSREReviewer, "test-model", now)
	if err != nil {
		t.Fatalf("start SRE review: %v", err)
	}
	if err := database.FinalizeTaxonomyReviewPair(ctx, *claim, curriculum.ID, sre.ID,
		taxonomy.Review{Decision: taxonomy.ReviewApprove}, taxonomy.Review{Decision: taxonomy.ReviewApprove}, "", now); err != nil {
		t.Fatalf("finalize review pair: %v", err)
	}
	claim, err = database.RefreshTaxonomyClaim(ctx, workflow.ID, claim.LeaseOwner, now)
	if err != nil {
		t.Fatalf("refresh publishing claim: %v", err)
	}

	store := taxonomy.NewStore(t.TempDir())
	expected, err := store.PreviewRevision(taxonomy.Snapshot{})
	if err != nil {
		t.Fatalf("preview taxonomy snapshot: %v", err)
	}
	if err := database.SetTaxonomyExpectedSnapshot(ctx, *claim, expected, now); err != nil {
		t.Fatalf("persist expected snapshot: %v", err)
	}
	published, err := store.Publish(taxonomy.Snapshot{})
	if err != nil {
		t.Fatalf("publish taxonomy snapshot before simulated crash: %v", err)
	}
	if published.Revision != expected {
		t.Fatalf("published revision = %q, want %q", published.Revision, expected)
	}

	handler := &Handler{db: database, taxonomy: store}
	if err := handler.RecoverTaxonomyPublications(ctx); err != nil {
		t.Fatalf("recover filesystem-published taxonomy workflow: %v", err)
	}
	stored, err := database.GetTaxonomyWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load recovered taxonomy workflow: %v", err)
	}
	if stored.State != taxonomy.WorkflowCompleted || stored.PublishedRevision != expected {
		t.Fatalf("recovered taxonomy workflow = %#v", stored)
	}
}

func TestTaxonomyPublicationSerializesConcurrentMappings(t *testing.T) {
	database := testpostgres.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	root := t.TempDir()
	challengesDir := filepath.Join(root, "challenges")

	first := writeTaxonomyWorkflowChallenge(t, challengesDir, "taxonomy-concurrent-one", "Concurrent one")
	second := writeTaxonomyWorkflowChallenge(t, challengesDir, "taxonomy-concurrent-two", "Concurrent two")
	store := taxonomy.NewStore(root)
	base, err := store.Publish(taxonomy.Snapshot{
		Skills: []taxonomy.Skill{{
			Kind:       taxonomy.KindSkill,
			ID:         "skill-1111111111111111",
			Title:      "Inspect a service",
			Definition: "Inspect observable service state before changing it.",
			MappingGuidance: taxonomy.MappingGuidance{
				OutcomeWhen: []string{"The challenge requires inspecting a service state."},
			},
		}},
		Tags: []taxonomy.Tag{{
			Kind:       taxonomy.KindTag,
			ID:         "tag-1111111111111111",
			Title:      "Operations",
			Definition: "Operational troubleshooting exercises.",
			MappingGuidance: taxonomy.MappingGuidance{
				IncludeWhen: []string{"The challenge asks the learner to inspect or repair an operational system."},
			},
		}},
	})
	if err != nil {
		t.Fatalf("publish base taxonomy snapshot: %v", err)
	}

	firstClaim := prepareTaxonomyPublication(t, database, first, base.Revision, now)
	secondClaim := prepareTaxonomyPublication(t, database, second, base.Revision, now)
	handler := &Handler{db: database, challengesDir: challengesDir, taxonomy: store}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for _, claim := range []taxonomy.Claim{firstClaim, secondClaim} {
		wait.Add(1)
		go func(claim taxonomy.Claim) {
			defer wait.Done()
			<-start
			if err := handler.completeTaxonomyPublication(ctx, claim, now); err != nil {
				errs <- fmt.Errorf("publish %s: %w", claim.Workflow.ChallengeID, err)
				return
			}
			errs <- nil
		}(claim)
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("publish concurrent taxonomy mapping: %v", err)
		}
	}

	current, err := store.LoadCurrent()
	if err != nil {
		t.Fatalf("load concurrently published taxonomy snapshot: %v", err)
	}
	mappings := make(map[string]taxonomy.ChallengeMapping, len(current.ChallengeMappings))
	for _, mapping := range current.ChallengeMappings {
		mappings[mapping.Challenge.ID] = mapping
	}
	for _, entry := range []*challenge.Entry{first, second} {
		mapping, exists := mappings[entry.ID]
		if !exists || mapping.Challenge.Revision != entry.Revision {
			t.Fatalf("current taxonomy lost concurrent mapping for %s: %#v", entry.ID, current.ChallengeMappings)
		}
		workflow, err := database.GetTaxonomyWorkflowByChallenge(ctx, entry.ID, entry.Revision)
		if err != nil {
			t.Fatalf("load completed workflow for %s: %v", entry.ID, err)
		}
		if workflow.State != taxonomy.WorkflowCompleted || workflow.PublishedRevision == "" {
			t.Fatalf("workflow for %s = %#v, want completed publication", entry.ID, workflow)
		}
	}
}

func prepareTaxonomyPublication(t *testing.T, database *db.DB, entry *challenge.Entry, baseRevision string, now time.Time) taxonomy.Claim {
	t.Helper()
	ctx := context.Background()
	workflow, _, err := database.CreateOrGetTaxonomyWorkflow(ctx, entry.ID, entry.Revision, baseRevision, now)
	if err != nil {
		t.Fatalf("create taxonomy workflow for %s: %v", entry.ID, err)
	}
	claim, err := database.ClaimTaxonomyWorkflow(ctx, "taxonomy-worker-"+entry.ID, time.Minute, now)
	if err != nil || claim == nil || claim.Workflow.ID != workflow.ID {
		t.Fatalf("claim taxonomy workflow for %s = %#v, %v", entry.ID, claim, err)
	}
	changes := taxonomy.ChangeSet{ChallengeMappings: []taxonomy.ChallengeMappingChange{{
		Operation: taxonomy.ChangeUpsert,
		Value: &taxonomy.ChallengeMapping{
			Challenge: taxonomy.ChallengeRef{ID: entry.ID, Title: entry.Title, Revision: entry.Revision},
			Tags:      []taxonomy.Ref{{ID: "tag-1111111111111111", Title: "Operations"}},
			Outcomes:  []taxonomy.OutcomeRef{{ID: "skill-1111111111111111", Title: "Inspect a service", Primary: true}},
		},
	}}}
	mapper, err := database.StartTaxonomyAgentRun(ctx, *claim, taxonomy.AgentRoleMapper, "test-model", now)
	if err != nil {
		t.Fatalf("start mapper for %s: %v", entry.ID, err)
	}
	if err := database.FinalizeTaxonomyMapper(ctx, *claim, mapper.ID, changes, now); err != nil {
		t.Fatalf("finalize mapper for %s: %v", entry.ID, err)
	}
	claim, err = database.RefreshTaxonomyClaim(ctx, workflow.ID, claim.LeaseOwner, now)
	if err != nil {
		t.Fatalf("refresh reviewer claim for %s: %v", entry.ID, err)
	}
	curriculum, err := database.StartTaxonomyAgentRun(ctx, *claim, taxonomy.AgentRoleCurriculumReviewer, "test-model", now)
	if err != nil {
		t.Fatalf("start curriculum reviewer for %s: %v", entry.ID, err)
	}
	sre, err := database.StartTaxonomyAgentRun(ctx, *claim, taxonomy.AgentRoleSREReviewer, "test-model", now)
	if err != nil {
		t.Fatalf("start SRE reviewer for %s: %v", entry.ID, err)
	}
	if err := database.FinalizeTaxonomyReviewPair(ctx, *claim, curriculum.ID, sre.ID,
		taxonomy.Review{Decision: taxonomy.ReviewApprove}, taxonomy.Review{Decision: taxonomy.ReviewApprove}, baseRevision, now); err != nil {
		t.Fatalf("finalize review pair for %s: %v", entry.ID, err)
	}
	claim, err = database.RefreshTaxonomyClaim(ctx, workflow.ID, claim.LeaseOwner, now)
	if err != nil {
		t.Fatalf("refresh publication claim for %s: %v", entry.ID, err)
	}
	return *claim
}

func writeTaxonomyWorkflowChallenge(t *testing.T, root, id, title string) *challenge.Entry {
	t.Helper()
	dir := filepath.Join(root, id)
	writeTestFile(t, filepath.Join(dir, "challenge.yaml"), fmt.Sprintf("id: %s\nsource_slug: %s\ntitle: %s\nruntime: node\ndifficulty: easy\ndescription: taxonomy publication fixture\nimage: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\npublished_at: 2026-08-01T00:00:00Z\nnodes:\n  - name: host\n    title: Host\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n    node: host\n", id, id, title))
	writeTestFile(t, filepath.Join(dir, "problem.md"), "problem\n")
	writeTestFile(t, filepath.Join(dir, "solution.md"), "<!-- checkpoint: complete -->\nsolution\n")
	writeTestFile(t, filepath.Join(dir, "hints", "complete.md"), "hint\n")
	writeTestFile(t, filepath.Join(dir, "nodes", "host", "generate.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(dir, "nodes", "host", "checks.sh"), "#!/bin/sh\n")
	writeTestFile(t, filepath.Join(dir, "nodes", "host", "answer.sh"), "#!/bin/sh\n")
	entry, err := challenge.Get(root, id)
	if err != nil {
		t.Fatalf("load taxonomy fixture %s: %v", id, err)
	}
	return entry
}
