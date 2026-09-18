// Package documentation provides the constrained, read-only source and page
// access used by documentation agents. It never performs network requests.
package docsource

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
	"strings"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"golang.org/x/net/html"
)

const (
	MaxReadBytes         = 256 * 1024
	MaxRenderedPageBytes = 2 * 1024 * 1024
)

type Snapshot struct {
	Context    domain.DocumentContext
	Root       string
	SourceRoot string
}

// NewPinnedSnapshot verifies the fixed source identity in build-info. The
// rendered tree is an input for evidence reads only; it is never hashed as a
// snapshot identity.
func NewPinnedSnapshot(expected domain.DocumentContext, root, sourceRoot string) (Snapshot, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(sourceRoot) == "" {
		return Snapshot{}, errors.New("documentation rendered and source roots are required")
	}
	if err := expected.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("documentation pinned context: %w", err)
	}
	infoBytes, err := os.ReadFile(filepath.Join(filepath.Clean(root), "build-info.json"))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read documentation build-info: %w", err)
	}
	if len(infoBytes) == 0 || len(infoBytes) > MaxReadBytes {
		return Snapshot{}, errors.New("documentation build-info exceeds the read limit")
	}
	var info struct {
		Source     string `json:"source"`
		Repository string `json:"repository"`
		Revision   string `json:"revision"`
		Version    string `json:"version"`
		Locale     string `json:"locale"`
	}
	decoder := json.NewDecoder(bytes.NewReader(infoBytes))
	if err := decoder.Decode(&info); err != nil {
		return Snapshot{}, fmt.Errorf("decode documentation build-info: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Snapshot{}, errors.New("documentation build-info contains multiple values")
	}
	if info.Source != expected.SourceID || info.Repository != expected.Repository || info.Revision != expected.Commit || info.Version != expected.Version || info.Locale != expected.Language {
		return Snapshot{}, errors.New("documentation build-info does not match the configured pinned source")
	}
	snapshot := Snapshot{Context: expected, Root: filepath.Clean(root), SourceRoot: filepath.Clean(sourceRoot)}
	if _, err := snapshot.requireDirectory(snapshot.Root, "documentation snapshot root"); err != nil {
		return Snapshot{}, err
	}
	if _, err := snapshot.requireDirectory(snapshot.SourceRoot, "documentation source root"); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func NewSnapshot(ctx domain.DocumentContext, root, sourceRoot string) (Snapshot, error) {
	if err := ctx.Validate(); err != nil {
		return Snapshot{}, err
	}
	if strings.TrimSpace(root) == "" || strings.TrimSpace(sourceRoot) == "" {
		return Snapshot{}, errors.New("documentation rendered and source roots are required")
	}
	snapshot := Snapshot{Context: ctx, Root: filepath.Clean(root), SourceRoot: filepath.Clean(sourceRoot)}
	if _, err := snapshot.requireDirectory(snapshot.Root, "documentation snapshot root"); err != nil {
		return Snapshot{}, err
	}
	if _, err := snapshot.requireDirectory(snapshot.SourceRoot, "documentation source root"); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

type Page = domain.Page
type Metadata = domain.Metadata
type SourceFragment = domain.SourceFragment

func (s Snapshot) ReadPage(path, anchor string) (Page, error) {
	if path != s.Context.PagePath || anchor != s.Context.Anchor {
		return Page{}, errors.New("documentation page is outside the pinned context")
	}
	content, digest, err := s.pageEvidence(path, anchor)
	if err != nil {
		return Page{}, err
	}
	return Page{Context: s.Context, Path: path, Anchor: anchor, Content: content, Digest: digest}, nil
}

// pageEvidence returns the fixed anchor's normalized text. This is the only
// rendered content an Agent receives, so markup, site chrome, and unrelated
// sections cannot change the evidence identity.
func (s Snapshot) pageEvidence(path, anchor string) (string, string, error) {
	limit := int64(MaxReadBytes)
	if strings.HasSuffix(strings.ToLower(path), ".html") {
		limit = MaxRenderedPageBytes
	}
	content, _, err := s.readFromLimit(s.Root, path, limit)
	if err != nil {
		return "", "", err
	}
	if strings.HasSuffix(strings.ToLower(path), ".html") {
		content, err = renderedSectionText(content, anchor)
		if err != nil {
			return "", "", err
		}
	} else {
		content, err = markdownSectionText(content, anchor)
		if err != nil {
			return "", "", err
		}
	}
	return content, evidenceDigest(content), nil
}

func (s Snapshot) ReadMetadata(path string) (Metadata, error) {
	if path != s.Context.PagePath {
		return Metadata{}, errors.New("documentation page is outside the pinned context")
	}
	content, err := s.pageMarkup(path)
	if err != nil {
		return Metadata{}, err
	}
	title, anchors := headings(content, strings.HasSuffix(strings.ToLower(path), ".html"))
	return Metadata{Context: s.Context, Path: path, Title: title, Anchors: anchors, Digest: evidenceDigest(content)}, nil
}

func (s Snapshot) pageMarkup(path string) (string, error) {
	limit := int64(MaxReadBytes)
	if strings.HasSuffix(strings.ToLower(path), ".html") {
		limit = MaxRenderedPageBytes
	}
	content, _, err := s.readFromLimit(s.Root, path, limit)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(strings.ToLower(path), ".html") {
		return renderedMainContent(content)
	}
	return content, nil
}

func headings(content string, htmlDocument bool) (string, []string) {
	if !htmlDocument {
		var title string
		var anchors []string
		for _, line := range strings.Split(content, "\n") {
			trimmed := strings.TrimSpace(line)
			if title == "" && strings.HasPrefix(trimmed, "# ") {
				title = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			}
			if strings.HasPrefix(trimmed, "#") {
				value := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
				if value != "" {
					anchors = append(anchors, slug(value))
				}
			}
		}
		return title, anchors
	}
	document, err := html.Parse(strings.NewReader(content))
	if err != nil {
		return "", nil
	}
	var title string
	anchors := []string{}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && len(node.Data) == 2 && node.Data[0] == 'h' && node.Data[1] >= '1' && node.Data[1] <= '6' {
			text := strings.TrimSpace(htmlText(node))
			if title == "" && node.Data == "h1" {
				title = text
			}
			if text != "" {
				for _, attribute := range node.Attr {
					if attribute.Key == "id" && strings.TrimSpace(attribute.Val) != "" {
						anchors = append(anchors, strings.TrimSpace(attribute.Val))
						break
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	return title, anchors
}

func htmlText(node *html.Node) string {
	var result strings.Builder
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.TextNode {
			result.WriteString(current.Data)
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(result.String()), " ")
}

func (s Snapshot) ReadSource(path string, startLine, endLine int) (SourceFragment, error) {
	return s.readFragment(s.SourceRoot, path, domain.EvidenceSource, startLine, endLine)
}
func (s Snapshot) ReadInclude(path string, startLine, endLine int) (SourceFragment, error) {
	return s.readFragment(s.SourceRoot, path, domain.EvidenceInclude, startLine, endLine)
}

func (s Snapshot) readFragment(root, path string, kind domain.EvidenceKind, start, end int) (SourceFragment, error) {
	content, digest, err := s.readFromLimit(root, path, MaxReadBytes)
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

func (s Snapshot) readFromLimit(root, path string, limit int64) (string, string, error) {
	return readRootFileVerified(root, path, limit)
}

func (s Snapshot) requireDirectory(root, description string) (os.FileInfo, error) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", description)
	}
	return info, nil
}

func evidenceDigest(content string) string {
	h := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(h[:])
}

// renderedMainContent excludes site chrome before metadata or a page section
// is read. It does not contribute to DocumentContext identity.
func renderedMainContent(content string) (string, error) {
	document, err := html.Parse(strings.NewReader(content))
	if err != nil {
		return "", errors.New("documentation rendered page is not valid HTML")
	}
	main := findMain(document)
	if main == nil {
		return "", errors.New("documentation rendered page has no main content")
	}
	var body bytes.Buffer
	if err := html.Render(&body, main); err != nil {
		return "", err
	}
	if body.Len() > MaxReadBytes {
		return "", errors.New("documentation rendered page body exceeds read limit")
	}
	return body.String(), nil
}

func renderedSectionText(content, anchor string) (string, error) {
	document, err := html.Parse(strings.NewReader(content))
	if err != nil {
		return "", errors.New("documentation rendered page is not valid HTML")
	}
	main := findMain(document)
	if main == nil {
		return "", errors.New("documentation rendered page has no main content")
	}
	if strings.TrimSpace(anchor) == "" {
		return htmlText(main), nil
	}
	heading := findHeading(main, anchor)
	if heading == nil {
		return "", errors.New("documentation anchor is absent from the rendered page")
	}
	level := headingLevel(heading)
	var text strings.Builder
	collecting := false
	stopped := false
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if stopped {
			return
		}
		if node.Type == html.ElementNode && headingLevel(node) > 0 {
			if node == heading {
				collecting = true
			} else if collecting && headingLevel(node) <= level {
				stopped = true
				return
			}
		}
		if collecting && node.Type == html.TextNode {
			text.WriteString(node.Data)
			text.WriteByte(' ')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(main)
	section := strings.Join(strings.Fields(text.String()), " ")
	if section == "" {
		return "", errors.New("documentation anchor section has no text")
	}
	return section, nil
}

func markdownSectionText(content, anchor string) (string, error) {
	if strings.TrimSpace(anchor) == "" {
		return content, nil
	}
	lines := strings.Split(content, "\n")
	start := -1
	level := 0
	for index, line := range lines {
		candidateLevel, title := markdownHeading(line)
		if candidateLevel > 0 && slug(title) == anchor {
			start, level = index, candidateLevel
			break
		}
	}
	if start < 0 {
		return "", errors.New("documentation anchor is absent from the page")
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		candidateLevel, _ := markdownHeading(lines[index])
		if candidateLevel > 0 && candidateLevel <= level {
			end = index
			break
		}
	}
	return strings.Join(lines[start:end], "\n"), nil
}

func markdownHeading(line string) (int, string) {
	trimmed := strings.TrimLeft(line, " ")
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level >= len(trimmed) || trimmed[level] != ' ' {
		return 0, ""
	}
	return level, strings.TrimSpace(trimmed[level:])
}

func findMain(document *html.Node) *html.Node {
	var main *html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if main != nil {
			return
		}
		if node.Type == html.ElementNode && node.Data == "main" {
			main = node
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	return main
}

func findHeading(root *html.Node, anchor string) *html.Node {
	var heading *html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if heading != nil {
			return
		}
		if node.Type == html.ElementNode && headingLevel(node) > 0 {
			for _, attribute := range node.Attr {
				if attribute.Key == "id" && strings.TrimSpace(attribute.Val) == anchor {
					heading = node
					return
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return heading
}

func headingLevel(node *html.Node) int {
	if node == nil || node.Type != html.ElementNode || len(node.Data) != 2 || node.Data[0] != 'h' || node.Data[1] < '1' || node.Data[1] > '6' {
		return 0
	}
	return int(node.Data[1] - '0')
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
