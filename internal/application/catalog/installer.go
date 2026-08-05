package catalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	appexecution "github.com/breakfix/breakfix/internal/application/execution"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/challenge"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
)

// BundlePuller and SourceLayerReader deliberately model only the two OCI
// operations Catalog needs. The installer never receives Registry access via
// a generated worker or an authoring session.
type BundlePuller interface {
	PullOCIArchive(context.Context, string, string) error
}

type SourceLayerReader interface {
	ReadSourceLayer(string) ([]byte, error)
}

type RuntimeBuilder interface {
	ExecuteWork(context.Context, execution.Work, []byte) (execution.BuildOutput, error)
}

type RuntimePublisher interface {
	PublishArtifactWork(context.Context, execution.Work) (execution.ArtifactReference, error)
	PublishChallengeWork(context.Context, execution.Work, string) (execution.ArtifactReference, error)
}

type RuntimeVerifier interface {
	ExecuteWork(context.Context, execution.Work, func(context.Context, execution.VerificationEnvironment) error) (execution.VerificationReport, error)
}

// ReleaseStore is the complete durable boundary for a Catalog install. It is
// intentionally separate from Generation repositories: a release can recover
// every phase without a Generator run, authoring session, or Roadmap task.
type ReleaseStore interface {
	CreateOrGetRelease(context.Context, catalogdomain.Release) (*catalogdomain.Release, bool, error)
	InitializeRelease(context.Context, catalogdomain.Release, []catalogdomain.Entry, time.Time) (*catalogdomain.Release, error)
	FailPendingRelease(context.Context, string, string, time.Time) (*catalogdomain.Release, error)
	FailRelease(context.Context, string, string, time.Time) (*catalogdomain.Release, error)
	ReleaseByDigest(context.Context, catalogdomain.BundleDigest) (*catalogdomain.Release, error)
	Entries(context.Context, string) ([]catalogdomain.Entry, error)
	InstalledEntries(context.Context) ([]catalogdomain.Entry, error)
	ClaimEntry(context.Context, string, string, time.Duration, time.Time) (*catalogdomain.EntryClaim, error)
	RenewEntryLease(context.Context, catalogdomain.EntryClaim, time.Duration, time.Time) error
	CompleteEntryBuild(context.Context, catalogdomain.EntryClaim, execution.BuildOutput, time.Time) (*catalogdomain.EntryClaim, error)
	CompleteEntryArtifactPublish(context.Context, catalogdomain.EntryClaim, execution.ArtifactReference, time.Time) (*catalogdomain.EntryClaim, error)
	RecordEntryVerificationEnvironment(context.Context, catalogdomain.EntryClaim, execution.VerificationEnvironment, time.Time) (*catalogdomain.EntryClaim, error)
	CompleteEntryVerification(context.Context, catalogdomain.EntryClaim, execution.VerificationReport, time.Time) (*catalogdomain.EntryClaim, error)
	RetryEntry(context.Context, catalogdomain.EntryClaim, string, time.Time, time.Time) error
	FailEntry(context.Context, catalogdomain.EntryClaim, string, *execution.VerificationReport, time.Time) error
	PrepareReleaseCommit(context.Context, string, []catalogdomain.Commit, time.Time) (*catalogdomain.Release, []catalogdomain.Commit, error)
	Commits(context.Context, string) ([]catalogdomain.Commit, error)
	ClaimReleaseCommit(context.Context, string, string, time.Duration, time.Time) (*catalogdomain.Release, error)
	RenewReleaseCommitLease(context.Context, catalogdomain.Release, time.Duration, time.Time) error
	FailReleaseCommit(context.Context, catalogdomain.Release, string, time.Time) error
	CompleteCommitArtifact(context.Context, catalogdomain.Release, catalogdomain.Commit, execution.ArtifactReference, time.Time) (*catalogdomain.Commit, error)
	MarkCommitMaterialized(context.Context, catalogdomain.Release, catalogdomain.Commit, time.Time) (*catalogdomain.Commit, error)
	CompleteReleaseCommit(context.Context, catalogdomain.Release, roadmap.Revision, time.Time) (*catalogdomain.Release, error)
}

type RoadmapReader interface {
	CurrentRoadmap(context.Context) (*roadmap.Revision, error)
}

type InstallerConfig struct {
	DataDir          string
	ChallengesDir    string
	ReleaseReference string
	InstallDeadline  time.Duration
	LeaseTTL         time.Duration
	PollInterval     time.Duration
	WorkerID         string
	Snapshot         appexecution.SnapshotConfig
	Puller           BundlePuller
	LayerReader      SourceLayerReader
	Store            ReleaseStore
	Roadmap          RoadmapReader
	Builder          RuntimeBuilder
	Publisher        RuntimePublisher
	Verifier         RuntimeVerifier
}

