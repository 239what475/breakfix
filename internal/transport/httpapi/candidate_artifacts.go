package httpapi

import (
	"errors"
	"fmt"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/execution"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
)

// validateCandidateStagingArtifact enforces the Server-owned resource identity
// of an artifact handoff. A worker may report only the staging repository or
// alias derived from the claimed candidate; a syntactically valid reference to
// another candidate is never an acceptable result.
func (h *Handler) validateRuntimeStagingArtifact(action runtime.Context, artifact execution.ArtifactReference) error {
	if err := artifact.Validate(action.Snapshot.Runtime); err != nil {
		return err
	}
	switch action.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		expected, err := candidate.CandidateOCIRepository(h.registryRepository, action.Identity.CandidateID)
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
		if action.Build == nil || action.Build.Incus == nil {
			return errors.New("node candidate has no build image identity")
		}
		expected, err := incus.AliasForCandidate(h.incusConfig.NamePrefix, action.Identity.CandidateID)
		if err != nil {
			return fmt.Errorf("derive candidate Incus alias: %w", err)
		}
		if artifact.IncusAlias != expected || artifact.IncusFingerprint != action.Build.Incus.Fingerprint {
			return errors.New("candidate artifact does not match the claimed Node build")
		}
		return nil
	default:
		return errors.New("runtime action has an unsupported candidate runtime")
	}
}

// validateCandidateChallengeArtifact enforces both final-artifact ownership and
// content identity. Publishing must not turn a candidate artifact into a
// different image merely because both values are valid immutable references.
func (h *Handler) validateRuntimeChallengeArtifact(action runtime.Context, artifact execution.ArtifactReference) error {
	if action.Artifact == nil || action.ChallengeID == "" {
		return errors.New("runtime action has no challenge publication input")
	}
	if err := artifact.Validate(action.Snapshot.Runtime); err != nil {
		return err
	}
	switch action.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		expected, err := candidate.ChallengeOCIRepository(h.registryRepository, action.ChallengeID)
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
		stagingDigest, err := candidate.OCIDigest(action.Artifact.OCIReference)
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
		expected, err := incus.AliasForChallenge(h.incusConfig.NamePrefix, action.ChallengeID)
		if err != nil {
			return fmt.Errorf("derive challenge Incus alias: %w", err)
		}
		if artifact.IncusAlias != expected || artifact.IncusFingerprint != action.Artifact.IncusFingerprint {
			return errors.New("challenge artifact does not match the verified Node artifact")
		}
		return nil
	default:
		return errors.New("runtime action has an unsupported challenge runtime")
	}
}
