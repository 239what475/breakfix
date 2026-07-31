package taxonomy_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	. "github.com/breakfix/breakfix/internal/taxonomy"
	"github.com/breakfix/breakfix/internal/testpostgres"
)

func TestMappingWorkflowUsesGenericRunsAndPublishesAfterReviewPair(t *testing.T) {
	root := t.TempDir()
	entry := writeWorkflowChallenge(t, root)
	database := testpostgres.New(t)
	service := NewService(database, NewStore(root), filepath.Join(root, "challenges"), config.AgentConfig{Model: "test-model"})

	if _, err := service.EnqueueChallenge(context.Background(), entry, ""); err != nil {
		t.Fatal(err)
	}
	mapperRun := scheduleAndClaim(t, service, database, WorkStageMapper)
	if mapperRun.Run.Purpose != RuntimePurposeMapper || mapperRun.Run.SessionID != "" || mapperRun.Run.OwnerKind != "taxonomy-mapping" {
		t.Fatalf("mapper is not a generic no-session runtime run: %#v", mapperRun.Run)
	}
	if err := service.FinalizeMapper(context.Background(), *mapperRun, mappingChangeSet(entry)); err != nil {
		t.Fatal(err)
	}

	items, err := database.ListTaxonomyMappings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Candidate == nil || items[0].ActiveRunID != "" || items[0].CurriculumReview != nil || items[0].SREReview != nil {
		t.Fatalf("mapper finalization did not atomically stage candidate: %#v", items)
	}

	reviewRun := scheduleAndClaim(t, service, database, WorkStageReview)
	if reviewRun.Run.Purpose != RuntimePurposeReview || reviewRun.Run.SessionID != "" {
		t.Fatalf("review pair is not a generic no-session runtime run: %#v", reviewRun.Run)
	}
	if err := service.FinalizeReviewPair(context.Background(), *reviewRun, Review{Decision: ReviewApprove}, Review{Decision: ReviewApprove}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}

	current, err := NewStore(root).LoadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	index, err := NewCatalogIndex(*current, []challenge.Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if mapping, ok := index.Mapping(entry.ID); !ok || mapping.Challenge.Revision != entry.Revision {
		t.Fatalf("published taxonomy does not expose verified challenge: %#v, %v", mapping, ok)
	}
	items, err = database.ListTaxonomyMappings(context.Background())
	if err != nil || len(items) != 1 || items[0].State != MappingPublished || items[0].PublishedRevision != current.Revision || items[0].Round != 1 {
		t.Fatalf("publication state is not durable: %#v, %v", items, err)
	}
}

func TestEnqueueUnmappedBootstrapsFirstTaxonomySnapshot(t *testing.T) {
	root := t.TempDir()
	entry := writeWorkflowChallenge(t, root)
	database := testpostgres.New(t)
	service := NewService(database, NewStore(root), filepath.Join(root, "challenges"), config.AgentConfig{Model: "test-model"})

	if err := service.EnqueueUnmapped(context.Background()); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListTaxonomyMappings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("initial taxonomy work count = %d, want 1", len(items))
	}
	item := items[0]
	if item.ChallengeID != entry.ID || item.ChallengeRevision != entry.Revision || item.BaseRevision != "" || item.State != MappingPending {
		t.Fatalf("initial taxonomy work = %#v", item)
	}
}

