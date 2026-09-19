package documentpractice

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/audit"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
)

type fakeCorpus struct {
	pages    []CorpusPage
	sections map[string]bool
}

func (c fakeCorpus) CorpusPages() []CorpusPage { return c.pages }

func (c fakeCorpus) HasSection(section string) bool { return c.sections[section] }

func (c fakeCorpus) WorkflowContext() domain.DocumentContext {
	return domain.DocumentContext{FormatVersion: domain.FormatVersion, SourceID: "kubernetes", Repository: "https://github.com/kubernetes/website.git", Commit: strings.Repeat("a", 40), Version: "v1.34", Language: "en", License: "CC BY 4.0"}
}

func corpusFixture() fakeCorpus {
	return fakeCorpus{
		sections: map[string]bool{"docs/concepts": true, "docs/tasks": true},
		pages: []CorpusPage{
			{Path: "docs/concepts", Title: "Concepts", PageKind: "index", Anchors: []CorpusAnchor{{ID: "concepts", Level: 1}, {ID: "concept-h2", Level: 2}}},
			{Path: "docs/concepts/pods", Title: "Pods", PageKind: "content", Anchors: []CorpusAnchor{{ID: "pods", Level: 1}, {ID: "pod-lifetime", Level: 2}, {ID: "pod-phase", Level: 2}}},
			{Path: "docs/concepts/services", Title: "Services", PageKind: "content", Anchors: []CorpusAnchor{{ID: "services", Level: 1}}},
			{Path: "docs/tasks/configure", Title: "Configure", PageKind: "content", Anchors: []CorpusAnchor{{ID: "configure-h2", Level: 2}}},
			{Path: "docs/tasks/deploy", Title: "Deploy", PageKind: "content", Anchors: []CorpusAnchor{{ID: "deploy-h2", Level: 2}}},
		},
	}
}

func newBatchService(t *testing.T) (*BatchService, *memoryDocumentStore) {
	t.Helper()
	store := newMemoryDocumentStore()
	clock := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	service := &Service{store: store, now: func() time.Time { clock = clock.Add(time.Millisecond); return clock }}
	batches, err := NewBatchService(service, corpusFixture())
	if err != nil {
		t.Fatal(err)
	}
	return batches, store
}

func batchCreateAction(t *testing.T) *audit.HumanAction {
	t.Helper()
	return &audit.HumanAction{ID: "audit-batch", UserID: "u-admin", Action: audit.ActionDocumentationBatchCreate, TargetType: audit.TargetDocumentBatch, TargetID: "pending", Detail: json.RawMessage(`{}`), CreatedAt: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)}
}

