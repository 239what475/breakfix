package docsproject

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const formatVersion = 1

type Upstream struct {
	Source  string `json:"source"`
	Commit  string `json:"commit"`
	Version string `json:"version"`
	Locale  string `json:"locale"`
}

type Anchor struct {
	ID     string `json:"id"`
	Level  int    `json:"level"`
	Title  string `json:"title"`
	Digest string `json:"digest"`
	Parent string `json:"parent"`
}

type PageManifest struct {
	FormatVersion    int            `json:"format_version"`
	GeneratorVersion string         `json:"generator_version"`
	Upstream         Upstream       `json:"upstream"`
	Path             string         `json:"path"`
	PageKind         string         `json:"page_kind"`
	Title            string         `json:"title"`
	Digest           string         `json:"digest"`
	Anchors          []Anchor       `json:"anchors"`
	FeatureStates    []FeatureState `json:"feature_states"`
	Assets           []Asset        `json:"assets"`
	Links            LinkStats      `json:"links"`
	CodeBlocks       int            `json:"code_blocks"`
	DegradedTables   int            `json:"degraded_tables"`
	DroppedElements  int            `json:"dropped_elements"`
}

type GlobalManifest struct {
	FormatVersion    int             `json:"format_version"`
	GeneratorVersion string          `json:"generator_version"`
	Upstream         Upstream        `json:"upstream"`
	BuildInfo        json.RawMessage `json:"build_info"`
	SiteOrigin       string          `json:"site_origin"`
	Tree             Tree            `json:"tree"`
	Pages            []string        `json:"pages"`
	Orphans          []string        `json:"orphans"`
	Redirects        Redirects       `json:"redirects"`
	Stats            Stats           `json:"stats"`
	Warnings         []string        `json:"warnings,omitempty"`
}

type Redirects struct {
	Count  int    `json:"count"`
	Digest string `json:"digest"`
}

type Stats struct {
	Pages        int `json:"pages"`
	IndexPages   int `json:"index_pages"`
	Anchors      int `json:"anchors"`
	Assets       int `json:"assets"`
	AssetsCopied int `json:"assets_copied"`
}

type FailureReport struct {
	FormatVersion    int           `json:"format_version"`
	GeneratorVersion string        `json:"generator_version"`
	Failures         []PageFailure `json:"failures"`
}

type PageFailure struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

type pageFailuresError struct {
	count int
}

func (e *pageFailuresError) Error() string {
	return fmt.Sprintf("%d projection failures; see report.json", e.count)
}

type upstreamBuildInfo struct {
	Source   string `json:"source"`
	Revision string `json:"revision"`
	Version  string `json:"version"`
	Locale   string `json:"locale"`
}

func runProjection(config Config, state treeState) error {
	normalization, err := loadNormalization(config.Root, state, config.SiteOrigin)
	if err != nil {
		return &InputError{Err: err}
	}
	upstream, rawBuildInfo, err := loadUpstream(config.Root)
	if err != nil {
		return &InputError{Err: err}
	}
	manifests, failures := projectPages(config, state, normalization, upstream)
	if len(failures) > 0 {
		return reportProjectionFailures(config, failures)
	}
	assetsCopied, failures := copyReferencedAssets(config, manifests)
	if len(failures) > 0 {
		return reportProjectionFailures(config, failures)
	}
	if err := os.Remove(filepath.Join(config.Out, "report.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale failure report: %w", err)
	}
	global, err := newGlobalManifest(config, state, upstream, rawBuildInfo, normalization, manifests, assetsCopied)
	if err != nil {
		return err
	}
	encoded, err := marshalJSON(global)
	if err != nil {
		return fmt.Errorf("encode global manifest: %w", err)
	}
	return writeAtomically(filepath.Join(config.Out, "manifest.json"), encoded)
}

func reportProjectionFailures(config Config, failures []PageFailure) error {
	if err := writeFailureReport(config.Out, config.Version, failures); err != nil {
		return fmt.Errorf("write failure report: %w", err)
	}
	_ = os.Remove(filepath.Join(config.Out, "manifest.json"))
	return &pageFailuresError{count: len(failures)}
}

type pageResult struct {
	path     string
	manifest PageManifest
	err      error
}

