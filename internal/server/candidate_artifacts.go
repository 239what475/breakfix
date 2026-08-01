package server

import (
	"errors"
	"fmt"

	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/incusprovider"
)

// validateCandidateStagingArtifact enforces the Server-owned resource identity
// of an artifact handoff. A worker may report only the staging repository or
// alias derived from the claimed candidate; a syntactically valid reference to
// another candidate is never an acceptable result.
func (h *Handler) validateCandidateStagingArtifact(view generation.WorkerView, artifact generation.ArtifactReference) error {
	if err := artifact.Validate(view.Snapshot.Runtime); err != nil {
		return err
	}
	switch view.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		expected, err := candidate.CandidateOCIRepository(h.registryAddr, view.ID)
		if err != nil {
			return fmt.Errorf("derive candidate OCI repository: %w", err)
		}
		actual, err := candidate.OCIRepository(artifact.OCIReference)
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("candidate artifact OCI repository does not belong to candidate")
		}
		return nil

	case challenge.RuntimeNode:
		if view.Build == nil || view.Build.Incus == nil {
			return errors.New("node candidate has no build image identity")
		}
		expected, err := incusprovider.AliasForCandidate(h.incusConfig.NamePrefix, view.ID)
		if err != nil {
			return fmt.Errorf("derive candidate Incus alias: %w", err)
		}
		if artifact.IncusAlias != expected || artifact.IncusFingerprint != view.Build.Incus.Fingerprint {
			return errors.New("candidate artifact does not match the claimed Node build")
		}
		return nil
	default:
		return generation.ErrCandidateInvalidState
	}
}

// validateCandidateChallengeArtifact enforces both final-artifact ownership and
// content identity. Publishing must not turn a candidate artifact into a
// different image merely because both values are valid immutable references.
func (h *Handler) validateCandidateChallengeArtifact(view generation.WorkerView, artifact generation.ArtifactReference) error {
	if view.Publication == nil || view.Artifact == nil {
		return generation.ErrCandidateInvalidState
	}
	if err := artifact.Validate(view.Snapshot.Runtime); err != nil {
		return err
	}
	switch view.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		expected, err := candidate.ChallengeOCIRepository(h.registryAddr, view.Publication.ChallengeID)
		if err != nil {
			return fmt.Errorf("derive challenge OCI repository: %w", err)
		}
		actual, err := candidate.OCIRepository(artifact.OCIReference)
		if err != nil {
			return err
		}
		if actual != expected {
			return errors.New("challenge artifact OCI repository does not belong to publication")
		}
		stagingDigest, err := candidate.OCIDigest(view.Artifact.OCIReference)
		if err != nil {
			return fmt.Errorf("read candidate artifact digest: %w", err)
		}
		finalDigest, err := candidate.OCIDigest(artifact.OCIReference)
		if err != nil {
			return err
		}
		if finalDigest != stagingDigest {
			return errors.New("challenge artifact digest differs from verified candidate artifact")
		}
		return nil

	case challenge.RuntimeNode:
		expected, err := incusprovider.AliasForChallenge(h.incusConfig.NamePrefix, view.Publication.ChallengeID)
		if err != nil {
			return fmt.Errorf("derive challenge Incus alias: %w", err)
		}
		if artifact.IncusAlias != expected || artifact.IncusFingerprint != view.Artifact.IncusFingerprint {
			return errors.New("challenge artifact does not match the verified Node artifact")
		}
		return nil
	default:
		return generation.ErrCandidateInvalidState
	}
}
