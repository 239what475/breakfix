// Package documentation models the admin-maintained, globally shared list of
// externally hosted documentation sites behind the /documentation aggregation
// page. Entries carry no content: the server only stores where a browser
// should go and whether the target is known to allow iframe embedding.
package doclinks

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// ErrLinkInvalid marks input that violates the write rules. The HTTP layer
// maps it to a 400; the repository enforces the same rules on its way in.
var ErrLinkInvalid = errors.New("documentation link is invalid")

// Link is one entry of the documentation aggregation list.
type Link struct {
	Key       string    `json:"key"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Embed     bool      `json:"embed"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate enforces the write-path rules: a non-empty title and an absolute
// http(s) URL.
func (l Link) Validate() error {
	if strings.TrimSpace(l.Title) == "" {
		return fmt.Errorf("%w: title is required", ErrLinkInvalid)
	}
	if err := validateURL(l.URL); err != nil {
		return fmt.Errorf("%w: %s", ErrLinkInvalid, err)
	}
	return nil
}

// validateURL accepts only absolute http and https addresses; the aggregation
// page renders everything else either in an iframe or as an external link.
func validateURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("URL must be an absolute http or https address")
	}
	return nil
}

// NewKey generates the short random primary key. Keys are random rather than
// derived from the title so renaming a link never invalidates ?doc=<key>
// references.
func NewKey() (string, error) {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate documentation link key: %w", err)
	}
	return "doc-" + hex.EncodeToString(buf), nil
}
