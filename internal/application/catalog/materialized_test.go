package catalog

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appoperations "github.com/breakfix/breakfix/internal/application/operations"
	"github.com/breakfix/breakfix/internal/content/scenario"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	scenariodomain "github.com/breakfix/breakfix/internal/domain/scenario"
)

func TestCatalogReadsMaterializedScenarioThroughRunnableRevision(t *testing.T) {
	service, entry, lifecycle, resolver := newRunnableMaterializedCatalog(t)
	values, err := service.ListOperations(context.Background())
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(values) != 1 || values[0].Entry.ID != entry.ID || values[0].Entry.Image != entry.Image {
		t.Fatalf("catalog projection = %#v", values)
	}
	if lifecycle.active[0].Revision.RunnableRevisionRef != resolver.reference {
		t.Fatalf("scenario did not retain the public runnable reference: %#v", lifecycle.active[0].Revision)
	}
}

func TestCatalogRejectsMaterializedArtifactThatDiffersFromRunnableRevision(t *testing.T) {
	service, _, _, resolver := newRunnableMaterializedCatalog(t)
	resolver.revision.Artifact.ArtifactDigest = testDigest("f")
	resolver.revision.Artifact.ProviderReference = "incus://catalog/test@" + testDigest("f")
	if err := service.CheckIntegrity(context.Background()); !errors.Is(err, ErrMaterializedIntegrity) || !strings.Contains(err.Error(), "materialized source does not match") {
		t.Fatalf("integrity error = %v", err)
	}
}

func TestCatalogReadinessAllowsEmptyBootstrapWithPublicResolver(t *testing.T) {
	availability, err := NewAvailability("registry.example/catalog@"+testDigest("b"), &catalogReadinessStore{release: &catalogdomain.Release{State: catalogdomain.ReleaseInstalling}})
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(filepath.Join(t.TempDir(), "missing"), availability, &staticLifecycleStore{}, staticRunnableResolver{})
	if err := service.Readiness(context.Background()); err != nil {
		t.Fatalf("empty bootstrap readiness = %v", err)
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

type staticRunnableResolver struct {
	reference runnable.RevisionReference
	revision  runnable.RunnableRevision
}

func (s staticRunnableResolver) ResolveRunnableRevision(_ context.Context, id, digest string) (runnable.RunnableRevision, error) {
	if s.reference.ID != id || s.reference.Digest != digest {
		return runnable.RunnableRevision{}, errors.New("runnable revision not found")
	}
	return s.revision, nil
}

type catalogReadinessStore struct{ release *catalogdomain.Release }

func (s *catalogReadinessStore) ReleaseByDigest(context.Context, catalogdomain.BundleDigest) (*catalogdomain.Release, error) {
	return s.release, nil
}

func newRunnableMaterializedCatalog(t *testing.T) (*Service, scenario.Entry, *staticLifecycleStore, *staticRunnableResolver) {
	t.Helper()
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	writeScenarioSource(t, candidate, false)
	contentRevision, err := ContentRevision(candidate)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := scenario.ValidateCandidateDir(candidate)
	if err != nil {
		t.Fatal(err)
	}
	entry.ID = "chal-integrity"
	entry.RevisionID = "chrev-aaaaaaaaaaaaaaaa"
	entry.ContentRevision = string(contentRevision)
	publicRevision, reference := catalogRunnableRevision(t, *entry, testDigest("a"))
	scenariosDir := filepath.Join(root, "scenarios")
	published, err := scenario.PromoteDirectoryAt(scenariosDir, candidate, entry.ID, entry.RevisionID, "cleanup-logs", strings.TrimPrefix(publicRevision.Artifact.ArtifactDigest, "sha256:"), string(contentRevision), time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	stable := scenariodomain.Scenario{ID: published.ID, SourceKind: scenariodomain.SourceRelease, SourceRef: "catalog/cleanup-logs", State: scenariodomain.StateActive, ActiveRevisionID: published.RevisionID, SourceSlug: published.SourceSlug, CreatedAt: published.PublishedAt, UpdatedAt: published.PublishedAt}
	revision := scenariodomain.Revision{
		ID: published.RevisionID, ScenarioID: published.ID, SourceKind: scenariodomain.SourceRelease, SourceRef: stable.SourceRef, SourceRevisionID: "catalog-entry",
		Title: published.Title, Type: published.Type, Tags: published.Tags, ContentRevision: published.ContentRevision, SourceSlug: published.SourceSlug,
		MaterializedPath: scenario.MaterializedPath(published.SourceSlug, published.RevisionID), MaterializedRevision: published.Revision,
		RunnableRevisionRef: reference, VerificationReportRef: runnable.VerificationReportReference{ID: "verification-report-01", Digest: testDigest("c")},
		State: scenariodomain.RevisionActive, PublishedAt: published.PublishedAt, CreatedAt: published.PublishedAt,
	}
	lifecycle := &staticLifecycleStore{active: []scenariodomain.ActiveRevision{{Scenario: stable, Revision: revision}}, stable: stable, revision: revision}
	resolver := &staticRunnableResolver{reference: reference, revision: publicRevision}
	return NewService(scenariosDir, nil, lifecycle, resolver), *published, lifecycle, resolver
}

func catalogRunnableRevision(t *testing.T, entry scenario.Entry, artifactDigest string) (runnable.RunnableRevision, runnable.RevisionReference) {
	t.Helper()
	spec, err := appoperations.Compile(appoperations.Input{
		ContentID: entry.ID, ContentRevision: entry.ContentRevision, Entry: entry,
		Source: runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "runnable-sources/catalog.tar.gz", Digest: testDigest("d")},
	}, appoperations.Config{
		MaxNodes: 4,
		Node:     catalogProfile(), K8s: catalogProfile(),
		Lifecycle: runnable.LifecyclePolicy{CreateTimeoutSeconds: 600, ResetTimeoutSeconds: 600, StopTimeoutSeconds: 300, ReapTimeoutSeconds: 300, IdleTTLSeconds: 1800, MaxLifetimeSeconds: 3600},
	})
	if err != nil {
		t.Fatal(err)
	}
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	value := runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: spec, Artifact: runnable.ArtifactReference{
		FormatVersion: runnable.FormatVersion, Runtime: runnable.RuntimeNode, ProviderReference: "incus://catalog/test@" + artifactDigest,
		ArtifactDigest: artifactDigest, BuiltFromSpecDigest: specDigest, BuilderVersion: "builder-01",
	}}
	digest, err := value.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return value, runnable.RevisionReference{ID: "runnable-revision-01", Digest: digest}
}

func catalogProfile() appoperations.RuntimeProfileConfig {
	return appoperations.RuntimeProfileConfig{ProfileRevision: "profile-01", BaseImage: "registry.example/base@" + testDigest("e"), Resources: runnable.ResourceLimits{CPU: "2", MemoryBytes: 2 << 30, EphemeralBytes: 4 << 30, MaxProcesses: 256, MaxConcurrentTasks: 2}, Network: runnable.NetworkPrivate, MaxActionTimeout: 1200}
}

func testDigest(character string) string { return "sha256:" + strings.Repeat(character, 64) }