// Installer is a Server-owned coordinator for configured Catalog Releases.
// It runs no admin API and no GenerationWorkflow; its durable state is wholly
// represented by Catalog Release, Entry, and Commit records.
type Installer struct {
	dataDir       string
	challengesDir string
	reference     string
	deadline      time.Duration
	leaseTTL      time.Duration
	pollInterval  time.Duration
	workerID      string
	snapshot      appexecution.SnapshotConfig
	puller        BundlePuller
	layerReader   SourceLayerReader
	store         ReleaseStore
	roadmap       RoadmapReader
	builder       RuntimeBuilder
	publisher     RuntimePublisher
	verifier      RuntimeVerifier
	now           func() time.Time
	sleep         func(context.Context, time.Duration) error
	mu            sync.Mutex
}

// deterministicCatalogError marks malformed immutable content. Transport,
// Registry availability, and local storage errors remain ordinary errors so
// a Pending release can retry them until its deadline.
type deterministicCatalogError struct{ err error }

func (e *deterministicCatalogError) Error() string { return e.err.Error() }
func (e *deterministicCatalogError) Unwrap() error { return e.err }

func deterministicCatalogFailure(err error) error {
	if err == nil {
		return nil
	}
	return &deterministicCatalogError{err: err}
}

func isDeterministicCatalogError(err error) bool {
	var value *deterministicCatalogError
	return errors.As(err, &value)
}

func NewInstaller(config InstallerConfig) (*Installer, error) {
	if strings.TrimSpace(config.DataDir) == "" || strings.TrimSpace(config.ChallengesDir) == "" ||
		config.InstallDeadline <= 0 || config.LeaseTTL <= 0 || config.PollInterval <= 0 || strings.TrimSpace(config.WorkerID) == "" ||
		config.Puller == nil || config.LayerReader == nil || config.Store == nil || config.Roadmap == nil ||
		config.Builder == nil || config.Publisher == nil || config.Verifier == nil {
		return nil, errors.New("catalog installer requires data paths, configured runtime, durable store, roadmap reader, and execution adapters")
	}
	if _, err := bundleDigest(config.ReleaseReference); err != nil {
		return nil, err
	}
	return &Installer{
		dataDir: filepath.Clean(config.DataDir), challengesDir: filepath.Clean(config.ChallengesDir), reference: strings.TrimSpace(config.ReleaseReference),
		deadline: config.InstallDeadline, leaseTTL: config.LeaseTTL, pollInterval: config.PollInterval, workerID: strings.TrimSpace(config.WorkerID),
		snapshot: config.Snapshot, puller: config.Puller, layerReader: config.LayerReader, store: config.Store, roadmap: config.Roadmap,
		builder: config.Builder, publisher: config.Publisher, verifier: config.Verifier,
		now: func() time.Time { return time.Now().UTC() }, sleep: sleepContext,
	}, nil
}

