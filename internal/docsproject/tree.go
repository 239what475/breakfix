package docsproject

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// TreeNode is one sidebar entry in the rendered documentation navigation.
type TreeNode struct {
	Title    string     `json:"title"`
	Path     string     `json:"path"`
	Children []TreeNode `json:"children,omitempty"`
}

// Tree is the sidebar-derived document hierarchy included in manifest.json.
type Tree struct {
	Nodes []TreeNode `json:"nodes"`
}

type treeState struct {
	Tree     Tree
	AllPages []string
	Pages    []string
	Orphans  []string
	Warnings []string
}

func loadTree(root string, requested []string) (treeState, error) {
	homePath := filepath.Join(root, "docs", "home", "index.html")
	home, err := parseHTMLFile(homePath)
	if err != nil {
		return treeState{}, &InputError{Err: fmt.Errorf("read documentation sidebar: %w", err)}
	}
	tree, err := extractTree(home)
	if err != nil {
		return treeState{}, &InputError{Err: err}
	}
	allPages := treePaths(tree)
	if len(allPages) == 0 {
		return treeState{}, &InputError{Err: errors.New("documentation sidebar contains no pages")}
	}
	pageSet := make(map[string]struct{}, len(allPages))
	for _, path := range allPages {
		pageSet[path] = struct{}{}
	}
	pages := allPages
	warnings := []string(nil)
	if len(requested) > 0 {
		pages = make([]string, 0, len(requested))
		for _, requestedPath := range requested {
			path, err := normalizeSitePath(requestedPath)
			if err != nil {
				return treeState{}, &InputError{Err: fmt.Errorf("invalid requested page %q: %w", requestedPath, err)}
			}
			if _, ok := pageSet[path]; !ok {
				return treeState{}, &InputError{Err: fmt.Errorf("requested page %q is not in the documentation tree", path)}
			}
			pages = append(pages, path)
		}
		pages = uniqueSorted(pages)
		warnings = append(warnings, "cross-page sidebar validation skipped for -pages subset")
	} else if err := validateTreeSamples(root, tree, allPages); err != nil {
		return treeState{}, &InputError{Err: err}
	}
	if err := validatePageFiles(root, pages); err != nil {
		return treeState{}, &InputError{Err: err}
	}
	orphans, err := findOrphans(root, pageSet)
	if err != nil {
		return treeState{}, &InputError{Err: err}
	}
	warnings = append(warnings, "tree root docs/ redirects to docs/home/ and is excluded from pages and orphans")
	return treeState{Tree: tree, AllPages: allPages, Pages: pages, Orphans: orphans, Warnings: warnings}, nil
}

func parseHTMLFile(path string) (*html.Node, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return html.Parse(file)
}

func extractTree(document *html.Node) (Tree, error) {
	nav := findElement(document, func(node *html.Node) bool {
		return node.Data == "nav" && attribute(node, "id") == "td-section-nav"
	})
	if nav == nil {
		return Tree{}, errors.New("documentation sidebar nav#td-section-nav is absent")
	}
	var list *html.Node
	for child := nav.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == "ul" {
			list = child
			break
		}
	}
	if list == nil {
		return Tree{}, errors.New("documentation sidebar has no root list")
	}
	nodes := treeNodes(list)
	if len(nodes) == 1 && nodes[0].Path == "docs/" {
		nodes = nodes[0].Children
	}
	return Tree{Nodes: nodes}, nil
}

func treeNodes(list *html.Node) []TreeNode {
	var nodes []TreeNode
	for child := list.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode || child.Data != "li" {
			continue
		}
		link := firstLink(child)
		if link == nil {
			continue
		}
		path, err := normalizeSitePath(attribute(link, "href"))
		if err != nil {
			continue // locale and non-documentation controls are not tree nodes.
		}
		node := TreeNode{Title: normalizedText(link), Path: path}
		if childList := directChild(child, "ul"); childList != nil {
			node.Children = treeNodes(childList)
		}
		nodes = append(nodes, node)
	}
	return nodes
}

