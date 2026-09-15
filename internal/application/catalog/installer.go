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
	appoperations "github.com/breakfix/breakfix/internal/application/operations"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/scenario"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/publication"
	"github.com/breakfix/breakfix/internal/domain/runnable"
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

// ReleaseStore is the complete durable boundary for a Catalog install. It is
// intentionally separate from Generation repositories. Runtime Worker owns
// all provider work; Server only stages source and completes finalization.
type ReleaseStore interface {
	CreateOrGetRelease(context.Context, catalogdomain.Release) (*catalogdomain.Release, bool, error)
	InitializeRelease(context.Context, catalogdomain.Release, []catalogdomain.Entry, time.Time) (*catalogdomain.Release, error)
	ReportSourceInfrastructureFailure(context.Context, string, string, time.Time) (*catalogdomain.Release, error)
	FailPendingRelease(context.Context, string, string, time.Time) (*catalogdomain.Release, error)
	FailRelease(context.Context, string, string, time.Time) (*catalogdomain.Release, error)
	RecordCatalogFinalizerFailure(context.Context, string, publication.Diagnostic) (*catalogdomain.Release, error)
	CatalogBootstrapState(context.Context) (catalogdomain.BootstrapState, error)
	EnsureCatalogResourceReaps(context.Context, time.Time) error
	Entries(context.Context, string) ([]catalogdomain.Entry, error)
	PrepareReleaseCommit(context.Context, string, []catalogdomain.Commit, time.Time) (*catalogdomain.Release, []catalogdomain.Commit, error)
	Commits(context.Context, string) ([]catalogdomain.Commit, error)
	MarkCommitMaterialized(context.Context, string, string, string, time.Time) (*catalogdomain.Commit, error)
	CompleteReleaseCommit(context.Context, string, time.Time) (*catalogdomain.Release, error)
	MarkCatalogEntryVerified(context.Context, string, time.Time) error
	RecordCatalogCommitArtifact(context.Context, string, string, execution.ArtifactReference, time.Time) error
}

// RunnableStore is the only runtime boundary used during Catalog installation.
// Catalog owns source and publication state; the public Worker owns every
// provider action and returns immutable values through this interface.
type RunnableStore interface {
	StoreRunnableSource(context.Context, runnable.SourceArchive, []byte, time.Time) error
	ScheduleMaterialization(context.Context, runnable.RunnableSpec, int64, time.Time) (runnable.ActionIdentity, error)
	ResolveMaterializedRunnableRevision(context.Context, runnable.ActionIdentity) (runnable.RevisionReference, error)
	ScheduleVerification(context.Context, runnable.RevisionReference, int64, time.Time) (runnable.ActionIdentity, error)
	ResolveVerificationForAction(context.Context, runnable.ActionIdentity) (runnable.StoredVerificationReport, error)
}

type InstallerConfig struct {
	DataDir          string
	ScenariosDir     string
	ReleaseReference string
	PollInterval     time.Duration
	Snapshot         appexecution.SnapshotConfig
	Puller           BundlePuller
	LayerReader      SourceLayerReader
	Store            ReleaseStore
	Runnable         RunnableStore
	Operations       appoperations.Config
}

// Installer is a Server-owned coordinator for configured Catalog Releases.
// It runs no admin API and no GenerationWorkflow; its durable state is wholly
// represented by Catalog Release, Entry, and Commit records.
type Installer struct {
	dataDir      string
	scenariosDir string
	reference    string
	digest       catalogdomain.BundleDigest
	pollInterval time.Duration
	snapshot     appexecution.SnapshotConfig
	puller       BundlePuller
	layerReader  SourceLayerReader
	store        ReleaseStore
	runnable     RunnableStore
	operations   appoperations.Config
	now          func() time.Time
	sleep        func(context.Context, time.Duration) error
	mu           sync.Mutex
}

func deterministicCatalogFailure(err error) error {
	return publication.Deterministic(err)
}

func isDeterministicCatalogError(err error) bool {
	return publication.IsDeterministic(err)
}

