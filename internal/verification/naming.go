package verification

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

func JobName(taskID string) string {
	return boundedName("verifier-", taskID)
}

func BuildJobName(taskID string) string {
	return boundedName("builder-", taskID)
}

func PublisherJobName(taskID string) string {
	return boundedName("publisher-", taskID)
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

// PublishedImageName is the Registry tag selected by the trusted Server when
// an author publishes a verified candidate. It is intentionally derived only
// from the platform-owned opaque challenge ID.
func PublishedImageName(registryAddr, challengeID string) string {
	image := "challenge-" + challengeID + ":latest"
	if registry := strings.TrimSpace(registryAddr); registry != "" {
		return registry + "/" + image
	}
	return image
}

func BaseImage(registryAddr, runtime string) string {
	name := "breakfix-base:latest"
	if strings.TrimSpace(runtime) == "vcluster" {
		name = "breakfix-k8s-base:latest"
	}
	if registry := strings.TrimSpace(registryAddr); registry != "" {
		return registry + "/" + name
	}
	return name
}

func boundedName(prefix, value string) string {
	const maxLength = 63
	if len(prefix)+len(value) <= maxLength {
		return prefix + value
	}
	sum := sha256.Sum256([]byte(value))
	return prefix + fmt.Sprintf("%x", sum[:])[:maxLength-len(prefix)]
}
