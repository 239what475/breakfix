package catalog

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	appexecution "github.com/breakfix/breakfix/internal/application/execution"
	"github.com/breakfix/breakfix/internal/content/scenario"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/environment"
	"github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/publication"
)

func TestInstallerCreatesCommitIntentsAfterRuntimeEntriesAreReady(t *testing.T) {
	root := filepath.Join("..", "..", "..", "test", "fixtures", "catalog-release")
	source, err := LoadPortableSource(root)
	if err != nil {
		t.Fatalf("load fixture source: %v", err)
	}
	if len(source.Scenarios) == 0 {
		t.Fatal("fixture has no scenarios")
	}
	now := time.Date(2026, time.August, 4, 12, 0, 0, 0, time.UTC)
	releaseID := "catalog-release-test"
	entry := catalogdomain.Entry{
		ID: "catalog-entry-test", ReleaseID: releaseID, SourcePath: source.Scenarios[0].Path, SourceRef: "node-runtime-fixture", Title: source.Scenarios[0].Entry.Title,
		Type: source.Scenarios[0].Entry.Type, Tags: source.Scenarios[0].Entry.Tags,
		ContentRevision: source.Scenarios[0].ContentRevision, ArchiveSHA256: "sha256:" + strings.Repeat("a", 64),
		Snapshot: execution.Snapshot{Runtime: source.Scenarios[0].Entry.Runtime},
		State:    catalogdomain.EntryReadyToCommit, StateVersion: 4, RuntimeAttempt: 0, NextRunAt: now, Build: &execution.BuildOutput{Runtime: source.Scenarios[0].Entry.Runtime},
		Artifact:     &execution.ArtifactReference{Runtime: source.Scenarios[0].Entry.Runtime},
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
		DataDir: t.TempDir(), ScenariosDir: t.TempDir(), ReleaseReference: "registry.example.com/breakfix/catalog@sha256:" + strings.Repeat("d", 64),
		PollInterval: time.Second, Puller: installerPuller{}, LayerReader: installerLayerReader{}, Store: store,
	})
	if err != nil {
		t.Fatalf("create installer: %v", err)
	}
	if err := installer.prepareCommit(context.Background(), &store.release); err != nil {
		t.Fatalf("prepare commit: %v", err)
	}
	if store.release.State != catalogdomain.ReleaseCommitting || len(store.commits) != 1 {
		t.Fatalf("prepared catalog state = release:%#v commits:%#v", store.release, store.commits)
	}
	commit := store.commits[0]
	if commit.State != catalogdomain.CommitPrepared || commit.StateVersion != 1 || commit.RuntimeAttempt != 1 || commit.EntryID != entry.ID || commit.ScenarioID == "" {
		t.Fatalf("commit intent = %#v", commit)
	}
}

