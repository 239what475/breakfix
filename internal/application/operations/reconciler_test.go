package operations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	scenariodomain "github.com/breakfix/breakfix/internal/domain/scenario"
)

func TestBindingReconcilerLeavesPendingMaterializationForRetry(t *testing.T) {
	active := operationsActiveRevision()
	store := &reconcileBindingStore{}
	publisher := &reconcilePublisher{prepared: &PreparedPublication{Action: runnable.ActionIdentity{Phase: runnable.ActionMaterializeArtifact}}}
	r, err := NewBindingReconciler(reconcileSource{active: []scenariodomain.ActiveRevision{active}}, store, publisher, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("pending reconcile: %v", err)
	}
	if publisher.prepareCalls != 1 || publisher.finalizeCalls != 1 || store.resolveCalls != 1 {
		t.Fatalf("pending reconcile calls = prepare:%d finalize:%d resolve:%d", publisher.prepareCalls, publisher.finalizeCalls, store.resolveCalls)
	}
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("repeat pending reconcile: %v", err)
	}
	if publisher.prepareCalls != 2 {
		t.Fatalf("repeat reconcile did not retry deterministic preparation: %d", publisher.prepareCalls)
	}
}

func TestBindingReconcilerSkipsBoundAndDocumentationRevisions(t *testing.T) {
	active := operationsActiveRevision()
	documentation := active
	documentation.Revision.Type = scenario.ScenarioDocumentationExample
	store := &reconcileBindingStore{resolved: map[string]runnable.RevisionReference{active.Revision.ID: {ID: "rr-bound", Digest: testRunnableDigest("a")}}}
	publisher := &reconcilePublisher{}
	r, err := NewBindingReconciler(reconcileSource{active: []scenariodomain.ActiveRevision{active, documentation}}, store, publisher, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("bound reconcile: %v", err)
	}
	if publisher.prepareCalls != 0 {
		t.Fatalf("bound/documentation revisions invoked publisher %d times", publisher.prepareCalls)
	}
}

func TestBindingReconcilerContinuesAfterTransientRunError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r, err := NewBindingReconciler(failingRevisionSource{}, &reconcileBindingStore{}, &reconcilePublisher{}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("reconciler stopped after transient error: %v", err)
	}
}

type reconcileSource struct {
	active []scenariodomain.ActiveRevision
}

type failingRevisionSource struct{}

func (failingRevisionSource) ListActiveScenarioRevisions(context.Context) ([]scenariodomain.ActiveRevision, error) {
	return nil, errors.New("temporary database failure")
}

func (s reconcileSource) ListActiveScenarioRevisions(context.Context) ([]scenariodomain.ActiveRevision, error) {
	return s.active, nil
}

type reconcileBindingStore struct {
	resolved     map[string]runnable.RevisionReference
	resolveCalls int
}

func (s *reconcileBindingStore) ResolveOperationsRevisionBinding(_ context.Context, id string) (runnable.RevisionReference, error) {
	s.resolveCalls++
	if value, ok := s.resolved[id]; ok {
		return value, nil
	}
	return runnable.RevisionReference{}, runnable.ErrMaterializationNotReady
}

type reconcilePublisher struct {
	prepared      *PreparedPublication
	prepareCalls  int
	finalizeCalls int
}

func (p *reconcilePublisher) PrepareRevision(context.Context, string, string, int64, time.Time) (*PreparedPublication, error) {
	p.prepareCalls++
	return p.prepared, nil
}

func (p *reconcilePublisher) Finalize(context.Context, PreparedPublication, time.Time) (runnable.RevisionReference, error) {
	p.finalizeCalls++
	return runnable.RevisionReference{}, runnable.ErrMaterializationNotReady
}

func operationsActiveRevision() scenariodomain.ActiveRevision {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	return scenariodomain.ActiveRevision{
		Scenario: scenariodomain.Scenario{ID: "operations-node", SourceKind: scenariodomain.SourceAuthoring, SourceRef: "source", OwnerUserID: "owner", State: scenariodomain.StateActive, ActiveRevisionID: "chrev-aaaaaaaaaaaaaaaa", SourceSlug: "operations-node", CreatedAt: now, UpdatedAt: now},
		Revision: scenariodomain.Revision{ID: "chrev-aaaaaaaaaaaaaaaa", ScenarioID: "operations-node", SourceKind: scenariodomain.SourceAuthoring, SourceRef: "source", SourceRevisionID: "1", Title: "Operations", Runtime: scenario.RuntimeNode, Type: scenario.ScenarioOperationsScenario, ContentRevision: testRunnableDigest("a"), SourceSlug: "operations-node", MaterializedPath: "operations-node/chrev-aaaaaaaaaaaaaaaa", MaterializedRevision: testRunnableDigest("b"), Artifact: legacyScenarioArtifact(), State: scenariodomain.RevisionActive, PublishedAt: now, CreatedAt: now},
	}
}

func legacyScenarioArtifact() (artifact execution.ArtifactReference) {
	return execution.ArtifactReference{Runtime: scenario.RuntimeNode, IncusAlias: "operations", IncusFingerprint: strings.Repeat("c", 64)}
}
