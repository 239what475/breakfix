package catalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/execution"
	scenariodomain "github.com/breakfix/breakfix/internal/domain/scenario"
)

func TestCatalogIntegrityFailsClosedForMissingOrChangedActiveSource(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, root string, entry scenario.Entry)
		want   string
	}{
		{
			name: "missing active source", want: "referenced materialized source is missing",
			mutate: func(t *testing.T, root string, entry scenario.Entry) {
				t.Helper()
				if err := os.RemoveAll(filepath.Join(root, entry.SourceSlug)); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "changed active source", want: "materialized source does not match",
			mutate: func(t *testing.T, root string, entry scenario.Entry) {
				t.Helper()
				writeCatalogFile(t, filepath.Join(root, entry.SourceSlug, entry.RevisionID, "solution.md"), []byte("<!-- checkpoint: cleanup-script-ready -->\nchanged\n"), 0o644)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, entry, root, _ := newMaterializedCatalog(t)
			test.mutate(t, root, entry)
			err := service.CheckIntegrity(context.Background())
			if err == nil || !errors.Is(err, ErrMaterializedIntegrity) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("integrity error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCatalogReadsOnlyActiveLifecycleRevisions(t *testing.T) {
	service, entry, root, lifecycle := newMaterializedCatalog(t)
	if err := os.MkdirAll(filepath.Join(root, "unreferenced", "chrev-bbbbbbbbbbbbbbbb"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCatalogFile(t, filepath.Join(root, "unreferenced", "chrev-bbbbbbbbbbbbbbbb", "scenario.yaml"), []byte("not a scenario"), 0o600)

	visible, err := service.List(context.Background())
	if err != nil || len(visible) != 1 {
		t.Fatalf("visible catalog = %#v, err=%v", visible, err)
	}
	if visible[0].Catalog.ID != entry.ID || visible[0].Catalog.ActiveRevisionID != entry.RevisionID ||
		visible[0].Catalog.Title != entry.Title || visible[0].Catalog.Description != entry.Description ||
		visible[0].Catalog.Runtime != entry.Runtime || !visible[0].Catalog.Available {
		t.Fatalf("catalog read model = %#v", visible[0].Catalog)
	}
	if len(lifecycle.active) != 1 || lifecycle.active[0].Revision.ID != entry.RevisionID {
		t.Fatalf("lifecycle active revisions = %#v", lifecycle.active)
	}
}

func TestHistoricalEntryDoesNotFollowActiveScenarioPointer(t *testing.T) {
	_, entry, root, _ := newMaterializedCatalog(t)
	now := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	lifecycle := &staticLifecycleStore{
		stable: scenariodomain.Scenario{
			ID: entry.ID, SourceKind: scenariodomain.SourceAuthoring, SourceRef: "author-session", OwnerUserID: "author",
			State: scenariodomain.StateDeprecated, ActiveRevisionID: "chrev-bbbbbbbbbbbbbbbb", SourceSlug: entry.SourceSlug,
			CreatedAt: now, UpdatedAt: now,
		},
		revision: scenariodomain.Revision{
			ID: entry.RevisionID, ScenarioID: entry.ID, SourceKind: scenariodomain.SourceAuthoring, SourceRef: "author-session", SourceRevisionID: "1",
			Title: entry.Title, Runtime: entry.Runtime, Type: entry.Type, Tags: entry.Tags, ContentRevision: entry.ContentRevision, SourceSlug: entry.SourceSlug,
			MaterializedPath: scenario.MaterializedPath(entry.SourceSlug, entry.RevisionID), MaterializedRevision: entry.Revision,
			Artifact: execution.ArtifactReference{Runtime: entry.Runtime, IncusAlias: "historical-alias", IncusFingerprint: entry.Image},
			State:    scenariodomain.RevisionSuperseded, PublishedAt: entry.PublishedAt, CreatedAt: entry.PublishedAt,
		},
	}
	service := NewService(root, nil, lifecycle)
	historical, err := service.HistoricalEntry(context.Background(), entry.ID, entry.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if historical.ID != entry.ID || historical.RevisionID != entry.RevisionID || historical.Title != entry.Title {
		t.Fatalf("historical entry = %#v, want %#v", historical, entry)
	}
}

func TestCatalogReadinessAllowsEmptyBootstrapAndBypassesAvailability(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-scenarios")
	releaseStore := &catalogReadinessStore{release: &catalogdomain.Release{State: catalogdomain.ReleaseInstalling}}
	availability, err := NewAvailability("registry.example/catalog@sha256:"+strings.Repeat("b", 64), releaseStore)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(root, availability, &staticLifecycleStore{})
	if err := service.Readiness(context.Background()); err != nil {
		t.Fatalf("empty bootstrap readiness = %v", err)
	}
	if _, err := service.List(context.Background()); !errors.Is(err, ErrReleaseNotReady) {
		t.Fatalf("catalog list did not honor availability gate: %v", err)
	}
}

type staticLifecycleStore struct {
	active   []scenariodomain.ActiveRevision
	stable   scenariodomain.Scenario
	revision scenariodomain.Revision
}

func (s *staticLifecycleStore) ListActiveScenarioRevisions(context.Context) ([]scenariodomain.ActiveRevision, error) {
	return append([]scenariodomain.ActiveRevision(nil), s.active...), nil
}

func (s *staticLifecycleStore) GetScenario(context.Context, string) (*scenariodomain.Scenario, error) {
	value := s.stable
	return &value, nil
}

func (s *staticLifecycleStore) GetScenarioRevision(context.Context, string, string) (*scenariodomain.Revision, error) {
	value := s.revision
	return &value, nil
}

type catalogReadinessStore struct{ release *catalogdomain.Release }

func (s *catalogReadinessStore) ReleaseByDigest(context.Context, catalogdomain.BundleDigest) (*catalogdomain.Release, error) {
	return s.release, nil
}

func newMaterializedCatalog(t *testing.T) (*Service, scenario.Entry, string, *staticLifecycleStore) {
	t.Helper()
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	writeScenarioSource(t, candidate, false)
	contentRevision, err := ContentRevision(candidate)
	if err != nil {
		t.Fatal(err)
	}
	scenariosDir := filepath.Join(root, "scenarios")
	published, err := scenario.PromoteDirectoryAt(scenariosDir, candidate, "chal-integrity", "chrev-aaaaaaaaaaaaaaaa", "cleanup-logs", strings.Repeat("a", 64), string(contentRevision), time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	stable := scenariodomain.Scenario{
		ID: published.ID, SourceKind: scenariodomain.SourceRelease, SourceRef: "catalog/cleanup-logs", State: scenariodomain.StateActive,
		ActiveRevisionID: published.RevisionID, SourceSlug: published.SourceSlug, CreatedAt: published.PublishedAt, UpdatedAt: published.PublishedAt,
	}
	revision := scenariodomain.Revision{
		ID: published.RevisionID, ScenarioID: published.ID, SourceKind: scenariodomain.SourceRelease, SourceRef: stable.SourceRef, SourceRevisionID: "catalog-entry",
		Title: published.Title, Runtime: published.Runtime, Type: published.Type, Tags: published.Tags, ContentRevision: published.ContentRevision,
		SourceSlug: published.SourceSlug, MaterializedPath: scenario.MaterializedPath(published.SourceSlug, published.RevisionID), MaterializedRevision: published.Revision,
		Artifact: execution.ArtifactReference{Runtime: published.Runtime, IncusAlias: "catalog-alias", IncusFingerprint: published.Image},
		State:    scenariodomain.RevisionActive, PublishedAt: published.PublishedAt, CreatedAt: published.PublishedAt,
	}
	lifecycle := &staticLifecycleStore{active: []scenariodomain.ActiveRevision{{Scenario: stable, Revision: revision}}, stable: stable, revision: revision}
	return NewService(scenariosDir, nil, lifecycle), *published, scenariosDir, lifecycle
}
