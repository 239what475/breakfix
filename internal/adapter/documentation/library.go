package docsource

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/breakfix/breakfix/internal/docsproject"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
)

// MaxLibraryManifestBytes bounds the global manifest and page manifests read
// while verifying a library. Page markdown keeps the rendered-page limit.
const MaxLibraryManifestBytes = 8 * 1024 * 1024

// Library reads the pinned page from an offline generated document library
// (docs-project output). Parsing already happened offline; this adapter only
// verifies digests and slices. It never parses rendered HTML.
type Library struct {
	Context    domain.DocumentContext
	Root       string
	SourceRoot string
	global     docsproject.GlobalManifest
}

// NewPinnedLibrary verifies the global manifest against the pinned context
// before any page is served. The library carries its own generator identity;
// the server build does not assert one.
func NewPinnedLibrary(expected domain.DocumentContext, libraryRoot, sourceRoot string) (Library, error) {
	if strings.TrimSpace(libraryRoot) == "" {
		return Library{}, errors.New("documentation library root is required")
	}
	if err := expected.Validate(); err != nil {
		return Library{}, fmt.Errorf("documentation pinned context: %w", err)
	}
	library := Library{Context: expected, Root: filepath.Clean(libraryRoot), SourceRoot: strings.TrimSpace(sourceRoot)}
	if _, err := library.requireDirectory(library.Root, "documentation library root"); err != nil {
		return Library{}, err
	}
	content, err := readRootFile(library.Root, "manifest.json", MaxLibraryManifestBytes)
	if err != nil {
		return Library{}, fmt.Errorf("read documentation library manifest: %w", err)
	}
	global, err := docsproject.DecodeGlobalManifest([]byte(content))
	if err != nil {
		return Library{}, fmt.Errorf("decode documentation library manifest: %w", err)
	}
	if global.FormatVersion != docsproject.FormatVersion {
		return Library{}, fmt.Errorf("documentation library format version %d is unsupported", global.FormatVersion)
	}
	if global.Upstream.Source != expected.SourceID || global.Upstream.Commit != expected.Commit ||
		global.Upstream.Version != expected.Version || global.Upstream.Locale != expected.Language {
		return Library{}, errors.New("documentation library upstream identity does not match the configured pinned source")
	}
	var buildInfo struct {
		Repository string `json:"repository"`
	}
	if err := json.Unmarshal(global.BuildInfo, &buildInfo); err != nil || buildInfo.Repository == "" {
		return Library{}, errors.New("documentation library build-info omits the upstream repository")
	}
	if buildInfo.Repository != expected.Repository {
		return Library{}, errors.New("documentation library build-info repository does not match the configured pinned source")
	}
	if !libraryContains(global.Pages, expected.PagePath) {
		return Library{}, fmt.Errorf("documentation library does not contain the pinned page %q", expected.PagePath)
	}
	library.global = global
	return library, nil
}

func libraryContains(pages []string, pagePath string) bool {
	for _, candidate := range pages {
		if strings.TrimSuffix(candidate, "/") == strings.TrimSuffix(pagePath, "/") {
			return true
		}
	}
	return false
}

func (l Library) ReadPage(path, anchor string) (Page, error) {
	if path != l.Context.PagePath || anchor != l.Context.Anchor {
		return Page{}, errors.New("documentation page is outside the pinned context")
	}
	markdown, manifest, err := l.verifiedPage(path)
	if err != nil {
		return Page{}, err
	}
	if strings.TrimSpace(anchor) == "" {
		return Page{Context: l.Context, Path: path, Content: markdown, Digest: manifest.Digest}, nil
	}
	section, entry, err := l.anchorSection(manifest, []byte(markdown), anchor)
	if err != nil {
		return Page{}, err
	}
	return Page{Context: l.Context, Path: path, Anchor: anchor, Content: string(section), Digest: entry.Digest}, nil
}

// anchorSection returns the markdown slice for one anchor and proves it is the
// slice the offline parser digest pinned. Any slicing ambiguity is a digest
// mismatch, never silently different evidence.
func (l Library) anchorSection(manifest docsproject.PageManifest, markdown []byte, anchor string) ([]byte, docsproject.Anchor, error) {
	headings := make([]docsproject.ExtractedHeading, 0, len(manifest.Anchors))
	var entry *docsproject.Anchor
	for index := range manifest.Anchors {
		candidate := manifest.Anchors[index]
		headings = append(headings, docsproject.ExtractedHeading{ID: candidate.ID, Level: candidate.Level, Title: candidate.Title})
		if candidate.ID == anchor {
			entry = &manifest.Anchors[index]
		}
	}
	if entry == nil {
		return nil, docsproject.Anchor{}, fmt.Errorf("documentation anchor %q is absent from the page manifest", anchor)
	}
	section, err := docsproject.MarkdownAnchorSection(markdown, headings, anchor)
	if err != nil {
		return nil, docsproject.Anchor{}, err
	}
	if evidenceDigest(string(section)) != entry.Digest {
		return nil, docsproject.Anchor{}, errors.New("documentation anchor slice does not match the pinned anchor digest")
	}
	return section, *entry, nil
}

