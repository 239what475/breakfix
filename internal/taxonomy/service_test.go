package taxonomy_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/db"
	. "github.com/breakfix/breakfix/internal/taxonomy"
)

func TestMappingWorkflowPublishesOnlyAfterBothReviewersApprove(t *testing.T) {
	root := t.TempDir()
	entry := writeWorkflowChallenge(t, root)
	database, err := db.New(filepath.Join(root, "breakfix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runner := &scriptedRunner{mapping: mappingChangeSet(entry)}
	service := NewServiceWithRunner(database, NewStore(root), filepath.Join(root, "challenges"), runner)
	if err := service.EnqueueUnmapped(context.Background()); err != nil {
		t.Fatal(err)
	}
	processed, err := service.ProcessOne(context.Background(), "worker-a")
	if err != nil || !processed {
		t.Fatalf("mapper stage = %v, %v", processed, err)
	}
	items, err := database.ListTaxonomyWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].State != WorkPending || items[0].Round != 0 || items[0].Candidate == nil || items[0].CurriculumReview != nil || items[0].SREReview != nil {
		t.Fatalf("mapper candidate was not durably staged: %#v", items)
	}
	processed, err = service.ProcessOne(context.Background(), "worker-b")
	if err != nil || !processed {
		t.Fatalf("reviewer stage = %v, %v", processed, err)
	}
	items, err = database.ListTaxonomyWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].State != WorkReadyPublish || items[0].Round != 1 || items[0].CurriculumReview == nil || items[0].SREReview == nil {
		t.Fatalf("candidate was not durably approved: %#v", items)
	}
	processed, err = service.ProcessOne(context.Background(), "worker-c")
	if err != nil || !processed {
		t.Fatalf("publish stage = %v, %v", processed, err)
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
		t.Fatalf("published taxonomy does not expose the verified challenge: %#v, %v", mapping, ok)
	}
	items, err = database.ListTaxonomyWork(context.Background())
	if err != nil || items[0].State != WorkPublished || items[0].PublishedRevision != current.Revision {
		t.Fatalf("work publication state is not durable: %#v, %v", items, err)
	}
	if !runner.sawRoles("mapper", "curriculum-reviewer", "sre-reviewer") {
		t.Fatalf("workflow did not invoke all committee roles: %#v", runner.roles)
	}
}

