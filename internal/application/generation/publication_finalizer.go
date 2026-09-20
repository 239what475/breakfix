package generation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/scenario"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/publication"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

const defaultPublicationFinalizerInterval = 5 * time.Second

var errPublicationInvariant = errors.New("candidate publication invariant breach")

type PublicationFinalizerStore interface {
	PendingGenerationPublicationFinalizations(context.Context, time.Time) ([]domain.PublicationFinalization, error)
	FinalizeGenerationScenarioPublication(context.Context, string, string, string, string, scenario.ScenarioType, []string, time.Time) error
	RecordGenerationPublicationFinalizerFailure(context.Context, string, string, publication.Diagnostic) (*domain.Workflow, error)
}

type RunnableRevisionResolver interface {
	ResolveRunnableRevision(context.Context, string, string) (runnable.RunnableRevision, error)
}

type PublicationFinalizerConfig struct {
	Store        PublicationFinalizerStore
	Runnable     RunnableRevisionResolver
	ScenariosDir string
	Interval     time.Duration
	// OnTick optionally reports each background pass to the bootstrap's
	// in-memory service registry.
	OnTick func(error)
}

// PublicationFinalizer materializes Operations source and atomically exposes
// it through the Generation store. Provider artifact identity is always read
// from the immutable public RunnableRevision, never supplied by a Worker.
type PublicationFinalizer struct {
	store        PublicationFinalizerStore
	runnable     RunnableRevisionResolver
	scenariosDir string
	interval     time.Duration
	// OnTick optionally reports each background pass to the bootstrap's
	// in-memory service registry.
	OnTick func(error)
	now    func() time.Time
}

func NewPublicationFinalizer(config PublicationFinalizerConfig) (*PublicationFinalizer, error) {
	if config.Store == nil || config.Runnable == nil || config.ScenariosDir == "" {
		return nil, errors.New("generation publication finalizer requires store, runnable resolver, and scenario directory")
	}
	if config.Interval <= 0 {
		config.Interval = defaultPublicationFinalizerInterval
	}
	return &PublicationFinalizer{store: config.Store, runnable: config.Runnable, scenariosDir: filepath.Clean(config.ScenariosDir), interval: config.Interval, OnTick: config.OnTick, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (f *PublicationFinalizer) Recover(ctx context.Context) error { return f.RunOnce(ctx) }

func (f *PublicationFinalizer) Run(ctx context.Context) error {
	if f == nil {
		return errors.New("generation publication finalizer is not configured")
	}
	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			err := f.RunOnce(ctx)
			if f.OnTick != nil {
				f.OnTick(err)
			}
			if err != nil && ctx.Err() == nil {
				slog.Warn("finalize generation publications", "err", err)
			}
		}
	}
}

func (f *PublicationFinalizer) RunOnce(ctx context.Context) error {
	if f == nil || f.store == nil {
		return errors.New("generation publication finalizer is not configured")
	}
	pending, err := f.store.PendingGenerationPublicationFinalizations(ctx, f.now())
	if err != nil {
		return fmt.Errorf("list pending generation publications: %w", err)
	}
	for _, value := range pending {
		entry, materializeErr := f.materialize(ctx, &value.Candidate)
		if materializeErr != nil {
			if err := f.recordFailure(ctx, value, materializeErr); err != nil {
				slog.Warn("record generation publication diagnostic", "workflow_id", value.Workflow.ID, "err", err)
			}
			continue
		}
		if err := f.store.FinalizeGenerationScenarioPublication(ctx, value.Workflow.ID, value.Candidate.ID, entry.ContentRevision, entry.Revision, entry.Type, entry.Tags, f.now()); err != nil {
			if recordErr := f.recordFailure(ctx, value, classifyPublicationFinalizerError(err)); recordErr != nil {
				slog.Warn("record generation publication diagnostic", "workflow_id", value.Workflow.ID, "err", recordErr)
			}
		}
	}
	return nil
}

func (f *PublicationFinalizer) recordFailure(ctx context.Context, value domain.PublicationFinalization, err error) error {
	diagnostic, diagnosticErr := publication.NewDiagnostic(classifyPublicationFinalizerError(err), f.now())
	if diagnosticErr != nil {
		return diagnosticErr
	}
	_, err = f.store.RecordGenerationPublicationFinalizerFailure(ctx, value.Workflow.ID, value.Candidate.ID, diagnostic)
	return err
}

func classifyPublicationFinalizerError(err error) error {
	if err == nil || publication.CategoryOf(err).Valid() {
		return err
	}
	if errors.Is(err, domain.ErrCandidateInvalidState) || errors.Is(err, domain.ErrScenarioSourceRefConflict) {
		return publication.Deterministic(err)
	}
	return publication.Transient(err)
}

