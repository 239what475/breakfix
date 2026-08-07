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

// SourceRefSegment turns a title into a stable portable ASCII segment used by
// author-created roadmap entities. ASCII titles retain their readable form.
// Titles that include non-ASCII letters or numbers receive a deterministic
// digest suffix, so a display title never makes a portable source_ref empty or
// accidentally collide with the ASCII portion of another title.
func SourceRefSegment(title string) string {
	normalized := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(title)), " "))
	var builder strings.Builder
	separator := true
	needsDigest := false
	for _, r := range normalized {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			separator = false
			continue
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			needsDigest = true
			continue
		}
		if !separator {
			builder.WriteByte('-')
			separator = true
		}
	}
	segment := strings.Trim(builder.String(), "-")
	if segment == "" {
		segment = "item"
		needsDigest = true
	}
	if !needsDigest {
		return segment
	}
	sum := sha256.Sum256([]byte(normalized))
	return segment + "-" + hex.EncodeToString(sum[:8])
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
