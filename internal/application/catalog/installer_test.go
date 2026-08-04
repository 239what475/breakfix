package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	appexecution "github.com/breakfix/breakfix/internal/application/execution"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
)

func TestInstallerPublishesOnlyAfterEveryEntryVerifies(t *testing.T) {
	root := writePortableReleaseWithTwoChallenges(t)
	fixture := newInstallerFixture(t, root)

	runUntil(t, fixture, func() bool { return fixture.store.allEntries(catalogdomain.EntryReadyToCommit) })
	if current := fixture.roadmap.current(); current != nil {
		t.Fatalf("roadmap became visible before every entry was committed: %#v", current)
	}
	visible, err := NewService(fixture.challengesDir, fixture.roadmap).List(context.Background())
	if err != nil {
		t.Fatalf("list incomplete catalog: %v", err)
	}
	if len(visible) != 0 {
		t.Fatalf("incomplete release exposed catalog entries: %#v", visible)
	}

	if err := fixture.installer.RunOnce(context.Background()); err != nil {
		t.Fatalf("prepare release commit: %v", err)
	}
	if state := fixture.store.releaseState(); state != catalogdomain.ReleaseCommitting {
		t.Fatalf("release state after all verified entries = %s, want %s", state, catalogdomain.ReleaseCommitting)
	}
	if current := fixture.roadmap.current(); current != nil {
		t.Fatalf("roadmap became visible before final commit: %#v", current)
	}

	if err := fixture.installer.RunOnce(context.Background()); err != nil {
		t.Fatalf("complete release commit: %v", err)
	}
	if state := fixture.store.releaseState(); state != catalogdomain.ReleaseReady {
		t.Fatalf("release state = %s, want %s", state, catalogdomain.ReleaseReady)
	}
	current := fixture.roadmap.current()
	if current == nil || len(current.ChallengeBindings) != 2 {
		t.Fatalf("published roadmap = %#v", current)
	}
	visible, err = NewService(fixture.challengesDir, fixture.roadmap).List(context.Background())
	if err != nil {
		t.Fatalf("list published catalog: %v", err)
	}
	if len(visible) != 2 {
		t.Fatalf("visible catalog entries = %d, want 2", len(visible))
	}
	if !fixture.store.allRoadmapEntriesProcessed() {
		t.Fatalf("catalog entries did not establish the roadmap processed baseline: %#v", fixture.store.roadmapEntries)
	}
}

func TestInstallerSameDigestDoesNotRepeatCompletedPhases(t *testing.T) {
	fixture := newInstallerFixture(t, portableReleaseRoot(t))
	runUntil(t, fixture, func() bool { return fixture.store.releaseState() == catalogdomain.ReleaseReady })
	before := fixture.runtime.calls()
	releasesBefore := fixture.store.releaseCount()
	entriesBefore := fixture.store.entryCount()
	commitsBefore := fixture.store.commitCount()

	restarted := fixture.restart(t)
	if err := restarted.RunOnce(context.Background()); err != nil {
		t.Fatalf("resume ready release: %v", err)
	}
	if got := fixture.runtime.calls(); got != before {
		t.Fatalf("ready digest re-executed a completed phase: got %#v, want %#v", got, before)
	}
	if fixture.store.releaseCount() != releasesBefore || fixture.store.entryCount() != entriesBefore || fixture.store.commitCount() != commitsBefore {
		t.Fatalf("same digest allocated durable state again: releases=%d entries=%d commits=%d", fixture.store.releaseCount(), fixture.store.entryCount(), fixture.store.commitCount())
	}
}

func TestInstallerRestartResumesStableEntryAndCommitIdentities(t *testing.T) {
	fixture := newInstallerFixture(t, portableReleaseRoot(t))
	if err := fixture.installer.RunOnce(context.Background()); err != nil {
		t.Fatalf("build first entry: %v", err)
	}
	entry := fixture.store.entries()[0]
	if entry.State != catalogdomain.EntryArtifactPublishing || entry.Build == nil {
		t.Fatalf("entry after initial build = %#v", entry)
	}
	entryID := entry.ID

	fixture.installer = fixture.restart(t)
	runUntil(t, fixture, func() bool { return fixture.store.allEntries(catalogdomain.EntryReadyToCommit) })
	if got := fixture.runtime.calls().build; got != 1 {
		t.Fatalf("restart repeated completed build phase %d times", got)
	}
	if got := fixture.store.entries()[0].ID; got != entryID {
		t.Fatalf("entry identity changed after restart: %q != %q", got, entryID)
	}
	if err := fixture.installer.RunOnce(context.Background()); err != nil {
		t.Fatalf("prepare release commit: %v", err)
	}
	commit := fixture.store.commits()[0]
	if commit.State != catalogdomain.CommitPrepared {
		t.Fatalf("prepared commit = %#v", commit)
	}
	commitID := commit.ID

	fixture.installer = fixture.restart(t)
	runUntil(t, fixture, func() bool { return fixture.store.releaseState() == catalogdomain.ReleaseReady })
	if got := fixture.store.commits()[0].ID; got != commitID {
		t.Fatalf("commit identity changed after restart: %q != %q", got, commitID)
	}
	if got := fixture.runtime.calls().final; got != 1 {
		t.Fatalf("restart repeated final artifact publication %d times", got)
	}
}

