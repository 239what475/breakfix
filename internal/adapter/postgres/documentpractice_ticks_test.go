package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	app "github.com/breakfix/breakfix/internal/application/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/audit"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

// This file drives the real application ticks - one watchdog pass and the
// batch scheduler loop - against live PostgreSQL. The repository tests fence
// the SQL; the application fakes cover the mapping policy on the memory
// store; here the two meet: a parked workflow whose bound action failed maps
// onto Failed and the batch item follows it, which is the tier below the
// merged watchdog/controls E2E chain. The Agent roles are never invoked by
// these ticks, so every role is a stub that refuses to run.

type ticksReader struct{}

func (ticksReader) ReadPage(string, string) (app.Page, error) {
	return domain.Page{}, errors.New("the ticks under test never read a page")
}

func (ticksReader) ReadMetadata(string) (app.Metadata, error) {
	return app.Metadata{}, errors.New("the ticks under test never read metadata")
}

type ticksPlanner struct{}

func (ticksPlanner) Propose(context.Context, app.Page, app.Metadata, []domain.EvidenceReference) (domain.LearningUnitPlan, error) {
	return domain.LearningUnitPlan{}, errors.New("the ticks under test never plan")
}

type ticksPlanReviewer struct{}

func (ticksPlanReviewer) ReviewPlan(context.Context, string, domain.LearningUnitPlan) (domain.ReviewOpinion, error) {
	return domain.ReviewOpinion{}, errors.New("the ticks under test never review a plan")
}

type ticksGenerator struct{}

func (ticksGenerator) Generate(context.Context, domain.LearningUnitPlan, runnable.RuntimeProfile, runnable.LifecyclePolicy) (app.CandidateBlueprint, error) {
	return app.CandidateBlueprint{}, errors.New("the ticks under test never generate")
}

type ticksCandidateReviewer struct{}

func (ticksCandidateReviewer) ReviewCandidate(context.Context, string, domain.LearningUnitPlan, domain.PracticeCandidate, []app.GeneratedFile) (domain.ReviewOpinion, error) {
	return domain.ReviewOpinion{}, errors.New("the ticks under test never review a candidate")
}

type ticksVerificationReviewer struct{}

func (ticksVerificationReviewer) ReviewVerification(context.Context, string, domain.LearningUnitPlan, runnable.VerificationReport) (domain.ReviewOpinion, error) {
	return domain.ReviewOpinion{}, errors.New("the ticks under test never review a verification")
}

type ticksProfiles struct{}

func (ticksProfiles) DocumentationRuntimeConstraints() []domain.RuntimeConstraint { return nil }

func (ticksProfiles) DocumentationLifecyclePolicy() runnable.LifecyclePolicy {
	return runnable.LifecyclePolicy{}
}

func (ticksProfiles) ResolveDocumentationRuntimeProfile(domain.RuntimeConstraint) (runnable.RuntimeProfile, error) {
	return runnable.RuntimeProfile{}, errors.New("the ticks under test never resolve a profile")
}

func newTicksPipeline(t *testing.T, service *app.Service) *app.AgentPipeline {
	t.Helper()
	pipeline, err := app.NewAgentPipeline(
		service, ticksReader{}, ticksPlanner{},
		[]app.PlanReviewRole{ticksPlanReviewer{}}, ticksGenerator{},
		[]app.CandidateReviewRole{ticksCandidateReviewer{}},
		[]app.VerificationReviewRole{ticksVerificationReviewer{}}, ticksProfiles{},
		app.AgentPipelineConfig{Model: "ticks-test-model", PromptVersion: "ticks-prompt", ToolVersion: "ticks-tools", PolicyVersion: "ticks-policy"},
	)
	if err != nil {
		t.Fatalf("new ticks pipeline: %v", err)
	}
	return pipeline
}

