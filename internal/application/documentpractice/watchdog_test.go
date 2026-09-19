package documentpractice

import (
	"context"
	"testing"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

type watchdogFixture struct {
	store    *memoryDocumentStore
	pipeline *AgentPipeline
	action   runnable.ActionIdentity
}

func newWatchdogFixture(t *testing.T, ctx context.Context, workflowID string, state domain.WorkflowState, stateVersion int64) watchdogFixture {
	t.Helper()
	store := newMemoryDocumentStore()
	now := func() time.Time { return time.Date(2026, 9, 19, 8, 0, 0, 0, time.UTC) }
	pipeline := &AgentPipeline{service: &Service{store: store, now: now}, now: now}
	workflow, err := domain.NewWorkflow(workflowID, time.Date(2026, 9, 19, 6, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	workflow.State = state
	workflow.StateVersion = stateVersion
	store.workflows[workflowID] = workflow
	action := runnable.ActionIdentity{Content: runnable.ContentIdentity{Kind: "documentation-practice", ID: "practice-x", Revision: "r1"}, SpecDigest: serviceDigest("1"), Phase: runnable.ActionVerify, StateVersion: stateVersion}
	if err := store.BindRunnableAction(ctx, workflowID, action, workflow.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	return watchdogFixture{store: store, pipeline: pipeline, action: action}
}

func TestWatchdogMapsFailedActionImmediately(t *testing.T) {
	ctx := context.Background()
	f := newWatchdogFixture(t, ctx, "document-watchdog-failed", domain.Verifying, 6)
	f.store.setWatchdogStatus(f.action, WatchdogActionStatus{Phase: runnable.ActionVerify, State: "failed", Attempt: 2, FailureClass: "assertion", FailureCode: "pod-not-running", FailureSummary: "pod never became Running", UpdatedAt: f.pipeline.now().Add(-time.Minute)})

	if err := f.pipeline.Watchdog(ctx); err != nil {
		t.Fatal(err)
	}
	workflow, err := f.store.GetWorkflow(ctx, "document-watchdog-failed")
	if err != nil {
		t.Fatal(err)
	}
	if workflow.State != domain.Failed || workflow.StateVersion != 7 {
		t.Fatalf("watchdog mapping = %s v%d, want Failed v7", workflow.State, workflow.StateVersion)
	}
	found := false
	for _, artifact := range workflow.Artifacts {
		if artifact.Kind == "watchdog.force_fail" && artifact.OwnerRole == "system" {
			found = true
		}
	}
	if !found {
		t.Fatal("watchdog did not write its system ledger artifact")
	}
	if len(f.store.humanActions) != 0 {
		t.Fatalf("watchdog wrote %d human action rows, want none", len(f.store.humanActions))
	}
	if f.store.watchdogReasons["document-watchdog-failed"] == "" {
		t.Fatal("watchdog mapping lost its reason")
	}
}

func TestWatchdogMapsExhaustedQueuedActionOnlyAfterGrace(t *testing.T) {
	ctx := context.Background()
	f := newWatchdogFixture(t, ctx, "document-watchdog-exhausted", domain.MaterializingArtifact, 4)
	idle := f.pipeline.now().Add(-runnableActionWatchdogBudget + time.Minute)
	f.store.setWatchdogStatus(f.action, WatchdogActionStatus{Phase: runnable.ActionVerify, State: "queued", Attempt: 5, UpdatedAt: idle})

	if err := f.pipeline.Watchdog(ctx); err != nil {
		t.Fatal(err)
	}
	if workflow, err := f.store.GetWorkflow(ctx, "document-watchdog-exhausted"); err != nil || workflow.State != domain.MaterializingArtifact {
		t.Fatalf("in-grace workflow moved: %s, %v", workflow.State, err)
	}

	f.store.setWatchdogStatus(f.action, WatchdogActionStatus{Phase: runnable.ActionVerify, State: "queued", Attempt: 5, UpdatedAt: f.pipeline.now().Add(-runnableActionWatchdogBudget - time.Second)})
	if err := f.pipeline.Watchdog(ctx); err != nil {
		t.Fatal(err)
	}
	workflow, err := f.store.GetWorkflow(ctx, "document-watchdog-exhausted")
	if err != nil || workflow.State != domain.Failed {
		t.Fatalf("exhausted workflow not mapped: %s, %v", workflow.State, err)
	}
}

func TestWatchdogSparesRunningFifthAttemptUntilLeaseExpires(t *testing.T) {
	ctx := context.Background()
	f := newWatchdogFixture(t, ctx, "document-watchdog-running", domain.Verifying, 6)
	live := f.pipeline.now().Add(-runnableActionWatchdogBudget - time.Hour)
	f.store.setWatchdogStatus(f.action, WatchdogActionStatus{Phase: runnable.ActionVerify, State: "running", Attempt: 5, UpdatedAt: live, LeaseExpired: false})

	if err := f.pipeline.Watchdog(ctx); err != nil {
		t.Fatal(err)
	}
	if workflow, err := f.store.GetWorkflow(ctx, "document-watchdog-running"); err != nil || workflow.State != domain.Verifying {
		t.Fatalf("running fifth attempt was killed: %s, %v", workflow.State, err)
	}

	expired := f.pipeline.now().Add(-runnableActionWatchdogBudget - time.Second)
	f.store.setWatchdogStatus(f.action, WatchdogActionStatus{Phase: runnable.ActionVerify, State: "running", Attempt: 5, UpdatedAt: expired, LeaseExpired: true})
	if err := f.pipeline.Watchdog(ctx); err != nil {
		t.Fatal(err)
	}
	workflow, err := f.store.GetWorkflow(ctx, "document-watchdog-running")
	if err != nil || workflow.State != domain.Failed {
		t.Fatalf("expired fifth attempt not mapped: %s, %v", workflow.State, err)
	}
}

func TestWatchdogLeavesTerminalWorkflowsUntouched(t *testing.T) {
	ctx := context.Background()
	for _, state := range []domain.WorkflowState{domain.Published, domain.NoPractice, domain.Rejected, domain.Failed} {
		f := newWatchdogFixture(t, ctx, "document-watchdog-terminal", state, 9)
		f.store.setWatchdogStatus(f.action, WatchdogActionStatus{Phase: runnable.ActionVerify, State: "failed", Attempt: 5, UpdatedAt: f.pipeline.now().Add(-24 * time.Hour)})
		if err := f.pipeline.Watchdog(ctx); err != nil {
			t.Fatal(err)
		}
		workflow, err := f.store.GetWorkflow(ctx, "document-watchdog-terminal")
		if err != nil {
			t.Fatal(err)
		}
		if workflow.State != state || workflow.StateVersion != 9 {
			t.Fatalf("terminal workflow %s changed to %s v%d", state, workflow.State, workflow.StateVersion)
		}
	}
}

func TestWatchdogContinuesPastOneLostFence(t *testing.T) {
	ctx := context.Background()
	f := newWatchdogFixture(t, ctx, "document-watchdog-race", domain.Verifying, 6)
	f.store.setWatchdogStatus(f.action, WatchdogActionStatus{Phase: runnable.ActionVerify, State: "failed", Attempt: 2, UpdatedAt: f.pipeline.now().Add(-time.Minute)})
	// The workflow advanced between listing and mapping; the mapping loses its
	// state-version fence, the watchdog logs it, and the pass still succeeds.
	advanced, err := f.store.GetWorkflow(ctx, "document-watchdog-race")
	if err != nil {
		t.Fatal(err)
	}
	advanced.StateVersion = 7
	f.store.workflows["document-watchdog-race"] = advanced
	if err := f.pipeline.Watchdog(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestWatchdogPolicyTable(t *testing.T) {
	now := time.Date(2026, 9, 19, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		candidate WatchdogCandidate
		want      bool
	}{
		{"failed", WatchdogCandidate{Action: WatchdogActionStatus{State: "failed"}}, true},
		{"freshly exhausted queued", WatchdogCandidate{Action: WatchdogActionStatus{State: "queued", Attempt: 5, UpdatedAt: now.Add(-time.Second)}}, false},
		{"graced exhausted queued", WatchdogCandidate{Action: WatchdogActionStatus{State: "queued", Attempt: 5, UpdatedAt: now.Add(-runnableActionWatchdogBudget - time.Second)}}, true},
		{"attempt four queued", WatchdogCandidate{Action: WatchdogActionStatus{State: "queued", Attempt: 4, UpdatedAt: now.Add(-48 * time.Hour)}}, false},
		{"live leased fifth attempt", WatchdogCandidate{Action: WatchdogActionStatus{State: "running", Attempt: 5, UpdatedAt: now.Add(-48 * time.Hour)}}, false},
		{"expired leased fifth attempt", WatchdogCandidate{Action: WatchdogActionStatus{State: "running", Attempt: 5, LeaseExpired: true, UpdatedAt: now.Add(-runnableActionWatchdogBudget - time.Second)}}, true},
		{"healthy queued", WatchdogCandidate{Action: WatchdogActionStatus{State: "queued", Attempt: 1, UpdatedAt: now.Add(-runnableActionWatchdogBudget - time.Second)}}, false},
	}
	for _, tc := range cases {
		if _, got := watchdogMapping(tc.candidate, now, runnableActionWatchdogBudget); got != tc.want {
			t.Fatalf("%s: watchdogMapping = %t, want %t", tc.name, got, tc.want)
		}
	}
}

func TestWatchdogNilPipelineIsRejected(t *testing.T) {
	var pipeline *AgentPipeline
	if err := pipeline.Watchdog(context.Background()); err == nil {
		t.Fatal("nil pipeline watchdog passed")
	}
}
