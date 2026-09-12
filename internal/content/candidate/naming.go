package candidate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/breakfix/breakfix/internal/domain/generation"
)

// OpaqueName derives the resource component used for candidate-scoped OCI
// repositories. It intentionally depends only on the opaque domain ID, never
// on author-visible scenario metadata.
func OpaqueName(value string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(digest[:12])
}

func CandidateOCIRepository(registryRoot, candidateRevisionID string) (string, error) {
	return ociRepository(registryRoot, "candidates", candidateRevisionID)
}

// ScenarioOCIRepository is scoped to one immutable published Scenario
// revision. A later revision must never overwrite a prior runtime artifact.
func ScenarioOCIRepository(registryRoot, scenarioID, scenarioRevisionID string) (string, error) {
	if strings.TrimSpace(scenarioID) == "" || strings.TrimSpace(scenarioRevisionID) == "" {
		return "", errors.New("scenario OCI repository requires a scenario and revision")
	}
	return ociRepository(registryRoot, "scenarios", scenarioID+"\x00"+scenarioRevisionID)
}

func CandidateOCIImageReference(registryRoot, candidateRevisionID string) (string, error) {
	repository, err := CandidateOCIRepository(registryRoot, candidateRevisionID)
	if err != nil {
		return "", err
	}
	return repository + ":artifact", nil
}

func ScenarioOCIImageReference(registryRoot, scenarioID, scenarioRevisionID string) (string, error) {
	repository, err := ScenarioOCIRepository(registryRoot, scenarioID, scenarioRevisionID)
	if err != nil {
		return "", err
	}
	return repository + ":published", nil
}

// BuildOCIRepository is the durable Registry scope for one concrete K8s build
// action. Its opaque identity includes the owner, candidate and state version,
// but intentionally excludes a runtime retry attempt so every takeover reaches
// the same provider-side resource.
func BuildOCIRepository(registryRoot, ownerID, candidateRevisionID string, stateVersion int64) (string, error) {
	if strings.TrimSpace(ownerID) == "" || strings.TrimSpace(candidateRevisionID) == "" || stateVersion < 1 {
		return "", errors.New("OCI build repository requires owner, candidate, and state version")
	}
	identity := strings.TrimSpace(ownerID) + "\x00" + strings.TrimSpace(candidateRevisionID) + "\x00build\x00" + fmt.Sprintf("%d", stateVersion)
	return ociRepository(registryRoot, "builds", identity)
}

// BuildOCIImageReference is the stable tagged handle used to create or get a
// build-scoped immutable OCI artifact before it is promoted to staging.
func BuildOCIImageReference(registryRoot, ownerID, candidateRevisionID string, stateVersion int64) (string, error) {
	repository, err := BuildOCIRepository(registryRoot, ownerID, candidateRevisionID, stateVersion)
	if err != nil {
		return "", err
	}
	return repository + ":artifact", nil
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
