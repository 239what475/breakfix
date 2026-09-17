package docsproject

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type Asset struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type LinkStats struct {
	Internal     int      `json:"internal"`
	PageInternal int      `json:"page_internal"`
	External     int      `json:"external"`
	ToOrphans    int      `json:"to_orphans"`
	OutOfTree    []string `json:"out_of_tree"`
	Dropped      int      `json:"dropped"`
}

type normalizationContext struct {
	root       string
	baseURL    *url.URL
	siteOrigin *url.URL
	redirects  redirectTable
	tree       map[string]struct{}
	orphans    map[string]struct{}
}

func loadNormalization(root string, state treeState, siteOrigin string) (normalizationContext, error) {
	baseURL, err := loadBaseURL(root)
	if err != nil {
		return normalizationContext{}, err
	}
	redirects, err := loadRedirects(filepath.Join(root, "_redirects"))
	if err != nil {
		return normalizationContext{}, err
	}
	context := normalizationContext{root: root, baseURL: baseURL, siteOrigin: baseURL, redirects: redirects, tree: map[string]struct{}{}, orphans: map[string]struct{}{}}
	if strings.TrimSpace(siteOrigin) != "" {
		origin, err := url.Parse(strings.TrimSpace(siteOrigin))
		if err != nil || origin.Scheme == "" || origin.Host == "" || origin.Path != "" && origin.Path != "/" {
			return normalizationContext{}, errors.New("site origin must be an absolute URL without a path")
		}
		context.siteOrigin = origin
	}
	for _, page := range state.AllPages {
		context.tree[page] = struct{}{}
	}
	for _, page := range state.Orphans {
		context.orphans[page] = struct{}{}
	}
	return context, nil
}

