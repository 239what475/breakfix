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
	"net/url"
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
	Context      domain.DocumentContext
	Root         string
	SourceRoot   string
	BuildBaseURL string
}

// NewPinnedSnapshot verifies the fixed source identity in build-info, then
// derives one content digest from the configured page's rendered main element.
// Unrelated pages and site chrome are deliberately outside the context scope.
func NewPinnedSnapshot(expected domain.DocumentContext, root, sourceRoot string) (Snapshot, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(sourceRoot) == "" {
		return Snapshot{}, errors.New("documentation rendered and source roots are required")
	}
	if err := expected.ValidateIdentity(); err != nil {
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
		BaseURL    string `json:"base_url"`
	}
	decoder := json.NewDecoder(bytes.NewReader(infoBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&info); err != nil {
		return Snapshot{}, fmt.Errorf("decode documentation build-info: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Snapshot{}, errors.New("documentation build-info contains multiple values")
	}
	baseURL, err := parseBuildBaseURL(info.BaseURL)
	if err != nil {
		return Snapshot{}, err
	}
	if info.Source != expected.SourceID || info.Repository != expected.Repository || info.Revision != expected.Commit || info.Version != expected.Version || info.Locale != expected.Language {
		return Snapshot{}, errors.New("documentation build-info does not match the configured pinned source")
	}
	snapshot := Snapshot{Context: expected, Root: filepath.Clean(root), SourceRoot: filepath.Clean(sourceRoot), BuildBaseURL: baseURL}
	if _, err := snapshot.requireDirectory(snapshot.Root, "documentation snapshot root"); err != nil {
		return Snapshot{}, err
	}
	if _, err := snapshot.requireDirectory(snapshot.SourceRoot, "documentation source root"); err != nil {
		return Snapshot{}, err
	}
	_, digest, err := snapshot.pageContent(expected.PagePath)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Context.ContentDigest = digest
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
	_, digest, err := snapshot.pageContent(ctx.PagePath)
	if err != nil {
		return Snapshot{}, err
	}
	if digest != ctx.ContentDigest {
		return Snapshot{}, errors.New("documentation page content does not match its pinned digest")
	}
	return snapshot, nil
}

type Page = domain.Page
type Metadata = domain.Metadata
type SourceFragment = domain.SourceFragment

func (s Snapshot) ReadPage(path, anchor string) (Page, error) {
	if path != s.Context.PagePath {
		return Page{}, errors.New("documentation page is outside the pinned context")
	}
	content, digest, err := s.pageContent(path)
	if err != nil {
		return Page{}, err
	}
	if digest != s.Context.ContentDigest {
		return Page{}, errors.New("documentation page content changed after the snapshot was opened")
	}
	return Page{Context: s.Context, Path: path, Anchor: anchor, Content: content, Digest: digest}, nil
}

func (s Snapshot) pageContent(path string) (string, string, error) {
	limit := int64(MaxReadBytes)
	if strings.HasSuffix(strings.ToLower(path), ".html") {
		limit = MaxRenderedPageBytes
	}
	content, _, err := s.readFromLimit(s.Root, path, limit)
	if err != nil {
		return "", "", err
	}
	if strings.HasSuffix(strings.ToLower(path), ".html") {
		content, err = renderedPageContent(content)
		if err != nil {
			return "", "", err
		}
	}
	return content, contentDigest(content), nil
}

func (s Snapshot) ReadMetadata(path string) (Metadata, error) {
	page, err := s.ReadPage(path, "")
	if err != nil {
		return Metadata{}, err
	}
	title, anchors := headings(page.Content, strings.HasSuffix(strings.ToLower(path), ".html"))
	return Metadata{Context: page.Context, Path: path, Title: title, Anchors: anchors, Digest: page.Digest}, nil
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
	if info.Size() > limit {
		return "", "", errors.New("documentation file exceeds read limit")
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return "", "", err
	}
	h := sha256.Sum256(b)
	return string(b), "sha256:" + hex.EncodeToString(h[:]), nil
}

func (s Snapshot) requireDirectory(root, description string) (os.FileInfo, error) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", description)
	}
	return info, nil
}

func contentDigest(content string) string {
	h := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(h[:])
}

// renderedPageContent deliberately exposes the page body, not site chrome or
// scripts. Its bounded main content is the evidence identity.
func renderedPageContent(content string) (string, error) {
	document, err := html.Parse(strings.NewReader(content))
	if err != nil {
		return "", errors.New("documentation rendered page is not valid HTML")
	}
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

func parseBuildBaseURL(value string) (string, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.HasSuffix(parsed.Path, "/") {
		return "", errors.New("documentation build-info has an invalid base_url")
	}
	return parsed.String(), nil
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