func TestBatchScopeResolutionAppliesTheDeterministicAnchorRule(t *testing.T) {
	batches, store := newBatchService(t)
	ctx := context.Background()

	// A full scope drops the index page and the page without a level-2 anchor.
	batch, err := batches.CreateBatch(ctx, domain.BatchScope{Kind: domain.BatchScopeFull}, 0, batchCreateAction(t))
	if err != nil {
		t.Fatalf("create full batch: %v", err)
	}
	if batch.State != domain.BatchPending || batch.Concurrency != DefaultBatchConcurrency {
		t.Fatalf("created batch = %#v", batch)
	}
	items, _, err := store.ListBatchItems(ctx, domain.BatchItemFilter{BatchID: batch.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].PagePath != "docs/concepts/pods" || items[0].Anchor != "pod-lifetime" || items[1].PagePath != "docs/tasks/configure" || items[2].PagePath != "docs/tasks/deploy" {
		t.Fatalf("full scope items = %#v", items)
	}
	for _, item := range items {
		if item.Title == "" || !strings.HasPrefix(item.WorkflowID, "document-workflow-practice-") {
			t.Fatalf("item identity incomplete: %#v", item)
		}
	}
	// index and no-anchor pages are excluded with reasons.
	excluded := batch.Resolution.Excluded
	if len(excluded) != 2 || excluded[0].Reason != domain.ExcludedIndex || excluded[1].Reason != domain.ExcludedNoAnchor {
		t.Fatalf("resolution exclusions = %#v", excluded)
	}

	// A section scope resolves the subtree; an explicit override replaces the
	// default anchor choice for its page.
	batch, err = batches.CreateBatch(ctx, domain.BatchScope{Kind: domain.BatchScopeSections, Sections: []string{"docs/concepts"}, Overrides: []domain.BatchAnchorRef{{PagePath: "docs/concepts/pods", Anchor: "pod-phase"}}}, 3, batchCreateAction(t))
	if err != nil {
		t.Fatalf("create section batch: %v", err)
	}
	items, _, err = store.ListBatchItems(ctx, domain.BatchItemFilter{BatchID: batch.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Anchor != "pod-phase" || items[0].PagePath != "docs/concepts/pods" {
		t.Fatalf("section scope items = %#v", items)
	}
	if batch.Concurrency != 3 {
		t.Fatalf("concurrency = %d", batch.Concurrency)
	}

	// An override outside the section scope is rejected.
	if _, err := batches.CreateBatch(ctx, domain.BatchScope{Kind: domain.BatchScopePages, Pages: []string{"docs/tasks/deploy"}, Overrides: []domain.BatchAnchorRef{{PagePath: "docs/concepts/pods", Anchor: "pod-phase"}}}, 0, batchCreateAction(t)); err == nil {
		t.Fatal("override outside the scope was accepted")
	}
	if _, err := batches.CreateBatch(ctx, domain.BatchScope{Kind: domain.BatchScopePages, Pages: []string{"docs/absent"}}, 0, batchCreateAction(t)); err == nil {
		t.Fatal("unknown page scope was accepted")
	}
	if _, err := batches.CreateBatch(ctx, domain.BatchScope{Kind: domain.BatchScopeSections, Sections: []string{"docs/absent"}}, 0, batchCreateAction(t)); err == nil {
		t.Fatal("unknown section scope was accepted")
	}
	if _, err := batches.CreateBatch(ctx, domain.BatchScope{Kind: domain.BatchScopePages}, 0, batchCreateAction(t)); err == nil {
		t.Fatal("empty scope was accepted")
	}
}

func TestBatchCreationSkipsAlreadyPublishedWorkflows(t *testing.T) {
	batches, store := newBatchService(t)
	ctx := context.Background()
	store.setPublishedAnchor("docs/concepts/pods", "pod-lifetime")

	batch, err := batches.CreateBatch(ctx, domain.BatchScope{Kind: domain.BatchScopeFull}, 0, batchCreateAction(t))
	if err != nil {
		t.Fatal(err)
	}
	items, _, err := store.ListBatchItems(ctx, domain.BatchItemFilter{BatchID: batch.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	skipped := 0
	for _, item := range items {
		if item.State == domain.ItemSkipped {
			skipped++
			if item.Detail != "already-published" {
				t.Fatalf("skipped item detail = %q", item.Detail)
			}
		} else if item.State != domain.ItemPending {
			t.Fatalf("fresh item state = %s", item.State)
		}
	}
	if skipped != 1 {
		t.Fatalf("skipped items = %d, want 1", skipped)
	}

	// The batch creation is audited against the batch record in one transaction.
	if len(store.humanActions) != 1 {
		t.Fatalf("human actions = %d, want exactly the create audit", len(store.humanActions))
	}
	if store.humanActions[0].Action != audit.ActionDocumentationBatchCreate || store.humanActions[0].TargetID != batch.ID {
		t.Fatalf("create audit = %#v", store.humanActions[0])
	}
}

func TestBatchStateTransitionsAreFenced(t *testing.T) {
	cases := []struct {
		from domain.BatchState
		to   domain.BatchState
		ok   bool
	}{
		{domain.BatchPending, domain.BatchRunning, true},
		{domain.BatchPending, domain.BatchCancelled, true},
		{domain.BatchPending, domain.BatchPaused, false},
		{domain.BatchPending, domain.BatchCompleted, false},
		{domain.BatchRunning, domain.BatchPaused, true},
		{domain.BatchPaused, domain.BatchRunning, true},
		{domain.BatchRunning, domain.BatchCompleted, true},
		{domain.BatchRunning, domain.BatchCancelled, true},
		{domain.BatchPaused, domain.BatchCompleted, false},
		{domain.BatchPaused, domain.BatchCancelled, true},
		{domain.BatchCompleted, domain.BatchRunning, false},
		{domain.BatchCancelled, domain.BatchRunning, false},
	}
	for _, tc := range cases {
		err := domain.TransitionBatch(tc.from, tc.to)
		if tc.ok && err != nil {
			t.Fatalf("%s -> %s = %v", tc.from, tc.to, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s -> %s was accepted", tc.from, tc.to)
		}
	}
	// The trivial transition is always a no-op (idempotent replay).
	if err := domain.TransitionBatch(domain.BatchRunning, domain.BatchRunning); err != nil {
		t.Fatalf("self transition = %v", err)
	}
}

func TestBatchItemTerminalStatesNeverReopen(t *testing.T) {
	for _, state := range []domain.BatchItemState{domain.ItemPublished, domain.ItemNoPractice, domain.ItemRejected, domain.ItemFailed, domain.ItemSkipped, domain.ItemCancelled} {
		if !state.Terminal() {
			t.Fatalf("%s must be terminal", state)
		}
	}
	for _, state := range []domain.BatchItemState{domain.ItemPending, domain.ItemScheduled, domain.ItemRunning} {
		if state.Terminal() {
			t.Fatalf("%s must not be terminal", state)
		}
	}
	if domain.ItemPending.Valid() == false || domain.BatchPending.Valid() == false {
		t.Fatal("state validity is broken")
	}
}