// Unclassified finalizer errors are operational by default. Content and
// invariant checks mark their own errors deterministic before reaching this
// boundary.
func catalogFinalizerFailure(err error) error {
	if err == nil || publication.CategoryOf(err).Valid() {
		return err
	}
	return publication.Transient(err)
}

func catalogContentFailure(err error) error {
	if err == nil || publication.CategoryOf(err).Valid() {
		return err
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return publication.Transient(err)
	}
	return publication.Deterministic(err)
}

func NewInstaller(config InstallerConfig) (*Installer, error) {
	if strings.TrimSpace(config.DataDir) == "" || strings.TrimSpace(config.ScenariosDir) == "" ||
		config.PollInterval <= 0 || config.Puller == nil || config.LayerReader == nil || config.Store == nil {
		return nil, errors.New("catalog installer requires data paths, source adapters, and a durable store")
	}
	digest, err := catalogdomain.BundleDigestFromReference(config.ReleaseReference)
	if err != nil {
		return nil, err
	}
	return &Installer{
		dataDir: filepath.Clean(config.DataDir), scenariosDir: filepath.Clean(config.ScenariosDir), reference: strings.TrimSpace(config.ReleaseReference),
		digest: digest, pollInterval: config.PollInterval,
		snapshot: config.Snapshot, puller: config.Puller, layerReader: config.LayerReader, store: config.Store,
		runnable: config.Runnable, operations: config.Operations,
		now: func() time.Time { return time.Now().UTC() }, sleep: sleepContext,
	}, nil
}

var ErrBootstrapConflict = errors.New("catalog baseline bootstrap conflicts with durable platform state")

// ValidateBootstrap rejects configuration changes that would reinterpret an
// existing baseline or import a baseline into an authoring-created platform.
// Failed empty-platform attempts are allowed here so Server and Runtime Worker
// can finish their cleanup before a replacement digest starts.
func (i *Installer) ValidateBootstrap(ctx context.Context) error {
	state, err := i.store.CatalogBootstrapState(ctx)
	if err != nil {
		return err
	}
	_, _, err = selectBootstrapRelease(state, i.digest)
	return err
}

func selectBootstrapRelease(state catalogdomain.BootstrapState, digest catalogdomain.BundleDigest) (*catalogdomain.Release, bool, error) {
	var ready []*catalogdomain.Release
	var active []*catalogdomain.Release
	var configured *catalogdomain.Release
	for index := range state.Releases {
		release := &state.Releases[index]
		if release.BundleDigest == digest {
			configured = release
		}
		switch release.State {
		case catalogdomain.ReleaseReady:
			ready = append(ready, release)
		case catalogdomain.ReleasePending, catalogdomain.ReleaseInstalling, catalogdomain.ReleaseCommitting:
			active = append(active, release)
		}
	}
	if len(ready) > 1 {
		return nil, false, fmt.Errorf("%w: multiple Ready Catalog baselines exist", ErrBootstrapConflict)
	}
	if len(ready) == 1 {
		if ready[0].BundleDigest != digest {
			return nil, false, fmt.Errorf("%w: configured digest %s differs from Ready baseline %s", ErrBootstrapConflict, digest, ready[0].BundleDigest)
		}
		if len(active) != 0 {
			return nil, false, fmt.Errorf("%w: an active release exists after the baseline became Ready", ErrBootstrapConflict)
		}
		return ready[0], false, nil
	}
	if state.PublishedScenarioCount != 0 {
		return nil, false, fmt.Errorf("%w: %d scenarios were published without a Ready Catalog baseline", ErrBootstrapConflict, state.PublishedScenarioCount)
	}
	if len(active) > 1 {
		return nil, false, fmt.Errorf("%w: multiple Catalog bootstrap attempts are active", ErrBootstrapConflict)
	}
	if len(active) == 1 {
		if active[0].BundleDigest != digest {
			return nil, false, fmt.Errorf("%w: configured digest %s differs from active bootstrap %s", ErrBootstrapConflict, digest, active[0].BundleDigest)
		}
		return active[0], false, nil
	}
	if configured != nil {
		return configured, false, nil
	}
	return nil, state.FailedCleanupPending, nil
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
				return ctx.Err()
			}
			return err
		}
	}
}