func TestInstallerDeterministicFailuresLeaveNoVisibleCatalog(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*installerFixture)
	}{
		{
			name: "source",
			configure: func(fixture *installerFixture) {
				fixture.reader.err = errors.New("portable source layer has an invalid contract")
			},
		},
		{
			name: "build",
			configure: func(fixture *installerFixture) {
				fixture.runtime.buildErr = execution.NewArtifactError("SOURCE_INVALID", "candidate source is invalid")
			},
		},
		{
			name: "verify",
			configure: func(fixture *installerFixture) {
				fixture.runtime.verifyErr = execution.NewArtifactError("CHECKPOINT_FAILED", "catalog verification failed")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInstallerFixture(t, portableReleaseRoot(t))
			test.configure(fixture)
			runUntil(t, fixture, func() bool { return fixture.store.releaseState() == catalogdomain.ReleaseFailed })
			if current := fixture.roadmap.current(); current != nil {
				t.Fatalf("failed release published roadmap: %#v", current)
			}
			visible, err := NewService(fixture.challengesDir, fixture.roadmap).List(context.Background())
			if err != nil {
				t.Fatalf("list failed catalog: %v", err)
			}
			if len(visible) != 0 {
				t.Fatalf("failed release exposed entries: %#v", visible)
			}
		})
	}
}

type installerFixture struct {
	installer     *Installer
	store         *memoryReleaseStore
	roadmap       *memoryRoadmap
	runtime       *fakeCatalogRuntime
	puller        *fakeBundlePuller
	reader        *fakeLayerReader
	dataDir       string
	challengesDir string
	reference     string
	snapshot      appexecution.SnapshotConfig
	now           time.Time
}

func newInstallerFixture(t *testing.T, sourceRoot string) *installerFixture {
	t.Helper()
	bundle, err := BuildPortableBundle(sourceRoot)
	if err != nil {
		t.Fatalf("build test portable bundle: %v", err)
	}
	fixture := &installerFixture{
		store:         newMemoryReleaseStore(),
		roadmap:       &memoryRoadmap{},
		runtime:       &fakeCatalogRuntime{},
		puller:        &fakeBundlePuller{archive: []byte("catalog release")},
		reader:        &fakeLayerReader{layer: bundle.SourceLayer},
		dataDir:       t.TempDir(),
		challengesDir: filepath.Join(t.TempDir(), "challenges"),
		reference:     "registry.test.example/breakfix/catalog@sha256:" + strings.Repeat("f", 64),
		snapshot:      testSnapshotConfig(),
		now:           time.Date(2026, time.August, 4, 8, 0, 0, 0, time.UTC),
	}
	fixture.store.roadmap = fixture.roadmap
	fixture.installer = fixture.newInstaller(t)
	return fixture
}

func (f *installerFixture) restart(t *testing.T) *Installer {
	t.Helper()
	return f.newInstaller(t)
}

func (f *installerFixture) newInstaller(t *testing.T) *Installer {
	t.Helper()
	installer, err := NewInstaller(InstallerConfig{
		DataDir:          f.dataDir,
		ChallengesDir:    f.challengesDir,
		ReleaseReference: f.reference,
		InstallDeadline:  time.Hour,
		LeaseTTL:         time.Minute,
		PollInterval:     time.Millisecond,
		WorkerID:         "catalog-test-server",
		Snapshot:         f.snapshot,
		Puller:           f.puller,
		LayerReader:      f.reader,
		Store:            f.store,
		Roadmap:          f.roadmap,
		Builder:          fakeBuilder{runtime: f.runtime},
		Publisher:        fakePublisher{runtime: f.runtime},
		Verifier:         fakeVerifier{runtime: f.runtime},
	})
	if err != nil {
		t.Fatalf("create test installer: %v", err)
	}
	installer.now = func() time.Time { return f.now }
	return installer
}

