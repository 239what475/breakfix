package docsproject

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunEmitsPageAndGlobalManifestsWithDigests(t *testing.T) {
	root := t.TempDir()
	navigation := sidebar("Documentation", "/docs/", sidebar("Home", "/docs/home/"), sidebar("Setup", "/docs/setup/", sidebar("Install", "/docs/setup/install/")))
	writeProjectionPage(t, root, "docs/home/", navigation, "<h1>Home</h1><h2 id=\"welcome\">Welcome</h2><p>Start here.</p>")
	writeProjectionPage(t, root, "docs/setup/", navigation, "<h1>Setup</h1><h2 id=\"install\">Install</h2><p>Prepare.</p>")
	writeProjectionPage(t, root, "docs/setup/install/", navigation, "<h1>Install</h1><p>Install it.</p>")
	writeNormalizerFile(t, root, "build-info.json", `{"source":"kubernetes","revision":"abc123","version":"snapshot","locale":"en","base_url":"http://localhost:1313/"}`)
	writeNormalizerFile(t, root, "_redirects", "/docs/old/ /docs/setup/ 301\n")
	out := filepath.Join(t.TempDir(), "documents")
	if err := Run(Config{Root: root, Out: out, Workers: 1, Version: "docs-project-v2"}); err != nil {
		t.Fatal(err)
	}
	markdown, err := os.ReadFile(filepath.Join(out, "docs", "setup", "index.md"))
	if err != nil || string(markdown) != "# Setup\n\n## Install\n\nPrepare.\n" {
		t.Fatalf("markdown = %q, %v", markdown, err)
	}
	pageBytes, err := os.ReadFile(filepath.Join(out, "docs", "setup", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := DecodePageManifest(pageBytes)
	if err != nil {
		t.Fatal(err)
	}
	if page.Path != "docs/setup/" || page.PageKind != "index" || page.Digest != digest(markdown) || len(page.Anchors) != 1 || page.Anchors[0].ID != "install" || page.Anchors[0].Digest == "" {
		t.Fatalf("page manifest = %#v", page)
	}
	globalBytes, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	global, err := DecodeGlobalManifest(globalBytes)
	if err != nil {
		t.Fatal(err)
	}
	if global.Upstream.Commit != "abc123" || global.Redirects.Count != 1 || global.Stats.Pages != 3 || global.Stats.IndexPages != 1 || global.Stats.Anchors != 2 || string(global.BuildInfo) == "" {
		t.Fatalf("global manifest = %#v", global)
	}
}

func TestManifestDecodersRejectUnknownFields(t *testing.T) {
	if _, err := DecodePageManifest([]byte(`{"format_version":1,"unknown":true}`)); err == nil {
		t.Fatal("page manifest accepted unknown field")
	}
	if _, err := DecodeGlobalManifest([]byte(`{"format_version":1}{}`)); err == nil {
		t.Fatal("global manifest accepted multiple values")
	}
}

func TestAnchorsUseSectionByteRangesAndParents(t *testing.T) {
	markdown := []byte("# Page\n\n## One\n\n### Nested\n\ntext\n\n## Two\n")
	anchors := anchorsForMarkdown(markdown, []ExtractedHeading{{ID: "one", Level: 2, Title: "One"}, {ID: "nested", Level: 3, Title: "Nested"}, {ID: "two", Level: 2, Title: "Two"}})
	if len(anchors) != 3 || anchors[0].Parent != "" || anchors[1].Parent != "one" || anchors[2].Parent != "" || anchors[0].Digest != digest(markdown[strings.Index(string(markdown), "## One"):strings.Index(string(markdown), "## Two")]) {
		t.Fatalf("anchors = %#v", anchors)
	}
}

func writeProjectionPage(t *testing.T, root, page, navigation, main string) {
	t.Helper()
	writeTreePageContents(t, root, page, "<html><body><nav id=\"td-section-nav\"><ul>"+navigation+"</ul></nav><main>"+main+"</main></body></html>")
}