// Run continuously resumes the configured immutable release. A transient
// provider failure merely leaves its leased phase recoverable; deterministic
// artifact failures are recorded as terminal release failures.
func (i *Installer) Run(ctx context.Context) error {
	for {
		if err := i.RunOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("catalog release installation pass failed", "reference", i.reference, "err", err)
		}
		if err := i.sleep(ctx, i.pollInterval); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

// RunOnce is exposed for deterministic tests and local bootstrap. It advances
// at most one Entry phase or one release commit pass.
func (i *Installer) RunOnce(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	release, source, err := i.ensureRelease(ctx)
	if err != nil {
		return err
	}
	if release.State == catalogdomain.ReleaseReady || release.State == catalogdomain.ReleaseFailed {
		return nil
	}
	if source == nil {
		return fmt.Errorf("catalog release %q has no staged source", release.ID)
	}
	if release.State == catalogdomain.ReleaseInstalling {
		claim, err := i.store.ClaimEntry(ctx, release.ID, i.workerID, i.leaseTTL, i.now().UTC())
		if err != nil {
			return err
		}
		if claim != nil {
			return i.runEntry(ctx, source, *claim)
		}
		return i.prepareCommit(ctx, release, source)
	}
	if release.State == catalogdomain.ReleaseCommitting {
		claim, err := i.store.ClaimReleaseCommit(ctx, release.ID, i.workerID, i.leaseTTL, i.now().UTC())
		if err != nil {
			return err
		}
		if claim == nil {
			return nil
		}
		return i.runCommit(ctx, source, *claim)
	}
	return fmt.Errorf("catalog release %q has unsupported state %s", release.ID, release.State)
}

func (i *Installer) ensureRelease(ctx context.Context) (*catalogdomain.Release, *PortableSource, error) {
	digest, err := bundleDigest(i.reference)
	if err != nil {
		return nil, nil, err
	}
	release, err := i.store.ReleaseByDigest(ctx, digest)
	if err != nil && !errors.Is(err, catalogdomain.ErrReleaseNotFound) {
		return nil, nil, err
	}
	if errors.Is(err, catalogdomain.ErrReleaseNotFound) {
		id := catalogdomain.ReleaseIDForBundle(digest)
		now := i.now().UTC()
		seed := catalogdomain.Release{
			ID: id, BundleDigest: digest, State: catalogdomain.ReleasePending,
			DeadlineAt: now.Add(i.deadline), CreatedAt: now, UpdatedAt: now,
		}
		release, _, err = i.store.CreateOrGetRelease(ctx, seed)
		if err != nil {
			return nil, nil, err
		}
	}
	if release.State.Terminal() {
		return release, nil, nil
	}
	if release.State == catalogdomain.ReleasePending {
		now := i.now().UTC()
		if !release.DeadlineAt.After(now) {
			failed, failErr := i.store.FailPendingRelease(ctx, release.ID, "catalog release installation deadline exceeded", now)
			if failErr != nil {
				return nil, nil, failErr
			}
			return failed, nil, nil
		}
		source, sourceDigest, stageErr := i.stageSource(ctx, release.ID)
		if stageErr != nil {
			if isDeterministicCatalogError(stageErr) {
				failed, failErr := i.store.FailPendingRelease(ctx, release.ID, errorSummary(stageErr), now)
				if failErr != nil {
					return nil, nil, failErr
				}
				return failed, nil, nil
			}
			return nil, nil, stageErr
		}
		if err := i.validateAppendOnly(ctx, source); err != nil {
			failed, failErr := i.store.FailPendingRelease(ctx, release.ID, errorSummary(err), now)
			if failErr != nil {
				return nil, nil, failErr
			}
			return failed, nil, nil
		}
		entries, err := i.newEntries(ctx, release.ID, source)
		if err != nil {
			return nil, nil, err
		}
		seed := *release
		seed.Name = source.Manifest.Metadata.Name
		seed.Version = source.Manifest.Metadata.Version
		seed.SourceDigest = sourceDigest
		initialized, err := i.store.InitializeRelease(ctx, seed, entries, now)
		if err != nil {
			return nil, nil, err
		}
		return initialized, source, nil
	}
	source, sourceDigest, err := i.loadOrStageSource(ctx, *release)
	if err != nil {
		return nil, nil, err
	}
	if sourceDigest != release.SourceDigest {
		failed, failErr := i.store.FailRelease(ctx, release.ID, "staged catalog source digest does not match its durable release", i.now().UTC())
		if failErr != nil {
			return nil, nil, failErr
		}
		return failed, nil, nil
	}
	return release, source, nil
}

func (i *Installer) newEntries(ctx context.Context, releaseID string, source *PortableSource) ([]catalogdomain.Entry, error) {
	installed, err := i.store.InstalledEntries(ctx)
	if err != nil {
		return nil, err
	}
	known := make(map[string]catalogdomain.Entry, len(installed))
	for _, entry := range installed {
		known[entry.SourceRef] = entry
	}
	bindings := portableBindingsByPath(source.Roadmap)
	now := i.now().UTC()
	entries := make([]catalogdomain.Entry, 0, len(source.Challenges))
	for _, sourceChallenge := range source.Challenges {
		binding, exists := bindings[sourceChallenge.Path]
		if !exists {
			return nil, fmt.Errorf("catalog source %q has no roadmap binding", sourceChallenge.Path)
		}
		if prior, exists := known[binding.Challenge.SourceRef]; exists {
			if prior.Title != sourceChallenge.Entry.Title || prior.ContentRevision != sourceChallenge.ContentRevision {
				return nil, fmt.Errorf("catalog source_ref %q changes an installed challenge", binding.Challenge.SourceRef)
			}
			continue
		}
		archive, err := archiveSourceCandidate(filepath.Join(source.Root, filepath.FromSlash(sourceChallenge.Path)))
		if err != nil {
			return nil, fmt.Errorf("archive catalog source %q: %w", sourceChallenge.Path, err)
		}
		snapshot, err := appexecution.Freeze(sourceChallenge.Entry, i.snapshot)
		if err != nil {
			return nil, fmt.Errorf("freeze catalog source %q: %w", sourceChallenge.Path, err)
		}
		entries = append(entries, catalogdomain.Entry{
			ID: catalogdomain.EntryIDFor(releaseID, sourceChallenge.Path), ReleaseID: releaseID, SourcePath: sourceChallenge.Path,
			SourceRef: binding.Challenge.SourceRef, Title: sourceChallenge.Entry.Title, ContentRevision: sourceChallenge.ContentRevision,
			ArchiveSHA256: candidate.Digest(archive), Snapshot: snapshot, State: catalogdomain.EntryPending,
			NextRunAt: now, CreatedAt: now, UpdatedAt: now,
		})
	}
	return entries, nil
}

func (i *Installer) runEntry(ctx context.Context, source *PortableSource, claim catalogdomain.EntryClaim) error {
	leaseCtx, cancelLease := i.entryLeaseContext(ctx, claim)
	defer cancelLease()
	archive, err := sourceArchive(source, claim.Entry)
	if err != nil {
		return i.failEntry(leaseCtx, claim, err, nil)
	}
	work := execution.Work{
		OwnerID: claim.Entry.ID, CandidateID: claim.Entry.ID, ArchiveSHA256: claim.Entry.ArchiveSHA256,
		Snapshot: claim.Entry.Snapshot, Attempt: int64(claim.Entry.Attempt), DeadlineAt: claim.Release.DeadlineAt,
		Build: claim.Entry.Build, Artifact: claim.Entry.Artifact, VerificationEnvironment: claim.Entry.VerifyEnvironment,
	}
	switch claim.Entry.State {
	case catalogdomain.EntryBuilding:
		output, err := i.builder.ExecuteWork(leaseCtx, work, archive)
		if err != nil {
			return i.handleEntryError(leaseCtx, claim, err)
		}
		_, err = i.store.CompleteEntryBuild(leaseCtx, claim, output, i.now().UTC())
		return err

	case catalogdomain.EntryArtifactPublishing:
		artifact, err := i.publisher.PublishArtifactWork(leaseCtx, work)
		if err != nil {
			return i.handleEntryError(leaseCtx, claim, err)
		}
		_, err = i.store.CompleteEntryArtifactPublish(leaseCtx, claim, artifact, i.now().UTC())
		return err

	case catalogdomain.EntryVerifying:
		report, err := i.verifier.ExecuteWork(leaseCtx, work, func(recordCtx context.Context, environment execution.VerificationEnvironment) error {
			_, recordErr := i.store.RecordEntryVerificationEnvironment(recordCtx, claim, environment, i.now().UTC())
			return recordErr
		})
		if err != nil {
			return i.handleEntryError(leaseCtx, claim, err)
		}
		_, err = i.store.CompleteEntryVerification(leaseCtx, claim, report, i.now().UTC())
		return err
	default:
		return i.failEntry(leaseCtx, claim, fmt.Errorf("catalog entry has unsupported state %s", claim.Entry.State), nil)
	}
}

func (i *Installer) prepareCommit(ctx context.Context, release *catalogdomain.Release, source *PortableSource) error {
	entries, err := i.store.Entries(ctx, release.ID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.State != catalogdomain.EntryReadyToCommit {
			return nil
		}
	}
	now := i.now().UTC()
	intents := make([]catalogdomain.Commit, 0, len(entries))
	for _, entry := range entries {
		challengeID := challenge.NewID()
		intents = append(intents, catalogdomain.Commit{
			ID: catalogdomain.EntryCommitIDFor(release.ID, entry.ID), ReleaseID: release.ID, EntryID: entry.ID,
			ChallengeID: challengeID, SourceSlug: challenge.SourceSlugFor(entry.Title, challengeID),
			State: catalogdomain.CommitPrepared, CreatedAt: now, UpdatedAt: now,
		})
	}
	_, _, err = i.store.PrepareReleaseCommit(ctx, release.ID, intents, now)
	return err
}

func (i *Installer) runCommit(ctx context.Context, source *PortableSource, release catalogdomain.Release) error {
	leaseCtx, cancelLease := i.releaseLeaseContext(ctx, release)
	defer cancelLease()
	entries, err := i.store.Entries(leaseCtx, release.ID)
	if err != nil {
		return err
	}
	byID := make(map[string]catalogdomain.Entry, len(entries))
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	commits, err := i.store.Commits(leaseCtx, release.ID)
	if err != nil {
		return err
	}
	for _, commit := range commits {
		entry, exists := byID[commit.EntryID]
		if !exists {
			return fmt.Errorf("catalog commit %q has no entry", commit.ID)
		}
		switch commit.State {
		case catalogdomain.CommitPrepared:
			work := execution.Work{
				OwnerID: release.CommitID, CandidateID: entry.ID, ArchiveSHA256: entry.ArchiveSHA256, Snapshot: entry.Snapshot,
				Attempt: int64(entry.Attempt), DeadlineAt: release.DeadlineAt, Build: entry.Build, Artifact: entry.Artifact,
			}
			artifact, publishErr := i.publisher.PublishChallengeWork(leaseCtx, work, commit.ChallengeID)
			if publishErr != nil {
				return i.handleCommitError(leaseCtx, release, publishErr)
			}
			updated, updateErr := i.store.CompleteCommitArtifact(leaseCtx, release, commit, artifact, i.now().UTC())
			if updateErr != nil {
				return updateErr
			}
			commit = *updated
			fallthrough
		case catalogdomain.CommitArtifactPublished:
			if err := i.materializeCommit(source, entry, commit); err != nil {
				return i.handleCommitError(leaseCtx, release, err)
			}
			if _, err := i.store.MarkCommitMaterialized(leaseCtx, release, commit, i.now().UTC()); err != nil {
				return err
			}
		case catalogdomain.CommitMaterialized:
			if err := i.ensureMaterialized(entry, commit); err != nil {
				return i.handleCommitError(leaseCtx, release, deterministicCatalogFailure(err))
			}
		case catalogdomain.CommitCommitted:
		default:
			return fmt.Errorf("catalog commit %q has unsupported state %s", commit.ID, commit.State)
		}
	}
	commits, err = i.store.Commits(leaseCtx, release.ID)
	if err != nil {
		return err
	}
	compiled, err := i.compileRevision(leaseCtx, source, release, entries, commits)
	if err != nil {
		return i.handleCommitError(leaseCtx, release, err)
	}
	_, err = i.store.CompleteReleaseCommit(leaseCtx, release, compiled, i.now().UTC())
	return err
}

func (i *Installer) handleCommitError(ctx context.Context, release catalogdomain.Release, err error) error {
	var artifact *execution.ArtifactError
	if errors.As(err, &artifact) || isDeterministicCatalogError(err) {
		if failErr := i.store.FailReleaseCommit(ctx, release, errorSummary(err), i.now().UTC()); failErr != nil {
			return failErr
		}
		return nil
	}
	return err
}

func (i *Installer) handleEntryError(ctx context.Context, claim catalogdomain.EntryClaim, err error) error {
	var artifact *execution.ArtifactError
	if errors.As(err, &artifact) {
		return i.failEntry(ctx, claim, err, artifact.Report)
	}
	return i.retryEntry(ctx, claim, err)
}

func (i *Installer) failEntry(ctx context.Context, claim catalogdomain.EntryClaim, err error, report *execution.VerificationReport) error {
	summary := errorSummary(err)
	if failErr := i.store.FailEntry(ctx, claim, summary, report, i.now().UTC()); failErr != nil {
		return failErr
	}
	return nil
}

func (i *Installer) retryEntry(ctx context.Context, claim catalogdomain.EntryClaim, err error) error {
	now := i.now().UTC()
	if !claim.Release.DeadlineAt.After(now) {
		return i.failEntry(ctx, claim, fmt.Errorf("catalog release deadline exceeded: %w", err), nil)
	}
	return i.store.RetryEntry(ctx, claim, errorSummary(err), catalogRetryAt(claim.Entry.Attempt, now), now)
}

func (i *Installer) entryLeaseContext(parent context.Context, claim catalogdomain.EntryClaim) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go i.renewEntryLease(ctx, cancel, claim)
	return ctx, cancel
}