func publicationFailure(err error) error {
	if err == nil || publication.CategoryOf(err).Valid() {
		return err
	}
	if errors.Is(err, errPublicationInvariant) {
		return publication.Deterministic(err)
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return publication.Transient(err)
	}
	return publication.Deterministic(err)
}

func (f *PublicationFinalizer) materialize(ctx context.Context, revision *domain.Revision) (*scenario.Entry, error) {
	if revision == nil || revision.Publication == nil || revision.RunnableRevisionRef == nil || revision.VerificationReportRef == nil || f.runnable == nil {
		return nil, publication.Deterministic(domain.ErrCandidateInvalidState)
	}
	intent := *revision.Publication
	if err := intent.ValidateIntent(); err != nil {
		return nil, publicationFailure(fmt.Errorf("%w: invalid intent: %v", errPublicationInvariant, err))
	}
	runnableRevision, err := f.runnable.ResolveRunnableRevision(ctx, revision.RunnableRevisionRef.ID, revision.RunnableRevisionRef.Digest)
	if err != nil {
		return nil, publicationFailure(err)
	}
	image, err := runnableArtifactImage(runnableRevision.Artifact)
	if err != nil {
		return nil, publicationFailure(err)
	}
	archive, err := candidate.ReadArchive(revision.ArchivePath, revision.ArchiveDigest)
	if err != nil {
		return nil, publicationFailure(err)
	}
	root, err := os.MkdirTemp("", "breakfix-publish-candidate-")
	if err != nil {
		return nil, publication.Transient(err)
	}
	defer os.RemoveAll(root) //nolint:errcheck
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o750); err != nil {
		return nil, publication.Transient(err)
	}
	if err := scenario.ExtractTarGz(source, bytes.NewReader(archive)); err != nil {
		return nil, publication.Deterministic(err)
	}
	candidateEntry, err := ValidateCandidateDir(source)
	if err != nil {
		return nil, publication.Deterministic(err)
	}
	contentRevision, err := appcatalog.ContentRevision(source)
	if err != nil {
		return nil, publicationFailure(err)
	}
	if string(contentRevision) != revision.ContentRevision || (intent.BaseActiveRevisionID == "" && scenario.SourceSlugFor(candidateEntry.Title, intent.ScenarioID) != intent.SourceSlug) || scenario.MaterializedPath(intent.SourceSlug, intent.ScenarioRevisionID) != intent.TargetPath {
		return nil, publicationFailure(fmt.Errorf("%w: intent does not match frozen candidate", errPublicationInvariant))
	}
	expectedRoot := filepath.Join(root, "expected")
	expected, err := scenario.PromoteDirectoryAt(expectedRoot, source, intent.ScenarioID, intent.ScenarioRevisionID, intent.SourceSlug, image, string(contentRevision), intent.RequestedAt)
	if err != nil {
		return nil, publicationFailure(err)
	}
	target := filepath.Join(f.scenariosDir, intent.TargetPath)
	if existing, found, err := validateExistingPublication(target, expected); err != nil || found {
		return existing, publicationFailure(err)
	}
	materialized, err := scenario.MaterializeWithPath(f.scenariosDir, expected.ID, expected.RevisionID, intent.TargetPath, func(destination string) error { return scenario.CopyRegularFiles(expected.Dir, destination) })
	if err == nil {
		return materialized, nil
	}
	if existing, found, validationErr := validateExistingPublication(target, expected); found || validationErr != nil {
		return existing, publicationFailure(validationErr)
	}
	return nil, publicationFailure(err)
}

func runnableArtifactImage(artifact runnable.ArtifactReference) (string, error) {
	if err := artifact.Validate(); err != nil {
		return "", err
	}
	switch artifact.Runtime {
	case runnable.RuntimeNode:
		_, digest, found := strings.Cut(strings.TrimPrefix(artifact.ProviderReference, "incus://"), "@")
		if !found || digest != artifact.ArtifactDigest {
			return "", errors.New("node runnable artifact reference is invalid")
		}
		return strings.TrimPrefix(digest, "sha256:"), nil
	case runnable.RuntimeK8s:
		return artifact.ProviderReference, nil
	default:
		return "", errors.New("unsupported runnable artifact runtime")
	}
}

func validateExistingPublication(target string, expected *scenario.Entry) (*scenario.Entry, bool, error) {
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, publicationFailure(err)
	}
	if !info.IsDir() {
		return nil, true, fmt.Errorf("%w: target is not a directory", errPublicationInvariant)
	}
	existing, err := scenario.ValidateDir(target)
	if err != nil {
		return nil, true, fmt.Errorf("%w: invalid target: %v", errPublicationInvariant, err)
	}
	if expected == nil || existing.ID != expected.ID || existing.RevisionID != expected.RevisionID || existing.SourceSlug != expected.SourceSlug || existing.Image != expected.Image || existing.Revision != expected.Revision {
		return nil, true, fmt.Errorf("%w: target conflicts with publication intent", errPublicationInvariant)
	}
	return existing, true, nil
}
