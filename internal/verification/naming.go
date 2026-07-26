package verification

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

func JobName(taskID string) string {
	return boundedName("verifier-", taskID)
}

// TaskName deterministically maps an immutable submission to its sole
// VerifyTask. The name does not contain challenge metadata because a candidate
// becomes a catalog challenge only after verification and author approval.
func TaskName(submissionID string) string {
	return boundedName("verify-", submissionID)
}

func EnvironmentName(taskID string) string {
	return boundedName("verify-env-", taskID)
}

func ImageName(registryAddr, taskID string) string {
	image := "verify-" + taskID + ":latest"
	if registry := strings.TrimSpace(registryAddr); registry != "" {
		return registry + "/" + image
	}
	return image
}

func boundedName(prefix, value string) string {
	const maxLength = 63
	if len(prefix)+len(value) <= maxLength {
		return prefix + value
	}
	sum := sha256.Sum256([]byte(value))
	return prefix + fmt.Sprintf("%x", sum[:])[:maxLength-len(prefix)]
}