func (i *Installer) renewEntryLease(ctx context.Context, cancel context.CancelFunc, claim catalogdomain.EntryClaim) {
	interval := i.leaseTTL / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := i.store.RenewEntryLease(ctx, claim, i.leaseTTL, i.now().UTC()); err != nil {
				cancel()
				return
			}
		}
	}
}

func (i *Installer) releaseLeaseContext(parent context.Context, release catalogdomain.Release) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go i.renewReleaseLease(ctx, cancel, release)
	return ctx, cancel
}

func (i *Installer) renewReleaseLease(ctx context.Context, cancel context.CancelFunc, release catalogdomain.Release) {
	interval := i.leaseTTL / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := i.store.RenewReleaseCommitLease(ctx, release, i.leaseTTL, i.now().UTC()); err != nil {
				cancel()
				return
			}
		}
	}
}

func (i *Installer) k8sBase(ctx context.Context, work execution.Work) ([]byte, error) {
	if work.Snapshot.Runtime != challenge.RuntimeK8s {
		return nil, nil
	}
	if work.Snapshot.K8s == nil {
		return nil, errors.New("catalog K8s execution has no runtime snapshot")
	}
	root, err := os.MkdirTemp("", "breakfix-catalog-base-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(root) }()
	archivePath := filepath.Join(root, "base.oci.tar")
	if err := i.puller.PullOCIArchive(ctx, work.Snapshot.K8s.BaseImageDigest, archivePath); err != nil {
		return nil, fmt.Errorf("pull trusted catalog K8s base: %w", err)
	}
	return os.ReadFile(archivePath)
}

func (i *Installer) stageSource(ctx context.Context, releaseID string) (*PortableSource, catalogdomain.ContentRevision, error) {
	root := i.releaseSourceRoot(releaseID)
	if _, err := os.Lstat(root); err == nil {
		source, digest, loadErr := loadSourceAt(root)
		if loadErr != nil {
			return nil, "", deterministicCatalogFailure(fmt.Errorf("read persisted catalog source: %w", loadErr))
		}
		return source, digest, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("inspect persisted catalog source: %w", err)
	}
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return nil, "", err
	}
	staging, err := os.MkdirTemp(parent, "."+releaseID+"-")
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	archive := filepath.Join(staging, "release.oci.tar")
	if err := i.puller.PullOCIArchive(ctx, i.reference, archive); err != nil {
		return nil, "", fmt.Errorf("pull configured catalog release: %w", err)
	}
	layer, err := i.layerReader.ReadSourceLayer(archive)
	if err != nil {
		return nil, "", deterministicCatalogFailure(fmt.Errorf("read catalog release source layer: %w", err))
	}
	sourceRoot := filepath.Join(staging, "source")
	if err := os.Mkdir(sourceRoot, 0o750); err != nil {
		return nil, "", err
	}
	if err := challenge.ExtractTarGz(sourceRoot, bytes.NewReader(layer)); err != nil {
		return nil, "", deterministicCatalogFailure(fmt.Errorf("extract catalog release source: %w", err))
	}
	_, digest, err := loadSourceAt(sourceRoot)
	if err != nil {
		return nil, "", deterministicCatalogFailure(err)
	}
	if err := os.Remove(archive); err != nil {
		return nil, "", err
	}
	if err := os.Rename(sourceRoot, root); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, "", fmt.Errorf("persist catalog release source: %w", err)
		}
		return loadSourceAt(root)
	}
	persisted, persistedDigest, err := loadSourceAt(root)
	if err != nil {
		return nil, "", deterministicCatalogFailure(fmt.Errorf("read persisted catalog source: %w", err))
	}
	if persistedDigest != digest {
		return nil, "", deterministicCatalogFailure(errors.New("persisted catalog source digest changed during promotion"))
	}
	return persisted, persistedDigest, nil
}

