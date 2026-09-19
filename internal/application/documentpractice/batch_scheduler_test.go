package documentpractice

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
)

// itemReader serves the fixture page bound to whatever page/anchor the
// scheduler requested, the way the real library binds context per page.
type itemReader struct{ base domain.Page }

func (r itemReader) ReadPage(path, anchor string) (domain.Page, error) {
	page := r.base
	page.Path = path
	page.Anchor = anchor
	page.Context.PagePath = path
	page.Context.Anchor = anchor
	return page, nil
}

func (r itemReader) ReadMetadata(path string) (domain.Metadata, error) {
	page, err := r.ReadPage(path, "")
	if err != nil {
		return domain.Metadata{}, err
	}
	return domain.Metadata{Context: page.Context, Path: path, Title: "Fixture", Anchors: []string{"configure-h2", "deploy-h2", "pod-lifetime"}}, nil
}

// itemPlanner proposes the fixture plan bound to the requested page context,
// blocking on one gate token per call so tests hold chains mid-flight.
type itemPlanner struct {
	base  domain.LearningUnitPlan
	gates chan struct{}
}

func (p itemPlanner) Propose(ctx context.Context, page domain.Page, _ domain.Metadata, evidence []domain.EvidenceReference) (domain.LearningUnitPlan, error) {
	if p.gates != nil {
		select {
		case <-p.gates:
		case <-ctx.Done():
			return domain.LearningUnitPlan{}, ctx.Err()
		}
	}
	plan := p.base
	plan.Context = page.Context
	plan.Evidence = evidence
	return plan, nil
}

type schedulerFixture struct {
	store     *memoryDocumentStore
	service   *Service
	scheduler *BatchScheduler
	batches   *BatchService
	gates     chan struct{}
	clock     time.Time
}

func newSchedulerFixture(t *testing.T, concurrency int, gate bool) *schedulerFixture {
	return newSchedulerFixturePages(t, concurrency, []string{"docs/tasks/configure", "docs/tasks/deploy"}, gate)
}