func (l Library) ReadMetadata(path string) (Metadata, error) {
	if path != l.Context.PagePath {
		return Metadata{}, errors.New("documentation page is outside the pinned context")
	}
	_, manifest, err := l.verifiedPage(path)
	if err != nil {
		return Metadata{}, err
	}
	anchors := make([]string, 0, len(manifest.Anchors))
	for _, anchor := range manifest.Anchors {
		anchors = append(anchors, anchor.ID)
	}
	return Metadata{Context: l.Context, Path: path, Title: manifest.Title, Anchors: anchors, Digest: manifest.Digest}, nil
}

// verifiedPage loads one page directory and proves the markdown bytes are the
// bytes the page digest pinned before anything is derived from them.
func (l Library) verifiedPage(path string) (string, docsproject.PageManifest, error) {
	markdown, err := readRootFile(l.Root, filepath.ToSlash(filepath.Join(path, "index.md")), MaxRenderedPageBytes)
	if err != nil {
		return "", docsproject.PageManifest{}, err
	}
	manifestBytes, err := readRootFile(l.Root, filepath.ToSlash(filepath.Join(path, "index.json")), MaxLibraryManifestBytes)
	if err != nil {
		return "", docsproject.PageManifest{}, err
	}
	manifest, err := docsproject.DecodePageManifest([]byte(manifestBytes))
	if err != nil {
		return "", docsproject.PageManifest{}, fmt.Errorf("decode documentation page manifest: %w", err)
	}
	if strings.TrimSuffix(manifest.Path, "/") != strings.TrimSuffix(path, "/") {
		return "", docsproject.PageManifest{}, errors.New("documentation page manifest path does not match the requested page")
	}
	if manifest.FormatVersion != docsproject.FormatVersion {
		return "", docsproject.PageManifest{}, fmt.Errorf("documentation page manifest format version %d is unsupported", manifest.FormatVersion)
	}
	if manifest.Upstream != (docsproject.Upstream{Source: l.Context.SourceID, Commit: l.Context.Commit, Version: l.Context.Version, Locale: l.Context.Language}) {
		return "", docsproject.PageManifest{}, errors.New("documentation page manifest upstream identity does not match the configured pinned source")
	}
	if evidenceDigest(markdown) != manifest.Digest {
		return "", docsproject.PageManifest{}, errors.New("documentation page bytes do not match the pinned page digest")
	}
	return markdown, manifest, nil
}

// ReadSource and ReadInclude still serve upstream source fragments. They are
// scheduled for removal with the runtime source-reading path.
func (l Library) ReadSource(path string, startLine, endLine int) (SourceFragment, error) {
	return l.fragment(domain.EvidenceSource, path, startLine, endLine)
}

func (l Library) ReadInclude(path string, startLine, endLine int) (SourceFragment, error) {
	return l.fragment(domain.EvidenceInclude, path, startLine, endLine)
}

func (l Library) fragment(kind domain.EvidenceKind, path string, start, end int) (SourceFragment, error) {
	if strings.TrimSpace(l.SourceRoot) == "" {
		return SourceFragment{}, errors.New("documentation source root is not configured")
	}
	if _, err := l.requireDirectory(l.SourceRoot, "documentation source root"); err != nil {
		return SourceFragment{}, err
	}
	content, digest, err := readRootFileVerified(l.SourceRoot, path, MaxReadBytes)
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
	return SourceFragment{Context: l.Context, Evidence: domain.EvidenceReference{ID: id, Kind: kind, Path: path, Digest: digest, StartLine: start, EndLine: end, Quote: fragment}, Content: fragment}, nil
}

func (l Library) requireDirectory(root, description string) (os.FileInfo, error) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", description)
	}
	return info, nil
}

// readRootFile reads one safe relative file below root with a byte limit.
func readRootFile(root, path string, limit int64) (string, error) {
	content, _, err := readRootFileVerified(root, path, limit)
	return content, err
}

func readRootFileVerified(root, path string, limit int64) (string, string, error) {
	if err := domain.ValidateRelativePath(path); err != nil {
		return "", "", err
	}
	full := filepath.Join(root, filepath.FromSlash(path))
	root, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	target, err := filepath.Abs(full)
	if err != nil {
		return "", "", err
	}
	if target != root && !strings.HasPrefix(target, root+string(filepath.Separator)) {
		return "", "", errors.New("documentation path escapes the library root")
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
	if info.Size() > limit {
		return "", "", errors.New("documentation file exceeds read limit")
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return "", "", err
	}
	return string(b), evidenceDigest(string(b)), nil
}