func runUntil(t *testing.T, fixture *installerFixture, done func() bool) {
	t.Helper()
	for range 32 {
		if done() {
			return
		}
		if err := fixture.installer.RunOnce(context.Background()); err != nil {
			t.Fatalf("advance catalog installer: %v", err)
		}
	}
	t.Fatalf("catalog installer did not reach requested state: release=%s entries=%#v commits=%#v", fixture.store.releaseState(), fixture.store.entries(), fixture.store.commits())
}

func testSnapshotConfig() appexecution.SnapshotConfig {
	return appexecution.SnapshotConfig{
		MaxNodes: 4,
		Node: appexecution.NodeRuntimeConfig{
			BaseImageFingerprint:  strings.Repeat("a", 64),
			ProfileRevision:       "node-profile-v1",
			NetworkPolicyRevision: "node-network-v1",
			CPU:                   "1",
			Memory:                "512MiB",
			Processes:             256,
			RootDisk:              "5GiB",
		},
	}
}

type fakeBundlePuller struct {
	mu      sync.Mutex
	archive []byte
	err     error
	pulls   int
}

func (p *fakeBundlePuller) PullOCIArchive(_ context.Context, _ string, destination string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pulls++
	if p.err != nil {
		return p.err
	}
	return os.WriteFile(destination, p.archive, 0o600)
}

type fakeLayerReader struct {
	mu    sync.Mutex
	layer []byte
	err   error
}

func (r *fakeLayerReader) ReadSourceLayer(_ string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	return append([]byte(nil), r.layer...), nil
}

type runtimeCalls struct {
	build    int
	artifact int
	verify   int
	final    int
}

type fakeCatalogRuntime struct {
	mu        sync.Mutex
	buildErr  error
	verifyErr error
	finalErr  error
	counters  runtimeCalls
}

func (r *fakeCatalogRuntime) calls() runtimeCalls {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counters
}

type fakeBuilder struct{ runtime *fakeCatalogRuntime }

func (b fakeBuilder) ExecuteWork(_ context.Context, work execution.Work, _ []byte, _ []byte) (execution.BuildOutput, []byte, error) {
	if err := work.Validate(); err != nil {
		return execution.BuildOutput{}, nil, err
	}
	b.runtime.mu.Lock()
	b.runtime.counters.build++
	err := b.runtime.buildErr
	b.runtime.mu.Unlock()
	if err != nil {
		return execution.BuildOutput{}, nil, err
	}
	return execution.BuildOutput{Runtime: work.Snapshot.Runtime, Incus: &execution.IncusBuildReference{
		Project: "catalog-build", WorkflowID: work.OwnerID, CandidateRevisionID: work.CandidateID, Attempt: work.Attempt,
		InstanceName: "build-" + work.CandidateID, Alias: "build-" + work.CandidateID,
		Fingerprint: strings.Repeat("b", 64),
	}}, nil, nil
}

type fakePublisher struct{ runtime *fakeCatalogRuntime }

func (p fakePublisher) PublishArtifactWork(_ context.Context, work execution.Work, _ []byte) (execution.ArtifactReference, error) {
	if err := work.Validate(); err != nil {
		return execution.ArtifactReference{}, err
	}
	p.runtime.mu.Lock()
	p.runtime.counters.artifact++
	p.runtime.mu.Unlock()
	return execution.ArtifactReference{Runtime: work.Snapshot.Runtime, IncusAlias: "candidate-" + work.CandidateID, IncusFingerprint: strings.Repeat("c", 64)}, nil
}

func (p fakePublisher) PublishChallengeWork(_ context.Context, work execution.Work, _ string) (execution.ArtifactReference, error) {
	if err := work.Validate(); err != nil {
		return execution.ArtifactReference{}, err
	}
	p.runtime.mu.Lock()
	p.runtime.counters.final++
	err := p.runtime.finalErr
	p.runtime.mu.Unlock()
	if err != nil {
		return execution.ArtifactReference{}, err
	}
	return execution.ArtifactReference{Runtime: work.Snapshot.Runtime, IncusAlias: "challenge-" + work.CandidateID, IncusFingerprint: strings.Repeat("d", 64)}, nil
}

type fakeVerifier struct{ runtime *fakeCatalogRuntime }

