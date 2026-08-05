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

// ReleaseStore is the complete durable boundary for a Catalog install. It is
// intentionally separate from Generation repositories. Runtime Worker owns
// all provider work; Server only stages source and completes finalization.
type ReleaseStore interface {
	CreateOrGetRelease(context.Context, catalogdomain.Release) (*catalogdomain.Release, bool, error)
	InitializeRelease(context.Context, catalogdomain.Release, []catalogdomain.Entry, time.Time) (*catalogdomain.Release, error)
	ReportSourceInfrastructureFailure(context.Context, string, string, time.Time) (*catalogdomain.Release, error)
	FailPendingRelease(context.Context, string, string, time.Time) (*catalogdomain.Release, error)
	FailRelease(context.Context, string, string, time.Time) (*catalogdomain.Release, error)
	ReleaseByDigest(context.Context, catalogdomain.BundleDigest) (*catalogdomain.Release, error)
	Entries(context.Context, string) ([]catalogdomain.Entry, error)
	InstalledEntries(context.Context) ([]catalogdomain.Entry, error)
	PrepareReleaseCommit(context.Context, string, []catalogdomain.Commit, time.Time) (*catalogdomain.Release, []catalogdomain.Commit, error)
	Commits(context.Context, string) ([]catalogdomain.Commit, error)
	MarkCommitMaterialized(context.Context, string, string, time.Time) (*catalogdomain.Commit, error)
	CompleteReleaseCommit(context.Context, string, roadmap.Revision, time.Time) (*catalogdomain.Release, error)
}

type RoadmapReader interface {
	CurrentRoadmap(context.Context) (*roadmap.Revision, error)
}

type InstallerConfig struct {
	DataDir          string
	ChallengesDir    string
	ReleaseReference string
	PollInterval     time.Duration
	Snapshot         appexecution.SnapshotConfig
	Puller           BundlePuller
	LayerReader      SourceLayerReader
	Store            ReleaseStore
	Roadmap          RoadmapReader
}

// Installer is a Server-owned coordinator for configured Catalog Releases.
// It runs no admin API and no GenerationWorkflow; its durable state is wholly
// represented by Catalog Release, Entry, and Commit records.
type Installer struct {
	dataDir       string
	challengesDir string
	reference     string
	pollInterval  time.Duration
	snapshot      appexecution.SnapshotConfig
	puller        BundlePuller
	layerReader   SourceLayerReader
	store         ReleaseStore
	roadmap       RoadmapReader
	now           func() time.Time
	sleep         func(context.Context, time.Duration) error
	mu            sync.Mutex
}

// deterministicCatalogError marks malformed immutable content. Transport,
// Registry availability and local storage errors remain ordinary errors so a
// Pending release can consume its own bounded source retry budget.
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
		config.PollInterval <= 0 || config.Puller == nil || config.LayerReader == nil || config.Store == nil || config.Roadmap == nil {
		return nil, errors.New("catalog installer requires data paths, source adapters, durable store, and roadmap reader")
	}
	if _, err := catalogdomain.BundleDigestFromReference(config.ReleaseReference); err != nil {
		return nil, err
	}
	return &Installer{
		dataDir: filepath.Clean(config.DataDir), challengesDir: filepath.Clean(config.ChallengesDir), reference: strings.TrimSpace(config.ReleaseReference),
		pollInterval: config.PollInterval,
		snapshot:     config.Snapshot, puller: config.Puller, layerReader: config.LayerReader, store: config.Store, roadmap: config.Roadmap,
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
		return i.prepareCommit(ctx, release, source)
	}
	if release.State == catalogdomain.ReleaseCommitting {
		return i.finalizeCommit(ctx, source, *release)
	}
	return fmt.Errorf("catalog release %q has unsupported state %s", release.ID, release.State)
}

