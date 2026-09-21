// Package documentation provides the constrained, read-only library access
// used by documentation agents. It never performs network requests and never
// parses rendered HTML: parsing happened offline in docs-project, and this
// adapter only verifies digests and slices the parsed output.
package docsource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/breakfix/breakfix/internal/docsproject"
)

// MaxLibraryManifestBytes bounds the global manifest and page manifests read
// while verifying a library. Page markdown keeps the rendered-page limit.
const MaxLibraryManifestBytes = 8 * 1024 * 1024

// MaxRenderedPageBytes bounds one parsed page's markdown.
const MaxRenderedPageBytes = 2 * 1024 * 1024

// LibraryIdentity is the upstream identity a library must prove before any
// page is served. The library carries its own copy; the deployment pins the
// identity, never a page.
type LibraryIdentity struct {
	SourceID   string
	Repository string
	Commit     string
	Version    string
	Language   string
	License    string
}

// Library reads the pinned page from an offline generated document library
// (docs-project output). The library carries its own generator identity; the
// server build does not assert one.
type Library struct {
	Context    DocumentContext
	Root       string
	global     docsproject.GlobalManifest
	assetIndex map[string]string
	titleIndex map[string]string
	corpus     map[string]CorpusPage
}

// NewPinnedLibrary verifies the global manifest against the pinned upstream
// identity before any page is served. The deployment no longer pins a page:
// every page of the opened library is readable through the digest-verified
// accessors.
func NewPinnedLibrary(identity LibraryIdentity, libraryRoot string) (Library, error) {
	if strings.TrimSpace(libraryRoot) == "" {
		return Library{}, errors.New("documentation library root is required")
	}
	expected := DocumentContext{
		FormatVersion: FormatVersion, SourceID: identity.SourceID, Repository: identity.Repository,
		Commit: identity.Commit, Version: identity.Version, Language: identity.Language, License: identity.License,
	}
	if expected.SourceID == "" || expected.Repository == "" || expected.Commit == "" || expected.Version == "" || expected.Language == "" || expected.License == "" {
		return Library{}, errors.New("documentation library identity is incomplete")
	}
	library := Library{Context: expected, Root: filepath.Clean(libraryRoot)}
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
	library.global = global
	if err := library.buildAssetIndex(); err != nil {
		return Library{}, err
	}
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
	normalized := strings.TrimSuffix(strings.TrimSpace(path), "/")
	if !libraryContains(l.global.Pages, normalized) {
		return Page{}, fmt.Errorf("documentation page %q is not part of the library", normalized)
	}
	markdown, manifest, err := l.verifiedPage(normalized)
	if err != nil {
		return Page{}, err
	}
	context := l.evidenceContext(manifest)
	context.PagePath = normalized
	context.Anchor = anchor
	if strings.TrimSpace(anchor) == "" {
		return Page{Context: context, Path: normalized, Content: markdown, Digest: manifest.Digest}, nil
	}
	section, entry, err := l.anchorSection(manifest, []byte(markdown), anchor)
	if err != nil {
		return Page{}, err
	}
	return Page{Context: context, Path: normalized, Anchor: anchor, Content: string(section), Digest: entry.Digest}, nil
}

// evidenceContext extends the pinned upstream context with the evidence
// triple attributes: the offline parser version and the parsed page digest.
func (l Library) evidenceContext(manifest docsproject.PageManifest) DocumentContext {
	context := l.Context
	context.ParserVersion = manifest.GeneratorVersion
	context.PageDigest = manifest.Digest
	return context
}

// Identity exposes the opened library's generator identity for diagnostics.
// The upstream commit is the one pinned inside the library, not the config.
func (l Library) Identity() (parserVersion, upstreamCommit string) {
	return l.global.GeneratorVersion, l.global.Upstream.Commit
}