func (v fakeVerifier) ExecuteWork(ctx context.Context, work execution.Work, record func(context.Context, execution.VerificationEnvironment) error) (execution.VerificationReport, error) {
	if err := work.Validate(); err != nil {
		return execution.VerificationReport{}, err
	}
	v.runtime.mu.Lock()
	v.runtime.counters.verify++
	err := v.runtime.verifyErr
	v.runtime.mu.Unlock()
	if recordErr := record(ctx, execution.VerificationEnvironment{
		Runtime: work.Snapshot.Runtime, Name: "verify-" + work.CandidateID, UID: "uid-" + work.CandidateID,
		WorkflowID: work.OwnerID, Attempt: work.Attempt,
	}); recordErr != nil {
		return execution.VerificationReport{}, recordErr
	}
	if err != nil {
		var artifact *execution.ArtifactError
		if errors.As(err, &artifact) && artifact.Report != nil {
			return *artifact.Report, err
		}
		return execution.VerificationReport{}, err
	}
	return passingReportFor(work.Snapshot), nil
}

func passingReportFor(snapshot execution.Snapshot) execution.VerificationReport {
	answers := make([]execution.ExecutionResult, 0, len(snapshot.Node.Nodes))
	for _, node := range snapshot.Node.Nodes {
		answers = append(answers, execution.ExecutionResult{Location: node.Name, ExitCode: 0})
	}
	checks := make([]execution.CheckpointResult, 0, len(snapshot.Checkpoints))
	for _, checkpoint := range snapshot.Checkpoints {
		checks = append(checks, execution.CheckpointResult{ID: checkpoint.ID, Passed: true, Summary: "passed"})
	}
	return execution.VerificationReport{Passed: true, Answers: answers, Checkpoints: checks, Summary: "all catalog checks passed"}
}

type memoryRoadmap struct {
	mu       sync.Mutex
	revision *roadmap.Revision
}

func (r *memoryRoadmap) CurrentRoadmap(_ context.Context) (*roadmap.Revision, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.revision == nil {
		return nil, roadmap.ErrNoCurrentRevision
	}
	clone := r.revision.Clone()
	return &clone, nil
}

func (r *memoryRoadmap) current() *roadmap.Revision {
	value, err := r.CurrentRoadmap(context.Background())
	if errors.Is(err, roadmap.ErrNoCurrentRevision) {
		return nil
	}
	if err != nil {
		panic(err)
	}
	return value
}

func (r *memoryRoadmap) set(value roadmap.Revision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := value.Clone()
	r.revision = &clone
}

type memoryRoadmapEntry struct {
	topicID            string
	topicProcessed     bool
	challengeProcessed bool
}

type memoryReleaseStore struct {
	mu             sync.Mutex
	release        *catalogdomain.Release
	entriesByID    map[string]catalogdomain.Entry
	commitsByID    map[string]catalogdomain.Commit
	roadmap        *memoryRoadmap
	roadmapEntries map[string]memoryRoadmapEntry
}

func newMemoryReleaseStore() *memoryReleaseStore {
	return &memoryReleaseStore{
		entriesByID:    make(map[string]catalogdomain.Entry),
		commitsByID:    make(map[string]catalogdomain.Commit),
		roadmapEntries: make(map[string]memoryRoadmapEntry),
	}
}

func (s *memoryReleaseStore) CreateOrGetRelease(_ context.Context, release catalogdomain.Release) (*catalogdomain.Release, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release != nil {
		if s.release.BundleDigest != release.BundleDigest {
			return nil, false, catalogdomain.ErrReleaseNotFound
		}
		return cloneRelease(*s.release), false, nil
	}
	if release.State != catalogdomain.ReleasePending || !release.Valid() {
		return nil, false, errors.New("invalid pending release")
	}
	s.release = cloneRelease(release)
	return cloneRelease(release), true, nil
}

func (s *memoryReleaseStore) InitializeRelease(_ context.Context, release catalogdomain.Release, entries []catalogdomain.Entry, _ time.Time) (*catalogdomain.Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release == nil {
		return nil, catalogdomain.ErrReleaseNotFound
	}
	if s.release.State != catalogdomain.ReleasePending {
		return cloneRelease(*s.release), nil
	}
	if release.ID != s.release.ID || !release.SourceDigest.Valid() || strings.TrimSpace(release.Name) == "" || strings.TrimSpace(release.Version) == "" {
		return nil, errors.New("invalid initialized release")
	}
	for _, entry := range entries {
		if !entry.Valid() {
			return nil, errors.New("invalid entry")
		}
		s.entriesByID[entry.ID] = cloneEntry(entry)
	}
	s.release.Name = release.Name
	s.release.Version = release.Version
	s.release.SourceDigest = release.SourceDigest
	s.release.State = catalogdomain.ReleaseInstalling
	return cloneRelease(*s.release), nil
}

