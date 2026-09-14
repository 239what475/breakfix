package operations

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestPublisherPrepareAndFinalizeBindsWorkerRevision(t *testing.T) {
	root := fixtureDirectory(t, "node-runtime-fixture")
	entry, err := scenario.ValidateCandidateDir(root)
	if err != nil {
		t.Fatal(err)
	}
	entry.ID = "operations-node"
	entry.RevisionID = "chrev-aaaaaaaaaaaaaaaa"
	contentRevision := testRunnableDigest("a")
	entry.ContentRevision = contentRevision
	store := &publisherStore{}
	publisher, err := NewPublisher(store, config())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	prepared, err := publisher.Prepare(context.Background(), PublishedRevision{
		ScenarioID: "operations-node", ScenarioRevisionID: entry.RevisionID, ContentRevision: contentRevision, Root: root, Entry: *entry,
	}, 1, now)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared.Action.Phase != runnable.ActionMaterializeArtifact || prepared.Spec.Identity.ID != "operations-node" || len(store.archive) == 0 {
		t.Fatalf("prepared publication = %#v", prepared)
	}
	if _, err := publisher.Finalize(context.Background(), *prepared, now); !errors.Is(err, runnable.ErrMaterializationNotReady) {
		t.Fatalf("finalize before Worker completion = %v", err)
	}
	digest := testRunnableDigest("c")
	store.reference = runnable.RevisionReference{ID: "rr-materialized", Digest: digest}
	got, err := publisher.Finalize(context.Background(), *prepared, now.Add(time.Second))
	if err != nil || got != store.reference || store.boundScenario != "operations-node" || store.boundRevision != entry.RevisionID {
		t.Fatalf("finalize = %#v, err=%v store=%#v", got, err, store)
	}
}

type publisherStore struct {
	archive       []byte
	action        runnable.ActionIdentity
	reference     runnable.RevisionReference
	boundScenario string
	boundRevision string
}

func (s *publisherStore) StoreRunnableSource(_ context.Context, _ runnable.SourceArchive, archive []byte, _ time.Time) error {
	s.archive = append([]byte(nil), archive...)
	return nil
}

func (s *publisherStore) ScheduleMaterialization(_ context.Context, spec runnable.RunnableSpec, stateVersion int64, _ time.Time) (runnable.ActionIdentity, error) {
	digest, err := spec.Digest()
	if err != nil {
		return runnable.ActionIdentity{}, err
	}
	s.action = runnable.ActionIdentity{Content: spec.Identity, SpecDigest: digest, Phase: runnable.ActionMaterializeArtifact, StateVersion: stateVersion}
	return s.action, nil
}

func (s *publisherStore) ResolveMaterializedRunnableRevision(_ context.Context, action runnable.ActionIdentity) (runnable.RevisionReference, error) {
	if s.reference == (runnable.RevisionReference{}) || action != s.action {
		return runnable.RevisionReference{}, runnable.ErrMaterializationNotReady
	}
	return s.reference, nil
}

func (s *publisherStore) BindOperationsRevision(_ context.Context, scenarioID, revisionID string, reference runnable.RevisionReference, _ time.Time) error {
	if reference != s.reference {
		return errors.New("unexpected reference")
	}
	s.boundScenario, s.boundRevision = scenarioID, revisionID
	return nil
}

var _ MaterializationStore = (*publisherStore)(nil)

func testRunnableDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
