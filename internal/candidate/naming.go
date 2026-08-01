package candidate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/breakfix/breakfix/internal/domain/generation"
)

// OpaqueName derives the resource component used for candidate-scoped OCI
// repositories. It intentionally depends only on the opaque domain ID, never
// on author-visible challenge metadata.
func OpaqueName(value string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(digest[:12])
}

func CandidateOCIRepository(registryRoot, candidateRevisionID string) (string, error) {
	return ociRepository(registryRoot, "candidates", candidateRevisionID)
}

func ChallengeOCIRepository(registryRoot, challengeID string) (string, error) {
	return ociRepository(registryRoot, "challenges", challengeID)
}

func CandidateOCIImageReference(registryRoot, candidateRevisionID string) (string, error) {
	repository, err := CandidateOCIRepository(registryRoot, candidateRevisionID)
	if err != nil {
		return "", err
	}
	return repository + ":artifact", nil
}

func ChallengeOCIImageReference(registryRoot, challengeID string) (string, error) {
	repository, err := ChallengeOCIRepository(registryRoot, challengeID)
	if err != nil {
		return "", err
	}
	return repository + ":published", nil
}

// OCIRepository returns the repository portion of a validated immutable OCI
// reference. The digest remains content identity; the repository is the
// resource-ownership boundary enforced by Server.
func OCIRepository(reference string) (string, error) {
	repository, digest, found := strings.Cut(strings.TrimSpace(reference), "@")
	if !found || strings.TrimSpace(repository) == "" || !generation.ValidSHA256(digest) {
		return "", errors.New("invalid immutable OCI reference")
	}
	return repository, nil
}

func OCIDigest(reference string) (string, error) {
	_, digest, found := strings.Cut(strings.TrimSpace(reference), "@")
	if !found || !generation.ValidSHA256(digest) {
		return "", errors.New("invalid immutable OCI reference")
	}
	return digest, nil
}

func ociRepository(registryRoot, scope, id string) (string, error) {
	root := strings.TrimRight(strings.TrimSpace(registryRoot), "/")
	if root == "" || strings.ContainsAny(root, " \t\r\n@") || strings.TrimSpace(id) == "" {
		return "", errors.New("OCI repository requires a registry root and opaque ID")
	}
	return root + "/" + scope + "/" + OpaqueName(id), nil
}