func TestInstallerNewEntriesUseDirectScenarioMetadata(t *testing.T) {
	sourceRoot := filepath.Join("..", "..", "..", "test", "fixtures", "catalog-release")
	source, err := LoadPortableSource(sourceRoot)
	if err != nil {
		t.Fatalf("load fixture source: %v", err)
	}
	installer, err := NewInstaller(InstallerConfig{
		DataDir: t.TempDir(), ScenariosDir: t.TempDir(), ReleaseReference: "registry.example.com/breakfix/catalog@sha256:" + strings.Repeat("b", 64),
		PollInterval: time.Second, Puller: installerPuller{}, LayerReader: installerLayerReader{}, Store: &installerStore{},
		Snapshot: appexecution.SnapshotConfig{MaxNodes: 1, Node: appexecution.NodeRuntimeConfig{
			BaseImageFingerprint: strings.Repeat("c", 64), ProfileRevision: "profile", NetworkPolicyRevision: "network",
			CPU: "1", Memory: "512MiB", Processes: 64, RootDisk: "5GiB",
		}, K8s: appexecution.K8sRuntimeConfig{
			BaseImageDigest: "registry.example/k8s-base@sha256:" + strings.Repeat("d", 64), ProfileRevision: "vk8s-profile", Version: "v1.36.2",
			ManagementTerminalImage: "registry.example/k8s-base@sha256:" + strings.Repeat("d", 64),
			Resources: execution.K8sResources{
				ControlPlaneCPU: "500m", ControlPlaneMemory: "512Mi", ControlPlaneEphemeralStorage: "1Gi",
				WorkloadCPU: "250m", WorkloadMemory: "256Mi", WorkloadEphemeralStorage: "1Gi",
				QuotaCPU: "1", QuotaMemory: "2Gi", QuotaEphemeralStorage: "4Gi",
			},
			Network: environment.VK8sNetwork{PublicEgressCIDR: "0.0.0.0/0", ProtectedCIDRs: []string{"10.0.0.0/8"}},
		}},
	})
	if err != nil {
		t.Fatalf("create installer: %v", err)
	}
	entries, err := installer.newEntries("catalog-release-direct-metadata", source)
	if err != nil {
		t.Fatalf("build direct catalog entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("catalog entries = %#v", entries)
	}
	byRef := make(map[string]catalogdomain.Entry, len(entries))
	for _, entry := range entries {
		byRef[entry.SourceRef] = entry
	}
	if entry := byRef["node-runtime-fixture"]; entry.Type != scenario.ScenarioOperationsScenario || !slices.Equal(entry.Tags, []string{"linux", "runtime-fixture"}) {
		t.Fatalf("Node direct catalog metadata = %#v", entry)
	}
	if entry := byRef["k8s-reproduction-core"]; entry.Type != scenario.ScenarioOperationsScenario || entry.Snapshot.Runtime != scenario.RuntimeK8s || !slices.Equal(entry.Tags, []string{"kubernetes", "runtime-fixture"}) {
		t.Fatalf("K8s direct catalog metadata = %#v", entry)
	}
}

func TestInstallerMaterializedCommitCapturesRevision(t *testing.T) {
	sourceRoot := filepath.Join("..", "..", "..", "test", "fixtures", "catalog-release")
	source, err := LoadPortableSource(sourceRoot)
	if err != nil {
		t.Fatalf("load fixture source: %v", err)
	}
	entry := catalogdomain.Entry{
		ID: "catalog-entry-materialized", ReleaseID: "catalog-release-materialized", SourcePath: source.Scenarios[0].Path,
		SourceRef: "node-runtime-fixture", Title: source.Scenarios[0].Entry.Title, Type: source.Scenarios[0].Entry.Type, Tags: source.Scenarios[0].Entry.Tags,
		ContentRevision: source.Scenarios[0].ContentRevision, Snapshot: execution.Snapshot{Runtime: source.Scenarios[0].Entry.Runtime},
	}
	commit := catalogdomain.Commit{
		ID: "catalog-commit-materialized", ReleaseID: entry.ReleaseID, EntryID: entry.ID, ScenarioID: "chal-materialized", ScenarioRevisionID: "chrev-aaaaaaaaaaaaaaaa",
		SourceSlug: scenario.SourceSlugFor(entry.Title, "chal-materialized"), State: catalogdomain.CommitArtifactPublished,
		Artifact: &execution.ArtifactReference{Runtime: scenario.RuntimeNode, IncusAlias: "catalog-materialized", IncusFingerprint: strings.Repeat("a", 64)},
	}
	installer, err := NewInstaller(InstallerConfig{
		DataDir: t.TempDir(), ScenariosDir: t.TempDir(), ReleaseReference: "registry.example.com/breakfix/catalog@sha256:" + strings.Repeat("b", 64),
		PollInterval: time.Second, Puller: installerPuller{}, LayerReader: installerLayerReader{}, Store: &installerStore{},
	})
	if err != nil {
		t.Fatalf("create installer: %v", err)
	}
	publishedAt := time.Date(2026, time.August, 6, 0, 0, 0, 123456789, time.UTC)
	installer.now = func() time.Time { return publishedAt }
	published, err := installer.materializeCommit(source, entry, commit)
	if err != nil {
		t.Fatalf("materialize catalog commit: %v", err)
	}
	if !published.PublishedAt.Equal(publishedAt.Truncate(time.Microsecond)) {
		t.Fatalf("materialized published_at = %s, want PostgreSQL-compatible %s", published.PublishedAt, publishedAt.Truncate(time.Microsecond))
	}
	commit.MaterializedRevision = published.Revision
	if _, err := installer.ensureMaterialized(entry, commit); err != nil {
		t.Fatalf("verify materialized catalog commit: %v", err)
	}
	stored, err := scenario.ValidateDir(filepath.Join(installer.scenariosDir, commit.SourceSlug, commit.ScenarioRevisionID))
	if err != nil {
		t.Fatalf("read materialized scenario: %v", err)
	}
	if commit.MaterializedRevision != stored.Revision {
		t.Fatalf("durable materialized revision = %q, want %q", commit.MaterializedRevision, stored.Revision)
	}
}

func TestCatalogBootstrapSelectsOnlyTheInitialBaseline(t *testing.T) {
	digestA := catalogdomain.BundleDigest("sha256:" + strings.Repeat("a", 64))
	digestB := catalogdomain.BundleDigest("sha256:" + strings.Repeat("b", 64))
	release := func(digest catalogdomain.BundleDigest, state catalogdomain.ReleaseState) catalogdomain.Release {
		return catalogdomain.Release{ID: catalogdomain.ReleaseIDForBundle(digest), BundleDigest: digest, State: state}
	}
	tests := []struct {
		name      string
		state     catalogdomain.BootstrapState
		digest    catalogdomain.BundleDigest
		wantState catalogdomain.ReleaseState
		wantWait  bool
		wantErr   bool
	}{
		{name: "empty platform", digest: digestA},
		{name: "resume active digest", state: catalogdomain.BootstrapState{Releases: []catalogdomain.Release{release(digestA, catalogdomain.ReleaseInstalling)}}, digest: digestA, wantState: catalogdomain.ReleaseInstalling},
		{name: "reject changed active digest", state: catalogdomain.BootstrapState{Releases: []catalogdomain.Release{release(digestA, catalogdomain.ReleaseInstalling)}}, digest: digestB, wantErr: true},
		{name: "restart ready baseline", state: catalogdomain.BootstrapState{Releases: []catalogdomain.Release{release(digestA, catalogdomain.ReleaseReady)}, PublishedScenarioCount: 4}, digest: digestA, wantState: catalogdomain.ReleaseReady},
		{name: "reject changed ready digest", state: catalogdomain.BootstrapState{Releases: []catalogdomain.Release{release(digestA, catalogdomain.ReleaseReady)}, PublishedScenarioCount: 4}, digest: digestB, wantErr: true},
		{name: "reject authoring content", state: catalogdomain.BootstrapState{PublishedScenarioCount: 1}, digest: digestA, wantErr: true},
		{name: "wait for failed release cleanup", state: catalogdomain.BootstrapState{Releases: []catalogdomain.Release{release(digestA, catalogdomain.ReleaseFailed)}, FailedCleanupPending: true}, digest: digestB, wantWait: true},
		{name: "replace cleaned failed release", state: catalogdomain.BootstrapState{Releases: []catalogdomain.Release{release(digestA, catalogdomain.ReleaseFailed)}}, digest: digestB},
		{name: "retain failed configured digest", state: catalogdomain.BootstrapState{Releases: []catalogdomain.Release{release(digestA, catalogdomain.ReleaseFailed)}, FailedCleanupPending: true}, digest: digestA, wantState: catalogdomain.ReleaseFailed},
		{name: "reject multiple active attempts", state: catalogdomain.BootstrapState{Releases: []catalogdomain.Release{release(digestA, catalogdomain.ReleasePending), release(digestB, catalogdomain.ReleaseInstalling)}}, digest: digestA, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selected, wait, err := selectBootstrapRelease(test.state, test.digest)
			if (err != nil) != test.wantErr {
				t.Fatalf("select bootstrap error = %v, want error %v", err, test.wantErr)
			}
			if test.wantErr {
				if !errors.Is(err, ErrBootstrapConflict) {
					t.Fatalf("bootstrap error = %v, want conflict", err)
				}
				return
			}
			if wait != test.wantWait {
				t.Fatalf("cleanup wait = %v, want %v", wait, test.wantWait)
			}
			if test.wantState == "" {
				if selected != nil {
					t.Fatalf("selected release = %#v, want none", selected)
				}
				return
			}
			if selected == nil || selected.State != test.wantState {
				t.Fatalf("selected release = %#v, want state %s", selected, test.wantState)
			}
		})
	}
}