func (s *memoryReleaseStore) FailPendingRelease(_ context.Context, id, summary string, _ time.Time) (*catalogdomain.Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release == nil || s.release.ID != id || s.release.State != catalogdomain.ReleasePending {
		return nil, errors.New("pending release claim lost")
	}
	s.release.State = catalogdomain.ReleaseFailed
	s.release.LastError = summary
	return cloneRelease(*s.release), nil
}

func (s *memoryReleaseStore) FailRelease(_ context.Context, id, summary string, _ time.Time) (*catalogdomain.Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release == nil || s.release.ID != id || s.release.State.Terminal() {
		return nil, errors.New("release claim lost")
	}
	s.release.State = catalogdomain.ReleaseFailed
	s.release.LastError = summary
	s.release.LeaseOwner = ""
	s.release.LeaseExpires = nil
	return cloneRelease(*s.release), nil
}

func (s *memoryReleaseStore) ReleaseByDigest(_ context.Context, digest catalogdomain.BundleDigest) (*catalogdomain.Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release == nil || s.release.BundleDigest != digest {
		return nil, catalogdomain.ErrReleaseNotFound
	}
	return cloneRelease(*s.release), nil
}

func (s *memoryReleaseStore) Entries(_ context.Context, releaseID string) ([]catalogdomain.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release == nil || s.release.ID != releaseID {
		return nil, catalogdomain.ErrReleaseNotFound
	}
	return s.entriesLocked(), nil
}

func (s *memoryReleaseStore) InstalledEntries(_ context.Context) ([]catalogdomain.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release == nil || s.release.State != catalogdomain.ReleaseReady {
		return []catalogdomain.Entry{}, nil
	}
	return s.entriesLocked(), nil
}

func (s *memoryReleaseStore) ClaimEntry(_ context.Context, releaseID, workerID string, ttl time.Duration, now time.Time) (*catalogdomain.EntryClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release == nil || s.release.ID != releaseID || s.release.State != catalogdomain.ReleaseInstalling {
		return nil, nil
	}
	entries := s.entriesLocked()
	for _, entry := range entries {
		if !entry.State.Leaseable() || entry.NextRunAt.After(now) || (entry.LeaseExpires != nil && entry.LeaseExpires.After(now)) {
			continue
		}
		if entry.State == catalogdomain.EntryPending {
			entry.State = catalogdomain.EntryBuilding
		}
		entry.Attempt++
		entry.LeaseOwner = "lease-" + workerID
		expires := now.Add(ttl)
		entry.LeaseExpires = &expires
		s.entriesByID[entry.ID] = entry
		return &catalogdomain.EntryClaim{Release: *cloneRelease(*s.release), Entry: entry}, nil
	}
	return nil, nil
}

func (s *memoryReleaseStore) RenewEntryLease(_ context.Context, claim catalogdomain.EntryClaim, ttl time.Duration, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, err := s.claimedEntryLocked(claim, claim.Entry.State)
	if err != nil {
		return err
	}
	expires := now.Add(ttl)
	entry.LeaseExpires = &expires
	s.entriesByID[entry.ID] = entry
	return nil
}

func (s *memoryReleaseStore) CompleteEntryBuild(_ context.Context, claim catalogdomain.EntryClaim, output execution.BuildOutput, _ time.Time) (*catalogdomain.EntryClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, err := s.claimedEntryLocked(claim, catalogdomain.EntryBuilding)
	if err != nil {
		return nil, err
	}
	entry.Build = &output
	entry.State = catalogdomain.EntryArtifactPublishing
	entry.LeaseOwner = ""
	entry.LeaseExpires = nil
	s.entriesByID[entry.ID] = entry
	return &catalogdomain.EntryClaim{Release: *cloneRelease(*s.release), Entry: entry}, nil
}

func (s *memoryReleaseStore) CompleteEntryArtifactPublish(_ context.Context, claim catalogdomain.EntryClaim, artifact execution.ArtifactReference, _ time.Time) (*catalogdomain.EntryClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, err := s.claimedEntryLocked(claim, catalogdomain.EntryArtifactPublishing)
	if err != nil {
		return nil, err
	}
	entry.Artifact = &artifact
	entry.State = catalogdomain.EntryVerifying
	entry.LeaseOwner = ""
	entry.LeaseExpires = nil
	s.entriesByID[entry.ID] = entry
	return &catalogdomain.EntryClaim{Release: *cloneRelease(*s.release), Entry: entry}, nil
}

