package k8s

import (
	crand "crypto/rand"
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

// isNotFound returns true if the error is a K8s NotFound error.
func isNotFound(err error) bool {
	return k8sErrors.IsNotFound(err)
}
