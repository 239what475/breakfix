package docsproject

import (
	"encoding/json"
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

func TestMarkdownAnchorSectionRejectsAbsentAnchor(t *testing.T) {
	markdown := []byte("# Page\n\n## One\n")
	if _, err := MarkdownAnchorSection(markdown, []ExtractedHeading{{ID: "one", Level: 2, Title: "One"}}, "missing"); err == nil {
		t.Fatal("missing anchor was accepted")
	}
}

func TestRunReportsPageFailuresAndUsesExitCodeOne(t *testing.T) {
	root := t.TempDir()
	navigation := sidebar("Documentation", "/docs/", sidebar("Home", "/docs/home/"), sidebar("Broken", "/docs/broken/"))
	writeProjectionPage(t, root, "docs/home/", navigation, "<h1>Home</h1>")
	writeProjectionPage(t, root, "docs/broken/", navigation, "<h1></h1><p>Broken title.</p>")
	writeNormalizerFile(t, root, "build-info.json", `{"source":"kubernetes","revision":"abc123","version":"snapshot","locale":"en","base_url":"http://localhost:1313/"}`)
	writeNormalizerFile(t, root, "_redirects", "")
	out := filepath.Join(t.TempDir(), "documents")
	err := Run(Config{Root: root, Out: out, Workers: 2, Version: "docs-project-v2"})
	if ExitCode(err) != 1 {
		t.Fatalf("exit code = %d, error = %v", ExitCode(err), err)
	}
	content, readErr := os.ReadFile(filepath.Join(out, "report.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	var report FailureReport
	if err := json.Unmarshal(content, &report); err != nil || len(report.Failures) != 1 || report.Failures[0].Path != "docs/broken/" || !strings.Contains(report.Failures[0].Error, "title") {
		t.Fatalf("report = %#v, decode error = %v", report, err)
	}
}

func TestRunResumeReusesMatchingPagesAndRegeneratesVersionMismatch(t *testing.T) {
	root := t.TempDir()
	navigation := sidebar("Documentation", "/docs/", sidebar("Home", "/docs/home/"), sidebar("Setup", "/docs/setup/"))
	writeProjectionPage(t, root, "docs/home/", navigation, "<h1>Home</h1><p>Original.</p>")
	writeProjectionPage(t, root, "docs/setup/", navigation, "<h1>Setup</h1><p>Setup.</p>")
	writeNormalizerFile(t, root, "build-info.json", `{"source":"kubernetes","revision":"abc123","version":"snapshot","locale":"en","base_url":"http://localhost:1313/"}`)
	writeNormalizerFile(t, root, "_redirects", "")
	out := filepath.Join(t.TempDir(), "documents")
	config := Config{Root: root, Out: out, Workers: 2, Version: "docs-project-v2"}
	if err := Run(config); err != nil {
		t.Fatal(err)
	}
	writeProjectionPage(t, root, "docs/home/", navigation, "<h1>Home</h1><p>Changed source.</p>")
	if err := os.Remove(filepath.Join(out, "docs", "setup", "index.md")); err != nil {
		t.Fatal(err)
	}
	config.Resume = true
	if err := Run(config); err != nil {
		t.Fatal(err)
	}
	home, err := os.ReadFile(filepath.Join(out, "docs", "home", "index.md"))
	if err != nil || !strings.Contains(string(home), "Original.") {
		t.Fatalf("matching resume rewrote home = %q, %v", home, err)
	}
	if _, err := os.Stat(filepath.Join(out, "docs", "setup", "index.md")); err != nil {
		t.Fatalf("missing page was not regenerated: %v", err)
	}
	config.Version = "docs-project-v3"
	if err := Run(config); err != nil {
		t.Fatal(err)
	}
	home, err = os.ReadFile(filepath.Join(out, "docs", "home", "index.md"))
	if err != nil || !strings.Contains(string(home), "Changed source.") {
		t.Fatalf("version mismatch did not regenerate home = %q, %v", home, err)
	}
}

func writeProjectionPage(t *testing.T, root, page, navigation, main string) {
	t.Helper()
	writeTreePageContents(t, root, page, "<html><body><nav id=\"td-section-nav\"><ul>"+navigation+"</ul></nav><main>"+main+"</main></body></html>")
}
