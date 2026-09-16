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
	root      string
	baseURL   *url.URL
	redirects redirectTable
	tree      map[string]struct{}
	orphans   map[string]struct{}
}

func loadNormalization(root string, state treeState) (normalizationContext, error) {
	baseURL, err := loadBaseURL(root)
	if err != nil {
		return normalizationContext{}, err
	}
	redirects, err := loadRedirects(filepath.Join(root, "_redirects"))
	if err != nil {
		return normalizationContext{}, err
	}
	context := normalizationContext{root: root, baseURL: baseURL, redirects: redirects, tree: map[string]struct{}{}, orphans: map[string]struct{}{}}
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

func (table redirectTable) resolve(value string) string {
	next, exists := table.exact[value]
	if !exists {
		for _, rule := range table.wildcard {
			prefix := strings.TrimSuffix(rule.from, "*")
			if strings.HasPrefix(value, prefix) {
				next = strings.ReplaceAll(rule.to, ":splat", strings.TrimPrefix(value, prefix))
				exists = true
				break
			}
		}
	}
	if !exists || next == value {
		return value
	}
	return next
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
	case referenceDocs:
		page, fragment := splitFragment(resolved)
		if _, exists := normalizer.context.tree[page]; exists {
			normalizer.links.Internal++
			return page + fragment, true
		}
		if _, exists := normalizer.context.orphans[page]; exists {
			normalizer.links.ToOrphans++
			return page + fragment, true
		}
		normalizer.links.OutOfTree = append(normalizer.links.OutOfTree, page)
		return page + fragment, true
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
	return assetPath, nil
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
		parsed.Path = normalizer.context.redirects.resolve(parsed.Path)
		page, err := normalizeSitePath(parsed.Path)
		if err != nil {
			return "", referenceDropped
		}
		return page + fragmentSuffix(parsed), referenceDocs
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
