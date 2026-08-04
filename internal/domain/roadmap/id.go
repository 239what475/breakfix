package roadmap

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
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

// SourceRefSegment turns a title into the stable portable segment used by
// author-created roadmap entities. A new source_ref is intentionally derived
// only from its normalized English title: callers must reject an empty result
// rather than append an opaque identifier or a random suffix.
func SourceRefSegment(title string) string {
	var builder strings.Builder
	separator := true
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			separator = false
			continue
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			continue
		}
		if !separator {
			builder.WriteByte('-')
			separator = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func NewTopicSourceRef(domainSourceRef, title string) string {
	segment := SourceRefSegment(title)
	if strings.TrimSpace(domainSourceRef) == "" || segment == "" {
		return ""
	}
	return strings.TrimSpace(domainSourceRef) + "/" + segment
}

func NewTagSourceRef(title string) string {
	return SourceRefSegment(title)
}

func NewChallengeSourceRef(topicSourceRef, title string) string {
	segment := SourceRefSegment(title)
	if strings.TrimSpace(topicSourceRef) == "" || segment == "" {
		return ""
	}
	return strings.TrimSpace(topicSourceRef) + "/" + segment
}