// RunOnce is exposed for deterministic tests and local bootstrap. Runtime
// states are intentionally not advanced here: Runtime Worker claims them
// through the action-scoped Server API. This coordinator only stages source,
// creates commit intents, and resumes Server-owned finalization.
func (i *Installer) RunOnce(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	release, source, err := i.ensureRelease(ctx)
	if err != nil {
		return err
	}
	if release == nil {
		return nil
	}
	if release.State == catalogdomain.ReleaseReady || release.State == catalogdomain.ReleaseFailed {
		if err := i.removeTerminalSource(release.ID); err != nil {
			return err
		}
		return nil
	}
	if source == nil {
		return nil
	}
	if release.State == catalogdomain.ReleaseInstalling {
		if err := i.advanceEntries(ctx, source, *release); err != nil {
			return err
		}
		return i.prepareCommit(ctx, release)
	}
	if release.State == catalogdomain.ReleaseCommitting {
		return i.finalizeCommit(ctx, source, *release)
	}
	return fmt.Errorf("catalog release %q has unsupported state %s", release.ID, release.State)
}

func (i *Installer) advanceEntries(ctx context.Context, source *PortableSource, release catalogdomain.Release) error {
	if i.runnable == nil {
		return errors.New("catalog installer requires a public runnable store")
	}
	entries, err := i.store.Entries(ctx, release.ID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.State == catalogdomain.EntryReadyToCommit {
			continue
		}
		if entry.State != catalogdomain.EntryBuilding {
			return fmt.Errorf("catalog entry %q has unsupported public state %s", entry.ID, entry.State)
		}
		archive, spec, err := i.compileEntry(source, entry)
		if err != nil {
			return err
		}
		now := i.now().UTC()
		if err := i.runnable.StoreRunnableSource(ctx, spec.Source, archive, now); err != nil {
			return err
		}
		materialize, err := i.runnable.ScheduleMaterialization(ctx, spec, 1, now)
		if err != nil {
			return err
		}
		revision, err := i.runnable.ResolveMaterializedRunnableRevision(ctx, materialize)
		if errors.Is(err, runnable.ErrMaterializationNotReady) {
			continue
		}
		if err != nil {
			return err
		}
		verify, err := i.runnable.ScheduleVerification(ctx, revision, 1, now)
		if err != nil {
			return err
		}
		report, err := i.runnable.ResolveVerificationForAction(ctx, verify)
		if errors.Is(err, runnable.ErrMaterializationNotReady) {
			continue
		}
		if err != nil {
			return err
		}
		if !report.Report.Passed {
			_, err = i.store.FailRelease(ctx, release.ID, "catalog runnable verification did not pass", now)
			return err
		}
		if err := i.store.MarkCatalogEntryVerified(ctx, entry.ID, now); err != nil {
			return err
		}
	}
	return nil
}

