package documentpractice

import (
	"errors"
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

func TestWorkflowRestartOnlyFromFailedOrRejected(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	w, err := NewWorkflow("workflow-restart", now)
	if err != nil {
		t.Fatal(err)
	}
	w.MaxRevisions = 1
	if err := w.RestartAt(now); !errors.Is(err, ErrWorkflowConflict) {
		t.Fatalf("restart from Planning = %v, want conflict", err)
	}
	w.State = PlanReviewing
	if err := w.RestartAt(now); !errors.Is(err, ErrWorkflowConflict) {
		t.Fatalf("restart from PlanReviewing = %v, want conflict", err)
	}
	w.State = Failed
	if err := w.RestartAt(now); err != nil {
		t.Fatalf("restart from Failed = %v", err)
	}
	if w.State != Planning || w.Revision != 2 || w.StateVersion != 2 {
		t.Fatalf("restarted workflow = %#v", w)
	}
	w.State = Rejected
	if err := w.RestartAt(now); err != nil {
		t.Fatalf("restart from Rejected = %v", err)
	}
	if w.State != Planning || w.Revision != 3 {
		t.Fatalf("restarted workflow = %#v", w)
	}
	// The MaxRevisions cap bounds the automatic revision loop only.
	w.MaxRevisions = 2
	w.State = Failed
	if err := w.RestartAt(now); err != nil {
		t.Fatalf("restart beyond MaxRevisions = %v", err)
	}
	if w.Revision != 4 {
		t.Fatalf("restart revision = %d, want 4", w.Revision)
	}
	w.State = Published
	if err := w.RestartAt(now); !errors.Is(err, ErrWorkflowConflict) {
		t.Fatalf("restart from Published = %v, want conflict", err)
	}
	w.State = NoPractice
	if err := w.RestartAt(now); !errors.Is(err, ErrWorkflowConflict) {
		t.Fatalf("restart from NoPractice = %v, want conflict", err)
	}
}

func TestContextParserIdentityIsEvidenceNotIdentity(t *testing.T) {
	base := DocumentContext{FormatVersion: FormatVersion, SourceID: "kubernetes", Repository: "https://github.com/kubernetes/website.git", Commit: strings.Repeat("a", 40), Version: "v1.34", Language: "en", License: "CC BY 4.0", PagePath: "docs/pods.md", Anchor: "pod-lifecycle"}
	upgraded := base
	upgraded.ParserVersion = "docs-project-v11"
	upgraded.PageDigest = "sha256:" + strings.Repeat("b", 64)
	if err := upgraded.Validate(); err != nil {
		t.Fatalf("context with a bound parser identity = %v", err)
	}
	if ContentID(base) != ContentID(upgraded) {
		t.Fatal("parser version and page digest must not change ContentID")
	}
	half := base
	half.ParserVersion = "docs-project-v11"
	if err := half.Validate(); err == nil {
		t.Fatal("parser version without page digest was accepted")
	}
	badDigest := upgraded
	badDigest.PageDigest = "sha256:short"
	if err := badDigest.Validate(); err == nil {
		t.Fatal("malformed page digest was accepted")
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("legacy context without parser identity = %v", err)
	}
}