func firstLink(root *html.Node) *html.Node {
	var link *html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if link != nil || node.Type != html.ElementNode && node.Type != html.DocumentNode {
			return
		}
		if node != root && node.Data == "ul" {
			return
		}
		if node.Data == "a" && attribute(node, "href") != "" {
			link = node
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return link
}

func validateTreeSamples(root string, expected Tree, pages []string) error {
	count := min(20, len(pages))
	if count == 0 {
		return nil
	}
	random := rand.New(rand.NewPCG(1, 1)) // #nosec G404 -- the seed is the point: deterministic sampling
	permutation := random.Perm(len(pages))
	expectedBytes, _ := json.Marshal(expected)
	for _, index := range permutation[:count] {
		document, err := parseHTMLFile(pageFile(root, pages[index]))
		if err != nil {
			return fmt.Errorf("read sidebar sample %q: %w", pages[index], err)
		}
		actual, err := extractTree(document)
		if err != nil {
			return fmt.Errorf("extract sidebar sample %q: %w", pages[index], err)
		}
		actualBytes, _ := json.Marshal(actual)
		if string(actualBytes) != string(expectedBytes) {
			return fmt.Errorf("documentation sidebar differs in page %q", pages[index])
		}
	}
	return nil
}

func validatePageFiles(root string, pages []string) error {
	for _, path := range pages {
		info, err := os.Stat(pageFile(root, path))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("tree page file is missing for %q", path)
		}
	}
	return nil
}

func findOrphans(root string, treePages map[string]struct{}) ([]string, error) {
	docsRoot := filepath.Join(root, "docs")
	var orphans []string
	err := filepath.WalkDir(docsRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == "_print" {
			return filepath.SkipDir
		}
		if entry.IsDir() || entry.Name() != "index.html" {
			return nil
		}
		relative, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		sitePath := filepath.ToSlash(relative) + "/"
		// The tree root itself redirects to docs/home/ and is neither a page
		// nor an orphan.
		if _, found := treePages[sitePath]; !found && sitePath != "docs/" {
			orphans = append(orphans, sitePath)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan documentation pages: %w", err)
	}
	sort.Strings(orphans)
	return orphans, nil
}

func treePaths(tree Tree) []string {
	var paths []string
	var visit func([]TreeNode)
	visit = func(nodes []TreeNode) {
		for _, node := range nodes {
			paths = append(paths, node.Path)
			visit(node.Children)
		}
	}
	visit(tree.Nodes)
	return uniqueSorted(paths)
}

func pageFile(root, path string) string {
	return filepath.Join(root, filepath.FromSlash(path), "index.html")
}

func normalizeSitePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if index := strings.IndexAny(value, "?#"); index >= 0 {
		value = value[:index]
	}
	value = strings.TrimPrefix(value, "/")
	if !strings.HasPrefix(value, "docs/") {
		return "", errors.New("path is outside /docs/")
	}
	if strings.Contains(value, "//") || strings.Contains(value, "..") {
		return "", errors.New("path is not a clean site path")
	}
	if !strings.HasSuffix(value, "/") {
		value += "/"
	}
	return value, nil
}

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func findElement(root *html.Node, predicate func(*html.Node) bool) *html.Node {
	if root == nil {
		return nil
	}
	if root.Type == html.ElementNode && predicate(root) {
		return root
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if found := findElement(child, predicate); found != nil {
			return found
		}
	}
	return nil
}

func directChild(root *html.Node, name string) *html.Node {
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == name {
			return child
		}
	}
	return nil
}

func attribute(node *html.Node, key string) string {
	if node == nil {
		return ""
	}
	for _, item := range node.Attr {
		if item.Key == key {
			return strings.TrimSpace(item.Val)
		}
	}
	return ""
}

func normalizedText(root *html.Node) string {
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			text.WriteString(node.Data)
			text.WriteByte(' ')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return strings.Join(strings.Fields(text.String()), " ")
}