func newSchedulerFixturePages(t *testing.T, concurrency int, pages []string, gate bool) *schedulerFixture {
	t.Helper()
	store := newMemoryDocumentStore()
	f := &schedulerFixture{store: store, clock: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC), gates: make(chan struct{}, 16)}
	var clockMu sync.Mutex
	nextClock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		f.clock = f.clock.Add(time.Millisecond)
		return f.clock
	}
	f.service = &Service{store: store, now: nextClock}
	plan := validPlan()
	plan.NoPractice = true
	page := domain.Page{Context: plan.Context, Path: plan.Context.PagePath, Digest: plan.Evidence[0].Digest, Content: "# Pod lifecycle"}
	planner := itemPlanner{base: plan, gates: nil}
	if gate {
		planner.gates = f.gates
	}
	pipeline, err := NewAgentPipeline(f.service, itemReader{base: page}, planner, []PlanReviewRole{pipelineReviewer{role: "evidence"}, pipelineReviewer{role: "value"}}, pipelineGenerator{}, []CandidateReviewRole{pipelineReviewer{role: "safety"}, pipelineReviewer{role: "consistency"}}, []VerificationReviewRole{pipelineReviewer{role: "verification"}}, pipelineProfiles{}, AgentPipelineConfig{Model: "test-model", PromptVersion: "prompt-v1", ToolVersion: "tool-v1", PolicyVersion: "policy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	pipeline.now = f.service.now
	runNext := 0
	var runMu sync.Mutex
	pipeline.newRunID = func(prefix string) (string, error) {
		runMu.Lock()
		defer runMu.Unlock()
		runNext++
		return prefix + "-run-" + strconv.Itoa(runNext), nil
	}
	f.scheduler, err = NewBatchScheduler(f.service, pipeline)
	if err != nil {
		t.Fatal(err)
	}
	f.batches, err = NewBatchService(f.service, corpusFixture())
	if err != nil {
		t.Fatal(err)
	}
	// Create one batch over the given pages.
	if _, err := f.batches.CreateBatch(context.Background(), domain.BatchScope{Kind: domain.BatchScopePages, Pages: pages}, concurrency, batchCreateAction(t)); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *schedulerFixture) items(t *testing.T, batchID string) []domain.BatchItem {
	t.Helper()
	got, _, err := f.batches.GetBatch(context.Background(), batchID)
	if err != nil {
		t.Fatal(err)
	}
	_ = got
	items, _, err := f.service.store.ListBatchItems(context.Background(), domain.BatchItemFilter{BatchID: batchID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func (f *schedulerFixture) batchID(t *testing.T) string {
	t.Helper()
	batches, err := f.service.store.ListSchedulerBatches(context.Background(), []domain.BatchState{domain.BatchPending, domain.BatchRunning, domain.BatchPaused, domain.BatchCancelled, domain.BatchCompleted})
	if err != nil || len(batches) == 0 {
		t.Fatalf("batches = %#v, %v", batches, err)
	}
	return batches[0].ID
}

func eventually(t *testing.T, description string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

// tickUntil drives scheduler passes until the check holds, the way several
// real passes are needed for a transition to ripple through.
func tickUntil(t *testing.T, f *schedulerFixture, description string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := f.scheduler.tick(context.Background()); err != nil {
			t.Fatalf("tick: %v", err)
		}
		if check() {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSchedulerTopsUpToTheConcurrencyThreshold(t *testing.T) {
	f := newSchedulerFixture(t, 1, true)
	ctx := context.Background()
	batchID := f.batchID(t)

	// One ignition starts; the single concurrency slot is busy and the second
	// item stays Pending until the first chain is released.
	if err := f.scheduler.tick(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "one scheduled item", func() bool {
		scheduled, _ := f.service.store.ListBatchItemsByStates(ctx, batchID, []domain.BatchItemState{domain.ItemScheduled})
		return len(scheduled) == 1
	})
	if pending, _ := f.service.store.ListBatchItemsByStates(ctx, batchID, []domain.BatchItemState{domain.ItemPending}); len(pending) != 1 {
		t.Fatalf("pending items = %#v, want the second item held back by the threshold", pending)
	}

	// Release the first chain; its item lands NoPractice and the second item
	// is claimed on a later pass.
	f.gates <- struct{}{}
	tickUntil(t, f, "first item NoPractice", func() bool {
		for _, item := range f.items(t, batchID) {
			if item.State == domain.ItemNoPractice {
				return true
			}
		}
		return false
	})
	tickUntil(t, f, "second item scheduled", func() bool {
		scheduled, _ := f.service.store.ListBatchItemsByStates(ctx, batchID, []domain.BatchItemState{domain.ItemScheduled})
		return len(scheduled) == 1
	})
	f.gates <- struct{}{}
	tickUntil(t, f, "both items terminal", func() bool {
		terminal := 0
		for _, item := range f.items(t, batchID) {
			if item.State.Terminal() {
				terminal++
			}
		}
		return terminal == 2
	})
	tickUntil(t, f, "batch completed", func() bool {
		batch, _, err := f.batches.GetBatch(ctx, batchID)
		return err == nil && batch.State == domain.BatchCompleted
	})
}

func TestSchedulerIgnoresPausedBatchesAndResumes(t *testing.T) {
	f := newSchedulerFixturePages(t, 1, []string{"docs/tasks/configure", "docs/tasks/deploy", "docs/concepts/pods"}, true)
	ctx := context.Background()
	batchID := f.batchID(t)

	// The batch auto-starts and claims one item through its threshold.
	tickUntil(t, f, "batch auto-started with one claim", func() bool {
		batch, _, err := f.batches.GetBatch(ctx, batchID)
		if err != nil || batch.State != domain.BatchRunning {
			return false
		}
		scheduled, _ := f.service.store.ListBatchItemsByStates(ctx, batchID, []domain.BatchItemState{domain.ItemScheduled})
		return len(scheduled) == 1
	})
	if _, _, err := f.batches.PauseBatch(ctx, batchID, "u-admin", "hold during maintenance"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	// While paused no new items are claimed even though one slot is free.
	if err := f.scheduler.tick(ctx); err != nil {
		t.Fatal(err)
	}
	if pending, _ := f.service.store.ListBatchItemsByStates(ctx, batchID, []domain.BatchItemState{domain.ItemPending}); len(pending) != 2 {
		t.Fatalf("paused batch pending items = %#v, want both held back", pending)
	}
	if _, _, err := f.batches.ResumeBatch(ctx, batchID, "u-admin", "maintenance done"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	// Resuming frees nothing by itself: the held chain still owns its slot.
	// Releasing it lets the scheduler claim the next item.
	f.gates <- struct{}{}
	tickUntil(t, f, "resumed batch claims the next item", func() bool {
		scheduled, _ := f.service.store.ListBatchItemsByStates(ctx, batchID, []domain.BatchItemState{domain.ItemScheduled})
		noPractice, _ := f.service.store.ListBatchItemsByStates(ctx, batchID, []domain.BatchItemState{domain.ItemNoPractice})
		return len(scheduled) == 1 && len(noPractice) == 1
	})
	// The pause and resume verbs are audited.
	paused := 0
	resumed := 0
	for _, action := range f.store.humanActions {
		if action.Action == "documentation.batch.pause" {
			paused++
		}
		if action.Action == "documentation.batch.resume" {
			resumed++
		}
	}
	if paused != 1 || resumed != 1 {
		t.Fatalf("pause/resume audits = %d/%d", paused, resumed)
	}
}

func TestSchedulerCancelLeavesInFlightItemsToTheirTerminal(t *testing.T) {
	f := newSchedulerFixture(t, 2, true)
	ctx := context.Background()
	batchID := f.batchID(t)

	// Both items ignite and block mid-chain.
	if err := f.scheduler.tick(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, "two scheduled items", func() bool {
		scheduled, _ := f.service.store.ListBatchItemsByStates(ctx, batchID, []domain.BatchItemState{domain.ItemScheduled})
		return len(scheduled) == 2
	})

	if _, _, err := f.batches.CancelBatch(ctx, batchID, "u-admin", "wrong scope"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	// In-flight items run to their terminal states after the cancel.
	f.gates <- struct{}{}
	f.gates <- struct{}{}
	tickUntil(t, f, "in-flight items drained to terminal", func() bool {
		terminal := 0
		for _, item := range f.items(t, batchID) {
			if item.State.Terminal() {
				terminal++
			}
		}
		return terminal == 2
	})
	batch, _, err := f.batches.GetBatch(ctx, batchID)
	if err != nil || batch.State != domain.BatchCancelled {
		t.Fatalf("cancelled batch = %#v, %v", batch, err)
	}
	// A cancelled batch is left alone by later passes.
	if err := f.scheduler.tick(ctx); err != nil {
		t.Fatal(err)
	}
	if batch, _, _ = f.batches.GetBatch(ctx, batchID); batch.State != domain.BatchCancelled {
		t.Fatalf("cancelled batch moved to %s", batch.State)
	}
}

func TestSchedulerBackfillsCrashOrphans(t *testing.T) {
	f := newSchedulerFixture(t, 2, false)
	ctx := context.Background()
	batchID := f.batchID(t)
	items := f.items(t, batchID)

	// Simulate a crash mid-ignition: the item is Scheduled, the workflow was
	// created in Planning, and no goroutine is alive.
	if _, err := f.service.store.TransitionBatchItem(ctx, items[0].ID, domain.ItemPending, domain.ItemScheduled, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	workflow, err := f.service.Start(ctx, items[0].WorkflowID, testPageIdentity(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// The orphan returns to Pending and is immediately re-eligible.
	tickUntil(t, f, "orphan re-enqueued and re-ignited", func() bool {
		for _, item := range f.items(t, batchID) {
			if item.ID == items[0].ID && item.State == domain.ItemNoPractice {
				return true
			}
		}
		return false
	})
	_ = workflow
}

func TestSchedulerBackfillsTerminalWorkflowsAndClosesTheBatch(t *testing.T) {
	f := newSchedulerFixture(t, 2, false)
	ctx := context.Background()
	batchID := f.batchID(t)
	items := f.items(t, batchID)

	// Item 0: workflow already Published (e.g. replay of a published page).
	// Item 1: workflow stuck mid-chain after a crash - it keeps the batch open
	// with its item Scheduled until a human restarts the workflow.
	f.store.workflows[items[0].WorkflowID] = mustWorkflow(t, items[0].WorkflowID, domain.Published)
	f.store.workflows[items[1].WorkflowID] = mustWorkflow(t, items[1].WorkflowID, domain.Generating)
	for index, item := range items {
		state := domain.ItemRunning
		if index == 1 {
			state = domain.ItemScheduled
		}
		if _, err := f.service.store.TransitionBatchItem(ctx, item.ID, domain.ItemPending, state, "", time.Now()); err != nil {
			t.Fatalf("seed item state: %v", err)
		}
	}

	tickUntil(t, f, "published item backfilled", func() bool {
		items := f.items(t, batchID)
		return items[0].State == domain.ItemPublished
	})
	got := f.items(t, batchID)
	if got[0].State != domain.ItemPublished {
		t.Fatalf("published workflow item = %s", got[0].State)
	}
	if got[1].State != domain.ItemScheduled {
		t.Fatalf("mid-chain orphan item = %s, want it held for human rescue", got[1].State)
	}
	batch, _, err := f.batches.GetBatch(ctx, batchID)
	if err != nil || batch.State != domain.BatchRunning {
		t.Fatalf("batch with an in-flight item = %s, %v", batch.State, err)
	}

	// The human rescue (restart to Planning) lets the scheduler re-ignite.
	f.store.workflows[items[1].WorkflowID] = mustWorkflow(t, items[1].WorkflowID, domain.Planning)
	tickUntil(t, f, "rescued item terminal", func() bool {
		for _, item := range f.items(t, batchID) {
			if item.ID == items[1].ID && item.State.Terminal() {
				return true
			}
		}
		return false
	})
	tickUntil(t, f, "batch completed", func() bool {
		batch, _, err = f.batches.GetBatch(ctx, batchID)
		return err == nil && batch.State == domain.BatchCompleted
	})
}

func TestBatchRetryReenqueuesFailedItems(t *testing.T) {
	f := newSchedulerFixture(t, 2, false)
	ctx := context.Background()
	batchID := f.batchID(t)
	items := f.items(t, batchID)

	// Item 0 failed its chain; its workflow is Failed.
	for _, item := range items {
		f.store.workflows[item.WorkflowID] = mustWorkflow(t, item.WorkflowID, domain.Failed)
	}
	for _, item := range items {
		if _, err := f.service.store.TransitionBatchItem(ctx, item.ID, domain.ItemPending, domain.ItemFailed, "agent failed", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if won, err := f.service.store.TransitionBatchState(ctx, batchID, domain.BatchPending, domain.BatchRunning, nil, time.Now()); err != nil || !won {
		t.Fatalf("start batch: %t %v", won, err)
	}
	batch, counts, retried, err := f.batches.RetryFailedItems(ctx, batchID, "u-admin", "transient planner outage")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if batch.State != domain.BatchRunning {
		t.Fatalf("batch state = %s", batch.State)
	}
	if retried != 2 {
		t.Fatalf("retried = %d", retried)
	}
	if counts[domain.ItemPending] != 2 {
		t.Fatalf("counts after retry = %#v", counts)
	}
	// The workflows returned to Planning, ready for a fresh chain.
	for _, item := range items {
		workflow, err := f.service.store.GetWorkflow(ctx, item.WorkflowID)
		if err != nil || workflow.State != domain.Planning || workflow.Revision != 2 {
			t.Fatalf("restarted workflow = %#v, %v", workflow, err)
		}
	}
	// The retry verb is audited once for the batch, plus one restart per workflow.
	retries := 0
	restarts := 0
	for _, action := range f.store.humanActions {
		if action.Action == "documentation.batch.retry" {
			retries++
		}
		if action.Action == "documentation.workflow.restart" {
			restarts++
		}
	}
	if retries != 1 || restarts != 2 {
		t.Fatalf("retry audits = %d/%d", retries, restarts)
	}
	// Retrying a Cancelled (not Running/Paused) batch is refused.
	if _, _, err := f.batches.CancelBatch(ctx, batchID, "u-admin", "done with this batch"); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, _, _, err := f.batches.RetryFailedItems(ctx, batchID, "u-admin", "again"); err == nil {
		t.Fatal("retry of a cancelled batch was accepted")
	}
}

func mustWorkflow(t *testing.T, id string, state domain.WorkflowState) domain.Workflow {
	t.Helper()
	workflow, err := domain.NewWorkflow(id, time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	workflow.State = state
	return workflow
}
