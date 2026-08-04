package roadmap

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

var revisionPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// RuntimeID derives a stable opaque runtime identity from portable content.
// Portable source_ref remains the human-maintained identity; callers must not
// derive source_ref from this value or expose it as a filename contract.
func RuntimeID(kind, sourceRef string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + sourceRef))
	return "roadmap-" + strings.ToLower(kind) + "-" + hex.EncodeToString(sum[:8])
}

func ValidRevision(value string) bool {
	return revisionPattern.MatchString(strings.TrimSpace(value))
}
