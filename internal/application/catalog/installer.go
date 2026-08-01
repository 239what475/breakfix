package catalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/challenge"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/generation"
	taxonomydomain "github.com/breakfix/breakfix/internal/domain/taxonomy"
	"github.com/breakfix/breakfix/internal/taxonomy"
)

// BundlePuller copies one immutable OCI artifact to a Server-owned staging
// path. The installer never gives the release source to a worker until both
// the artifact and its portable content have been validated.
type BundlePuller interface {
	PullOCIArchive(context.Context, string, string) error
}

// SourceLayerReader validates the OCI artifact envelope and returns its sole
// catalog source layer. The OCI implementation is deliberately an adapter,
// not a dependency of this application package.
type SourceLayerReader interface {
	ReadSourceLayer(string) ([]byte, error)
}

// InstallationStore is the transactional boundary for an installation. It
// creates the release aggregate and the build workflows together.
type InstallationStore interface {
	CreateInstallation(context.Context, catalogdomain.Installation) (*catalogdomain.Release, error)
	HasReadyRelease(context.Context) (bool, error)
}

type Snapshotter func(challenge.Entry) (generation.ExecutionSnapshot, error)

type InstallerConfig struct {
	DataDir       string
	ChallengesDir string
	Taxonomy      *taxonomy.Store
	Puller        BundlePuller
	LayerReader   SourceLayerReader
	Store         InstallationStore
	Snapshotter   Snapshotter
}

// Installer owns the administrator-only source installation entry. It does
// not publish challenges itself: the Generate Worker performs the real build
// and verification phases after the installation transaction has succeeded.
type Installer struct {
	dataDir       string
	challengesDir string
	taxonomy      *taxonomy.Store
	puller        BundlePuller
	layerReader   SourceLayerReader
	store         InstallationStore
	snapshotter   Snapshotter
	now           func() time.Time
}