func TestMappingWorkflowRejectStartsNewMapperRoundWithBothReviews(t *testing.T) {
	root := t.TempDir()
	entry := writeWorkflowChallenge(t, root)
	database := testpostgres.New(t)
	service := NewService(database, NewStore(root), filepath.Join(root, "challenges"), config.AgentConfig{Model: "test-model"})
	if _, err := service.EnqueueChallenge(context.Background(), entry, ""); err != nil {
		t.Fatal(err)
	}
	mapper := scheduleAndClaim(t, service, database, WorkStageMapper)
	if err := service.FinalizeMapper(context.Background(), *mapper, mappingChangeSet(entry)); err != nil {
		t.Fatal(err)
	}
	review := scheduleAndClaim(t, service, database, WorkStageReview)
	if err := service.FinalizeReviewPair(context.Background(), *review,
		Review{Decision: ReviewReject, Feedback: "请将 outcome 与实际检查点对齐。"},
		Review{Decision: ReviewApprove}); err != nil {
		t.Fatal(err)
	}

	items, err := database.ListTaxonomyMappings(context.Background())
	if err != nil || len(items) != 1 || items[0].State != MappingPending || items[0].Round != 1 || items[0].CurriculumReview == nil || items[0].SREReview == nil {
		t.Fatalf("rejected review pair was not durably committed: %#v, %v", items, err)
	}
	nextMapper := scheduleAndClaim(t, service, database, WorkStageMapper)
	input, err := DecodeRunInput(nextMapper.Run.Input)
	if err != nil {
		t.Fatal(err)
	}
	if input.Round != 1 {
		t.Fatalf("mapper did not start the next semantic round: %#v", input)
	}
}