func TestMappingWorkflowRequeuesRejectedCandidateUsingSameMapperSession(t *testing.T) {
	root := t.TempDir()
	entry := writeWorkflowChallenge(t, root)
	database, err := db.New(filepath.Join(root, "breakfix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runner := &scriptedRunner{mapping: mappingChangeSet(entry), rejectCurriculumOnce: true}
	service := NewServiceWithRunner(database, NewStore(root), filepath.Join(root, "challenges"), runner)
	if err := service.EnqueueUnmapped(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProcessOne(context.Background(), "worker-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProcessOne(context.Background(), "worker-b"); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListTaxonomyWork(context.Background())
	if err != nil || items[0].State != WorkPending || items[0].CurriculumReview == nil || items[0].CurriculumReview.Decision != ReviewReject {
		t.Fatalf("rejected candidate was not requeued: %#v, %v", items, err)
	}
	if items[0].SREReview == nil || items[0].Round != 1 {
		t.Fatalf("rejected round did not persist the complete review pair: %#v", items)
	}
	if _, err := service.ProcessOne(context.Background(), "worker-c"); err != nil {
		t.Fatal(err)
	}
	if !runner.mapperResumed {
		t.Fatal("mapper revision did not resume its persisted session")
	}
	items, err = database.ListTaxonomyWork(context.Background())
	if err != nil || items[0].State != WorkPending || items[0].Round != 1 || items[0].CurriculumReview != nil || items[0].SREReview != nil {
		t.Fatalf("revised candidate was not staged for a new reviewer pair: %#v, %v", items, err)
	}
	if _, err := service.ProcessOne(context.Background(), "worker-d"); err != nil {
		t.Fatal(err)
	}
	items, err = database.ListTaxonomyWork(context.Background())
	if err != nil || items[0].State != WorkReadyPublish || items[0].Round != 2 {
		t.Fatalf("revised candidate was not re-reviewed: %#v, %v", items, err)
	}
}

func TestMappingWorkflowPersistsMapperSessionBeforeAgentFailure(t *testing.T) {
	root := t.TempDir()
	entry := writeWorkflowChallenge(t, root)
	database, err := db.New(filepath.Join(root, "breakfix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runner := &scriptedRunner{mapperFailures: 1}
	service := NewServiceWithRunner(database, NewStore(root), filepath.Join(root, "challenges"), runner)
	if _, err := service.EnqueueChallenge(context.Background(), entry, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProcessOne(context.Background(), "worker-a"); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListTaxonomyWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].State != WorkPending || !items[0].MapperStarted || items[0].TechnicalFailures != 1 || items[0].ExecutionFailures != 0 || items[0].LastError == "" {
		t.Fatalf("failed mapper did not persist its session start: %#v", items)
	}
}

func TestMappingWorkflowRequeuesInvalidMapperOutputUsingSameSession(t *testing.T) {
	root := t.TempDir()
	entry := writeWorkflowChallenge(t, root)
	database, err := db.New(filepath.Join(root, "breakfix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runner := &scriptedRunner{mapping: mappingChangeSet(entry), invalidMapperOnce: true}
	service := NewServiceWithRunner(database, NewStore(root), filepath.Join(root, "challenges"), runner)
	if _, err := service.EnqueueChallenge(context.Background(), entry, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProcessOne(context.Background(), "worker-a"); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListTaxonomyWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].State != WorkPending || !items[0].MapperStarted || items[0].TechnicalFailures != 1 || items[0].LastError == "" {
		t.Fatalf("invalid mapper output was not strictly rejected and requeued: %#v", items)
	}
	if _, err := service.ProcessOne(context.Background(), "worker-b"); err != nil {
		t.Fatal(err)
	}
	if !runner.mapperResumed {
		t.Fatal("corrected mapper output did not resume the persisted session")
	}
	items, err = database.ListTaxonomyWork(context.Background())
	if err != nil || items[0].State != WorkPending || items[0].Candidate == nil || items[0].CurriculumReview != nil || items[0].SREReview != nil {
		t.Fatalf("corrected mapper output did not reach the reviewer stage: %#v, %v", items, err)
	}
	if _, err := service.ProcessOne(context.Background(), "worker-c"); err != nil {
		t.Fatal(err)
	}
	items, err = database.ListTaxonomyWork(context.Background())
	if err != nil || items[0].State != WorkReadyPublish {
		t.Fatalf("corrected mapper output was not approved by the reviewer pair: %#v, %v", items, err)
	}
}

func TestMappingWorkflowRerunsBothReviewersAfterOneFails(t *testing.T) {
	root := t.TempDir()
	entry := writeWorkflowChallenge(t, root)
	database, err := db.New(filepath.Join(root, "breakfix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runner := &scriptedRunner{
		mapping:          mappingChangeSet(entry),
		reviewerFailures: map[string]int{"sre-reviewer": 1},
	}
	service := NewServiceWithRunner(database, NewStore(root), filepath.Join(root, "challenges"), runner)
	if _, err := service.EnqueueChallenge(context.Background(), entry, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProcessOne(context.Background(), "worker-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProcessOne(context.Background(), "worker-b"); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListTaxonomyWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].State != WorkPending || items[0].Candidate == nil || items[0].CurriculumReview != nil || items[0].SREReview != nil || items[0].TechnicalFailures != 1 {
		t.Fatalf("failed reviewer pair left partial durable state: %#v", items)
	}
	if runner.callCount("curriculum-reviewer") != 1 || runner.callCount("sre-reviewer") != 1 {
		t.Fatalf("reviewer pair was not run together: %#v", runner.roles)
	}
	if _, err := service.ProcessOne(context.Background(), "worker-c"); err != nil {
		t.Fatal(err)
	}
	items, err = database.ListTaxonomyWork(context.Background())
	if err != nil || items[0].State != WorkReadyPublish || items[0].TechnicalFailures != 0 || items[0].CurriculumReview == nil || items[0].SREReview == nil {
		t.Fatalf("reviewer retry did not complete as a pair: %#v, %v", items, err)
	}
	if runner.callCount("curriculum-reviewer") != 2 || runner.callCount("sre-reviewer") != 2 || !runner.curriculumResumed || !runner.sreResumed {
		t.Fatalf("reviewers did not both resume after pair failure: %#v", runner)
	}
}

func TestMappingWorkflowClearsPartialReviewerStateBeforeRetryingPair(t *testing.T) {
	root := t.TempDir()
	entry := writeWorkflowChallenge(t, root)
	database, err := db.New(filepath.Join(root, "breakfix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runner := &scriptedRunner{
		mapping:          mappingChangeSet(entry),
		reviewerFailures: map[string]int{"sre-reviewer": 1},
	}
	service := NewServiceWithRunner(database, NewStore(root), filepath.Join(root, "challenges"), runner)
	if _, err := service.EnqueueChallenge(context.Background(), entry, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProcessOne(context.Background(), "worker-a"); err != nil {
		t.Fatal(err)
	}
	claimed, err := database.ClaimTaxonomyWork(context.Background(), "worker-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.Candidate == nil {
		t.Fatalf("mapper candidate was not available for partial-state recovery: %#v", claimed)
	}
	claimed.CurriculumReview = &Review{Decision: ReviewApprove}
	if err := database.SaveClaimedTaxonomyWork(context.Background(), *claimed); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProcessOne(context.Background(), "worker-c"); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListTaxonomyWork(context.Background())
	if err != nil || len(items) != 1 || items[0].CurriculumReview != nil || items[0].SREReview != nil || items[0].TechnicalFailures != 1 {
		t.Fatalf("partial reviewer state survived retry preparation: %#v, %v", items, err)
	}
	if runner.callCount("curriculum-reviewer") != 1 || runner.callCount("sre-reviewer") != 1 {
		t.Fatalf("partial reviewer state did not rerun the full pair: %#v", runner.roles)
	}
}

func TestMappingWorkflowBacksOffAfterTenTechnicalFailures(t *testing.T) {
	root := t.TempDir()
	entry := writeWorkflowChallenge(t, root)
	database, err := db.New(filepath.Join(root, "breakfix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runner := &scriptedRunner{mapperFailures: 10}
	service := NewServiceWithRunner(database, NewStore(root), filepath.Join(root, "challenges"), runner)
	queued, err := service.EnqueueChallenge(context.Background(), entry, "")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 10; attempt++ {
		processed, err := service.ProcessOne(context.Background(), fmt.Sprintf("worker-%d", attempt))
		if err != nil || !processed {
			t.Fatalf("attempt %d = %v, %v", attempt+1, processed, err)
		}
	}
	items, err := database.ListTaxonomyWork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].State != WorkPending || items[0].Round != 0 || items[0].TechnicalFailures != 0 || items[0].ExecutionFailures != 1 || !items[0].NextRunAt.After(time.Now()) || items[0].MapperSessionID != queued.MapperSessionID || !items[0].MapperStarted {
		t.Fatalf("ten technical failures did not produce resumable delayed work: %#v", items)
	}
	claimed, err := database.ClaimTaxonomyWork(context.Background(), "worker-later", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Fatalf("delayed work was claimed before next_run_at: %#v", claimed)
	}
}

func TestMappingWorkflowCancelsWhenArtifactRevisionDisappears(t *testing.T) {
	root := t.TempDir()
	entry := writeWorkflowChallenge(t, root)
	database, err := db.New(filepath.Join(root, "breakfix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runner := &scriptedRunner{mapping: mappingChangeSet(entry)}
	service := NewServiceWithRunner(database, NewStore(root), filepath.Join(root, "challenges"), runner)
	if _, err := service.EnqueueChallenge(context.Background(), entry, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(entry.Dir); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ProcessOne(context.Background(), "worker-a"); err != nil {
		t.Fatal(err)
	}
	items, err := database.ListTaxonomyWork(context.Background())
	if err != nil || len(items) != 1 || items[0].State != WorkCancelled || items[0].TechnicalFailures != 0 {
		t.Fatalf("missing artifact was retried instead of cancelled: %#v, %v", items, err)
	}
	if len(runner.roles) != 0 {
		t.Fatalf("cancelled work invoked an agent: %#v", runner.roles)
	}
}

type scriptedRunner struct {
	mu                   sync.Mutex
	mapping              ChangeSet
	mapperFailures       int
	invalidMapperOnce    bool
	rejectCurriculumOnce bool
	mapperResumed        bool
	curriculumResumed    bool
	sreResumed           bool
	roles                []string
	reviewerFailures     map[string]int
	roleCalls            map[string]int
}

func (r *scriptedRunner) Run(_ context.Context, request AgentRequest) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.roles = append(r.roles, request.Role)
	if r.roleCalls == nil {
		r.roleCalls = make(map[string]int)
	}
	r.roleCalls[request.Role]++
	switch request.Role {
	case "mapper":
		if request.Resume {
			r.mapperResumed = true
		}
		if r.mapperFailures > 0 {
			r.mapperFailures--
			return "", errors.New("model unavailable")
		}
		if r.invalidMapperOnce {
			r.invalidMapperOnce = false
			return `{"skills":[],"tags":[],"challenge_mappings":[],"skill_mappings":[]}`, nil
		}
		data, err := json.Marshal(r.mapping)
		return string(data), err
	case "curriculum-reviewer":
		if request.Resume {
			r.curriculumResumed = true
		}
		if r.reviewerFailures[request.Role] > 0 {
			r.reviewerFailures[request.Role]--
			return "", errors.New("reviewer unavailable")
		}
		if r.rejectCurriculumOnce {
			r.rejectCurriculumOnce = false
			return `{"decision":"reject","feedback":"请重新核对主要学习目标。"}`, nil
		}
		return `{"decision":"approve"}`, nil
	case "sre-reviewer":
		if request.Resume {
			r.sreResumed = true
		}
		if r.reviewerFailures[request.Role] > 0 {
			r.reviewerFailures[request.Role]--
			return "", errors.New("reviewer unavailable")
		}
		return `{"decision":"approve"}`, nil
	default:
		return "", os.ErrInvalid
	}
}

func (r *scriptedRunner) callCount(role string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.roleCalls[role]
}

func (r *scriptedRunner) sawRoles(expected ...string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[string]bool, len(r.roles))
	for _, role := range r.roles {
		seen[role] = true
	}
	for _, role := range expected {
		if !seen[role] {
			return false
		}
	}
	return true
}

func writeWorkflowChallenge(t *testing.T, root string) challenge.Entry {
	t.Helper()
	dir := filepath.Join(root, "challenges", "cleanup-logs")
	writeWorkflowFile(t, filepath.Join(dir, "challenge.yaml"), "id: challenge-test\ntitle: Repair cleanup logs\ntype: script\nruntime: container\ndifficulty: easy\ndescription: Repair a broken log cleanup task.\nimage: test:v1\npublished_at: 2026-07-25T00:00:00Z\ncheckpoints:\n  - id: cleanup-ready\n    title: Cleanup works\n    description: Cleanup works for old logs.\n    hint: hints/cleanup-ready.md\n")
	writeWorkflowFile(t, filepath.Join(dir, "Dockerfile"), "FROM test\n")
	writeWorkflowFile(t, filepath.Join(dir, "generate.sh"), "#!/bin/sh\n")
	writeWorkflowFile(t, filepath.Join(dir, "problem.md"), "Repair the failed cleanup task.\n")
	writeWorkflowFile(t, filepath.Join(dir, "solution.md"), "Inspect then repair the task.\n")
	writeWorkflowFile(t, filepath.Join(dir, "hints", "cleanup-ready.md"), "Inspect the old logs.\n")
	writeWorkflowFile(t, filepath.Join(dir, "checks", "checkpoints.sh"), "#!/bin/sh\n")
	writeWorkflowFile(t, filepath.Join(dir, "answer.sh"), "#!/bin/sh\n")
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
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
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