// PinnedContext returns the verified source/commit/language identity that
// reader queries (such as the published practices index) must be scoped to.
func (l Library) PinnedContext() DocumentContext {
	return l.Context
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

// MaxLibraryAssetBytes bounds one static asset served from the library.
const MaxLibraryAssetBytes = 16 * 1024 * 1024

// DocumentAnchor is one parsed heading exposed to the reader.
type DocumentAnchor struct {
	ID    string `json:"id"`
	Level int    `json:"level"`
	Title string `json:"title"`
}

// DocumentAsset is one static asset referenced by a parsed page.
type DocumentAsset struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// DocumentPage is the parsed page served to the reader. Markdown is the
// offline-parsed output whose bytes the page digest covers.
type DocumentPage struct {
	Path     string           `json:"path"`
	PageKind string           `json:"page_kind"`
	Title    string           `json:"title"`
	Digest   string           `json:"digest"`
	Markdown string           `json:"markdown"`
	Anchors  []DocumentAnchor `json:"anchors"`
	Assets   []DocumentAsset  `json:"assets"`
}

// DocumentTreeChild is one node of the library tree for lazy navigation.
type DocumentTreeChild struct {
	Title       string `json:"title"`
	Path        string `json:"path"`
	HasChildren bool   `json:"has_children"`
}

// CorpusPage is one library page's batch-relevant summary: the path, its
// manifest title and kind, and its manifest anchors. Batch scope resolution
// reads these instead of re-reading page manifests.
type CorpusPage struct {
	Path     string           `json:"path"`
	Title    string           `json:"title"`
	PageKind string           `json:"page_kind"`
	Anchors  []DocumentAnchor `json:"anchors"`
}

// FirstLevel2Anchor returns the first level-2 anchor of the page in manifest
// order, or false when the page has none.
func (p CorpusPage) FirstLevel2Anchor() (DocumentAnchor, bool) {
	for _, anchor := range p.Anchors {
		if anchor.Level == 2 {
			return anchor, true
		}
	}
	return DocumentAnchor{}, false
}

// CorpusPages returns every page of the library ordered by path.
func (l Library) CorpusPages() []CorpusPage {
	pages := make([]CorpusPage, 0, len(l.corpus))
	for _, page := range l.corpus {
		pages = append(pages, page)
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Path < pages[j].Path })
	return pages
}

// HasSection reports whether the library tree contains one exact node path.
func (l Library) HasSection(section string) bool {
	normalized := strings.TrimSuffix(strings.TrimSpace(section), "/")
	_, found := findTreeNode(l.global.Tree.Nodes, normalized)
	return found
}

// ReadDocumentPage serves any page of the library after verifying its digest.
// Unlike ReadPage it is not bound to the agent pipeline's single pinned page:
// the whole library is the deployment-pinned corpus.
func (l Library) ReadDocumentPage(path string) (DocumentPage, error) {
	normalized := strings.TrimSuffix(strings.TrimSpace(path), "/")
	if !libraryContains(l.global.Pages, normalized) {
		return DocumentPage{}, fmt.Errorf("documentation page %q is not part of the library", normalized)
	}
	markdown, manifest, err := l.verifiedPage(normalized)
	if err != nil {
		return DocumentPage{}, err
	}
	page := DocumentPage{
		Path: normalized, PageKind: manifest.PageKind, Title: manifest.Title,
		Digest: manifest.Digest, Markdown: markdown,
		Anchors: make([]DocumentAnchor, 0, len(manifest.Anchors)),
		Assets:  make([]DocumentAsset, 0, len(manifest.Assets)),
	}
	for _, anchor := range manifest.Anchors {
		page.Anchors = append(page.Anchors, DocumentAnchor{ID: anchor.ID, Level: anchor.Level, Title: anchor.Title})
	}
	for _, asset := range manifest.Assets {
		page.Assets = append(page.Assets, DocumentAsset{Path: strings.TrimSuffix(asset.Path, "/"), Digest: asset.Digest})
	}
	return page, nil
}

// ReadDocumentTree returns the children of one tree node, or the top-level
// sections for an empty path. The tree mirrors the upstream sidebar structure.
func (l Library) ReadDocumentTree(path string) ([]DocumentTreeChild, error) {
	normalized := strings.TrimSuffix(strings.TrimSpace(path), "/")
	var children []docsproject.TreeNode
	if normalized == "" {
		children = l.global.Tree.Nodes
	} else {
		node, found := findTreeNode(l.global.Tree.Nodes, normalized)
		if !found {
			return nil, fmt.Errorf("documentation tree node %q is not part of the library", normalized)
		}
		children = node.Children
	}
	result := make([]DocumentTreeChild, 0, len(children))
	for _, child := range children {
		result = append(result, DocumentTreeChild{
			Title: child.Title, Path: strings.TrimSuffix(child.Path, "/"), HasChildren: len(child.Children) > 0,
		})
	}
	return result, nil
}

func findTreeNode(nodes []docsproject.TreeNode, path string) (*docsproject.TreeNode, bool) {
	for index := range nodes {
		if strings.TrimSuffix(nodes[index].Path, "/") == path {
			return &nodes[index], true
		}
		if found, ok := findTreeNode(nodes[index].Children, path); ok {
			return found, true
		}
	}
	return nil, false
}

