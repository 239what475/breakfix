package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/challenge"
)

const (
	candidatePublicationRecoveryInterval = 10 * time.Second
	candidatePublicationRecoveryLimit    = 1000
)

var errCandidatePublicationInvariant = errors.New("candidate publication invariant breach")

// RecoverCandidatePublications is Server's filesystem/database reconciler.
// Its input set comes only from explicit PostgreSQL publication intents whose
// immutable final artifact has already been recorded.
func (h *Handler) RecoverCandidatePublications(ctx context.Context) error {
	if h == nil || h.db == nil {
		return nil
	}
	revisions, err := h.db.ListRecoverableCandidatePublications(ctx, candidatePublicationRecoveryLimit)
	if err != nil {
		return err
	}
	var recoveryErrors []error
	for index := range revisions {
		if err := h.recoverCandidatePublication(ctx, &revisions[index]); err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("candidate %s: %w", revisions[index].ID, err))
		}
	}
	return errors.Join(recoveryErrors...)
}

func (h *Handler) StartCandidatePublicationRecovery(ctx context.Context) {
	if h == nil || h.db == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(candidatePublicationRecoveryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := h.RecoverCandidatePublications(ctx); err != nil {
					slog.Error("recover candidate challenge publications", "err", err)
				}
			}
		}
	}()
}

func (h *Handler) recoverCandidatePublication(ctx context.Context, revision *candidate.Revision) error {
	entry, err := h.materializeCandidatePublication(revision)
	if err != nil {
		return err
	}
	artifact := *revision.Publication.Artifact
	if err := h.db.RecoverCandidateChallengePublish(ctx, revision.ID, artifact, entry.Revision, time.Now().UTC()); err != nil {
		return err
	}
	slog.Info("recovered candidate challenge publication",
		"candidate_revision_id", revision.ID,
		"challenge_id", revision.Publication.ChallengeID,
		"challenge_revision", entry.Revision,
	)
	return nil
}

func (h *Handler) materializeCandidatePublication(revision *candidate.Revision) (*challenge.Entry, error) {
	if revision == nil || revision.Publication == nil || revision.Publication.Artifact == nil {
		return nil, candidate.ErrInvalidState
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
	if challenge.SourceSlugFor(candidateEntry.Title, publication.ChallengeID) != publication.SourceSlug {
		return nil, fmt.Errorf("%w: intent does not match immutable archive", errCandidatePublicationInvariant)
	}
	image := artifact.IncusFingerprint
	if artifact.Runtime == challenge.RuntimeK8s {
		image = artifact.OCIReference
	}
	expectedRoot := filepath.Join(root, "expected")
	expected, err := challenge.PromoteDirectoryAt(expectedRoot, source, publication.ChallengeID, image, publication.RequestedAt)
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
	// Another Server may have won the atomic rename. Accept only its complete,
	// byte-for-byte equivalent publication.
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
