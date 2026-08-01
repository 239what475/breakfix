package kubernetes

import (
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

// EnvironmentNamespace returns the deterministic namespace owned by one
// environment. It is always a DNS label and keeps the breakfix-u prefix used
// by orphan cleanup.
func EnvironmentNamespace(namespace, userID, environmentID string) string {
	return DNSLabelName(namespace+"-u", userID, environmentID)
}

// DNSLabelName joins the supplied parts into a Kubernetes DNS label. Long
// names retain a readable prefix and receive a stable digest suffix so they
// remain distinct after truncation.
func DNSLabelName(prefix string, values ...string) string {
	return DNSLabelNameWithLimit(63, prefix, values...)
}

// DNSLabelNameWithLimit joins the supplied parts into a DNS label bounded by
// maxLength. It is useful for consumers such as Helm releases that impose a
// stricter limit than Kubernetes' 63-character DNS label maximum.
func DNSLabelNameWithLimit(maxLength int, prefix string, values ...string) string {
	if maxLength < 1 {
		maxLength = 1
	}
	if maxLength > 63 {
		maxLength = 63
	}
	parts := make([]string, 0, len(values)+1)
	parts = append(parts, prefix)
	parts = append(parts, values...)
	raw := strings.Join(parts, "-")
	name := normalizeDNSLabel(raw)
	if len(name) <= maxLength {
		return name
	}
	sum := sha256.Sum256([]byte(raw))
	suffix := hex.EncodeToString(sum[:])[:12]
	if maxLength <= len(suffix) {
		return suffix[:maxLength]
	}
	base := strings.TrimRight(name[:maxLength-len(suffix)-1], "-")
	if base == "" {
		return suffix[:maxLength]
	}
	return base + "-" + suffix
}

func normalizeDNSLabel(value string) string {
	var builder strings.Builder
	previousDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			previousDash = false
			continue
		}
		if !previousDash {
			builder.WriteByte('-')
			previousDash = true
		}
	}
	name := strings.Trim(builder.String(), "-")
	if name == "" {
		return "x"
	}
	return name
}

// isNotFound returns true if the error is a K8s NotFound error.
func isNotFound(err error) bool {
	return k8sErrors.IsNotFound(err)
}