// ReadDocumentAsset serves one static asset after verifying the digest pinned
// by the page manifests that reference it.
func (l Library) ReadDocumentAsset(path string) ([]byte, string, error) {
	normalized := strings.TrimSuffix(strings.TrimSpace(path), "/")
	expected, found := l.assetIndex[normalized]
	if !found {
		return nil, "", fmt.Errorf("documentation asset %q is not part of the library", normalized)
	}
	content, digest, err := readRootFileVerified(l.Root, normalized, MaxLibraryAssetBytes)
	if err != nil {
		return nil, "", err
	}
	if digest != expected {
		return nil, "", errors.New("documentation asset bytes do not match the pinned asset digest")
	}
	return []byte(content), assetContentType(normalized), nil
}

func assetContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

// buildAssetIndex indexes every page manifest's assets and titles once so
// asset requests can be digest-verified and admin lists can resolve page
// titles without a corpus scan per request.
func (l *Library) buildAssetIndex() error {
	index := make(map[string]string)
	titles := make(map[string]string)
	corpus := make(map[string]CorpusPage)
	for _, pagePath := range l.global.Pages {
		manifestBytes, err := readRootFile(l.Root, filepath.ToSlash(filepath.Join(pagePath, "index.json")), MaxLibraryManifestBytes)
		if err != nil {
			return fmt.Errorf("read page manifest for asset index: %w", err)
		}
		manifest, err := docsproject.DecodePageManifest([]byte(manifestBytes))
		if err != nil {
			return fmt.Errorf("decode page manifest for asset index: %w", err)
		}
		normalized := strings.TrimSuffix(manifest.Path, "/")
		for _, asset := range manifest.Assets {
			index[strings.TrimSuffix(asset.Path, "/")] = asset.Digest
		}
		titles[normalized] = manifest.Title
		anchors := make([]DocumentAnchor, 0, len(manifest.Anchors))
		for _, anchor := range manifest.Anchors {
			anchors = append(anchors, DocumentAnchor{ID: anchor.ID, Level: anchor.Level, Title: anchor.Title})
		}
		corpus[normalized] = CorpusPage{Path: normalized, Title: manifest.Title, PageKind: manifest.PageKind, Anchors: anchors}
	}
	l.assetIndex = index
	l.titleIndex = titles
	l.corpus = corpus
	return nil
}

// DocumentPageTitle resolves one library page's title from the page manifests
// collected at open time. A page outside the library resolves to an empty
// title: the admin list labels workflows with whatever the library knows.
func (l Library) DocumentPageTitle(path string) string {
	normalized := strings.TrimSuffix(strings.TrimSpace(path), "/")
	return l.titleIndex[normalized]
}

// DocumentTitles lists every library page path with its manifest title. The
// map is a snapshot copy; callers must not mutate it expecting library
// changes.
func (l Library) DocumentTitles() map[string]string {
	titles := make(map[string]string, len(l.titleIndex))
	for path, title := range l.titleIndex {
		titles[path] = title
	}
	return titles
}

func (l Library) ReadMetadata(path string) (Metadata, error) {
	normalized := strings.TrimSuffix(strings.TrimSpace(path), "/")
	if !libraryContains(l.global.Pages, normalized) {
		return Metadata{}, fmt.Errorf("documentation page %q is not part of the library", normalized)
	}
	_, manifest, err := l.verifiedPage(normalized)
	if err != nil {
		return Metadata{}, err
	}
	anchors := make([]string, 0, len(manifest.Anchors))
	for _, anchor := range manifest.Anchors {
		anchors = append(anchors, anchor.ID)
	}
	context := l.evidenceContext(manifest)
	context.PagePath = normalized
	return Metadata{Context: context, Path: normalized, Title: manifest.Title, Anchors: anchors, Digest: manifest.Digest}, nil
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
	if manifest.GeneratorVersion != l.global.GeneratorVersion {
		return "", docsproject.PageManifest{}, errors.New("documentation page was produced by a different generator run than the library manifest")
	}
	if evidenceDigest(markdown) != manifest.Digest {
		return "", docsproject.PageManifest{}, errors.New("documentation page bytes do not match the pinned page digest")
	}
	return markdown, manifest, nil
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
	if err := ValidateRelativePath(path); err != nil {
		return "", "", err
	}
	full := filepath.Join(root, filepath.FromSlash(path))
	root, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	// Kubernetes projected volumes (ConfigMap items) expose every key as a
	// symlink into a ..data directory. Follow symlinks, then enforce that the
	// resolved file stays inside the mounted root: a symlink that escapes the
	// root is rejected exactly like a traversal path.
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	absolute, err := filepath.Abs(full)
	if err != nil {
		return "", "", err
	}
	target, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", "", err
	}
	if target != resolvedRoot && !strings.HasPrefix(target, resolvedRoot+string(filepath.Separator)) {
		return "", "", errors.New("documentation path escapes the library root")
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", "", err
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

func evidenceDigest(content string) string {
	h := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(h[:])
}