func loadBaseURL(root string) (*url.URL, error) {
	content, err := os.ReadFile(filepath.Join(root, "build-info.json"))
	if err != nil {
		return nil, fmt.Errorf("read build-info: %w", err)
	}
	var info struct {
		BaseURL string `json:"base_url"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	if err := decoder.Decode(&info); err != nil {
		return nil, fmt.Errorf("decode build-info: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("build-info must contain exactly one JSON value")
	}
	parsed, err := url.Parse(strings.TrimSpace(info.BaseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("build-info has no valid base_url")
	}
	return parsed, nil
}

type redirectTable struct {
	exact    map[string]string
	wildcard []redirectRule
	count    int
}

type redirectRule struct {
	from string
	to   string
}

func loadRedirects(filename string) (redirectTable, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return redirectTable{}, fmt.Errorf("read redirects: %w", err)
	}
	table := redirectTable{exact: map[string]string{}}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "/docs/") {
			continue
		}
		from, to := fields[0], fields[1]
		if strings.Contains(from, "*") {
			table.wildcard = append(table.wildcard, redirectRule{from: from, to: to})
			continue
		}
		if _, exists := table.exact[from]; exists {
			return redirectTable{}, fmt.Errorf("duplicate docs redirect %q", from)
		}
		table.exact[from] = to
	}
	table.count = len(table.exact) + len(table.wildcard)
	sort.Slice(table.wildcard, func(left, right int) bool { return table.wildcard[left].from < table.wildcard[right].from })
	return table, nil
}

// resolve maps a site path through the redirect table using a single hop.
// Rendered hrefs and _redirects entries disagree on trailing slashes, so an
// exact miss is retried with a trailing slash appended.
func (table redirectTable) resolve(value string) string {
	next, exists := table.lookup(value)
	if !exists && !strings.HasSuffix(value, "/") {
		next, exists = table.lookup(value + "/")
	}
	if !exists || next == value {
		return value
	}
	return next
}

func (table redirectTable) lookup(value string) (string, bool) {
	if target, exists := table.exact[value]; exists {
		return target, true
	}
	for _, rule := range table.wildcard {
		prefix := strings.TrimSuffix(rule.from, "*")
		if strings.HasPrefix(value, prefix) {
			return strings.ReplaceAll(rule.to, ":splat", strings.TrimPrefix(value, prefix)), true
		}
	}
	return "", false
}

type pageNormalizer struct {
	context normalizationContext
	page    string
	links   LinkStats
	assets  map[string]Asset
}

type externalAssetError struct {
	value string
}

func (e *externalAssetError) Error() string {
	return fmt.Sprintf("image %q is external", e.value)
}

func (context normalizationContext) forPage(page string) *pageNormalizer {
	return &pageNormalizer{context: context, page: page, assets: map[string]Asset{}}
}

func (normalizer *pageNormalizer) link(value string) (string, bool) {
	resolved, kind := normalizer.resolve(value)
	switch kind {
	case referencePageInternal:
		normalizer.links.PageInternal++
		return resolved, true
	case referenceExternal:
		normalizer.links.External++
		return resolved, true
	case referenceLocal:
		// Same-origin site paths outside /docs/ (the blog, release notes,
		// site-level pages) are not part of the offline library; keep them
		// as absolute references to the live site instead of dropping the
		// link text onto the page.
		normalizer.links.External++
		return normalizer.context.absoluteReference(resolved), true
	case referenceDocs:
		page, fragment := splitFragment(resolved)
		reference := relativePageReference(normalizer.page, page) + fragment
		if _, exists := normalizer.context.tree[page]; exists {
			normalizer.links.Internal++
			return reference, true
		}
		if _, exists := normalizer.context.orphans[page]; exists {
			normalizer.links.ToOrphans++
			return reference, true
		}
		normalizer.links.OutOfTree = append(normalizer.links.OutOfTree, page)
		return reference, true
	default:
		normalizer.links.Dropped++
		return "", false
	}
}

func (normalizer *pageNormalizer) image(value string) (string, error) {
	resolved, kind := normalizer.resolve(value)
	if kind != referenceDocs && kind != referenceLocal {
		if kind == referenceExternal {
			return "", &externalAssetError{value: value}
		}
		return "", fmt.Errorf("image %q is not a local rendered asset", value)
	}
	assetPath, _ := splitFragment(resolved)
	assetPath = strings.TrimPrefix(assetPath, "/")
	assetPath = strings.TrimSuffix(assetPath, "/")
	if assetPath == "" {
		return "", fmt.Errorf("image %q has no asset path", value)
	}
	file := filepath.Join(normalizer.context.root, filepath.FromSlash(assetPath))
	root, err := filepath.Abs(normalizer.context.root)
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(file)
	if err != nil || (target != root && !strings.HasPrefix(target, root+string(filepath.Separator))) {
		return "", fmt.Errorf("image %q escapes rendered root", value)
	}
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("image asset %q is missing", assetPath)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("read image asset %q: %w", assetPath, err)
	}
	digest := sha256.Sum256(content)
	normalizer.assets[assetPath] = Asset{Path: assetPath, Digest: "sha256:" + hex.EncodeToString(digest[:])}
	return relativeAssetReference(normalizer.page, assetPath), nil
}

// relativePageReference renders a canonical site path as a page reference
// relative to the page's own directory, so plain Markdown renderers and
// editors resolve links without knowing the library root.
func relativePageReference(page, target string) string {
	return relativeReference(page, target, true)
}

func relativeAssetReference(page, target string) string {
	return relativeReference(page, target, false)
}

func relativeReference(page, target string, directory bool) string {
	from := strings.TrimSuffix(page, "/")
	rel, err := filepath.Rel(from, path.Clean(target))
	if err != nil {
		return target
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		if directory {
			return "./"
		}
		return target
	}
	if directory {
		return rel + "/"
	}
	return rel
}

type referenceKind uint8

const (
	referenceDropped referenceKind = iota
	referencePageInternal
	referenceExternal
	referenceDocs
	referenceLocal
)

func (normalizer *pageNormalizer) resolve(value string) (string, referenceKind) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "#") {
		return value, referencePageInternal
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", referenceDropped
	}
	if parsed.Scheme != "" {
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return "", referenceDropped
		}
		if !sameOrigin(parsed, normalizer.context.baseURL) {
			return value, referenceExternal
		}
		parsed.Scheme, parsed.Host = "", ""
	}
	if parsed.Host != "" {
		return value, referenceExternal
	}
	if parsed.Path == "" {
		return "", referenceDropped
	}
	if !strings.HasPrefix(parsed.Path, "/") {
		parsed.Path = path.Join("/"+normalizer.page, parsed.Path)
	}
	if strings.HasPrefix(parsed.Path, "/docs/") {
		target := normalizer.context.redirects.resolve(parsed.Path)
		// A redirect target may carry its own anchor (for example
		// …/csi-driver-v1/#TokenRequest); that anchor wins, and without one
		// the source anchor survives — matching how the live site's
		// redirects treat fragments.
		targetPath, targetFragment := splitFragment(target)
		fragment := targetFragment
		if fragment == "" {
			fragment = fragmentSuffix(parsed)
		}
		page, err := normalizeSitePath(targetPath)
		if err == nil {
			return page + fragment, referenceDocs
		}
		// The redirect moved the link out of the docs tree (for example
		// …/setup/release/version-skew-policy/ → /releases/…); keep it as
		// a site link the caller can resolve absolutely.
		if target != parsed.Path && strings.HasPrefix(targetPath, "/") {
			return strings.TrimPrefix(targetPath, "/") + fragment, referenceLocal
		}
		return "", referenceDropped
	}
	assetPath := strings.TrimPrefix(path.Clean(parsed.Path), "/")
	if assetPath == "." || strings.HasPrefix(assetPath, "../") {
		return "", referenceDropped
	}
	return assetPath + fragmentSuffix(parsed), referenceLocal
}

func (normalizer *pageNormalizer) result() (LinkStats, []Asset) {
	normalizer.links.OutOfTree = uniqueSorted(normalizer.links.OutOfTree)
	assets := make([]Asset, 0, len(normalizer.assets))
	for _, asset := range normalizer.assets {
		assets = append(assets, asset)
	}
	sort.Slice(assets, func(left, right int) bool { return assets[left].Path < assets[right].Path })
	return normalizer.links, assets
}

func sameOrigin(left, right *url.URL) bool {
	return left.Scheme == right.Scheme && left.Host == right.Host
}

// absoluteReference renders a site-root-relative reference outside /docs/ as
// an absolute URL against the projection's site origin.
func (context normalizationContext) absoluteReference(resolved string) string {
	sitePath, fragment := splitFragment(resolved)
	target := *context.siteOrigin
	target.Path = path.Join("/", sitePath)
	target.Fragment = strings.TrimPrefix(fragment, "#")
	target.RawQuery = ""
	return target.String()
}

func fragmentSuffix(parsed *url.URL) string {
	if parsed.Fragment == "" {
		return ""
	}
	return "#" + parsed.Fragment
}

func splitFragment(value string) (string, string) {
	path, fragment, found := strings.Cut(value, "#")
	if !found {
		return value, ""
	}
	return path, "#" + fragment
}
