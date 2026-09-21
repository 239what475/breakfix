package documentpractice

import (
	"context"
	"testing"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
)

func TestIgnitionDispatcherAcknowledgesDurablyAndDrivesTheChain(t *testing.T) {
	now := time.Date(2026, 9, 21, 16, 0, 0, 0, time.UTC)
	fixture := newRetryFixture(t, now)
	store, service := fixture.service(t)
	pipeline := fixture.pipeline(t, service, fakePlanAgent{plan: fixture.plan},
		[]PlanReviewRole{pipelineReviewer{role: "evidence"}, pipelineReviewer{role: "value"}},
		pipelineGenerator{blueprint: fixture.blueprint},
		[]CandidateReviewRole{pipelineReviewer{role: "safety"}, pipelineReviewer{role: "consistency"}})
	dispatcher, err := NewIgnitionDispatcher(pipeline)
	if err != nil {
		t.Fatal(err)
	}

	// The acknowledgment returns immediately with the durable Planning state
	// and the ignition audit; the chain has not run yet.
	acknowledged, err := dispatcher.Ignite(context.Background(), "agent-dispatcher-chain", fixture.page.Path, "", nil)
	if err != nil || acknowledged.State != domain.Planning {
		t.Fatalf("ignition acknowledgment = %#v, %v", acknowledged, err)
	}
	if len(store.humanActions) != 0 {
		t.Fatalf("ignition without an action recorded a human audit: %#v", store.humanActions)
	}

	// The background chain drives the workflow to the first runtime phase.
	deadline := time.Now().Add(5 * time.Second)
	for {
		workflow, err := store.GetWorkflow(context.Background(), acknowledged.ID)
		if err != nil {
			t.Fatal(err)
		}
		if workflow.State == domain.MaterializingArtifact {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background chain did not park the workflow: %s", workflow.State)
		}
		time.Sleep(2 * time.Millisecond)
	}

	// A repeat ignition after the chain finished re-observes the durable state
	// and cannot start a second chain.
	replayed, err := dispatcher.Ignite(context.Background(), acknowledged.ID, fixture.page.Path, "", nil)
	if err != nil || replayed.State != domain.MaterializingArtifact {
		t.Fatalf("repeat ignition = %#v, %v", replayed, err)
	}
	if len(store.actions) != 1 {
		t.Fatalf("runnable bindings after repeat ignition = %d, want exactly one", len(store.actions))
	}
}

func TestIgnitionDispatcherDeduplicatesConcurrentChains(t *testing.T) {
	now := time.Date(2026, 9, 21, 17, 0, 0, 0, time.UTC)
	fixture := newRetryFixture(t, now)
	store, service := fixture.service(t)
	// The planner blocks until the test releases it, holding the first chain
	// in Planning while the second ignition arrives.
	release := make(chan struct{})
	planner := &blockingPlanAgent{plan: fixture.plan, release: release}
	pipeline := fixture.pipeline(t, service, planner,
		[]PlanReviewRole{pipelineReviewer{role: "evidence"}, pipelineReviewer{role: "value"}},
		pipelineGenerator{blueprint: fixture.blueprint},
		[]CandidateReviewRole{pipelineReviewer{role: "safety"}, pipelineReviewer{role: "consistency"}})
	dispatcher, err := NewIgnitionDispatcher(pipeline)
	if err != nil {
		t.Fatal(err)
	}

	first, err := dispatcher.Ignite(context.Background(), "agent-dispatcher-dedupe", fixture.page.Path, "", nil)
	if err != nil || first.State != domain.Planning {
		t.Fatalf("first ignition = %#v, %v", first, err)
	}
	if !dispatcher.Live(first.ID) {
		t.Fatal("first ignition did not register a live chain")
	}
	second, err := dispatcher.Ignite(context.Background(), first.ID, fixture.page.Path, "", nil)
	if err != nil || second.State != domain.Planning {
		t.Fatalf("second ignition = %#v, %v", second, err)
	}
	if plans := countLedgerPlans(t, store, first.ID); plans != 0 {
		t.Fatalf("blocked chain already submitted %d plans", plans)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		workflow, err := store.GetWorkflow(context.Background(), first.ID)
		if err != nil {
			t.Fatal(err)
		}
		if workflow.State == domain.MaterializingArtifact {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("released chain did not park the workflow: %s", workflow.State)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if kinds := countLedgerKinds(store.get(t, first.ID)); kinds["learning-unit-plan"] != 1 {
		t.Fatalf("deduplicated ignition ran %d plan chains, want 1", kinds["learning-unit-plan"])
	}
	if dispatcher.Live(first.ID) {
		t.Fatal("finished chain left its slot claimed")
	}
}

// blockingPlanAgent holds every proposal until its release channel closes.
type blockingPlanAgent struct {
	plan    domain.LearningUnitPlan
	release chan struct{}
}

func (a *blockingPlanAgent) Propose(ctx context.Context, _ domain.Page, _ domain.Metadata, _ []domain.EvidenceReference) (domain.LearningUnitPlan, error) {
	select {
	case <-a.release:
	case <-ctx.Done():
		return domain.LearningUnitPlan{}, ctx.Err()
	}
	return a.plan, nil
}

func countLedgerPlans(t *testing.T, store *memoryDocumentStore, workflowID string) int {
	t.Helper()
	return countLedgerKinds(store.get(t, workflowID))["learning-unit-plan"]
}

func (s *memoryDocumentStore) get(t *testing.T, workflowID string) domain.Workflow {
	t.Helper()
	workflow, err := s.GetWorkflow(context.Background(), workflowID)
	if err != nil {
		t.Fatal(err)
	}
	return workflow
}
