package httpapi

import (
	"context"
	"fmt"
	"testing"

	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/content/challenge"
	challengedomain "github.com/breakfix/breakfix/internal/domain/challenge"
	"github.com/breakfix/breakfix/internal/domain/execution"
)

func newHandlerForTest(t testing.TB, database *postgres.Store, client *kubernetes.Client, cfg config.Config) *Handler {
	t.Helper()
	dependencies := Dependencies{}
	if cfg.DataDir != "" {
		lifecycle, err := testCatalogLifecycle(cfg.ChallengesDir())
		if err != nil {
			t.Fatal(err)
		}
		dependencies.Catalog = appcatalog.NewService(cfg.ChallengesDir(), nil, lifecycle)
	}
	handler, err := NewHandlerWithDependencies(database, client, cfg, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func testCatalogLifecycle(challengesDir string) (*testLifecycleStore, error) {
	entries, err := challenge.List(challengesDir)
	if err != nil || len(entries) == 0 {
		if err == nil {
			err = fmt.Errorf("test catalog has no materialized challenges")
		}
		return nil, err
	}
	store := &testLifecycleStore{byID: make(map[string]challengedomain.ActiveRevision, len(entries))}
	for _, entry := range entries {
		if entry.SourceSlug == "" {
			return nil, fmt.Errorf("test challenge %q has no source slug", entry.ID)
		}
		stable := challengedomain.Challenge{
			ID: entry.ID, SourceKind: challengedomain.SourceRelease, SourceRef: "test/" + entry.SourceSlug, State: challengedomain.StateActive,
			ActiveRevisionID: entry.RevisionID, SourceSlug: entry.SourceSlug, CreatedAt: entry.PublishedAt, UpdatedAt: entry.PublishedAt,
		}
		artifact := execution.ArtifactReference{Runtime: entry.Runtime}
		if entry.Runtime == challenge.RuntimeK8s {
			artifact.OCIReference = entry.Image
		} else {
			artifact.IncusAlias = "test-" + entry.SourceSlug
			artifact.IncusFingerprint = entry.Image
		}
		revision := challengedomain.Revision{
			ID: entry.RevisionID, ChallengeID: entry.ID, SourceKind: stable.SourceKind, SourceRef: stable.SourceRef, SourceRevisionID: entry.ContentRevision,
			Title: entry.Title, Runtime: entry.Runtime, Type: entry.Type, Tags: entry.Tags, ContentRevision: entry.ContentRevision,
			SourceSlug: entry.SourceSlug, MaterializedPath: challenge.MaterializedPath(entry.SourceSlug, entry.RevisionID), MaterializedRevision: entry.Revision,
			Artifact: artifact, State: challengedomain.RevisionActive, PublishedAt: entry.PublishedAt, CreatedAt: entry.PublishedAt,
		}
		value := challengedomain.ActiveRevision{Challenge: stable, Revision: revision}
		if !value.Valid() {
			return nil, fmt.Errorf("test active catalog entry %q is invalid", entry.ID)
		}
		store.active = append(store.active, value)
		store.byID[entry.ID] = value
	}
	return store, nil
}

type testLifecycleStore struct {
	active []challengedomain.ActiveRevision
	byID   map[string]challengedomain.ActiveRevision
}

func (s *testLifecycleStore) ListActiveChallengeRevisions(context.Context) ([]challengedomain.ActiveRevision, error) {
	return append([]challengedomain.ActiveRevision(nil), s.active...), nil
}

func (s *testLifecycleStore) GetChallenge(_ context.Context, id string) (*challengedomain.Challenge, error) {
	value, exists := s.byID[id]
	if !exists {
		return nil, challengedomain.ErrNotFound
	}
	stable := value.Challenge
	return &stable, nil
}

func (s *testLifecycleStore) GetChallengeRevision(_ context.Context, id, revisionID string) (*challengedomain.Revision, error) {
	value, exists := s.byID[id]
	if !exists || value.Revision.ID != revisionID {
		return nil, challengedomain.ErrNotFound
	}
	revision := value.Revision
	return &revision, nil
}
