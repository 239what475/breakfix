package k8s

import (
	crand "crypto/rand"
	"path/filepath"
	"strings"

	k8sErrors "k8s.io/apimachinery/pkg/api/errors"
)

// RandomID generates a 6-character random identifier.
func RandomID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	//nolint:gosec
	crand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

// UserNamespace returns the namespace name for a given user.
func UserNamespace(namespace, userID string) string {
	return namespace + "-" + strings.ReplaceAll(userID, "_", "-")
}

// ChallengeDir returns the challenge directory path.
func ChallengeDir(challengesDir, challengeID string) string {
	return filepath.Join(challengesDir, challengeID)
}

// VerifyScriptPath returns the path to verify.sh for a challenge directory.
func VerifyScriptPath(challengeDir string) string {
	return filepath.Join(challengeDir, "verify.sh")
}

// Pod-side paths used by the generator for challenge verification.
const (
	PodVerifyPath   = "/tmp/verify.sh"
	PodAnswerPath   = "/tmp/answer.sh"
	PodMetadataPath = "/tmp/challenge-metadata.json"
)

// isNotFound returns true if the error is a K8s NotFound error.
func isNotFound(err error) bool {
	return k8sErrors.IsNotFound(err)
}
