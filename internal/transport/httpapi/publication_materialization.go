package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/generation"
	publicationdomain "github.com/breakfix/breakfix/internal/domain/publication"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
)

var errCandidatePublicationInvariant = errors.New("candidate publication invariant breach")

func candidatePublicationFailure(err error) error {
	if err == nil || publicationdomain.CategoryOf(err).Valid() {
		return err
	}
	if errors.Is(err, errCandidatePublicationInvariant) {
		return publicationdomain.Deterministic(err)
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return publicationdomain.Transient(err)
	}
	return publicationdomain.Deterministic(err)
}

// materializeCandidatePublication is the Server-owned final filesystem write.
// Runtime Worker only publishes the immutable runtime artifact and reports it
// under its fenced action lease.
func (h *Handler) materializeCandidatePublication(revision *generation.Revision) (*challenge.Entry, error) {
	if revision == nil || revision.Publication == nil || revision.Publication.Artifact == nil {
		return nil, publicationdomain.Deterministic(generation.ErrCandidateInvalidState)
	}
	publication := revision.Publication
	intent := *publication
	artifact := *intent.Artifact
	intent.Artifact = nil
	if err := intent.ValidateIntent(); err != nil {
		return nil, candidatePublicationFailure(fmt.Errorf("%w: invalid intent: %v", errCandidatePublicationInvariant, err))
	}
	if err := artifact.Validate(revision.Snapshot.Runtime); err != nil {
		return nil, candidatePublicationFailure(fmt.Errorf("%w: invalid final artifact: %v", errCandidatePublicationInvariant, err))
	}
	if err := h.validateRuntimeChallengeArtifact(runtime.Context{
		Snapshot: revision.Snapshot, Artifact: revision.Artifact, ChallengeID: publication.ChallengeID, ChallengeRevisionID: publication.ChallengeRevisionID,
	}, artifact); err != nil {
		return nil, candidatePublicationFailure(fmt.Errorf("%w: final artifact ownership: %v", errCandidatePublicationInvariant, err))
	}
	archive, err := candidate.ReadArchive(revision.ArchivePath, revision.ArchiveSHA256)
	if err != nil {
		return nil, candidatePublicationFailure(err)
	}
	root, err := os.MkdirTemp("", "breakfix-publish-candidate-")
	if err != nil {
		return nil, publicationdomain.Transient(err)
	}
	defer os.RemoveAll(root) //nolint:errcheck
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o750); err != nil {
		return nil, publicationdomain.Transient(err)
	}
	if err := challenge.ExtractTarGz(source, bytes.NewReader(archive)); err != nil {
		return nil, publicationdomain.Deterministic(err)
	}
	candidateEntry, err := challenge.ValidateCandidateDir(source)
	if err != nil {
		return nil, publicationdomain.Deterministic(err)
	}
	contentRevision, err := appcatalog.ContentRevision(source)
	if err != nil {
		return nil, candidatePublicationFailure(fmt.Errorf("hash verified candidate source: %w", err))
	}
	if (publication.BaseActiveRevisionID == "" && challenge.SourceSlugFor(candidateEntry.Title, publication.ChallengeID) != publication.SourceSlug) ||
		challenge.MaterializedPath(publication.SourceSlug, publication.ChallengeRevisionID) != publication.TargetPath {
		return nil, candidatePublicationFailure(fmt.Errorf("%w: intent does not match immutable archive", errCandidatePublicationInvariant))
	}
	image := artifact.IncusFingerprint
	if artifact.Runtime == challenge.RuntimeK8s {
		image = artifact.OCIReference
	}
	expectedRoot := filepath.Join(root, "expected")
	expected, err := challenge.PromoteDirectoryAt(expectedRoot, source, publication.ChallengeID, publication.ChallengeRevisionID, publication.SourceSlug, image, string(contentRevision), publication.RequestedAt)
	if err != nil {
		return nil, candidatePublicationFailure(err)
	}
	target := filepath.Join(h.challengesDir, publication.TargetPath)
	if existing, found, err := validateExistingCandidatePublication(target, expected); err != nil || found {
		return existing, candidatePublicationFailure(err)
	}

	materialized, err := challenge.MaterializeWithPath(h.challengesDir, expected.ID, expected.RevisionID, publication.TargetPath, func(destination string) error {
		return challenge.CopyRegularFiles(expected.Dir, destination)
	})
	if err == nil {
		return materialized, nil
	}
	if existing, found, validationErr := validateExistingCandidatePublication(target, expected); found || validationErr != nil {
		return existing, candidatePublicationFailure(validationErr)
	}
	return nil, candidatePublicationFailure(err)
}

func validateExistingCandidatePublication(target string, expected *challenge.Entry) (*challenge.Entry, bool, error) {
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, candidatePublicationFailure(err)
	}
	if !info.IsDir() {
		return nil, true, fmt.Errorf("%w: target is not a directory", errCandidatePublicationInvariant)
	}
	existing, err := challenge.ValidateDir(target)
	if err != nil {
		return nil, true, fmt.Errorf("%w: invalid target: %v", errCandidatePublicationInvariant, err)
	}
	if expected == nil || existing.ID != expected.ID || existing.RevisionID != expected.RevisionID || existing.SourceSlug != expected.SourceSlug ||
		existing.Image != expected.Image || existing.Revision != expected.Revision {
		return nil, true, fmt.Errorf("%w: target conflicts with publication intent", errCandidatePublicationInvariant)
	}
	return existing, true, nil
}
