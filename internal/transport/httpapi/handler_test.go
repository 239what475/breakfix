package httpapi

import (
	"context"
	"fmt"
	"testing"

	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/execution"
	scenariodomain "github.com/breakfix/breakfix/internal/domain/scenario"
)

func newHandlerForTest(t testing.TB, database *postgres.Store, client *kubernetes.Client, cfg config.Config) *Handler {
	t.Helper()
	dependencies := Dependencies{}
	if cfg.DataDir != "" {
		lifecycle, err := testCatalogLifecycle(cfg.ScenariosDir())
		if err != nil {
			t.Fatal(err)
		}
		dependencies.Catalog = appcatalog.NewService(cfg.ScenariosDir(), nil, lifecycle)
	}
	handler, err := NewHandlerWithDependencies(database, client, cfg, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func testCatalogLifecycle(scenariosDir string) (*testLifecycleStore, error) {
	entries, err := scenario.List(scenariosDir)
	if err != nil || len(entries) == 0 {
		if err == nil {
			err = fmt.Errorf("test catalog has no materialized scenarios")
		}
		return nil, err
	}
	store := &testLifecycleStore{byID: make(map[string]scenariodomain.ActiveRevision, len(entries))}
	for _, entry := range entries {
		if entry.SourceSlug == "" {
			return nil, fmt.Errorf("test scenario %q has no source slug", entry.ID)
		}
		stable := scenariodomain.Scenario{
			ID: entry.ID, SourceKind: scenariodomain.SourceRelease, SourceRef: "test/" + entry.SourceSlug, State: scenariodomain.StateActive,
			ActiveRevisionID: entry.RevisionID, SourceSlug: entry.SourceSlug, CreatedAt: entry.PublishedAt, UpdatedAt: entry.PublishedAt,
		}
		artifact := execution.ArtifactReference{Runtime: entry.Runtime}
		if entry.Runtime == scenario.RuntimeK8s {
			artifact.OCIReference = entry.Image
		} else {
			artifact.IncusAlias = "test-" + entry.SourceSlug
			artifact.IncusFingerprint = entry.Image
		}
		revision := scenariodomain.Revision{
			ID: entry.RevisionID, ScenarioID: entry.ID, SourceKind: stable.SourceKind, SourceRef: stable.SourceRef, SourceRevisionID: entry.ContentRevision,
			Title: entry.Title, Runtime: entry.Runtime, Type: entry.Type, Tags: entry.Tags, ContentRevision: entry.ContentRevision,
			SourceSlug: entry.SourceSlug, MaterializedPath: scenario.MaterializedPath(entry.SourceSlug, entry.RevisionID), MaterializedRevision: entry.Revision,
			Artifact: artifact, State: scenariodomain.RevisionActive, PublishedAt: entry.PublishedAt, CreatedAt: entry.PublishedAt,
		}
		value := scenariodomain.ActiveRevision{Scenario: stable, Revision: revision}
		if !value.Valid() {
			return nil, fmt.Errorf("test active catalog entry %q is invalid", entry.ID)
		}
		store.active = append(store.active, value)
		store.byID[entry.ID] = value
	}
	return store, nil
}

type testLifecycleStore struct {
	active []scenariodomain.ActiveRevision
	byID   map[string]scenariodomain.ActiveRevision
}

func (s *testLifecycleStore) ListActiveScenarioRevisions(context.Context) ([]scenariodomain.ActiveRevision, error) {
	return append([]scenariodomain.ActiveRevision(nil), s.active...), nil
}

func (s *testLifecycleStore) GetScenario(_ context.Context, id string) (*scenariodomain.Scenario, error) {
	value, exists := s.byID[id]
	if !exists {
		return nil, scenariodomain.ErrNotFound
	}
	stable := value.Scenario
	return &stable, nil
}

func (s *testLifecycleStore) GetScenarioRevision(_ context.Context, id, revisionID string) (*scenariodomain.Revision, error) {
	value, exists := s.byID[id]
	if !exists || value.Revision.ID != revisionID {
		return nil, scenariodomain.ErrNotFound
	}
	revision := value.Revision
	return &revision, nil
}