func projectPages(config Config, state treeState, normalization normalizationContext, upstream Upstream) ([]PageManifest, []PageFailure) {
	indexPages := indexPageSet(state.Tree)
	jobs := make(chan string)
	results := make(chan pageResult, len(state.Pages))
	var workers sync.WaitGroup
	for worker := 0; worker < config.Workers; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for pagePath := range jobs {
				manifest, err := projectPage(config, normalization, upstream, indexPages, pagePath)
				results <- pageResult{path: pagePath, manifest: manifest, err: err}
			}
		}()
	}
	go func() {
		for _, pagePath := range state.Pages {
			jobs <- pagePath
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()
	byPath := make(map[string]PageManifest, len(state.Pages))
	var failures []PageFailure
	for result := range results {
		if result.err != nil {
			failures = append(failures, PageFailure{Path: result.path, Error: result.err.Error()})
			continue
		}
		byPath[result.path] = result.manifest
	}
	manifests := make([]PageManifest, 0, len(byPath))
	for _, pagePath := range state.Pages {
		if manifest, found := byPath[pagePath]; found {
			manifests = append(manifests, manifest)
		}
	}
	sort.Slice(failures, func(left, right int) bool { return failures[left].Path < failures[right].Path })
	return manifests, failures
}

func projectPage(config Config, normalization normalizationContext, upstream Upstream, indexPages map[string]struct{}, pagePath string) (PageManifest, error) {
	if config.Resume {
		if existing, ok := existingPage(config.Out, pagePath, config.Version); ok {
			return existing, nil
		}
	}
	content, err := os.ReadFile(pageFile(config.Root, pagePath))
	if err != nil {
		return PageManifest{}, fmt.Errorf("read: %w", err)
	}
	extracted, err := extractPageWithNormalizer(content, normalization.forPage(pagePath))
	if err != nil {
		return PageManifest{}, fmt.Errorf("extract: %w", err)
	}
	pageKind := "content"
	if _, index := indexPages[pagePath]; index {
		pageKind = "index"
	}
	manifest := newPageManifest(config.Version, upstream, pagePath, pageKind, extracted)
	if err := writePage(config.Out, pagePath, extracted.Markdown, manifest); err != nil {
		return PageManifest{}, fmt.Errorf("write: %w", err)
	}
	return manifest, nil
}

func existingPage(out, pagePath, version string) (PageManifest, bool) {
	directory := filepath.Join(out, filepath.FromSlash(pagePath))
	content, err := os.ReadFile(filepath.Join(directory, "index.json"))
	if err != nil {
		return PageManifest{}, false
	}
	manifest, err := DecodePageManifest(content)
	if err != nil {
		return PageManifest{}, false
	}
	if manifest.GeneratorVersion != version || manifest.Path != pagePath {
		return PageManifest{}, false
	}
	markdown, err := os.ReadFile(filepath.Join(directory, "index.md"))
	if err != nil || manifest.Digest != digest(markdown) {
		return PageManifest{}, false
	}
	return manifest, true
}

func writeFailureReport(out, version string, failures []PageFailure) error {
	content, err := marshalJSON(FailureReport{FormatVersion: formatVersion, GeneratorVersion: version, Failures: failures})
	if err != nil {
		return err
	}
	return writeAtomically(filepath.Join(out, "report.json"), content)
}

func loadUpstream(root string) (Upstream, json.RawMessage, error) {
	content, err := os.ReadFile(filepath.Join(root, "build-info.json"))
	if err != nil {
		return Upstream{}, nil, fmt.Errorf("read build-info: %w", err)
	}
	var buildInfo upstreamBuildInfo
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&buildInfo); err != nil {
		return Upstream{}, nil, fmt.Errorf("decode build-info: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Upstream{}, nil, errors.New("build-info must contain exactly one JSON value")
	}
	if buildInfo.Source == "" || buildInfo.Revision == "" || buildInfo.Version == "" || buildInfo.Locale == "" {
		return Upstream{}, nil, errors.New("build-info omits required upstream identity")
	}
	return Upstream{Source: buildInfo.Source, Commit: buildInfo.Revision, Version: buildInfo.Version, Locale: buildInfo.Locale}, append(json.RawMessage(nil), content...), nil
}

func newPageManifest(version string, upstream Upstream, pagePath, pageKind string, page ExtractedPage) PageManifest {
	return PageManifest{
		FormatVersion:    formatVersion,
		GeneratorVersion: version,
		Upstream:         upstream,
		Path:             pagePath,
		PageKind:         pageKind,
		Title:            page.Title,
		Digest:           digest(page.Markdown),
		Anchors:          anchorsForMarkdown(page.Markdown, page.Headings),
		FeatureStates:    page.FeatureStates,
		Assets:           page.Assets,
		Links:            page.Links,
		CodeBlocks:       page.CodeBlocks,
		DegradedTables:   page.DegradedTables,
		DroppedElements:  page.DroppedElements,
	}
}

func anchorsForMarkdown(markdown []byte, headings []ExtractedHeading) []Anchor {
	type locatedHeading struct {
		ExtractedHeading
		start int
	}
	located := make([]locatedHeading, 0, len(headings))
	cursor := 0
	for _, heading := range headings {
		prefix := bytes.Repeat([]byte("#"), heading.Level)
		needle := append(append(prefix, ' '), []byte(heading.Title)...)
		index := bytes.Index(markdown[cursor:], needle)
		if index < 0 {
			continue
		}
		start := cursor + index
		located = append(located, locatedHeading{ExtractedHeading: heading, start: start})
		cursor = start + len(needle)
	}
	anchors := make([]Anchor, 0, len(located))
	stack := make([]locatedHeading, 0, 6)
	for index, heading := range located {
		for len(stack) > 0 && stack[len(stack)-1].Level >= heading.Level {
			stack = stack[:len(stack)-1]
		}
		parent := ""
		if len(stack) > 0 {
			parent = stack[len(stack)-1].ID
		}
		end := len(markdown)
		for next := index + 1; next < len(located); next++ {
			if located[next].Level <= heading.Level {
				end = located[next].start
				break
			}
		}
		anchors = append(anchors, Anchor{ID: heading.ID, Level: heading.Level, Title: heading.Title, Digest: digest(markdown[heading.start:end]), Parent: parent})
		stack = append(stack, heading)
	}
	return anchors
}

func markdownAnchorSection(markdown []byte, headings []ExtractedHeading, id string) ([]byte, error) {
	anchors := anchorsForMarkdown(markdown, headings)
	for _, anchor := range anchors {
		if anchor.ID != id {
			continue
		}
		needle := append(append(bytes.Repeat([]byte("#"), anchor.Level), ' '), []byte(anchor.Title)...)
		start := bytes.Index(markdown, needle)
		if start < 0 {
			break
		}
		end := len(markdown)
		for _, next := range anchors {
			if next.Level <= anchor.Level {
				candidate := bytes.Index(markdown[start+len(needle):], append(append(bytes.Repeat([]byte("#"), next.Level), ' '), []byte(next.Title)...))
				if candidate >= 0 {
					end = start + len(needle) + candidate
					break
				}
			}
		}
		return markdown[start:end], nil
	}
	return nil, fmt.Errorf("documentation anchor %q is absent from the page", id)
}

func newGlobalManifest(config Config, state treeState, upstream Upstream, rawBuildInfo json.RawMessage, normalization normalizationContext, pages []PageManifest, assetsCopied int) (GlobalManifest, error) {
	redirectBytes, err := os.ReadFile(filepath.Join(config.Root, "_redirects"))
	if err != nil {
		return GlobalManifest{}, fmt.Errorf("read redirects for manifest: %w", err)
	}
	manifest := GlobalManifest{
		FormatVersion:    formatVersion,
		GeneratorVersion: config.Version,
		Upstream:         upstream,
		BuildInfo:        rawBuildInfo,
		SiteOrigin:       normalization.siteOrigin.String(),
		Tree:             state.Tree,
		Pages:            append([]string(nil), state.Pages...),
		Orphans:          append([]string(nil), state.Orphans...),
		Redirects:        Redirects{Count: normalization.redirects.count, Digest: digest(redirectBytes)},
		Warnings:         append([]string(nil), state.Warnings...),
	}
	sort.Strings(manifest.Pages)
	sort.Strings(manifest.Orphans)
	for _, page := range pages {
		manifest.Stats.Pages++
		if page.PageKind == "index" {
			manifest.Stats.IndexPages++
		}
		manifest.Stats.Anchors += len(page.Anchors)
		manifest.Stats.Assets += len(page.Assets)
	}
	manifest.Stats.AssetsCopied = assetsCopied
	return manifest, nil
}

func writePage(out, pagePath string, markdown []byte, manifest PageManifest) error {
	directory := filepath.Join(out, filepath.FromSlash(pagePath))
	if err := writeAtomically(filepath.Join(directory, "index.md"), markdown); err != nil {
		return err
	}
	encoded, err := marshalJSON(manifest)
	if err != nil {
		return err
	}
	return writeAtomically(filepath.Join(directory, "index.json"), encoded)
}

func writeAtomically(filename string, content []byte) error {
	directory := filepath.Dir(filename)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".docs-project-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, filename)
}

func marshalJSON(value any) ([]byte, error) {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(content, '\n'), nil
}

func digest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func indexPageSet(tree Tree) map[string]struct{} {
	pages := map[string]struct{}{}
	var visit func([]TreeNode)
	visit = func(nodes []TreeNode) {
		for _, node := range nodes {
			if len(node.Children) > 0 {
				pages[node.Path] = struct{}{}
			}
			visit(node.Children)
		}
	}
	visit(tree.Nodes)
	return pages
}

// DecodePageManifest validates the page manifest schema and rejects fields a
// newer generator may have introduced.
func DecodePageManifest(content []byte) (PageManifest, error) {
	var manifest PageManifest
	if err := decodeStrictJSON(content, &manifest); err != nil {
		return PageManifest{}, err
	}
	return manifest, nil
}

// DecodeGlobalManifest validates the global manifest schema and rejects fields
// a newer generator may have introduced.
func DecodeGlobalManifest(content []byte) (GlobalManifest, error) {
	var manifest GlobalManifest
	if err := decodeStrictJSON(content, &manifest); err != nil {
		return GlobalManifest{}, err
	}
	return manifest, nil
}

func decodeStrictJSON(content []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("manifest must contain exactly one JSON value")
	}
	return nil
}