func TestInstallerValidateBootstrapRejectsPublishedAuthoringPlatform(t *testing.T) {
	store := &installerStore{publishedScenarios: 1}
	installer, err := NewInstaller(InstallerConfig{
		DataDir: t.TempDir(), ScenariosDir: t.TempDir(), ReleaseReference: "registry.example.com/breakfix/catalog@sha256:" + strings.Repeat("d", 64),
		PollInterval: time.Second, Puller: installerPuller{}, LayerReader: installerLayerReader{}, Store: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := installer.ValidateBootstrap(context.Background()); !errors.Is(err, ErrBootstrapConflict) {
		t.Fatalf("validate authoring platform bootstrap = %v, want conflict", err)
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
	release              catalogdomain.Release
	otherReleases        []catalogdomain.Release
	entries              []catalogdomain.Entry
	commits              []catalogdomain.Commit
	publishedScenarios   int
	failedCleanupPending bool
}

func (s *installerStore) CreateOrGetRelease(_ context.Context, release catalogdomain.Release) (*catalogdomain.Release, bool, error) {
	if s.release.ID == "" {
		s.release = release
		return &s.release, true, nil
	}
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
func (s *installerStore) RecordCatalogFinalizerFailure(_ context.Context, _ string, diagnostic publication.Diagnostic) (*catalogdomain.Release, error) {
	s.release.FinalizerErrorCategory = diagnostic.Category
	s.release.FinalizerLastError = diagnostic.LastError
	s.release.FinalizerLastAttemptedAt = &diagnostic.LastAttemptedAt
	s.release.FinalizerNextRetryAt = diagnostic.NextRetryAt
	if diagnostic.Category == publication.CategoryDeterministic {
		s.release.State = catalogdomain.ReleaseFailed
	}
	return &s.release, nil
}
func (s *installerStore) CatalogBootstrapState(context.Context) (catalogdomain.BootstrapState, error) {
	releases := append([]catalogdomain.Release(nil), s.otherReleases...)
	if s.release.ID != "" {
		releases = append(releases, s.release)
	}
	return catalogdomain.BootstrapState{
		Releases: releases, PublishedScenarioCount: s.publishedScenarios, FailedCleanupPending: s.failedCleanupPending,
	}, nil
}
func (*installerStore) EnsureCatalogResourceReaps(context.Context, time.Time) error { return nil }
func (s *installerStore) Entries(context.Context, string) ([]catalogdomain.Entry, error) {
	return append([]catalogdomain.Entry(nil), s.entries...), nil
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
func (*installerStore) MarkCommitMaterialized(context.Context, string, string, string, time.Time) (*catalogdomain.Commit, error) {
	return nil, errors.New("not used by this test")
}
func (*installerStore) CompleteReleaseCommit(context.Context, string, time.Time) (*catalogdomain.Release, error) {
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
