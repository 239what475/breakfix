package docsource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

// FormatVersion pins the library content format the reader surface serves.
const FormatVersion = "v1"

// DocumentContext identifies one immutable document scope. The upstream
// revision and fixed page coordinates are the identity; rendered HTML is
// presentation, not an identity input.
//
// ParserVersion and PageDigest extend the context with the offline parser
// identity (library generator_version) and the digest of the parsed page the
// evidence was sliced from. They are evidence attributes, never identity.
type DocumentContext struct {
	FormatVersion string `json:"format_version"`
	SourceID      string `json:"source_id"`
	Repository    string `json:"repository"`
	Commit        string `json:"commit"`
	Version       string `json:"version"`
	Language      string `json:"language"`
	License       string `json:"license"`
	PagePath      string `json:"page_path"`
	Anchor        string `json:"anchor,omitempty"`
	ParserVersion string `json:"parser_version,omitempty"`
	PageDigest    string `json:"page_digest,omitempty"`
}

func (c DocumentContext) Validate() error {
	if c.FormatVersion != FormatVersion || strings.TrimSpace(c.SourceID) == "" || strings.TrimSpace(c.Repository) == "" ||
		strings.TrimSpace(c.Commit) == "" || strings.TrimSpace(c.Version) == "" || strings.TrimSpace(c.Language) == "" ||
		strings.TrimSpace(c.License) == "" {
		return errors.New("document context requires a complete pinned source")
	}
	if err := ValidateRelativePath(c.PagePath); err != nil {
		return fmt.Errorf("document context page path: %w", err)
	}
	if (strings.TrimSpace(c.ParserVersion) == "") != (strings.TrimSpace(c.PageDigest) == "") {
		return errors.New("document context parser version and page digest must be bound together")
	}
	if c.PageDigest != "" && !runnable.ValidDigest(c.PageDigest) {
		return errors.New("document context page digest is not a valid sha256 digest")
	}
	return nil
}

func (c DocumentContext) Digest() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:]), nil
}

// Page is one anchored slice of a parsed page: the context it was read under,
// the page coordinates, and the digest-pinned content.
type Page struct {
	Context DocumentContext `json:"context"`
	Path    string          `json:"path"`
	Anchor  string          `json:"anchor,omitempty"`
	Content string          `json:"content"`
	Digest  string          `json:"digest"`
}

// Metadata is the anchor index of one parsed page.
type Metadata struct {
	Context DocumentContext `json:"context"`
	Path    string          `json:"path"`
	Title   string          `json:"title"`
	Anchors []string        `json:"anchors"`
	Digest  string          `json:"digest"`
}

func ValidateRelativePath(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || path.IsAbs(value) || path.Clean(value) != value || value == "." || strings.HasPrefix(value, "../") || strings.Contains(value, "\\") {
		return errors.New("path must be a safe relative path")
	}
	return nil
}