func (s *memoryReleaseStore) RecordEntryVerificationEnvironment(_ context.Context, claim catalogdomain.EntryClaim, environment execution.VerificationEnvironment, _ time.Time) (*catalogdomain.EntryClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, err := s.claimedEntryLocked(claim, catalogdomain.EntryVerifying)
	if err != nil {
		return nil, err
	}
	entry.VerifyEnvironment = &environment
	s.entriesByID[entry.ID] = entry
	return &catalogdomain.EntryClaim{Release: *cloneRelease(*s.release), Entry: entry}, nil
}

func (s *memoryReleaseStore) CompleteEntryVerification(_ context.Context, claim catalogdomain.EntryClaim, report execution.VerificationReport, _ time.Time) (*catalogdomain.EntryClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, err := s.claimedEntryLocked(claim, catalogdomain.EntryVerifying)
	if err != nil {
		return nil, err
	}
	entry.Verification = &report
	entry.State = catalogdomain.EntryReadyToCommit
	entry.LeaseOwner = ""
	entry.LeaseExpires = nil
	s.entriesByID[entry.ID] = entry
	return &catalogdomain.EntryClaim{Release: *cloneRelease(*s.release), Entry: entry}, nil
}

func (s *memoryReleaseStore) RetryEntry(_ context.Context, claim catalogdomain.EntryClaim, summary string, nextRunAt, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, err := s.claimedEntryLocked(claim, claim.Entry.State)
	if err != nil {
		return err
	}
	entry.LeaseOwner = ""
	entry.LeaseExpires = nil
	entry.NextRunAt = nextRunAt
	entry.LastError = summary
	s.entriesByID[entry.ID] = entry
	return nil
}

func (s *memoryReleaseStore) FailEntry(_ context.Context, claim catalogdomain.EntryClaim, summary string, report *execution.VerificationReport, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, err := s.claimedEntryLocked(claim, claim.Entry.State)
	if err != nil {
		return err
	}
	entry.State = catalogdomain.EntryFailed
	entry.LeaseOwner = ""
	entry.LeaseExpires = nil
	entry.LastError = summary
	if report != nil {
		entry.Verification = report
	}
	s.entriesByID[entry.ID] = entry
	s.release.State = catalogdomain.ReleaseFailed
	s.release.LastError = summary
	return nil
}

func (s *memoryReleaseStore) PrepareReleaseCommit(_ context.Context, releaseID string, intents []catalogdomain.Commit, _ time.Time) (*catalogdomain.Release, []catalogdomain.Commit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release == nil || s.release.ID != releaseID {
		return nil, nil, catalogdomain.ErrReleaseNotFound
	}
	if s.release.State == catalogdomain.ReleaseCommitting || s.release.State == catalogdomain.ReleaseReady {
		return cloneRelease(*s.release), s.commitsLocked(), nil
	}
	if s.release.State != catalogdomain.ReleaseInstalling || !s.allEntriesLocked(catalogdomain.EntryReadyToCommit) {
		return nil, nil, errors.New("release is not ready to commit")
	}
	for _, intent := range intents {
		s.commitsByID[intent.ID] = cloneCommit(intent)
	}
	if len(s.commitsByID) != len(s.entriesByID) {
		return nil, nil, errors.New("commit intents do not cover entries")
	}
	s.release.State = catalogdomain.ReleaseCommitting
	s.release.CommitID = catalogdomain.CommitIDForRelease(s.release.ID)
	return cloneRelease(*s.release), s.commitsLocked(), nil
}

func (s *memoryReleaseStore) Commits(_ context.Context, releaseID string) ([]catalogdomain.Commit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release == nil || s.release.ID != releaseID {
		return nil, catalogdomain.ErrReleaseNotFound
	}
	return s.commitsLocked(), nil
}

func (s *memoryReleaseStore) ClaimReleaseCommit(_ context.Context, releaseID, workerID string, ttl time.Duration, now time.Time) (*catalogdomain.Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release == nil || s.release.ID != releaseID || s.release.State != catalogdomain.ReleaseCommitting || (s.release.LeaseExpires != nil && s.release.LeaseExpires.After(now)) {
		return nil, nil
	}
	s.release.LeaseOwner = "lease-" + workerID
	expires := now.Add(ttl)
	s.release.LeaseExpires = &expires
	return cloneRelease(*s.release), nil
}

func (s *memoryReleaseStore) RenewReleaseCommitLease(_ context.Context, release catalogdomain.Release, ttl time.Duration, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.claimedReleaseLocked(release); err != nil {
		return err
	}
	expires := now.Add(ttl)
	s.release.LeaseExpires = &expires
	return nil
}

