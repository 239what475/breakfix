package challenge

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

var revisionIDPattern = regexp.MustCompile(`^chrev-[a-f0-9]{8,64}$`)

// ValidRevisionID validates the platform-owned opaque identity of one
// published Challenge revision. It is deliberately distinct from the sha256
// content and materialization revisions.
func ValidRevisionID(value string) bool {
	return revisionIDPattern.MatchString(strings.TrimSpace(value))
}

func NewRevisionID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "chrev-" + hex.EncodeToString([]byte(time.Now().UTC().Format("20060102150405.000000000")))
	}
	return "chrev-" + hex.EncodeToString(raw[:])
}

// MaterializedPath keeps every revision in its own immutable directory while
// retaining a readable challenge slug as the first path component.
func MaterializedPath(sourceSlug, revisionID string) string {
	if !ValidSourceSlug(sourceSlug) || !ValidRevisionID(revisionID) {
		return ""
	}
	return path.Join(sourceSlug, revisionID)
}

func ValidateMaterializedPath(value, sourceSlug, revisionID string) error {
	expected := MaterializedPath(sourceSlug, revisionID)
	if expected == "" || value != expected {
		return fmt.Errorf("materialized path must be %q", expected)
	}
	return nil
}
