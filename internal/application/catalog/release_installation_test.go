package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/oci"
	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/content/taxonomy"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/generation"
	taxonomydomain "github.com/breakfix/breakfix/internal/domain/taxonomy"
)

func TestInstallerStagesPortableReleaseWithDurableWorkflowInputs(t *testing.T) {
	sourceRoot, _, _ := writePortableRelease(t)
	bundle, err := BuildPortableBundle(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "foundation.oci.tar")
	if _, err := oci.WriteArtifactArchive(archive, oci.Artifact{
		ArtifactType: bundle.ArtifactType,
		Blobs:        []oci.ArtifactBlob{{MediaType: bundle.LayerMediaType, Data: bundle.SourceLayer}},
		Annotations:  bundle.Annotations,
	}); err != nil {
		t.Fatal(err)
	}

	dataDir := t.TempDir()
	store := &installationStoreStub{}
	installer, err := NewInstaller(InstallerConfig{
		DataDir:       dataDir,
		ChallengesDir: filepath.Join(dataDir, "challenges"),
		Taxonomy:      taxonomy.NewStore(dataDir),
		Puller:        copiedBundlePuller{source: archive},
		LayerReader: oci.ArtifactLayerReader{
			ArtifactType: ReleaseArtifactType,
			LayerType:    ReleaseSourceLayerType,
		},
		Store: store,
		Snapshotter: func(entry challenge.Entry) (generation.ExecutionSnapshot, error) {
			return validNodeSnapshot(entry), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 1, 10, 0, 0, 0, time.UTC)
	installer.now = func() time.Time { return now }
	reference := "registry.example/breakfix/catalog@sha256:" + strings.Repeat("a", 64)
	release, err := installer.Install(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	if release.State != catalogdomain.ReleaseInstalling || release.DeadlineAt == nil || !release.DeadlineAt.Equal(now.Add(generation.ExecutionDeadline)) {
		t.Fatalf("installed release = %#v", release)
	}
	if store.installation.Release.ID != release.ID || len(store.installation.Entries) != 1 {
		t.Fatalf("durable installation = %#v", store.installation)
	}
	entry := store.installation.Entries[0]
	if entry.Workflow.Source.Kind != generation.SourceRelease || entry.Workflow.Source.Ref != entry.Entry.ID ||
		entry.Candidate.SourceRevision != string(entry.Entry.ContentRevision) {
		t.Fatalf("release workflow lineage = %#v", entry)
	}
	if _, err := os.Stat(filepath.Join(dataDir, ".staging", "releases", release.ID, "source", "release.yaml")); err != nil {
		t.Fatalf("staged source missing: %v", err)
	}
	if _, err := os.Stat(entry.Candidate.ArchivePath); err != nil {
		t.Fatalf("staged candidate archive missing: %v", err)
	}
}

func TestReleaseCoordinatorCommitsPreparedReleaseAndRemovesResidue(t *testing.T) {
	sourceRoot, contentRevision, taxonomyRevision := writePortableRelease(t)
	dataDir := t.TempDir()
	now := time.Date(2026, time.August, 1, 11, 0, 0, 0, time.UTC)
	store, candidate := stagedReleaseFixture(t, sourceRoot, dataDir, contentRevision, taxonomyRevision, now, catalogdomain.ReleaseInstalling)
	coordinator, err := NewReleaseCoordinator(ReleaseCoordinatorConfig{
		DataDir: dataDir, ChallengesDir: filepath.Join(dataDir, "challenges"), Taxonomy: taxonomy.NewStore(dataDir),
		Releases: store, Candidates: coordinatorCandidates{candidate.ID: candidate},
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator.now = func() time.Time { return now }
	if err := coordinator.Advance(context.Background(), store.release.ID); err != nil {
		t.Fatal(err)
	}
	if store.release.State != catalogdomain.ReleaseReady {
		t.Fatalf("release state = %s, want Ready", store.release.State)
	}
	commit := store.commits[store.entries[0].ID]
	if commit.State != catalogdomain.CommitCommitted || commit.RuntimeIdentity == nil {
		t.Fatalf("release commit = %#v", commit)
	}
	published, err := challenge.Get(filepath.Join(dataDir, "challenges"), commit.RuntimeIdentity.ChallengeID)
	if err != nil {
		t.Fatalf("read committed challenge: %v", err)
	}
	if published.ContentRevision != string(contentRevision) {
		t.Fatalf("published content revision = %q, want %q", published.ContentRevision, contentRevision)
	}
	if _, err := taxonomy.NewStore(dataDir).LoadCurrent(); err != nil {
		t.Fatalf("load committed taxonomy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, ".staging", "releases", store.release.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("release staging remains after Ready: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "candidates", candidate.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate archive remains after Ready: %v", err)
	}
	if err := coordinator.Advance(context.Background(), store.release.ID); err != nil {
		t.Fatalf("recover ready release: %v", err)
	}
}

func TestReleaseCoordinatorKeepsCleaningUpUntilWorkerAndFilesystemCleanupFinish(t *testing.T) {
	sourceRoot, contentRevision, taxonomyRevision := writePortableRelease(t)
	dataDir := t.TempDir()
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	store, candidate := stagedReleaseFixture(t, sourceRoot, dataDir, contentRevision, taxonomyRevision, now, catalogdomain.ReleaseCleaningUp)
	entry := store.entries[0]
	store.entries[0].State = catalogdomain.EntryCleaningUp
	identity := catalogdomain.RuntimeIdentity{ChallengeID: "chal-cleanup", Slug: challenge.SourceSlugFor("Cleanup logs", "chal-cleanup")}
	store.commits[entry.ID] = catalogdomain.Commit{
		EntryID: entry.ID, ContentRevision: contentRevision, State: catalogdomain.CommitMaterialized, RuntimeIdentity: &identity,
		MaterializedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	stageRoot := filepath.Join(dataDir, ".staging", "releases", store.release.ID, "source")
	source, err := LoadPortableSource(stageRoot)
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(stageRoot, filepath.FromSlash(entry.SourcePath))
	if _, err := challenge.PromoteDirectoryAt(filepath.Join(dataDir, "challenges"), sourceDir, identity.ChallengeID,
		strings.Repeat("a", 64), string(contentRevision), now); err != nil {
		t.Fatal(err)
	}
	published, err := challenge.Get(filepath.Join(dataDir, "challenges"), identity.ChallengeID)
	if err != nil {
		t.Fatal(err)
	}
	runtimeTaxonomy, err := compilePortableTaxonomy(source.Taxonomy, map[string]*challenge.Entry{entry.SourcePath: published})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := taxonomy.NewStore(dataDir).Publish(runtimeTaxonomy); err != nil {
		t.Fatal(err)
	}
	store.cleanupReady = false
	coordinator, err := NewReleaseCoordinator(ReleaseCoordinatorConfig{
		DataDir: dataDir, ChallengesDir: filepath.Join(dataDir, "challenges"), Taxonomy: taxonomy.NewStore(dataDir),
		Releases: store, Candidates: coordinatorCandidates{candidate.ID: candidate},
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator.now = func() time.Time { return now }
	if err := coordinator.Advance(context.Background(), store.release.ID); err != nil {
		t.Fatal(err)
	}
	if store.release.State != catalogdomain.ReleaseCleaningUp {
		t.Fatalf("release became terminal before worker cleanup: %s", store.release.State)
	}
	if _, err := os.Stat(published.Dir); err != nil {
		t.Fatalf("materialized challenge removed before worker cleanup: %v", err)
	}

	store.cleanupReady = true
	if err := coordinator.Advance(context.Background(), store.release.ID); err != nil {
		t.Fatal(err)
	}
	if store.release.State != catalogdomain.ReleaseFailed {
		t.Fatalf("release state = %s, want Failed", store.release.State)
	}
	if _, err := os.Stat(published.Dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed release challenge remains: %v", err)
	}
	if _, err := taxonomy.NewStore(dataDir).LoadCurrent(); !errors.Is(err, taxonomydomain.ErrNoCurrentRevision) {
		t.Fatalf("failed release taxonomy remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, ".staging", "releases", store.release.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed release staging remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "candidates", candidate.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed release candidate remains: %v", err)
	}
}

func stagedReleaseFixture(t *testing.T, sourceRoot, dataDir string, contentRevision, taxonomyRevision catalogdomain.ContentRevision, now time.Time, state catalogdomain.ReleaseState) (*releaseStoreStub, generation.Revision) {
	t.Helper()
	release := catalogdomain.Release{
		ID: "catalog-release-test", Name: "foundation", Version: "2026.08.01",
		BundleDigest: catalogdomain.BundleDigest("sha256:" + strings.Repeat("b", 64)), TaxonomyContentRevision: taxonomyRevision,
		State: state, CreatedAt: now, UpdatedAt: now,
	}
	entry := catalogdomain.Entry{
		ID: "catalog-entry-test", ReleaseID: release.ID, SourcePath: "challenges/linux/cleanup-logs", ContentRevision: contentRevision,
		CandidateRevisionID: "catalog-candidate-test", State: catalogdomain.EntryReadyToCommit, CreatedAt: now, UpdatedAt: now,
	}
	stageRoot := filepath.Join(dataDir, ".staging", "releases", release.ID, "source")
	if err := challenge.CopyRegularFiles(sourceRoot, stageRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "candidates", entry.CandidateRevisionID), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "candidates", entry.CandidateRevisionID, "candidate.tar.gz"), []byte("candidate"), 0o400); err != nil {
		t.Fatal(err)
	}
	candidate := generation.Revision{
		ID: entry.CandidateRevisionID, Source: generation.Source{Kind: generation.SourceRelease, Ref: entry.ID}, SourceRevision: string(contentRevision),
		Snapshot:     generation.ExecutionSnapshot{Runtime: challenge.RuntimeNode},
		Artifact:     &generation.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: "catalog-candidate", IncusFingerprint: strings.Repeat("a", 64)},
		Verification: &generation.VerificationReport{Passed: true},
	}
	return &releaseStoreStub{
		release: release, entries: []catalogdomain.Entry{entry}, commits: map[string]catalogdomain.Commit{
			entry.ID: {EntryID: entry.ID, ContentRevision: contentRevision, State: catalogdomain.CommitPending, CreatedAt: now, UpdatedAt: now},
		},
	}, candidate
}

func validNodeSnapshot(entry challenge.Entry) generation.ExecutionSnapshot {
	checkpoints := make([]generation.CheckpointSnapshot, 0, len(entry.Checkpoints))
	for _, checkpoint := range entry.Checkpoints {
		checkpoints = append(checkpoints, generation.CheckpointSnapshot{ID: checkpoint.ID, Node: checkpoint.Node})
	}
	nodes := make([]generation.NodeSnapshot, 0, len(entry.Nodes))
	for _, node := range entry.Nodes {
		nodes = append(nodes, generation.NodeSnapshot{Name: node.Name, Title: node.Title})
	}
	return generation.ExecutionSnapshot{
		Runtime: challenge.RuntimeNode, Checkpoints: checkpoints,
		Node: &generation.NodeRuntimeSnapshot{
			BaseImageFingerprint: strings.Repeat("a", 64), ProfileRevision: "profile-v1", NetworkPolicyRevision: "network-v1", Nodes: nodes,
			Resources: generation.NodeResources{CPU: "1", Memory: "1Gi", Processes: 64, RootDisk: "4Gi"},
		},
	}
}

type copiedBundlePuller struct{ source string }

func (p copiedBundlePuller) PullOCIArchive(_ context.Context, _ string, destination string) error {
	data, err := os.ReadFile(p.source)
	if err != nil {
		return err
	}
	// #nosec G703 -- the installer supplies this controlled temporary archive destination in the test double.
	return os.WriteFile(destination, data, 0o600)
}

type installationStoreStub struct {
	ready        bool
	installation catalogdomain.Installation
}

func (s *installationStoreStub) CreateInstallation(_ context.Context, installation catalogdomain.Installation) (*catalogdomain.Release, error) {
	if err := installation.Validate(); err != nil {
		return nil, err
	}
	s.installation = installation
	return &installation.Release, nil
}

func (s *installationStoreStub) HasReadyRelease(context.Context) (bool, error) { return s.ready, nil }

type coordinatorCandidates map[string]generation.Revision

func (c coordinatorCandidates) GetCandidateRevision(_ context.Context, id string) (*generation.Revision, error) {
	value, exists := c[id]
	if !exists {
		return nil, generation.ErrCandidateNotFound
	}
	return &value, nil
}

type releaseStoreStub struct {
	release      catalogdomain.Release
	entries      []catalogdomain.Entry
	commits      map[string]catalogdomain.Commit
	cleanupReady bool
}

func (s *releaseStoreStub) GetRelease(_ context.Context, id string) (*catalogdomain.Release, error) {
	if id != s.release.ID {
		return nil, errors.New("release not found")
	}
	value := s.release
	return &value, nil
}

func (s *releaseStoreStub) ListEntries(_ context.Context, releaseID string) ([]catalogdomain.Entry, error) {
	if releaseID != s.release.ID {
		return nil, errors.New("release not found")
	}
	return append([]catalogdomain.Entry(nil), s.entries...), nil
}

func (s *releaseStoreStub) GetEntryCommit(_ context.Context, entryID string) (*catalogdomain.Commit, error) {
	value, exists := s.commits[entryID]
	if !exists {
		return nil, errors.New("entry commit not found")
	}
	if value.RuntimeIdentity != nil {
		identity := *value.RuntimeIdentity
		value.RuntimeIdentity = &identity
	}
	return &value, nil
}

func (s *releaseStoreStub) PrepareCommit(_ context.Context, entryID string, identity catalogdomain.RuntimeIdentity, now time.Time) (*catalogdomain.Commit, error) {
	value, exists := s.commits[entryID]
	if !exists {
		return nil, errors.New("entry commit not found")
	}
	if value.RuntimeIdentity == nil {
		value.RuntimeIdentity = &identity
		value.State = catalogdomain.CommitPrepared
		value.UpdatedAt = now
	} else if *value.RuntimeIdentity != identity {
		return nil, errors.New("entry commit identity conflict")
	}
	s.commits[entryID] = value
	return s.GetEntryCommit(context.Background(), entryID)
}

func (s *releaseStoreStub) MarkCommitMaterialized(_ context.Context, entryID string, now time.Time) (*catalogdomain.Commit, error) {
	value, exists := s.commits[entryID]
	if !exists || value.RuntimeIdentity == nil {
		return nil, errors.New("entry commit is not prepared")
	}
	value.State = catalogdomain.CommitMaterialized
	value.MaterializedAt = &now
	value.UpdatedAt = now
	s.commits[entryID] = value
	return s.GetEntryCommit(context.Background(), entryID)
}

func (s *releaseStoreStub) BeginReleaseCommit(_ context.Context, releaseID string, now time.Time) (*catalogdomain.Release, bool, error) {
	if releaseID != s.release.ID {
		return nil, false, errors.New("release not found")
	}
	if s.release.State == catalogdomain.ReleaseInstalling {
		for _, entry := range s.entries {
			if entry.State != catalogdomain.EntryReadyToCommit {
				return &s.release, false, nil
			}
		}
		s.release.State = catalogdomain.ReleaseCommitting
		s.release.UpdatedAt = now
	}
	release := s.release
	return &release, s.release.State == catalogdomain.ReleaseCommitting, nil
}

func (s *releaseStoreStub) CompleteReleaseCommit(_ context.Context, releaseID string, now time.Time) (*catalogdomain.Release, error) {
	if releaseID != s.release.ID || s.release.State != catalogdomain.ReleaseCommitting {
		return nil, errors.New("release is not committing")
	}
	for id, value := range s.commits {
		if value.State != catalogdomain.CommitMaterialized && value.State != catalogdomain.CommitCommitted {
			return nil, errors.New("entry commit is not materialized")
		}
		value.State = catalogdomain.CommitCommitted
		value.CommittedAt = &now
		value.UpdatedAt = now
		s.commits[id] = value
	}
	s.release.State = catalogdomain.ReleaseReady
	s.release.UpdatedAt = now
	return s.GetRelease(context.Background(), releaseID)
}

func (s *releaseStoreStub) ListRecoverableReleases(context.Context) ([]catalogdomain.Release, error) {
	if s.release.State == catalogdomain.ReleaseFailed {
		return nil, nil
	}
	return []catalogdomain.Release{s.release}, nil
}

func (s *releaseStoreStub) StartReleaseCleanup(_ context.Context, releaseID, reason string, now time.Time) error {
	if releaseID != s.release.ID {
		return errors.New("release not found")
	}
	s.release.State = catalogdomain.ReleaseCleaningUp
	if s.release.LastError == "" {
		s.release.LastError = reason
	}
	s.release.UpdatedAt = now
	for index := range s.entries {
		if s.entries[index].State != catalogdomain.EntryFailed {
			s.entries[index].State = catalogdomain.EntryCleaningUp
			s.entries[index].UpdatedAt = now
		}
	}
	return nil
}

func (s *releaseStoreStub) ReleaseCleanupReady(_ context.Context, releaseID string) (bool, error) {
	if releaseID != s.release.ID {
		return false, errors.New("release not found")
	}
	return s.cleanupReady, nil
}

func (s *releaseStoreStub) CompleteReleaseCleanup(_ context.Context, releaseID string, now time.Time) (*catalogdomain.Release, error) {
	if releaseID != s.release.ID || s.release.State != catalogdomain.ReleaseCleaningUp || !s.cleanupReady {
		return nil, fmt.Errorf("release cleanup is not complete")
	}
	s.release.State = catalogdomain.ReleaseFailed
	s.release.UpdatedAt = now
	return s.GetRelease(context.Background(), releaseID)
}
