package documentpractice

import (
	"strings"
	"testing"
	"time"
)

func TestWorkflowLedgerIsAppendOnlyAndTransitionsAreGated(t *testing.T) {
	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	w, err := NewWorkflow("workflow-1", now)
	if err != nil {
		t.Fatal(err)
	}
	a := ArtifactRecord{ID: "plan-1", Kind: "learning-unit-plan", ContentRevision: "1", Digest: "sha256:" + strings.Repeat("a", 64), SchemaVersion: FormatVersion, OwnerRole: "planner", CreatedAt: now}
	if err := w.Append(a, now); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(a, now); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(ArtifactRecord{ID: a.ID, Kind: a.Kind, ContentRevision: "2", Digest: "sha256:" + strings.Repeat("b", 64), SchemaVersion: FormatVersion, OwnerRole: "planner", CreatedAt: now}, now); err == nil {
		t.Fatal("artifact overwrite accepted")
	}
	if err := w.Advance(PlanReviewing, "learning-unit-plan"); err != nil {
		t.Fatal(err)
	}
	if err := w.Advance(Publishing); err == nil {
		t.Fatal("workflow skipped required stages")
	}
}

func TestWorkflowRevisionIsBounded(t *testing.T) {
	w, err := NewWorkflow("workflow-2", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	w.MaxRevisions = 1
	if err := w.Revise(); err == nil {
		t.Fatal("revision limit ignored")
	}
}