func (s *memoryReleaseStore) FailReleaseCommit(_ context.Context, release catalogdomain.Release, summary string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.claimedReleaseLocked(release); err != nil {
		return err
	}
	s.release.State = catalogdomain.ReleaseFailed
	s.release.LeaseOwner = ""
	s.release.LeaseExpires = nil
	s.release.LastError = summary
	return nil
}

func (s *memoryReleaseStore) CompleteCommitArtifact(_ context.Context, release catalogdomain.Release, commit catalogdomain.Commit, artifact execution.ArtifactReference, _ time.Time) (*catalogdomain.Commit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.claimedReleaseLocked(release); err != nil {
		return nil, err
	}
	stored, exists := s.commitsByID[commit.ID]
	if !exists || stored.State != catalogdomain.CommitPrepared {
		return nil, errors.New("commit claim lost")
	}
	stored.Artifact = &artifact
	stored.State = catalogdomain.CommitArtifactPublished
	s.commitsByID[stored.ID] = stored
	updated := cloneCommit(stored)
	return &updated, nil
}

func (s *memoryReleaseStore) MarkCommitMaterialized(_ context.Context, release catalogdomain.Release, commit catalogdomain.Commit, now time.Time) (*catalogdomain.Commit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.claimedReleaseLocked(release); err != nil {
		return nil, err
	}
	stored, exists := s.commitsByID[commit.ID]
	if !exists || stored.State != catalogdomain.CommitArtifactPublished {
		return nil, errors.New("commit claim lost")
	}
	materialized := now.UTC()
	stored.MaterializedAt = &materialized
	stored.State = catalogdomain.CommitMaterialized
	s.commitsByID[stored.ID] = stored
	updated := cloneCommit(stored)
	return &updated, nil
}

func (s *memoryReleaseStore) CompleteReleaseCommit(_ context.Context, release catalogdomain.Release, revision roadmap.Revision, now time.Time) (*catalogdomain.Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.claimedReleaseLocked(release); err != nil {
		return nil, err
	}
	for _, commit := range s.commitsByID {
		if commit.State != catalogdomain.CommitMaterialized {
			return nil, errors.New("incomplete commit")
		}
		committed := now.UTC()
		commit.CommittedAt = &committed
		commit.State = catalogdomain.CommitCommitted
		s.commitsByID[commit.ID] = commit
	}
	for _, binding := range revision.ChallengeBindings {
		s.roadmapEntries[binding.Challenge.ID] = memoryRoadmapEntry{topicID: binding.Topic.ID, topicProcessed: true, challengeProcessed: true}
	}
	s.release.State = catalogdomain.ReleaseReady
	s.release.LeaseOwner = ""
	s.release.LeaseExpires = nil
	s.roadmap.set(revision)
	return cloneRelease(*s.release), nil
}

func (s *memoryReleaseStore) claimedEntryLocked(claim catalogdomain.EntryClaim, state catalogdomain.EntryState) (catalogdomain.Entry, error) {
	if s.release == nil || s.release.ID != claim.Release.ID || s.release.State != catalogdomain.ReleaseInstalling {
		return catalogdomain.Entry{}, errors.New("entry release claim lost")
	}
	entry, exists := s.entriesByID[claim.Entry.ID]
	if !exists || entry.State != state || entry.LeaseOwner == "" || entry.LeaseOwner != claim.Entry.LeaseOwner || entry.Attempt != claim.Entry.Attempt {
		return catalogdomain.Entry{}, errors.New("entry claim lost")
	}
	return entry, nil
}

func (s *memoryReleaseStore) claimedReleaseLocked(release catalogdomain.Release) error {
	if s.release == nil || s.release.ID != release.ID || s.release.State != catalogdomain.ReleaseCommitting || s.release.LeaseOwner == "" || s.release.LeaseOwner != release.LeaseOwner {
		return errors.New("release commit claim lost")
	}
	return nil
}

func (s *memoryReleaseStore) entriesLocked() []catalogdomain.Entry {
	values := make([]catalogdomain.Entry, 0, len(s.entriesByID))
	for _, entry := range s.entriesByID {
		values = append(values, cloneEntry(entry))
	}
	slices.SortFunc(values, func(left, right catalogdomain.Entry) int {
		if left.SourcePath < right.SourcePath {
			return -1
		}
		if left.SourcePath > right.SourcePath {
			return 1
		}
		return strings.Compare(left.ID, right.ID)
	})
	return values
}

func (s *memoryReleaseStore) commitsLocked() []catalogdomain.Commit {
	values := make([]catalogdomain.Commit, 0, len(s.commitsByID))
	for _, commit := range s.commitsByID {
		values = append(values, cloneCommit(commit))
	}
	slices.SortFunc(values, func(left, right catalogdomain.Commit) int { return strings.Compare(left.EntryID, right.EntryID) })
	return values
}

