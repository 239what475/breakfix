package verify

import (
	"crypto/sha256"
	"fmt"
)

func EnvironmentName(workflowID string) string {
	return boundedName("verify-env-", workflowID)
}

func boundedName(prefix, value string) string {
	const maxLength = 63
	if len(prefix)+len(value) <= maxLength {
		return prefix + value
	}
	sum := sha256.Sum256([]byte(value))
	return prefix + fmt.Sprintf("%x", sum[:])[:maxLength-len(prefix)]
}