func TestFailedAgentRunLeavesSemanticRoundUntouchedAndRerunsStage(t *testing.T) {
	root := t.TempDir()
	entry := writeWorkflowChallenge(t, root)
	database := testpostgres.New(t)
	service := NewService(database, NewStore(root), filepath.Join(root, "challenges"), config.AgentConfig{Model: "test-model"})
	if _, err := service.EnqueueChallenge(context.Background(), entry, ""); err != nil {
		t.Fatal(err)
	}
	mapper := scheduleAndClaim(t, service, database, WorkStageMapper)
	if err := database.Fail(context.Background(), *mapper, "typed result protocol failure", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProcessOne(context.Background()); err == nil {
		t.Fatal("failed taxonomy AgentRun did not request reconcile backoff")
	}
	items, err := database.ListTaxonomyMappings(context.Background())
	if err != nil || len(items) != 1 || items[0].LastError == "" || items[0].ActiveRunID != "" || items[0].Round != 0 {
		t.Fatalf("terminal generic run changed semantic state: %#v, %v", items, err)
	}
	next := scheduleAndClaim(t, service, database, WorkStageMapper)
	if next.Run.ID == mapper.Run.ID {
		t.Fatal("technical failure reused a terminal agent run")
	}
}

func TestCatalogScanCancelsActiveRunWhenArtifactDisappears(t *testing.T) {
	root := t.TempDir()
	entry := writeWorkflowChallenge(t, root)
	database := testpostgres.New(t)
	service := NewService(database, NewStore(root), filepath.Join(root, "challenges"), config.AgentConfig{Model: "test-model"})
	if _, err := service.EnqueueChallenge(context.Background(), entry, ""); err != nil {
		t.Fatal(err)
	}
	mapper := scheduleAndClaim(t, service, database, WorkStageMapper)
	if err := os.RemoveAll(entry.Dir); err != nil {
		t.Fatal(err)
	}
	if err := service.EnqueueUnmapped(context.Background()); err != nil {
		t.Fatal(err)
	}
	item, err := database.GetTaxonomyMapping(context.Background(), mapper.Run.OwnerRef)
	if err != nil {
		t.Fatal(err)
	}
	if item.State != MappingCancelled || item.ActiveRunID != "" {
		t.Fatalf("missing artifact did not cancel work: %#v", item)
	}
	run, err := database.GetRun(context.Background(), mapper.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != agentruntime.RunCancelled {
		t.Fatalf("missing artifact did not cancel active run: %#v", run)
	}
}

func scheduleAndClaim(t *testing.T, service *Service, database interface {
	ClaimNext(context.Context, string, time.Duration, time.Time) (*agentruntime.Claim, error)
}, stage WorkStage) *agentruntime.Claim {
	t.Helper()
	if _, err := service.ProcessOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	claim, err := database.ClaimNext(context.Background(), "agent-worker", time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if claim == nil {
		t.Fatalf("%s stage did not schedule an agent run", stage)
	}
	input, err := DecodeRunInput(claim.Run.Input)
	if err != nil {
		t.Fatal(err)
	}
	if input.Stage != stage {
		t.Fatalf("scheduled stage = %q, want %q", input.Stage, stage)
	}
	return claim
}

func writeWorkflowChallenge(t *testing.T, root string) challenge.Entry {
	t.Helper()
	dir := filepath.Join(root, "challenges", "cleanup-logs")
	writeWorkflowFile(t, filepath.Join(dir, "challenge.yaml"), "id: challenge-test\nsource_slug: cleanup-logs\ntitle: Repair cleanup logs\nruntime: node\ndifficulty: easy\ndescription: Repair a broken log cleanup task.\nimage: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\npublished_at: 2026-07-25T00:00:00Z\nnodes:\n  - name: operator\n    title: Operator\ncheckpoints:\n  - id: cleanup-ready\n    title: Cleanup works\n    description: Cleanup works for old logs.\n    hint: hints/cleanup-ready.md\n    node: operator\n")
	writeWorkflowFile(t, filepath.Join(dir, "problem.md"), "Repair the failed cleanup task.\n")
	writeWorkflowFile(t, filepath.Join(dir, "solution.md"), "<!-- checkpoint: cleanup-ready -->\n\nInspect then repair the task.\n")
	writeWorkflowFile(t, filepath.Join(dir, "hints", "cleanup-ready.md"), "Inspect the old logs.\n")
	writeWorkflowFile(t, filepath.Join(dir, "nodes", "operator", "generate.sh"), "#!/bin/sh\n")
	writeWorkflowFile(t, filepath.Join(dir, "nodes", "operator", "checks.sh"), "#!/bin/sh\n")
	writeWorkflowFile(t, filepath.Join(dir, "nodes", "operator", "answer.sh"), "#!/bin/sh\n")
	entry, err := challenge.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return *entry
}

func writeWorkflowFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func mappingChangeSet(entry challenge.Entry) ChangeSet {
	return ChangeSet{
		Skills: []SkillChange{{Operation: ChangeUpsert, Value: &Skill{
			Kind: KindSkill, ID: "skill-3333333333333333", Title: "Repair log cleanup failures", Definition: "Diagnose and repair failed log cleanup automation.",
			MappingGuidance: MappingGuidance{OutcomeWhen: []string{"The challenge requires repairing log cleanup."}},
		}}},
		Tags: []TagChange{{Operation: ChangeUpsert, Value: &Tag{
			Kind: KindTag, ID: "tag-3333333333333333", Title: "Linux", Definition: "Linux administration and troubleshooting.",
			MappingGuidance: MappingGuidance{IncludeWhen: []string{"Linux administration is central to the task."}},
		}}},
		ChallengeMappings: []ChallengeMappingChange{{Operation: ChangeUpsert, Value: &ChallengeMapping{
			Challenge: ChallengeRef{ID: entry.ID, Title: entry.Title, Revision: entry.Revision},
			Tags:      []Ref{{ID: "tag-3333333333333333", Title: "Linux"}},
			Outcomes:  []OutcomeRef{{ID: "skill-3333333333333333", Title: "Repair log cleanup failures", Primary: true}},
		}}},
	}
}

func TestDecodeRunInputRejectsUnknownAndTrailingData(t *testing.T) {
	for _, raw := range []string{
		`{"work_id":"work","stage":"mapper","round":0,"extra":true}`,
		`{"work_id":"work","stage":"mapper","round":0} {}`,
		`{"work_id":"","stage":"mapper","round":0}`,
	} {
		if _, err := DecodeRunInput(json.RawMessage(raw)); err == nil {
			t.Fatalf("DecodeRunInput(%s) succeeded", raw)
		}
	}
	if _, err := DecodeRunInput(json.RawMessage(`{"work_id":"work","stage":"review","round":0}`)); err != nil {
		t.Fatalf("valid run input: %v", err)
	}
}

func TestValidateReviewRejectsIncompleteDecision(t *testing.T) {
	for _, review := range []Review{
		{Decision: ReviewApprove, Feedback: "unexpected"},
		{Decision: ReviewReject},
		{Decision: "unknown"},
	} {
		if err := ValidateReview(review); err == nil {
			t.Fatalf("ValidateReview(%#v) succeeded", review)
		}
	}
	if err := ValidateReview(Review{Decision: ReviewReject, Feedback: "具体问题"}); err != nil {
		t.Fatal(err)
	}
}