func (i *Installer) loadOrStageSource(ctx context.Context, release catalogdomain.Release) (*PortableSource, catalogdomain.ContentRevision, error) {
	root := i.releaseSourceRoot(release.ID)
	if _, err := os.Lstat(root); err == nil {
		source, digest, loadErr := loadSourceAt(root)
		if loadErr != nil {
			return nil, "", deterministicCatalogFailure(fmt.Errorf("read persisted catalog source: %w", loadErr))
		}
		return source, digest, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", fmt.Errorf("inspect persisted catalog source: %w", err)
	}
	return i.stageSource(ctx, release.ID)
}

func (i *Installer) releaseSourceRoot(releaseID string) string {
	return filepath.Join(i.dataDir, "catalog-releases", releaseID, "source")
}

func (i *Installer) materializeCommit(source *PortableSource, entry catalogdomain.Entry, commit catalogdomain.Commit) error {
	if err := i.ensureMaterialized(entry, commit); err == nil {
		return nil
	} else if !errors.Is(err, challenge.ErrNotFound) {
		return deterministicCatalogFailure(err)
	}
	if commit.Artifact == nil {
		return deterministicCatalogFailure(errors.New("catalog materialization has no final artifact"))
	}
	image, err := artifactImage(*commit.Artifact)
	if err != nil {
		return deterministicCatalogFailure(err)
	}
	sourceDir := filepath.Join(source.Root, filepath.FromSlash(entry.SourcePath))
	published, err := challenge.PromoteDirectoryAt(i.challengesDir, sourceDir, commit.ChallengeID, image, string(entry.ContentRevision), i.now().UTC())
	if err != nil {
		return fmt.Errorf("materialize catalog challenge %q: %w", entry.SourcePath, err)
	}
	if published.SourceSlug != commit.SourceSlug || published.ContentRevision != string(entry.ContentRevision) || published.Image != image {
		return deterministicCatalogFailure(errors.New("materialized catalog challenge does not match its commit intent"))
	}
	return nil
}