func (i *Installer) ensureRelease(ctx context.Context) (*catalogdomain.Release, *PortableSource, error) {
	digest, err := catalogdomain.BundleDigestFromReference(i.reference)
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
		now := i.now().UTC()
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
			ArchiveSHA256: candidate.Digest(archive), Snapshot: snapshot, State: catalogdomain.EntryBuilding,
			StateVersion: 1, RuntimeAttempt: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
		})
	}
	return entries, nil
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
			State: catalogdomain.CommitPrepared, StateVersion: 1, RuntimeAttempt: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
		})
	}
	_, _, err = i.store.PrepareReleaseCommit(ctx, release.ID, intents, now)
	return err
}

func (i *Installer) finalizeCommit(ctx context.Context, source *PortableSource, release catalogdomain.Release) error {
	entries, err := i.store.Entries(ctx, release.ID)
	if err != nil {
		return err
	}
	byID := make(map[string]catalogdomain.Entry, len(entries))
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	commits, err := i.store.Commits(ctx, release.ID)
	if err != nil {
		return err
	}
	allMaterialized := true
	for _, commit := range commits {
		entry, exists := byID[commit.EntryID]
		if !exists {
			return fmt.Errorf("catalog commit %q has no entry", commit.ID)
		}
		switch commit.State {
		case catalogdomain.CommitPrepared:
			allMaterialized = false
			continue
		case catalogdomain.CommitArtifactPublished:
			if err := i.materializeCommit(source, entry, commit); err != nil {
				return i.handleFinalizerError(ctx, release, err)
			}
			if _, err := i.store.MarkCommitMaterialized(ctx, release.ID, commit.ID, i.now().UTC()); err != nil {
				return err
			}
			allMaterialized = false
		case catalogdomain.CommitMaterialized:
			if err := i.ensureMaterialized(entry, commit); err != nil {
				return i.handleFinalizerError(ctx, release, deterministicCatalogFailure(err))
			}
		case catalogdomain.CommitCommitted:
		case catalogdomain.CommitFailed:
			return nil
		default:
			return fmt.Errorf("catalog commit %q has unsupported state %s", commit.ID, commit.State)
		}
	}
	if !allMaterialized {
		return nil
	}
	commits, err = i.store.Commits(ctx, release.ID)
	if err != nil {
		return err
	}
	compiled, err := i.compileRevision(ctx, source, release, entries, commits)
	if err != nil {
		return i.handleFinalizerError(ctx, release, err)
	}
	_, err = i.store.CompleteReleaseCommit(ctx, release.ID, compiled, i.now().UTC())
	return err
}

func (i *Installer) handleFinalizerError(ctx context.Context, release catalogdomain.Release, err error) error {
	if !isDeterministicCatalogError(err) {
		return err
	}
	_, failErr := i.store.FailRelease(ctx, release.ID, errorSummary(err), i.now().UTC())
	return failErr
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
	target := filepath.Join(i.challengesDir, commit.SourceSlug)
	if _, err := os.Lstat(target); err != nil {
		if os.IsNotExist(err) {
			return challenge.ErrNotFound
		}
		return err
	}
	published, err := challenge.ValidateDir(target)
	if err != nil {
		return err
	}
	if published.ID != commit.ChallengeID || published.SourceSlug != commit.SourceSlug || published.ContentRevision != string(entry.ContentRevision) || published.Image != image || published.Title != entry.Title {
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
		published, err := challenge.ValidateDir(filepath.Join(i.challengesDir, commit.SourceSlug))
		if err != nil {
			return roadmap.Revision{}, deterministicCatalogFailure(fmt.Errorf("read materialized catalog challenge %q: %w", binding.Challenge.Path, err))
		}
		if published.ID != commit.ChallengeID || published.SourceSlug != commit.SourceSlug || published.Title != binding.Challenge.Title ||
			published.ContentRevision != binding.Challenge.ContentRevision {
			return roadmap.Revision{}, deterministicCatalogFailure(fmt.Errorf("materialized catalog challenge %q does not match its roadmap binding", binding.Challenge.Path))
		}
		values[binding.Challenge.Path] = roadmap.ChallengeRef{
			ID: commit.ChallengeID, SourceRef: binding.Challenge.SourceRef, Title: binding.Challenge.Title,
			ContentRevision: binding.Challenge.ContentRevision, SourceSlug: published.SourceSlug, MaterializedRevision: published.Revision,
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
