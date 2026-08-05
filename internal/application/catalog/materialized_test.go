package catalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	roadmap "github.com/breakfix/breakfix/internal/domain/roadmap"
)

func TestCatalogIntegrityRejectsMissingAndChangedMaterializedSources(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, root string, binding roadmap.ChallengeRef)
		want   string
	}{
		{name: "missing root", mutate: func(t *testing.T, root string, _ roadmap.ChallengeRef) {
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
		}, want: "materialized challenges root is missing"},
		{name: "missing directory", mutate: func(t *testing.T, root string, binding roadmap.ChallengeRef) {
			if err := os.RemoveAll(filepath.Join(root, binding.SourceSlug)); err != nil {
				t.Fatal(err)
			}
		}, want: "referenced materialized source is missing"},
		{name: "content change", mutate: func(t *testing.T, root string, binding roadmap.ChallengeRef) {
			writeCatalogFile(t, filepath.Join(root, binding.SourceSlug, "solution.md"), []byte("<!-- checkpoint: cleanup-script-ready -->\nchanged\n"), 0o644)
		}, want: "materialized_revision"},
		{name: "path change", mutate: func(t *testing.T, root string, binding roadmap.ChallengeRef) {
			if err := os.Rename(filepath.Join(root, binding.SourceSlug), filepath.Join(root, "renamed-source")); err != nil {
				t.Fatal(err)
			}
		}, want: "referenced materialized source is missing"},
		{name: "executable bit change", mutate: func(t *testing.T, root string, binding roadmap.ChallengeRef) {
			path := filepath.Join(root, binding.SourceSlug, "nodes", "host", "generate.sh")
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}, want: "materialized_revision"},
		{name: "title change", mutate: func(t *testing.T, root string, binding roadmap.ChallengeRef) {
			mutatePublishedManifest(t, root, binding, "title: Cleanup logs", "title: Changed title")
		}, want: "materialized title"},
		{name: "content revision change", mutate: func(t *testing.T, root string, binding roadmap.ChallengeRef) {
			mutatePublishedManifest(t, root, binding, binding.ContentRevision, "sha256:"+strings.Repeat("f", 64))
		}, want: "materialized content_revision"},
		{name: "source slug change", mutate: func(t *testing.T, root string, binding roadmap.ChallengeRef) {
			mutatePublishedManifest(t, root, binding, "source_slug: "+binding.SourceSlug, "source_slug: changed-source")
		}, want: "materialized source_slug"},
		{name: "id change", mutate: func(t *testing.T, root string, binding roadmap.ChallengeRef) {
			mutatePublishedManifest(t, root, binding, "id: "+binding.ID, "id: chal-other")
		}, want: "referenced materialized source is missing"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, binding, root := newMaterializedCatalog(t)
			test.mutate(t, root, binding)
			err := service.CheckIntegrity(context.Background())
			if err == nil || !errors.Is(err, ErrMaterializedIntegrity) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("integrity error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCatalogIntegrityAllowsUnreferencedDirectories(t *testing.T) {
	service, binding, root := newMaterializedCatalog(t)
	if err := os.MkdirAll(filepath.Join(root, "unreferenced"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeCatalogFile(t, filepath.Join(root, "unreferenced", "broken"), []byte("not a challenge"), 0o600)
	if err := challenge.CopyRegularFiles(filepath.Join(root, binding.SourceSlug), filepath.Join(root, "stale-copy")); err != nil {
		t.Fatal(err)
	}
	mutatePublishedManifest(t, root, roadmap.ChallengeRef{SourceSlug: "stale-copy"}, "source_slug: "+binding.SourceSlug, "source_slug: stale-copy")
	if err := service.CheckIntegrity(context.Background()); err != nil {
		t.Fatalf("unreferenced directory blocked catalog integrity: %v", err)
	}
	visible, err := service.List(context.Background())
	if err != nil || len(visible) != 1 {
		t.Fatalf("visible catalog = %#v, err=%v", visible, err)
	}
}

func TestCatalogReadinessAllowsEmptyBootstrapAndBypassesAvailability(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-challenges")
	store := &staticRoadmapStore{revision: &roadmap.Revision{
		Revision: "sha256:" + strings.Repeat("a", 64),
		Domains:  []roadmap.Domain{}, Topics: []roadmap.Topic{}, Tags: []roadmap.Tag{}, ChallengeBindings: []roadmap.ChallengeBinding{},
	}}
	releaseStore := &catalogReadinessStore{release: &catalogdomain.Release{State: catalogdomain.ReleaseInstalling}}
	availability, err := NewAvailability("registry.example/catalog@sha256:"+strings.Repeat("b", 64), releaseStore)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(root, store, availability)
	if err := service.Readiness(context.Background()); err != nil {
		t.Fatalf("empty bootstrap readiness = %v", err)
	}
	if _, err := service.List(context.Background()); !errors.Is(err, ErrReleaseNotReady) {
		t.Fatalf("catalog list did not honor availability gate: %v", err)
	}
}

type staticRoadmapStore struct {
	revision *roadmap.Revision
	err      error
}

func (s *staticRoadmapStore) CurrentRoadmap(context.Context) (*roadmap.Revision, error) {
	return s.revision, s.err
}

type catalogReadinessStore struct {
	release *catalogdomain.Release
}

func (s *catalogReadinessStore) ReleaseByDigest(context.Context, catalogdomain.BundleDigest) (*catalogdomain.Release, error) {
	return s.release, nil
}

func newMaterializedCatalog(t *testing.T) (*Service, roadmap.ChallengeRef, string) {
	t.Helper()
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	writeChallengeSource(t, candidate, false)
	contentRevision, err := ContentRevision(candidate)
	if err != nil {
		t.Fatal(err)
	}
	challengesDir := filepath.Join(root, "challenges")
	published, err := challengePromoteForCatalogTest(challengesDir, candidate, contentRevision)
	if err != nil {
		t.Fatal(err)
	}
	domain := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindDomain, "linux"), SourceRef: "linux", Title: "Linux"}
	topic := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindTopic, "linux/files"), SourceRef: "linux/files", Title: "Files"}
	binding := roadmap.ChallengeBinding{Challenge: roadmap.ChallengeRef{
		ID: published.ID, SourceRef: "linux/files/cleanup-logs", Title: published.Title,
		ContentRevision: published.ContentRevision, SourceSlug: published.SourceSlug, MaterializedRevision: published.Revision,
	}, Topic: topic}
	revision := &roadmap.Revision{
		Revision:          "sha256:" + strings.Repeat("c", 64),
		Domains:           []roadmap.Domain{{ID: domain.ID, SourceRef: domain.SourceRef, Title: domain.Title, Definition: "Linux operations.", Scope: "Files.", NonGoals: "Kernel development."}},
		Topics:            []roadmap.Topic{{ID: topic.ID, SourceRef: topic.SourceRef, Title: topic.Title, Domain: domain, Definition: "File operations.", Scope: "Files.", NonGoals: "Services.", ChallengeGuidance: "Use for file state."}},
		ChallengeBindings: []roadmap.ChallengeBinding{binding},
	}
	if err := revision.Validate(); err != nil {
		t.Fatal(err)
	}
	return NewService(challengesDir, &staticRoadmapStore{revision: revision}, nil), binding.Challenge, challengesDir
}

func challengePromoteForCatalogTest(challengesDir, candidate string, contentRevision catalogdomain.ContentRevision) (*challenge.Entry, error) {
	return challenge.PromoteDirectoryAt(challengesDir, candidate, "chal-integrity", strings.Repeat("a", 64), string(contentRevision), time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC))
}

func mutatePublishedManifest(t *testing.T, root string, binding roadmap.ChallengeRef, old, replacement string) {
	t.Helper()
	path := filepath.Join(root, binding.SourceSlug, "challenge.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(data), old, replacement, 1)
	if updated == string(data) {
		t.Fatalf("manifest text %q not found", old)
	}
	writeCatalogFile(t, path, []byte(updated), 0o600)
}