func TestWatchdogAndSchedulerTicksDriveParkedWorkflowOntoItsBatchItem(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	service, err := app.NewService(database.DocumentPractice, database.Runnable)
	if err != nil {
		t.Fatalf("new document practice service: %v", err)
	}
	pipeline := newTicksPipeline(t, service)

	// The merged E2E chain's park: a workflow stalled at MaterializingArtifact
	// whose bound public action already failed on the worker side.
	workflow := seedWorkflowInState(t, database, "document-workflow-ticks", domain.MaterializingArtifact, now)
	identity := runnable.ActionIdentity{
		Content:      runnable.ContentIdentity{Kind: "practice", ID: "page-ticks", Revision: "1"},
		SpecDigest:   testRunnableDigest("c"),
		Phase:        runnable.ActionMaterializeArtifact,
		StateVersion: workflow.StateVersion,
	}
	if err := database.DocumentPractice.BindRunnableAction(ctx, workflow.ID, identity, now); err != nil {
		t.Fatalf("bind runnable action: %v", err)
	}
	insertWatchdogRunnableAction(t, database, identity, "failed", 2, now)

	// A live batch whose single item runs against the parked workflow.
	batch := domain.DocumentBatch{
		ID:          domain.NewBatchID(now),
		State:       domain.BatchPending,
		Scope:       domain.BatchScope{Kind: domain.BatchScopePages, Pages: []string{"docs/ticks"}},
		Concurrency: 2,
		Resolution:  domain.BatchResolution{Resolved: 1},
		TotalItems:  1,
		CreatedBy:   "u-admin",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	items := []domain.BatchItem{{
		ID: domain.NewBatchItemID(batch.ID, 0), BatchID: batch.ID, Ordinal: 0,
		PagePath: "docs/ticks", Anchor: "ticks-h2", Title: "Ticks",
		WorkflowID: workflow.ID, State: domain.ItemRunning, CreatedAt: now, UpdatedAt: now,
	}}
	action := audit.HumanAction{
		ID: "audit-batch-ticks", UserID: "u-admin", Action: audit.ActionDocumentationBatchCreate,
		TargetType: audit.TargetDocumentBatch, TargetID: batch.ID, Detail: []byte(`{}`), CreatedAt: now,
	}
	if err := database.DocumentPractice.CreateBatch(ctx, batch, items, &action); err != nil {
		t.Fatalf("create batch: %v", err)
	}

	// The real scheduler loop owns the batch lifecycle from here.
	scheduler, err := app.NewBatchScheduler(service, pipeline)
	if err != nil {
		t.Fatalf("new batch scheduler: %v", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = scheduler.Run(runCtx) }()

	// The real watchdog pass maps the failed action onto the workflow.
	if err := pipeline.Watchdog(ctx); err != nil {
		t.Fatalf("watchdog tick: %v", err)
	}

	// The scheduler starts the batch, backfills the item from the terminal
	// workflow, and closes the batch - no worker involved.
	deadline := time.Now().Add(20 * time.Second)
	for {
		got, counts, err := database.DocumentPractice.GetBatch(ctx, batch.ID)
		if err != nil {
			t.Fatalf("get batch: %v", err)
		}
		if got.State == domain.BatchCompleted && counts[domain.ItemFailed] == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scheduler did not follow the watchdog mapping: batch state %s, counts %#v", got.State, counts)
		}
		time.Sleep(500 * time.Millisecond)
	}

	failed, err := database.DocumentPractice.GetWorkflow(ctx, workflow.ID)
	if err != nil || failed.State != domain.Failed {
		t.Fatalf("workflow after watchdog = %s, %v; want Failed", failed.State, err)
	}
	var ownerRole string
	if err := database.conn.QueryRowContext(ctx, `SELECT owner_role FROM document_artifact_ledger WHERE kind = 'watchdog.force_fail' AND workflow_id = ?`, workflow.ID).Scan(&ownerRole); err != nil {
		t.Fatalf("watchdog ledger entry: %v", err)
	}
	if ownerRole != "system" {
		t.Fatalf("watchdog ledger owner role = %s, want system", ownerRole)
	}
	humanRows, err := database.Audit.ListHumanActions(ctx, HumanActionFilter{Limit: 50})
	if err != nil {
		t.Fatalf("list human actions: %v", err)
	}
	for _, row := range humanRows {
		if row.TargetID == workflow.ID {
			t.Fatalf("the machine mapping wrote a human action row: %#v", row)
		}
	}
}
