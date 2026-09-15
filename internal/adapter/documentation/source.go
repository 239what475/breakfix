// Package documentation provides the constrained, read-only source and page
// access used by documentation agents. It never performs network requests.
package docsource

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
)

const MaxReadBytes = 256 * 1024

type Snapshot struct {
	Context domain.DocumentContext
	Root    string
}

func NewSnapshot(ctx domain.DocumentContext, root string) (Snapshot, error) {
	if err := ctx.Validate(); err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(root) == "" {
		return Snapshot{}, errors.New("documentation snapshot root is required")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return Snapshot{}, errors.New("documentation snapshot root is not a directory")
	}
	return Snapshot{Context: ctx, Root: filepath.Clean(root)}, nil
}

type Page = domain.Page
type Metadata = domain.Metadata
type SourceFragment = domain.SourceFragment

func (s Snapshot) ReadPage(path, anchor string) (Page, error) {
	content, digest, err := s.read(path)
	if err != nil {
		return Page{}, err
	}
	return Page{Context: s.Context, Path: path, Anchor: anchor, Content: content, Digest: digest}, nil
}

func (s Snapshot) ReadMetadata(path string) (Metadata, error) {
	page, err := s.ReadPage(path, "")
	if err != nil {
		return Metadata{}, err
	}
	var title string
	var anchors []string
	for _, line := range strings.Split(page.Content, "\n") {
		trimmed := strings.TrimSpace(line)
		if title == "" && strings.HasPrefix(trimmed, "# ") {
			title = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
		}
		if strings.HasPrefix(trimmed, "#") {
			value := strings.TrimLeft(trimmed, "#")
			value = strings.TrimSpace(value)
			if value != "" {
				anchors = append(anchors, slug(value))
			}
		}
	}
	return Metadata{Context: page.Context, Path: path, Title: title, Anchors: anchors, Digest: page.Digest}, nil
}

func (s Snapshot) ReadSource(path string, startLine, endLine int) (SourceFragment, error) {
	return s.readFragment(path, domain.EvidenceSource, startLine, endLine)
}
func (s Snapshot) ReadInclude(path string, startLine, endLine int) (SourceFragment, error) {
	return s.readFragment(path, domain.EvidenceInclude, startLine, endLine)
}

func (s Snapshot) readFragment(path string, kind domain.EvidenceKind, start, end int) (SourceFragment, error) {
	content, digest, err := s.read(path)
	if err != nil {
		return SourceFragment{}, err
	}
	lines := strings.Split(content, "\n")
	if start == 0 {
		start = 1
	}
	if end == 0 {
		end = len(lines)
	}
	if start < 1 || end < start || end > len(lines) {
		return SourceFragment{}, errors.New("source line range is outside the file")
	}
	fragment := strings.Join(lines[start-1:end], "\n")
	id := fmt.Sprintf("%s-%d-%d", strings.ReplaceAll(strings.TrimSuffix(filepath.ToSlash(path), filepath.Ext(path)), "/", "-"), start, end)
	return SourceFragment{Context: s.Context, Evidence: domain.EvidenceReference{ID: id, Kind: kind, Path: path, Digest: digest, StartLine: start, EndLine: end, Quote: fragment}, Content: fragment}, nil
}

func (s Snapshot) read(path string) (string, string, error) {
	if err := domain.ValidateRelativePath(path); err != nil {
		return "", "", err
	}
	full := filepath.Join(s.Root, filepath.FromSlash(path))
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return "", "", err
	}
	target, err := filepath.Abs(full)
	if err != nil {
		return "", "", err
	}
	if target != root && !strings.HasPrefix(target, root+string(filepath.Separator)) {
		return "", "", errors.New("documentation path escapes snapshot root")
	}
	info, err := os.Lstat(target)
	if err != nil {
		return "", "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("documentation symlinks are not readable")
	}
	if !info.Mode().IsRegular() {
		return "", "", errors.New("documentation path is not a regular file")
	}
	if info.Size() > MaxReadBytes {
		return "", "", errors.New("documentation file exceeds read limit")
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return "", "", err
	}
	h := sha256.Sum256(b)
	return string(b), "sha256:" + hex.EncodeToString(h[:]), nil
}

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
