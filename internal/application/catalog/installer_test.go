package catalog

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
)

func TestInstallerCreatesCommitIntentsAfterRuntimeEntriesAreReady(t *testing.T) {
	root := filepath.Join("..", "..", "..", "test", "fixtures", "catalog-release")
	source, err := LoadPortableSource(root)
	if err != nil {
		t.Fatalf("load fixture source: %v", err)
	}
	if len(source.Challenges) == 0 {
		t.Fatal("fixture has no challenges")
	}
	now := time.Date(2026, time.August, 4, 12, 0, 0, 0, time.UTC)
	releaseID := "catalog-release-test"
	entry := catalogdomain.Entry{
		ID: "catalog-entry-test", ReleaseID: releaseID, SourcePath: source.Challenges[0].Path, SourceRef: "linux/test", Title: source.Challenges[0].Entry.Title,
		ContentRevision: source.Challenges[0].ContentRevision, ArchiveSHA256: "sha256:" + strings.Repeat("a", 64),
		Snapshot: execution.Snapshot{Runtime: source.Challenges[0].Entry.Runtime},
		State:    catalogdomain.EntryReadyToCommit, StateVersion: 4, RuntimeAttempt: 0, NextRunAt: now, Build: &execution.BuildOutput{Runtime: source.Challenges[0].Entry.Runtime},
		Artifact:     &execution.ArtifactReference{Runtime: source.Challenges[0].Entry.Runtime},
		Verification: &execution.VerificationReport{Passed: true}, CreatedAt: now, UpdatedAt: now,
	}
	// The fixture can use either runtime, so use a minimal valid runtime view
	// from its actual frozen content rather than constructing provider output.
	entry = readyFixtureEntry(t, entry, now)
	store := &installerStore{release: catalogdomain.Release{
		ID: releaseID, Name: "fixture", Version: "v1", BundleDigest: catalogdomain.BundleDigest("sha256:" + strings.Repeat("b", 64)),
		SourceDigest: catalogdomain.ContentRevision("sha256:" + strings.Repeat("c", 64)), State: catalogdomain.ReleaseInstalling,
		SourceAttempt: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	}, entries: []catalogdomain.Entry{entry}}
	installer, err := NewInstaller(InstallerConfig{
		DataDir: t.TempDir(), ChallengesDir: t.TempDir(), ReleaseReference: "registry.example.com/breakfix/catalog@sha256:" + strings.Repeat("d", 64),
		PollInterval: time.Second, Puller: installerPuller{}, LayerReader: installerLayerReader{}, Store: store, Roadmap: installerRoadmap{},
	})
	if err != nil {
		t.Fatalf("create installer: %v", err)
	}
	if err := installer.prepareCommit(context.Background(), &store.release, source); err != nil {
		t.Fatalf("prepare commit: %v", err)
	}
	if store.release.State != catalogdomain.ReleaseCommitting || len(store.commits) != 1 {
		t.Fatalf("prepared catalog state = release:%#v commits:%#v", store.release, store.commits)
	}
	commit := store.commits[0]
	if commit.State != catalogdomain.CommitPrepared || commit.StateVersion != 1 || commit.RuntimeAttempt != 1 || commit.EntryID != entry.ID || commit.ChallengeID == "" {
		t.Fatalf("commit intent = %#v", commit)
	}
}

func TestInstallerCompileRevisionCapturesMaterializedIdentity(t *testing.T) {
	sourceRoot := filepath.Join("..", "..", "..", "test", "fixtures", "catalog-release")
	source, err := LoadPortableSource(sourceRoot)
	if err != nil {
		t.Fatalf("load fixture source: %v", err)
	}
	binding := source.Roadmap.ChallengeBindings[0]
	entry := catalogdomain.Entry{
		ID: "catalog-entry-materialized", ReleaseID: "catalog-release-materialized", SourcePath: source.Challenges[0].Path,
		SourceRef: binding.Challenge.SourceRef, Title: binding.Challenge.Title, ContentRevision: source.Challenges[0].ContentRevision,
	}
	commit := catalogdomain.Commit{
		ID: "catalog-commit-materialized", ReleaseID: entry.ReleaseID, EntryID: entry.ID, ChallengeID: "chal-materialized",
		SourceSlug: challenge.SourceSlugFor(entry.Title, "chal-materialized"), State: catalogdomain.CommitMaterialized,
		Artifact: &execution.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: "catalog-materialized", IncusFingerprint: strings.Repeat("a", 64)},
	}
	installer, err := NewInstaller(InstallerConfig{
		DataDir: t.TempDir(), ChallengesDir: t.TempDir(), ReleaseReference: "registry.example.com/breakfix/catalog@sha256:" + strings.Repeat("b", 64),
		PollInterval: time.Second, Puller: installerPuller{}, LayerReader: installerLayerReader{}, Store: &installerStore{}, Roadmap: installerRoadmap{},
	})
	if err != nil {
		t.Fatalf("create installer: %v", err)
	}
	installer.now = func() time.Time { return time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC) }
	if err := installer.materializeCommit(source, entry, commit); err != nil {
		t.Fatalf("materialize catalog commit: %v", err)
	}
	compiled, err := installer.compileRevision(context.Background(), source, catalogdomain.Release{ID: entry.ReleaseID}, []catalogdomain.Entry{entry}, []catalogdomain.Commit{commit})
	if err != nil {
		t.Fatalf("compile catalog revision: %v", err)
	}
	published, err := challenge.ValidateDir(filepath.Join(installer.challengesDir, commit.SourceSlug))
	if err != nil {
		t.Fatalf("read materialized challenge: %v", err)
	}
	got := compiled.ChallengeBindings[0].Challenge
	if got.SourceSlug != published.SourceSlug || got.MaterializedRevision != published.Revision {
		t.Fatalf("compiled materialized identity = %#v, published = %#v", got, published)
	}
}