func (i *Installer) ensureMaterialized(entry catalogdomain.Entry, commit catalogdomain.Commit) error {
	if commit.Artifact == nil {
		return errors.New("catalog commit has no final artifact")
	}
	image, err := artifactImage(*commit.Artifact)
	if err != nil {
		return err
	}
	published, err := challenge.Get(i.challengesDir, commit.ChallengeID)
	if err != nil {
		return err
	}
	if published.SourceSlug != commit.SourceSlug || published.ContentRevision != string(entry.ContentRevision) || published.Image != image || published.Title != entry.Title {
		return errors.New("materialized catalog challenge conflicts with its durable commit")
	}
	return nil
}

func (i *Installer) compileRevision(ctx context.Context, source *PortableSource, release catalogdomain.Release, entries []catalogdomain.Entry, commits []catalogdomain.Commit) (roadmap.Revision, error) {
	current, err := i.roadmap.CurrentRoadmap(ctx)
	if err != nil && !errors.Is(err, roadmap.ErrNoCurrentRevision) {
		return roadmap.Revision{}, err
	}
	currentBySource := make(map[string]roadmap.ChallengeRef)
	if current != nil {
		for _, binding := range current.ChallengeBindings {
			currentBySource[binding.Challenge.SourceRef] = binding.Challenge
		}
	}
	entryByPath := make(map[string]catalogdomain.Entry, len(entries))
	for _, entry := range entries {
		entryByPath[entry.SourcePath] = entry
	}
	commitByEntry := make(map[string]catalogdomain.Commit, len(commits))
	for _, commit := range commits {
		commitByEntry[commit.EntryID] = commit
	}
	values := make(map[string]roadmap.ChallengeRef, len(source.Roadmap.ChallengeBindings))
	for _, binding := range source.Roadmap.ChallengeBindings {
		if existing, found := currentBySource[binding.Challenge.SourceRef]; found {
			values[binding.Challenge.Path] = existing
			continue
		}
		entry, found := entryByPath[binding.Challenge.Path]
		if !found {
			return roadmap.Revision{}, deterministicCatalogFailure(fmt.Errorf("roadmap binding %q has no catalog entry", binding.Challenge.Path))
		}
		commit, found := commitByEntry[entry.ID]
		if !found || commit.State != catalogdomain.CommitMaterialized {
			return roadmap.Revision{}, deterministicCatalogFailure(fmt.Errorf("roadmap binding %q has no materialized catalog commit", binding.Challenge.Path))
		}
		values[binding.Challenge.Path] = roadmap.ChallengeRef{
			ID: commit.ChallengeID, SourceRef: binding.Challenge.SourceRef, Title: binding.Challenge.Title,
			ContentRevision: binding.Challenge.ContentRevision,
		}
	}
	compiled, err := roadmap.CompilePortable(source.Roadmap, values)
	if err != nil {
		return roadmap.Revision{}, deterministicCatalogFailure(err)
	}
	return compiled, nil
}

