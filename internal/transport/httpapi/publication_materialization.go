package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/challenge"
	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

var errCandidatePublicationInvariant = errors.New("candidate publication invariant breach")

// materializeCandidatePublication is the Server-owned final filesystem write.
// Generate Worker only publishes the immutable runtime artifact and reports it
// under its GenerationWorkflow lease.
func (h *Handler) materializeCandidatePublication(revision *generation.Revision) (*challenge.Entry, error) {
	if revision == nil || revision.Publication == nil || revision.Publication.Artifact == nil {
		return nil, generation.ErrCandidateInvalidState
	}
	publication := revision.Publication
	intent := *publication
	artifact := *intent.Artifact
	intent.Artifact = nil
	if err := intent.ValidateIntent(); err != nil {
		return nil, fmt.Errorf("%w: invalid intent: %v", errCandidatePublicationInvariant, err)
	}
	if err := artifact.Validate(revision.Snapshot.Runtime); err != nil {
		return nil, fmt.Errorf("%w: invalid final artifact: %v", errCandidatePublicationInvariant, err)
	}
	if err := h.validateCandidateChallengeArtifact(revision.WorkerView(), artifact); err != nil {
		return nil, fmt.Errorf("%w: final artifact ownership: %v", errCandidatePublicationInvariant, err)
	}
	archive, err := candidate.ReadArchive(revision.ArchivePath, revision.ArchiveSHA256)
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "breakfix-publish-candidate-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root) //nolint:errcheck
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o750); err != nil {
		return nil, err
	}
	if err := challenge.ExtractTarGz(source, bytes.NewReader(archive)); err != nil {
		return nil, err
	}
	candidateEntry, err := challenge.ValidateCandidateDir(source)
	if err != nil {
		return nil, err
	}
	contentRevision, err := appcatalog.ContentRevision(source)
	if err != nil {
		return nil, fmt.Errorf("hash verified candidate source: %w", err)
	}
	if challenge.SourceSlugFor(candidateEntry.Title, publication.ChallengeID) != publication.SourceSlug {
		return nil, fmt.Errorf("%w: intent does not match immutable archive", errCandidatePublicationInvariant)
	}
	image := artifact.IncusFingerprint
	if artifact.Runtime == challenge.RuntimeK8s {
		image = artifact.OCIReference
	}
	expectedRoot := filepath.Join(root, "expected")
	expected, err := challenge.PromoteDirectoryAt(expectedRoot, source, publication.ChallengeID, image, string(contentRevision), publication.RequestedAt)
	if err != nil {
		return nil, err
	}
	target := filepath.Join(h.challengesDir, publication.TargetPath)
	if existing, found, err := validateExistingCandidatePublication(target, expected); err != nil || found {
		return existing, err
	}

	materialized, err := challenge.MaterializeWithSlug(h.challengesDir, expected.ID, expected.SourceSlug, func(destination string) error {
		return challenge.CopyRegularFiles(expected.Dir, destination)
	})
	if err == nil {
		return materialized, nil
	}
	if existing, found, validationErr := validateExistingCandidatePublication(target, expected); found || validationErr != nil {
		return existing, validationErr
	}
	return nil, err
}

func validateExistingCandidatePublication(target string, expected *challenge.Entry) (*challenge.Entry, bool, error) {
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() {
		return nil, true, fmt.Errorf("%w: target is not a directory", errCandidatePublicationInvariant)
	}
	existing, err := challenge.ValidateDir(target)
	if err != nil {
		return nil, true, fmt.Errorf("%w: invalid target: %v", errCandidatePublicationInvariant, err)
	}
	if expected == nil || existing.ID != expected.ID || existing.SourceSlug != expected.SourceSlug ||
		existing.Image != expected.Image || existing.Revision != expected.Revision {
		return nil, true, fmt.Errorf("%w: target conflicts with publication intent", errCandidatePublicationInvariant)
	}
	return existing, true, nil
}