func (s *memoryReleaseStore) allEntriesLocked(state catalogdomain.EntryState) bool {
	if len(s.entriesByID) == 0 {
		return false
	}
	for _, entry := range s.entriesByID {
		if entry.State != state {
			return false
		}
	}
	return true
}

func (s *memoryReleaseStore) releaseState() catalogdomain.ReleaseState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release == nil {
		return ""
	}
	return s.release.State
}

func (s *memoryReleaseStore) entries() []catalogdomain.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entriesLocked()
}

func (s *memoryReleaseStore) commits() []catalogdomain.Commit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commitsLocked()
}

func (s *memoryReleaseStore) releaseCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release == nil {
		return 0
	}
	return 1
}

func (s *memoryReleaseStore) entryCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entriesByID)
}

func (s *memoryReleaseStore) commitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.commitsByID)
}

func (s *memoryReleaseStore) allEntries(state catalogdomain.EntryState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allEntriesLocked(state)
}

func (s *memoryReleaseStore) allRoadmapEntriesProcessed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.roadmapEntries) != len(s.entriesByID) {
		return false
	}
	for _, entry := range s.roadmapEntries {
		if entry.topicID == "" || !entry.topicProcessed || !entry.challengeProcessed {
			return false
		}
	}
	return true
}

func cloneRelease(value catalogdomain.Release) *catalogdomain.Release {
	clone := value
	if value.LeaseExpires != nil {
		expires := *value.LeaseExpires
		clone.LeaseExpires = &expires
	}
	return &clone
}

func cloneEntry(value catalogdomain.Entry) catalogdomain.Entry {
	clone := value
	if value.LeaseExpires != nil {
		expires := *value.LeaseExpires
		clone.LeaseExpires = &expires
	}
	if value.Build != nil {
		build := *value.Build
		clone.Build = &build
	}
	if value.Artifact != nil {
		artifact := *value.Artifact
		clone.Artifact = &artifact
	}
	if value.VerifyEnvironment != nil {
		environment := *value.VerifyEnvironment
		clone.VerifyEnvironment = &environment
	}
	if value.Verification != nil {
		report := *value.Verification
		clone.Verification = &report
	}
	return clone
}

func cloneCommit(value catalogdomain.Commit) catalogdomain.Commit {
	clone := value
	if value.Artifact != nil {
		artifact := *value.Artifact
		clone.Artifact = &artifact
	}
	if value.MaterializedAt != nil {
		at := *value.MaterializedAt
		clone.MaterializedAt = &at
	}
	if value.CommittedAt != nil {
		at := *value.CommittedAt
		clone.CommittedAt = &at
	}
	return clone
}

func writePortableReleaseWithTwoChallenges(t *testing.T) string {
	t.Helper()
	root, firstRevision, _ := writePortableRelease(t)
	secondPath := filepath.Join(root, "challenges", "linux", "second-cleanup-logs")
	writeChallengeSource(t, secondPath, false)
	manifestPath := filepath.Join(secondPath, "challenge.yaml")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest = []byte(strings.Replace(string(manifest), "title: Cleanup logs", "title: Second cleanup logs", 1))
	if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	secondRevision, err := ContentRevision(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	writeCatalogFile(t, filepath.Join(root, "roadmap", "challenge-bindings", "second-cleanup-logs.yaml"), []byte(fmt.Sprintf(`kind: Challenge
challenge:
  path: challenges/linux/second-cleanup-logs
  source_ref: linux/shell-files/second-cleanup-logs
  title: Second cleanup logs
  content_revision: %s
topic:
  source_ref: linux/shell-files
  title: Shell and files
tags:
  - source_ref: shell
    title: Shell
`, secondRevision)), 0o644)
	roadmapRevision, err := ContentRevision(filepath.Join(root, "roadmap"))
	if err != nil {
		t.Fatal(err)
	}
	writeCatalogFile(t, filepath.Join(root, "release.yaml"), []byte(fmt.Sprintf(`apiVersion: breakfix.dev/catalog/v1
kind: CatalogRelease
metadata:
  name: foundation
  version: 2026.08.02
entries:
  - path: challenges/linux/cleanup-logs
    contentRevision: %s
  - path: challenges/linux/second-cleanup-logs
    contentRevision: %s
roadmap:
  contentRevision: %s
`, firstRevision, secondRevision, roadmapRevision)), 0o644)
	return root
}

func portableReleaseRoot(t *testing.T) string {
	t.Helper()
	root, _, _ := writePortableRelease(t)
	return root
}