func (i *Installer) validateAppendOnly(ctx context.Context, source *PortableSource) error {
	current, err := i.roadmap.CurrentRoadmap(ctx)
	if errors.Is(err, roadmap.ErrNoCurrentRevision) {
		return nil
	}
	if err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	domains := make(map[string]roadmap.PortableDomain, len(source.Roadmap.Domains))
	for _, value := range source.Roadmap.Domains {
		domains[value.SourceRef] = value
	}
	for _, value := range current.Domains {
		portable, exists := domains[value.SourceRef]
		if !exists || portable.Title != value.Title || portable.Definition != value.Definition || portable.Scope != value.Scope || portable.NonGoals != value.NonGoals {
			return fmt.Errorf("catalog release changes or removes installed domain %q", value.SourceRef)
		}
	}
	topics := make(map[string]roadmap.PortableTopic, len(source.Roadmap.Topics))
	for _, value := range source.Roadmap.Topics {
		topics[value.SourceRef] = value
	}
	for _, value := range current.Topics {
		portable, exists := topics[value.SourceRef]
		if !exists || portable.Title != value.Title || portable.Domain.SourceRef != value.Domain.SourceRef || portable.Domain.Title != value.Domain.Title ||
			portable.Definition != value.Definition || portable.Scope != value.Scope || portable.NonGoals != value.NonGoals || portable.ChallengeGuidance != value.ChallengeGuidance {
			return fmt.Errorf("catalog release changes or removes installed topic %q", value.SourceRef)
		}
	}
	tags := make(map[string]roadmap.PortableTag, len(source.Roadmap.Tags))
	for _, value := range source.Roadmap.Tags {
		tags[value.SourceRef] = value
	}
	for _, value := range current.Tags {
		portable, exists := tags[value.SourceRef]
		if !exists || portable.Title != value.Title || portable.Description != value.Description {
			return fmt.Errorf("catalog release changes or removes installed tag %q", value.SourceRef)
		}
	}
	bindings := portableBindingsBySourceRef(source.Roadmap)
	for _, value := range current.ChallengeBindings {
		portable, exists := bindings[value.Challenge.SourceRef]
		if !exists || portable.Challenge.Title != value.Challenge.Title || portable.Challenge.ContentRevision != value.Challenge.ContentRevision ||
			portable.Topic.SourceRef != value.Topic.SourceRef || portable.Topic.Title != value.Topic.Title || !samePortableTags(portable.Tags, value.Tags) {
			return fmt.Errorf("catalog release changes or removes installed challenge %q", value.Challenge.SourceRef)
		}
	}
	if err := requireExistingEdges(current.TopicEdges, source.Roadmap.TopicEdges); err != nil {
		return fmt.Errorf("catalog topic graph: %w", err)
	}
	if err := requireExistingEdges(current.ChallengeEdges, source.Roadmap.ChallengeEdges); err != nil {
		return fmt.Errorf("catalog challenge graph: %w", err)
	}
	return nil
}

