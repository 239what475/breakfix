package generation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/execution"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/publication"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
)

const defaultPublicationFinalizerInterval = 5 * time.Second

var errPublicationInvariant = errors.New("candidate publication invariant breach")

// PublicationFinalizerStore is the Server-owned persistence boundary for the
// tail after a Runtime Worker has recorded an immutable promoted artifact.
type PublicationFinalizerStore interface {
	PendingGenerationPublicationFinalizations(context.Context, time.Time) ([]domain.PublicationFinalization, error)
	FinalizeGenerationChallengePublication(context.Context, string, string, string, string, challenge.ScenarioType, []string, time.Time) error
	RecordGenerationPublicationFinalizerFailure(context.Context, string, string, publication.Diagnostic) (*domain.Workflow, error)
}

// ChallengeArtifactValidator verifies that a Worker-promoted artifact belongs
// to the exact final publication intent. Bootstrap supplies provider-specific
// naming rules without giving this application service provider credentials.
type ChallengeArtifactValidator interface {
	ValidateChallengeArtifact(runtime.Context, execution.ArtifactReference) error
}

type PublicationFinalizerConfig struct {
	Store         PublicationFinalizerStore
	ChallengesDir string
	Validator     ChallengeArtifactValidator
	Interval      time.Duration
}

// PublicationFinalizer owns the final filesystem materialization and durable
// Challenge publication. It never invokes a provider or HTTP handler.
type PublicationFinalizer struct {
	store         PublicationFinalizerStore
	challengesDir string
	validator     ChallengeArtifactValidator
	interval      time.Duration
	now           func() time.Time
}

func NewPublicationFinalizer(config PublicationFinalizerConfig) (*PublicationFinalizer, error) {
	if config.Store == nil || config.Validator == nil || config.ChallengesDir == "" {
		return nil, errors.New("generation publication finalizer requires store, challenge directory, and artifact validator")
	}
	if config.Interval <= 0 {
		config.Interval = defaultPublicationFinalizerInterval
	}
	return &PublicationFinalizer{
		store: config.Store, challengesDir: filepath.Clean(config.ChallengesDir), validator: config.Validator,
		interval: config.Interval, now: func() time.Time { return time.Now().UTC() },
	}, nil
}

// Recover finalizes already-promoted work before HTTP readiness. It is safe to
// repeat because the durable finalizer fence remains authoritative.
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
			if err := f.RunOnce(ctx); err != nil && ctx.Err() == nil {
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
		return fmt.Errorf("list pending generation publication finalizations: %w", err)
	}
	for _, value := range pending {
		entry, materializeErr := f.materialize(&value.Candidate)
		if materializeErr != nil {
			if err := f.recordFailure(ctx, value, materializeErr); err != nil {
				slog.Warn("record generation publication diagnostic", "workflow_id", value.Workflow.ID, "candidate_revision_id", value.Candidate.ID, "err", err)
			}
			continue
		}
		if err := f.store.FinalizeGenerationChallengePublication(ctx, value.Workflow.ID, value.Candidate.ID, entry.ContentRevision, entry.Revision, entry.Type, entry.Tags, f.now()); err != nil {
			if recordErr := f.recordFailure(ctx, value, classifyPublicationFinalizerError(err)); recordErr != nil {
				slog.Warn("record generation publication diagnostic", "workflow_id", value.Workflow.ID, "candidate_revision_id", value.Candidate.ID, "err", recordErr)
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
	if errors.Is(err, domain.ErrCandidateInvalidState) || errors.Is(err, domain.ErrChallengeSourceRefConflict) {
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

func (f *PublicationFinalizer) materialize(revision *domain.Revision) (*challenge.Entry, error) {
	if revision == nil || revision.Publication == nil || revision.Publication.Artifact == nil {
		return nil, publication.Deterministic(domain.ErrCandidateInvalidState)
	}
	publicationIntent := revision.Publication
	intent := *publicationIntent
	artifact := *intent.Artifact
	intent.Artifact = nil
	if err := intent.ValidateIntent(); err != nil {
		return nil, publicationFailure(fmt.Errorf("%w: invalid intent: %v", errPublicationInvariant, err))
	}
	if err := artifact.Validate(revision.Snapshot.Runtime); err != nil {
		return nil, publicationFailure(fmt.Errorf("%w: invalid final artifact: %v", errPublicationInvariant, err))
	}
	if err := f.validator.ValidateChallengeArtifact(runtime.Context{
		Snapshot: revision.Snapshot, Artifact: revision.Artifact, ChallengeID: publicationIntent.ChallengeID, ChallengeRevisionID: publicationIntent.ChallengeRevisionID,
	}, artifact); err != nil {
		return nil, publicationFailure(fmt.Errorf("%w: final artifact ownership: %v", errPublicationInvariant, err))
	}
	archive, err := candidate.ReadArchive(revision.ArchivePath, revision.ArchiveSHA256)
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
	if err := challenge.ExtractTarGz(source, bytes.NewReader(archive)); err != nil {
		return nil, publication.Deterministic(err)
	}
	candidateEntry, err := challenge.ValidateCandidateDir(source)
	if err != nil {
		return nil, publication.Deterministic(err)
	}
	contentRevision, err := appcatalog.ContentRevision(source)
	if err != nil {
		return nil, publicationFailure(fmt.Errorf("hash verified candidate source: %w", err))
	}
	if (publicationIntent.BaseActiveRevisionID == "" && challenge.SourceSlugFor(candidateEntry.Title, publicationIntent.ChallengeID) != publicationIntent.SourceSlug) ||
		challenge.MaterializedPath(publicationIntent.SourceSlug, publicationIntent.ChallengeRevisionID) != publicationIntent.TargetPath {
		return nil, publicationFailure(fmt.Errorf("%w: intent does not match immutable archive", errPublicationInvariant))
	}
	image := artifact.IncusFingerprint
	if artifact.Runtime == challenge.RuntimeK8s {
		image = artifact.OCIReference
	}
	expectedRoot := filepath.Join(root, "expected")
	expected, err := challenge.PromoteDirectoryAt(expectedRoot, source, publicationIntent.ChallengeID, publicationIntent.ChallengeRevisionID, publicationIntent.SourceSlug, image, string(contentRevision), publicationIntent.RequestedAt)
	if err != nil {
		return nil, publicationFailure(err)
	}
	target := filepath.Join(f.challengesDir, publicationIntent.TargetPath)
	if existing, found, err := validateExistingPublication(target, expected); err != nil || found {
		return existing, publicationFailure(err)
	}

	materialized, err := challenge.MaterializeWithPath(f.challengesDir, expected.ID, expected.RevisionID, publicationIntent.TargetPath, func(destination string) error {
		return challenge.CopyRegularFiles(expected.Dir, destination)
	})
	if err == nil {
		return materialized, nil
	}
	if existing, found, validationErr := validateExistingPublication(target, expected); found || validationErr != nil {
		return existing, publicationFailure(validationErr)
	}
	return nil, publicationFailure(err)
}

func validateExistingPublication(target string, expected *challenge.Entry) (*challenge.Entry, bool, error) {
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
	existing, err := challenge.ValidateDir(target)
	if err != nil {
		return nil, true, fmt.Errorf("%w: invalid target: %v", errPublicationInvariant, err)
	}
	if expected == nil || existing.ID != expected.ID || existing.RevisionID != expected.RevisionID || existing.SourceSlug != expected.SourceSlug ||
		existing.Image != expected.Image || existing.Revision != expected.Revision {
		return nil, true, fmt.Errorf("%w: target conflicts with publication intent", errPublicationInvariant)
	}
	return existing, true, nil
}