func NewInstaller(config InstallerConfig) (*Installer, error) {
	if strings.TrimSpace(config.DataDir) == "" || strings.TrimSpace(config.ChallengesDir) == "" ||
		config.Taxonomy == nil || config.Puller == nil || config.LayerReader == nil || config.Store == nil || config.Snapshotter == nil {
		return nil, errors.New("catalog installer requires data paths, taxonomy, artifact reader, store, and runtime snapshotter")
	}
	return &Installer{
		dataDir: filepath.Clean(config.DataDir), challengesDir: filepath.Clean(config.ChallengesDir), taxonomy: config.Taxonomy,
		puller: config.Puller, layerReader: config.LayerReader, store: config.Store, snapshotter: config.Snapshotter,
		now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// Install stages a digest-pinned portable source and atomically creates its
// release-owned candidate workflows. Files stay invisible until a later
// release-level commit marks the aggregate Ready.
func (i *Installer) Install(ctx context.Context, bundleReference string) (*catalogdomain.Release, error) {
	if i == nil {
		return nil, errors.New("catalog installer is not configured")
	}
	digest, err := bundleDigest(bundleReference)
	if err != nil {
		return nil, err
	}
	if err := i.ensureUninitialized(ctx); err != nil {
		return nil, err
	}

	now := i.now().UTC()
	releaseID := generation.NewID("catalog-release")
	stageRoot := filepath.Join(i.dataDir, ".staging", "releases", releaseID)
	if err := os.MkdirAll(stageRoot, 0o750); err != nil {
		return nil, fmt.Errorf("create catalog release staging: %w", err)
	}
	installed := false
	createdCandidates := make([]string, 0)
	defer func() {
		if installed {
			return
		}
		for _, id := range createdCandidates {
			_ = os.RemoveAll(filepath.Join(i.dataDir, "candidates", id))
		}
		_ = os.RemoveAll(stageRoot)
	}()

	archivePath := filepath.Join(stageRoot, "bundle.oci.tar")
	if err := i.puller.PullOCIArchive(ctx, strings.TrimSpace(bundleReference), archivePath); err != nil {
		return nil, fmt.Errorf("pull catalog release artifact: %w", err)
	}
	layer, err := i.layerReader.ReadSourceLayer(archivePath)
	if err != nil {
		return nil, fmt.Errorf("read catalog release source layer: %w", err)
	}
	sourceRoot := filepath.Join(stageRoot, "source")
	if err := os.Mkdir(sourceRoot, 0o750); err != nil {
		return nil, fmt.Errorf("create catalog source staging: %w", err)
	}
	if err := challenge.ExtractTarGz(sourceRoot, bytes.NewReader(layer)); err != nil {
		return nil, fmt.Errorf("extract catalog release source layer: %w", err)
	}
	source, err := LoadPortableSource(sourceRoot)
	if err != nil {
		return nil, fmt.Errorf("validate catalog release source: %w", err)
	}

	installation := catalogdomain.Installation{
		Release: catalogdomain.Release{
			ID: releaseID, Name: source.Manifest.Metadata.Name, Version: source.Manifest.Metadata.Version,
			BundleDigest: digest, TaxonomyContentRevision: source.Manifest.Taxonomy.ContentRevision,
			State: catalogdomain.ReleaseInstalling, DeadlineAt: deadlineAt(now), CreatedAt: now, UpdatedAt: now,
		},
		Entries: make([]catalogdomain.InstallationEntry, 0, len(source.Challenges)),
	}
	for _, sourceChallenge := range source.Challenges {
		entryID := generation.NewID("catalog-entry")
		candidateID := generation.NewID("catalog-candidate")
		workflowID := generation.NewID("generation-workflow")
		archive, err := archiveSourceCandidate(filepath.Join(source.Root, filepath.FromSlash(sourceChallenge.Path)))
		if err != nil {
			return nil, fmt.Errorf("archive catalog source %q: %w", sourceChallenge.Path, err)
		}
		archivePath, archiveDigest, err := candidate.SaveArchiveAtomic(i.dataDir, candidateID, archive)
		if err != nil {
			return nil, fmt.Errorf("stage catalog candidate %q: %w", sourceChallenge.Path, err)
		}
		createdCandidates = append(createdCandidates, candidateID)
		snapshot, err := i.snapshotter(sourceChallenge.Entry)
		if err != nil {
			return nil, fmt.Errorf("freeze catalog candidate runtime %q: %w", sourceChallenge.Path, err)
		}
		entry := catalogdomain.Entry{
			ID: entryID, ReleaseID: releaseID, SourcePath: sourceChallenge.Path, ContentRevision: sourceChallenge.ContentRevision,
			State: catalogdomain.EntryBuilding, CreatedAt: now, UpdatedAt: now,
		}
		candidateRevision := generation.Revision{
			ID: candidateID, Source: generation.Source{Kind: generation.SourceRelease, Ref: entryID},
			SourceRevision: string(sourceChallenge.ContentRevision), ArchivePath: archivePath, ArchiveSHA256: archiveDigest,
			Snapshot: snapshot, CreatedAt: now, UpdatedAt: now,
		}
		workflow := generation.Workflow{
			ID: workflowID, Source: generation.Source{Kind: generation.SourceRelease, Ref: entryID},
			SourceRevision: string(sourceChallenge.ContentRevision), State: generation.StateBuilding,
			CandidateRevisionID: candidateID, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
		}
		installation.Entries = append(installation.Entries, catalogdomain.InstallationEntry{
			Entry: entry, Candidate: candidateRevision, Workflow: workflow,
		})
	}
	release, err := i.store.CreateInstallation(ctx, installation)
	if err != nil {
		return nil, err
	}
	installed = true
	return release, nil
}

func deadlineAt(now time.Time) *time.Time {
	value := now.UTC().Add(generation.ExecutionDeadline)
	return &value
}

func (i *Installer) ensureUninitialized(ctx context.Context) error {
	ready, err := i.store.HasReadyRelease(ctx)
	if err != nil {
		return err
	}
	if ready {
		return errors.New("catalog release installation requires an uninitialized platform")
	}
	entries, err := challenge.List(i.challengesDir)
	if err != nil {
		return fmt.Errorf("read existing challenge catalog: %w", err)
	}
	if len(entries) != 0 {
		return errors.New("catalog release installation requires an empty challenge catalog")
	}
	if _, err := i.taxonomy.LoadCurrent(); err == nil {
		return errors.New("catalog release installation requires no current taxonomy snapshot")
	} else if !errors.Is(err, taxonomydomain.ErrNoCurrentRevision) {
		return fmt.Errorf("read current taxonomy snapshot: %w", err)
	}
	return nil
}

func bundleDigest(reference string) (catalogdomain.BundleDigest, error) {
	_, value, found := strings.Cut(strings.TrimSpace(reference), "@")
	if !found || !catalogdomain.BundleDigest(value).Valid() {
		return "", errors.New("catalog release must use an immutable OCI digest reference")
	}
	return catalogdomain.BundleDigest(value), nil
}

func archiveSourceCandidate(root string) ([]byte, error) {
	files, err := readSourceFiles(root)
	if err != nil {
		return nil, err
	}
	return writeSourceLayer(files)
}