func (i *Installer) ensureRelease(ctx context.Context) (*catalogdomain.Release, *PortableSource, error) {
	now := i.now().UTC()
	if err := i.store.EnsureCatalogResourceReaps(ctx, now); err != nil {
		return nil, nil, err
	}
	state, err := i.store.CatalogBootstrapState(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, failed := range state.Releases {
		if failed.State == catalogdomain.ReleaseFailed {
			if err := i.removeTerminalSource(failed.ID); err != nil {
				return nil, nil, err
			}
		}
	}
	release, waitForCleanup, err := selectBootstrapRelease(state, i.digest)
	if err != nil {
		return nil, nil, err
	}
	if waitForCleanup {
		return nil, nil, nil
	}
	if release == nil {
		id := catalogdomain.ReleaseIDForBundle(i.digest)
		seed := catalogdomain.Release{
			ID: id, BundleDigest: i.digest, State: catalogdomain.ReleasePending,
			SourceAttempt: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
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
		if release.NextRunAt.After(now) {
			return release, nil, nil
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
			updated, retryErr := i.store.ReportSourceInfrastructureFailure(ctx, release.ID, errorSummary(stageErr), now)
			if retryErr != nil {
				return nil, nil, retryErr
			}
			return updated, nil, nil
		}
		entries, err := i.newEntries(release.ID, source)
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
	if release.FinalizerNextRetryAt != nil && release.FinalizerNextRetryAt.After(i.now().UTC()) {
		return release, nil, nil
	}
	source, sourceDigest, err := i.loadOrStageSource(ctx, *release)
	if err != nil {
		updated, recordErr := i.recordFinalizerError(ctx, *release, catalogFinalizerFailure(err))
		if recordErr != nil {
			return nil, nil, recordErr
		}
		return updated, nil, nil
	}
	if sourceDigest != release.SourceDigest {
		updated, recordErr := i.recordFinalizerError(ctx, *release, deterministicCatalogFailure(errors.New("staged catalog source digest does not match its durable release")))
		if recordErr != nil {
			return nil, nil, recordErr
		}
		return updated, nil, nil
	}
	return release, source, nil
}

func (i *Installer) newEntries(releaseID string, source *PortableSource) ([]catalogdomain.Entry, error) {
	now := i.now().UTC()
	entries := make([]catalogdomain.Entry, 0, len(source.Scenarios))
	for _, sourceScenario := range source.Scenarios {
		sourceRef, err := sourceReference(sourceScenario.Path)
		if err != nil {
			return nil, err
		}
		archive, err := archiveSourceCandidate(filepath.Join(source.Root, filepath.FromSlash(sourceScenario.Path)))
		if err != nil {
			return nil, fmt.Errorf("archive catalog source %q: %w", sourceScenario.Path, err)
		}
		snapshot, err := appexecution.Freeze(sourceScenario.Entry, i.snapshot)
		if err != nil {
			return nil, fmt.Errorf("freeze catalog source %q: %w", sourceScenario.Path, err)
		}
		entries = append(entries, catalogdomain.Entry{
			ID: catalogdomain.EntryIDFor(releaseID, sourceScenario.Path), ReleaseID: releaseID, SourcePath: sourceScenario.Path,
			SourceRef: sourceRef, Title: sourceScenario.Entry.Title, Type: sourceScenario.Entry.Type, Tags: append([]string(nil), sourceScenario.Entry.Tags...), ContentRevision: sourceScenario.ContentRevision,
			ArchiveSHA256: candidate.Digest(archive), Snapshot: snapshot, State: catalogdomain.EntryBuilding,
			StateVersion: 1, RuntimeAttempt: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
		})
	}
	return entries, nil
}

func sourceReference(sourcePath string) (string, error) {
	const scenarioPrefix = scenarioSourcesDirname + "/"
	if !strings.HasPrefix(sourcePath, scenarioPrefix) {
		return "", fmt.Errorf("catalog source path %q is not a scenario source", sourcePath)
	}
	value := strings.TrimPrefix(sourcePath, scenarioPrefix)
	if value == "" {
		return "", fmt.Errorf("catalog source path %q has an empty source reference", sourcePath)
	}
	return value, nil
}

func (i *Installer) prepareCommit(ctx context.Context, release *catalogdomain.Release) error {
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
		scenarioID := scenario.NewID()
		scenarioRevisionID := scenario.NewRevisionID()
		intents = append(intents, catalogdomain.Commit{
			ID: catalogdomain.EntryCommitIDFor(release.ID, entry.ID), ReleaseID: release.ID, EntryID: entry.ID,
			ScenarioID: scenarioID, ScenarioRevisionID: scenarioRevisionID, SourceSlug: scenario.SourceSlugFor(entry.Title, scenarioID),
			State: catalogdomain.CommitPrepared, StateVersion: 1, RuntimeAttempt: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
		})
	}
	_, _, err = i.store.PrepareReleaseCommit(ctx, release.ID, intents, now)
	return err
}

func (i *Installer) finalizeCommit(ctx context.Context, source *PortableSource, release catalogdomain.Release) error {
	entries, err := i.store.Entries(ctx, release.ID)
	if err != nil {
		return i.handleFinalizerError(ctx, release, catalogFinalizerFailure(err))
	}
	byID := make(map[string]catalogdomain.Entry, len(entries))
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	commits, err := i.store.Commits(ctx, release.ID)
	if err != nil {
		return i.handleFinalizerError(ctx, release, catalogFinalizerFailure(err))
	}
	allMaterialized := true
	for _, commit := range commits {
		entry, exists := byID[commit.EntryID]
		if !exists {
			return i.handleFinalizerError(ctx, release, deterministicCatalogFailure(fmt.Errorf("catalog commit %q has no entry", commit.ID)))
		}
		switch commit.State {
		case catalogdomain.CommitPrepared:
			revision, err := i.resolveEntryRevision(ctx, source, entry)
			if err != nil {
				return i.handleFinalizerError(ctx, release, err)
			}
			artifact, err := catalogArtifact(revision.Artifact)
			if err != nil {
				return i.handleFinalizerError(ctx, release, deterministicCatalogFailure(err))
			}
			if err := i.store.RecordCatalogCommitArtifact(ctx, release.ID, commit.ID, artifact, i.now().UTC()); err != nil {
				return i.handleFinalizerError(ctx, release, catalogFinalizerFailure(err))
			}
			allMaterialized = false
			continue
		case catalogdomain.CommitArtifactPublished:
			materialized, err := i.materializeCommit(source, entry, commit)
			if err != nil {
				return i.handleFinalizerError(ctx, release, err)
			}
			// The materialized manifest is the immutable source that the active
			// revision will later validate. Persist its exact publication time,
			// rather than taking a second clock reading for the commit transition.
			if _, err := i.store.MarkCommitMaterialized(ctx, release.ID, commit.ID, materialized.Revision, materialized.PublishedAt.UTC()); err != nil {
				return i.handleFinalizerError(ctx, release, catalogFinalizerFailure(err))
			}
			allMaterialized = false
		case catalogdomain.CommitMaterialized:
			if _, err := i.ensureMaterialized(entry, commit); err != nil {
				return i.handleFinalizerError(ctx, release, catalogFinalizerFailure(err))
			}
		case catalogdomain.CommitCommitted:
		case catalogdomain.CommitFailed:
			return nil
		default:
			return i.handleFinalizerError(ctx, release, deterministicCatalogFailure(fmt.Errorf("catalog commit %q has unsupported state %s", commit.ID, commit.State)))
		}
	}
	if !allMaterialized {
		return nil
	}
	commits, err = i.store.Commits(ctx, release.ID)
	if err != nil {
		return i.handleFinalizerError(ctx, release, catalogFinalizerFailure(err))
	}
	_, err = i.store.CompleteReleaseCommit(ctx, release.ID, i.now().UTC())
	if err != nil {
		if errors.Is(err, catalogdomain.ErrBaselineEstablished) {
			return i.handleFinalizerError(ctx, release, deterministicCatalogFailure(err))
		}
		return i.handleFinalizerError(ctx, release, catalogFinalizerFailure(err))
	}
	return nil
}

func (i *Installer) resolveEntryRevision(ctx context.Context, source *PortableSource, entry catalogdomain.Entry) (runnable.RunnableRevision, error) {
	if i.runnable == nil {
		return runnable.RunnableRevision{}, errors.New("catalog runnable store is unavailable")
	}
	_, spec, err := i.compileEntry(source, entry)
	if err != nil {
		return runnable.RunnableRevision{}, err
	}
	identity, err := i.runnable.ScheduleMaterialization(ctx, spec, 1, i.now().UTC())
	if err != nil {
		return runnable.RunnableRevision{}, err
	}
	reference, err := i.runnable.ResolveMaterializedRunnableRevision(ctx, identity)
	if err != nil {
		return runnable.RunnableRevision{}, err
	}
	// The immutable revision is returned by the public action store only after
	// its digest binding has been checked there. Catalog needs its artifact
	// projection solely to write the Operations manifest.
	if resolver, ok := i.runnable.(interface {
		ResolveRunnableRevision(context.Context, string, string) (runnable.RunnableRevision, error)
	}); ok {
		return resolver.ResolveRunnableRevision(ctx, reference.ID, reference.Digest)
	}
	return runnable.RunnableRevision{}, errors.New("catalog runnable store cannot resolve revisions")
}

func (i *Installer) compileEntry(source *PortableSource, entry catalogdomain.Entry) ([]byte, runnable.RunnableSpec, error) {
	root := filepath.Join(source.Root, filepath.FromSlash(entry.SourcePath))
	candidateEntry, err := scenario.ValidateCandidateDir(root)
	if err != nil {
		return nil, runnable.RunnableSpec{}, err
	}
	publicSource, archive, err := appoperations.BuildSourceArchive(root)
	if err != nil {
		return nil, runnable.RunnableSpec{}, err
	}
	spec, err := appoperations.Compile(appoperations.Input{ContentID: entry.ID, ContentRevision: string(entry.ContentRevision), Entry: *candidateEntry, Source: publicSource}, i.operations)
	if err != nil {
		return nil, runnable.RunnableSpec{}, err
	}
	return archive, spec, nil
}

func catalogArtifact(value runnable.ArtifactReference) (execution.ArtifactReference, error) {
	if err := value.Validate(); err != nil {
		return execution.ArtifactReference{}, err
	}
	switch value.Runtime {
	case runnable.RuntimeNode:
		alias, digest, found := strings.Cut(strings.TrimPrefix(value.ProviderReference, "incus://"), "@")
		if !found || strings.TrimSpace(alias) == "" || digest != value.ArtifactDigest {
			return execution.ArtifactReference{}, errors.New("catalog Node artifact provider reference is invalid")
		}
		return execution.ArtifactReference{Runtime: scenario.RuntimeNode, IncusAlias: alias, IncusFingerprint: strings.TrimPrefix(digest, "sha256:")}, nil
	case runnable.RuntimeK8s:
		return execution.ArtifactReference{Runtime: scenario.RuntimeK8s, OCIReference: value.ProviderReference}, nil
	default:
		return execution.ArtifactReference{}, errors.New("catalog runnable artifact has unsupported runtime")
	}
}

func (i *Installer) handleFinalizerError(ctx context.Context, release catalogdomain.Release, err error) error {
	_, recordErr := i.recordFinalizerError(ctx, release, catalogFinalizerFailure(err))
	return recordErr
}

func (i *Installer) recordFinalizerError(ctx context.Context, release catalogdomain.Release, err error) (*catalogdomain.Release, error) {
	diagnostic, diagnosticErr := publication.NewDiagnostic(err, i.now().UTC())
	if diagnosticErr != nil {
		return nil, diagnosticErr
	}
	updated, err := i.store.RecordCatalogFinalizerFailure(ctx, release.ID, diagnostic)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func (i *Installer) stageSource(ctx context.Context, releaseID string) (*PortableSource, catalogdomain.ContentRevision, error) {
	root := i.releaseSourceRoot(releaseID)
	if _, err := os.Lstat(root); err == nil {
		source, digest, loadErr := loadSourceAt(root)
		if loadErr != nil {
			return nil, "", catalogContentFailure(fmt.Errorf("read persisted catalog source: %w", loadErr))
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
	if err := scenario.ExtractTarGz(sourceRoot, bytes.NewReader(layer)); err != nil {
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
			return nil, "", catalogContentFailure(fmt.Errorf("read persisted catalog source: %w", loadErr))
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

// ReadStagedEntryArchive is the Server-only source bridge for one claimed
// Catalog Entry build action. The Runtime Worker receives only the verified
// bytes and digest through the internal API, never the Server data volume.
func ReadStagedEntryArchive(dataDir, releaseID string, entry catalogdomain.Entry) ([]byte, error) {
	if strings.TrimSpace(dataDir) == "" || strings.TrimSpace(releaseID) == "" || entry.ReleaseID != releaseID {
		return nil, errors.New("catalog staged entry archive request is invalid")
	}
	root := filepath.Join(filepath.Clean(dataDir), "catalog-releases", releaseID, "source")
	source, _, err := loadSourceAt(root)
	if err != nil {
		return nil, fmt.Errorf("load staged catalog source: %w", err)
	}
	archive, err := sourceArchive(source, entry)
	if err != nil {
		return nil, fmt.Errorf("archive staged catalog entry: %w", err)
	}
	return archive, nil
}

func (i *Installer) removeTerminalSource(releaseID string) error {
	root := filepath.Dir(i.releaseSourceRoot(releaseID))
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("remove terminal catalog source: %w", err)
	}
	return nil
}

func (i *Installer) materializeCommit(source *PortableSource, entry catalogdomain.Entry, commit catalogdomain.Commit) (*scenario.Entry, error) {
	if published, err := i.ensureMaterialized(entry, commit); err == nil {
		return published, nil
	} else if !errors.Is(err, scenario.ErrNotFound) {
		return nil, catalogFinalizerFailure(err)
	}
	if commit.Artifact == nil {
		return nil, deterministicCatalogFailure(errors.New("catalog materialization has no final artifact"))
	}
	image, err := artifactImage(*commit.Artifact)
	if err != nil {
		return nil, deterministicCatalogFailure(err)
	}
	sourceDir := filepath.Join(source.Root, filepath.FromSlash(entry.SourcePath))
	if _, err := scenario.ValidateCandidateDir(sourceDir); err != nil {
		return nil, catalogContentFailure(fmt.Errorf("validate catalog scenario %q: %w", entry.SourcePath, err))
	}
	// PostgreSQL timestamps preserve microseconds. Using that precision in the
	// immutable manifest keeps its published_at identical to the durable commit
	// timestamp after a restart and catalog integrity check.
	publishedAt := i.now().UTC().Truncate(time.Microsecond)
	published, err := scenario.PromoteDirectoryAt(i.scenariosDir, sourceDir, commit.ScenarioID, commit.ScenarioRevisionID, commit.SourceSlug, image, string(entry.ContentRevision), publishedAt)
	if err != nil {
		return nil, catalogFinalizerFailure(fmt.Errorf("materialize catalog scenario %q: %w", entry.SourcePath, err))
	}
	if err := validateMaterializedCommit(entry, commit, published, image); err != nil {
		return nil, err
	}
	return published, nil
}

func (i *Installer) ensureMaterialized(entry catalogdomain.Entry, commit catalogdomain.Commit) (*scenario.Entry, error) {
	if commit.Artifact == nil {
		return nil, deterministicCatalogFailure(errors.New("catalog commit has no final artifact"))
	}
	image, err := artifactImage(*commit.Artifact)
	if err != nil {
		return nil, deterministicCatalogFailure(err)
	}
	target := filepath.Join(i.scenariosDir, scenario.MaterializedPath(commit.SourceSlug, commit.ScenarioRevisionID))
	if _, err := os.Lstat(target); err != nil {
		if os.IsNotExist(err) {
			return nil, scenario.ErrNotFound
		}
		return nil, catalogFinalizerFailure(err)
	}
	published, err := scenario.ValidateDir(target)
	if err != nil {
		return nil, catalogContentFailure(fmt.Errorf("validate materialized catalog scenario: %w", err))
	}
	if err := validateMaterializedCommit(entry, commit, published, image); err != nil {
		return nil, err
	}
	return published, nil
}

func validateMaterializedCommit(entry catalogdomain.Entry, commit catalogdomain.Commit, published *scenario.Entry, image string) error {
	if published == nil || published.ID != commit.ScenarioID || published.RevisionID != commit.ScenarioRevisionID || published.SourceSlug != commit.SourceSlug ||
		published.ContentRevision != string(entry.ContentRevision) || published.Image != image || published.Title != entry.Title ||
		published.Runtime != entry.Snapshot.Runtime || published.Type != entry.Type || !slices.Equal(published.Tags, entry.Tags) ||
		(commit.MaterializedRevision != "" && published.Revision != commit.MaterializedRevision) {
		return deterministicCatalogFailure(errors.New("materialized catalog scenario conflicts with its durable commit"))
	}
	return nil
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

func artifactImage(value execution.ArtifactReference) (string, error) {
	if err := value.Validate(value.Runtime); err != nil {
		return "", err
	}
	switch value.Runtime {
	case scenario.RuntimeNode:
		return value.IncusFingerprint, nil
	case scenario.RuntimeK8s:
		return value.OCIReference, nil
	default:
		return "", errors.New("catalog artifact has unsupported runtime")
	}
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