func portableBindingsByPath(value roadmap.PortableRevision) map[string]roadmap.PortableChallengeBinding {
	result := make(map[string]roadmap.PortableChallengeBinding, len(value.ChallengeBindings))
	for _, binding := range value.ChallengeBindings {
		result[binding.Challenge.Path] = binding
	}
	return result
}

func portableBindingsBySourceRef(value roadmap.PortableRevision) map[string]roadmap.PortableChallengeBinding {
	result := make(map[string]roadmap.PortableChallengeBinding, len(value.ChallengeBindings))
	for _, binding := range value.ChallengeBindings {
		result[binding.Challenge.SourceRef] = binding
	}
	return result
}

func samePortableTags(portable []roadmap.PortableRef, runtime []roadmap.Ref) bool {
	if len(portable) != len(runtime) {
		return false
	}
	left := append([]roadmap.PortableRef(nil), portable...)
	right := append([]roadmap.Ref(nil), runtime...)
	slices.SortFunc(left, func(a, b roadmap.PortableRef) int { return strings.Compare(a.SourceRef, b.SourceRef) })
	slices.SortFunc(right, func(a, b roadmap.Ref) int { return strings.Compare(a.SourceRef, b.SourceRef) })
	for index := range left {
		if left[index].SourceRef != right[index].SourceRef || left[index].Title != right[index].Title {
			return false
		}
	}
	return true
}

func requireExistingEdges(existing []roadmap.Edge, candidate []roadmap.PortableEdge) error {
	values := make(map[string]struct{}, len(candidate))
	for _, edge := range candidate {
		values[portableEdgeKey(edge.Source.SourceRef, edge.Target.SourceRef, edge.Relation, edge.Reason)] = struct{}{}
	}
	for _, edge := range existing {
		if _, exists := values[portableEdgeKey(edge.Source.SourceRef, edge.Target.SourceRef, edge.Relation, edge.Reason)]; !exists {
			return fmt.Errorf("changes or removes edge %q -> %q", edge.Source.SourceRef, edge.Target.SourceRef)
		}
	}
	return nil
}

func portableEdgeKey(source, target string, relation roadmap.Relation, reason string) string {
	return source + "\x00" + target + "\x00" + string(relation) + "\x00" + reason
}

func sourceArchive(source *PortableSource, entry catalogdomain.Entry) ([]byte, error) {
	archive, err := archiveSourceCandidate(filepath.Join(source.Root, filepath.FromSlash(entry.SourcePath)))
	if err != nil {
		return nil, err
	}
	if candidate.Digest(archive) != entry.ArchiveSHA256 {
		return nil, errors.New("catalog source archive digest does not match durable entry")
	}
	return archive, nil
}

func archiveSourceCandidate(root string) ([]byte, error) {
	files, err := readSourceFiles(root)
	if err != nil {
		return nil, err
	}
	return writeSourceLayer(files)
}

func loadSourceAt(root string) (*PortableSource, catalogdomain.ContentRevision, error) {
	source, err := LoadPortableSource(root)
	if err != nil {
		return nil, "", err
	}
	digest, err := ContentRevision(root)
	if err != nil {
		return nil, "", err
	}
	return source, digest, nil
}

func bundleDigest(reference string) (catalogdomain.BundleDigest, error) {
	_, value, found := strings.Cut(strings.TrimSpace(reference), "@")
	if !found || !catalogdomain.BundleDigest(value).Valid() {
		return "", errors.New("catalog release must use an immutable OCI digest reference")
	}
	return catalogdomain.BundleDigest(value), nil
}

func artifactImage(value execution.ArtifactReference) (string, error) {
	if err := value.Validate(value.Runtime); err != nil {
		return "", err
	}
	switch value.Runtime {
	case challenge.RuntimeNode:
		return value.IncusFingerprint, nil
	case challenge.RuntimeK8s:
		return value.OCIReference, nil
	default:
		return "", errors.New("catalog artifact has unsupported runtime")
	}
}

func catalogRetryAt(attempt int, now time.Time) time.Time {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second * time.Duration(1<<min(attempt-1, 6))
	if delay > time.Minute {
		delay = time.Minute
	}
	return now.Add(delay)
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func errorSummary(err error) string {
	if err == nil || strings.TrimSpace(err.Error()) == "" {
		return "catalog execution failed"
	}
	return strings.TrimSpace(err.Error())
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