func readyFixtureEntry(t *testing.T, entry catalogdomain.Entry, now time.Time) catalogdomain.Entry {
	t.Helper()
	// Runtime details are irrelevant to commit preparation. This helper keeps
	// the test focused on the Server coordinator's durable intent boundary.
	// The repository tests cover provider-shaped runtime snapshots.
	entry.Snapshot = execution.Snapshot{Runtime: "node", Checkpoints: []execution.CheckpointSnapshot{{ID: "ready", Node: "host"}}, Node: &execution.NodeRuntimeSnapshot{
		BaseImageFingerprint: strings.Repeat("e", 64), ProfileRevision: "profile", NetworkPolicyRevision: "network",
		Nodes: []execution.NodeSnapshot{{Name: "host", Title: "Host"}}, Resources: execution.NodeResources{CPU: "1", Memory: "512MiB", Processes: 64, RootDisk: "5GiB"},
	}}
	entry.Build = &execution.BuildOutput{Runtime: "node", Incus: &execution.IncusBuildReference{Project: "build", WorkflowID: entry.ID, CandidateRevisionID: entry.ID, Attempt: 1, InstanceName: "build", Alias: "build", Fingerprint: strings.Repeat("f", 64)}}
	entry.Artifact = &execution.ArtifactReference{Runtime: "node", IncusAlias: "candidate", IncusFingerprint: strings.Repeat("f", 64)}
	entry.Verification = &execution.VerificationReport{Passed: true, Answers: []execution.ExecutionResult{{Location: "host", ExitCode: 0}}, Checkpoints: []execution.CheckpointResult{{ID: "ready", Passed: true, Summary: "ready"}}}
	entry.NextRunAt = now
	return entry
}

type installerStore struct {
	release catalogdomain.Release
	entries []catalogdomain.Entry
	commits []catalogdomain.Commit
}

func (s *installerStore) CreateOrGetRelease(context.Context, catalogdomain.Release) (*catalogdomain.Release, bool, error) {
	return &s.release, false, nil
}
func (s *installerStore) InitializeRelease(_ context.Context, release catalogdomain.Release, entries []catalogdomain.Entry, _ time.Time) (*catalogdomain.Release, error) {
	s.release = release
	s.release.State = catalogdomain.ReleaseInstalling
	s.entries = append([]catalogdomain.Entry(nil), entries...)
	return &s.release, nil
}
func (s *installerStore) ReportSourceInfrastructureFailure(context.Context, string, string, time.Time) (*catalogdomain.Release, error) {
	return &s.release, nil
}
func (s *installerStore) FailPendingRelease(context.Context, string, string, time.Time) (*catalogdomain.Release, error) {
	s.release.State = catalogdomain.ReleaseFailed
	return &s.release, nil
}
func (s *installerStore) FailRelease(context.Context, string, string, time.Time) (*catalogdomain.Release, error) {
	s.release.State = catalogdomain.ReleaseFailed
	return &s.release, nil
}
func (s *installerStore) ReleaseByDigest(context.Context, catalogdomain.BundleDigest) (*catalogdomain.Release, error) {
	return nil, catalogdomain.ErrReleaseNotFound
}
func (s *installerStore) Entries(context.Context, string) ([]catalogdomain.Entry, error) {
	return append([]catalogdomain.Entry(nil), s.entries...), nil
}
func (*installerStore) InstalledEntries(context.Context) ([]catalogdomain.Entry, error) {
	return nil, nil
}
func (s *installerStore) PrepareReleaseCommit(_ context.Context, _ string, intents []catalogdomain.Commit, _ time.Time) (*catalogdomain.Release, []catalogdomain.Commit, error) {
	s.release.State = catalogdomain.ReleaseCommitting
	s.release.CommitID = catalogdomain.CommitIDForRelease(s.release.ID)
	s.commits = append([]catalogdomain.Commit(nil), intents...)
	return &s.release, append([]catalogdomain.Commit(nil), s.commits...), nil
}
func (s *installerStore) Commits(context.Context, string) ([]catalogdomain.Commit, error) {
	return append([]catalogdomain.Commit(nil), s.commits...), nil
}
func (*installerStore) MarkCommitMaterialized(context.Context, string, string, time.Time) (*catalogdomain.Commit, error) {
	return nil, errors.New("not used by this test")
}
func (*installerStore) CompleteReleaseCommit(context.Context, string, roadmap.Revision, time.Time) (*catalogdomain.Release, error) {
	return nil, errors.New("not used by this test")
}

type installerPuller struct{}

func (installerPuller) PullOCIArchive(context.Context, string, string) error {
	return errors.New("not used by this test")
}

type installerLayerReader struct{}

func (installerLayerReader) ReadSourceLayer(string) ([]byte, error) {
	return nil, errors.New("not used by this test")
}

type installerRoadmap struct{}

func (installerRoadmap) CurrentRoadmap(context.Context) (*roadmap.Revision, error) {
	return nil, roadmap.ErrNoCurrentRevision
}
