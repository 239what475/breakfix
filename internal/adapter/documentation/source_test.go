package docsource

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
)

func TestNewPinnedSnapshotBindsBuildInfoAndPageContent(t *testing.T) {
	root := t.TempDir()
	sourceRoot := t.TempDir()
	writeFile(t, filepath.Join(root, "docs", "pods.md"), "# Pod lifecycle\n")
	writeFile(t, filepath.Join(sourceRoot, "content", "pods.md"), "---\ntitle: Pod lifecycle\n---\n")
	context := unboundContext()
	buildInfo := fmt.Sprintf(`{"source":%q,"repository":%q,"revision":%q,"version":%q,"locale":%q,"base_url":"https://docs.example.test/"}`,
		context.SourceID, context.Repository, context.Commit, context.Version, context.Language)
	writeFile(t, filepath.Join(root, "build-info.json"), buildInfo)

	snapshot, err := NewPinnedSnapshot(context, root, sourceRoot)
	if err != nil || snapshot.Context.ContentDigest != contentDigest("# Pod lifecycle\n") || snapshot.BuildBaseURL != "https://docs.example.test/" {
		t.Fatalf("pinned snapshot = %#v, %v", snapshot, err)
	}
	writeFile(t, filepath.Join(root, "docs", "unrelated.md"), "changes outside the page scope are allowed\n")
	if _, err := snapshot.ReadPage(context.PagePath, context.Anchor); err != nil {
		t.Fatalf("unrelated page invalidated pinned content: %v", err)
	}
	writeFile(t, filepath.Join(root, "docs", "pods.md"), "changed\n")
	if _, err := snapshot.ReadPage(context.PagePath, context.Anchor); err == nil {
		t.Fatal("pinned page accepted changed content")
	}
}

func unboundContext() domain.DocumentContext {
	return domain.DocumentContext{FormatVersion: domain.FormatVersion, SourceID: "kubernetes", Repository: "https://github.com/kubernetes/website", Commit: strings.Repeat("a", 40), Version: "v1.34.0", Language: "en", License: "CC BY 4.0", MirrorOrigin: "https://docs.example.test", PagePath: "docs/pods.md", Anchor: "pod-lifecycle"}
}

func boundContext(t *testing.T, root string, context domain.DocumentContext) domain.DocumentContext {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(context.PagePath)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(context.PagePath, ".html") {
		value, err := renderedPageContent(string(content))
		if err != nil {
			t.Fatal(err)
		}
		content = []byte(value)
	}
	context.ContentDigest = contentDigest(string(content))
	return context
}

func TestSnapshotReadsPinnedFilesAndEvidence(t *testing.T) {
	root := t.TempDir()
	sourceRoot := t.TempDir()
	writeFile(t, filepath.Join(root, "docs", "pods.md"), "# Pod lifecycle\n\n## Running\n")
	writeFile(t, filepath.Join(root, "docs", "other.md"), "# Other\n")
	writeFile(t, filepath.Join(sourceRoot, "source.md"), "source evidence\ninclude evidence\n")
	context := boundContext(t, root, unboundContext())
	snapshot, err := NewSnapshot(context, root, sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	page, err := snapshot.ReadPage(context.PagePath, context.Anchor)
	if err != nil || page.Digest != context.ContentDigest || !strings.Contains(page.Content, "Pod lifecycle") {
		t.Fatalf("unexpected page: %#v, %v", page, err)
	}
	metadata, err := snapshot.ReadMetadata(context.PagePath)
	if err != nil || len(metadata.Anchors) != 2 {
		t.Fatalf("metadata = %#v, %v", metadata, err)
	}
	if _, err := snapshot.ReadPage("docs/other.md", ""); err == nil {
		t.Fatal("page outside the fixed context was accepted")
	}
	source, err := snapshot.ReadSource("source.md", 1, 1)
	if err != nil || source.Content != "source evidence" || source.Evidence.Kind != domain.EvidenceSource {
		t.Fatalf("source evidence = %#v, %v", source, err)
	}
	include, err := snapshot.ReadInclude("source.md", 2, 2)
	if err != nil || include.Content != "include evidence" || include.Evidence.Kind != domain.EvidenceInclude {
		t.Fatalf("include evidence = %#v, %v", include, err)
	}
	if _, err := snapshot.ReadSource("docs/pods.md", 1, 1); err == nil {
		t.Fatal("rendered tree was used as source evidence root")
	}
}

func TestSnapshotReadsRenderedHTMLHeadingMetadata(t *testing.T) {
	root := t.TempDir()
	sourceRoot := t.TempDir()
	path := filepath.Join(root, "docs", "pods", "index.html")
	writeFile(t, path, "<html><body><main><h1>Pod lifecycle</h1><h2 id=\"pod-phase\">Pod phase</h2></main></body></html>")
	context := unboundContext()
	context.PagePath = "docs/pods/index.html"
	context = boundContext(t, root, context)
	snapshot, err := NewSnapshot(context, root, sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := snapshot.ReadMetadata(context.PagePath)
	if err != nil || metadata.Title != "Pod lifecycle" || len(metadata.Anchors) != 1 || metadata.Anchors[0] != "pod-phase" {
		t.Fatalf("HTML metadata = %#v, %v", metadata, err)
	}
}

func TestSnapshotBoundsRenderedPageToMainContent(t *testing.T) {
	root := t.TempDir()
	sourceRoot := t.TempDir()
	page := "<html><body><aside>" + strings.Repeat("x", MaxReadBytes) + "</aside><main><h1 id=\"pod-lifetime\">Pod lifetime</h1></main></body></html>"
	writeFile(t, filepath.Join(root, "docs", "pods.html"), page)
	context := unboundContext()
	context.PagePath = "docs/pods.html"
	context = boundContext(t, root, context)
	snapshot, err := NewSnapshot(context, root, sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	value, err := snapshot.ReadPage(context.PagePath, context.Anchor)
	if err != nil || !strings.Contains(value.Content, "Pod lifetime") || strings.Contains(value.Content, strings.Repeat("x", 32)) {
		t.Fatalf("bounded rendered page = %#v, %v", value, err)
	}
}

func TestSnapshotRejectsSymlinksAndOversizedPages(t *testing.T) {
	root := t.TempDir()
	sourceRoot := t.TempDir()
	writeFile(t, filepath.Join(root, "source"), "x")
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "source"), filepath.Join(root, "docs", "pods.md")); err != nil {
		t.Fatal(err)
	}
	context := unboundContext()
	context.ContentDigest = contentDigest("x")
	if _, err := NewSnapshot(context, root, sourceRoot); err == nil {
		t.Fatal("symlink page accepted")
	}

	root = t.TempDir()
	sourceRoot = t.TempDir()
	writeFile(t, filepath.Join(root, "docs", "pods.md"), strings.Repeat("x", MaxReadBytes+1))
	context.ContentDigest = contentDigest(strings.Repeat("x", MaxReadBytes+1))
	if _, err := NewSnapshot(context, root, sourceRoot); err == nil {
		t.Fatal("oversized page accepted")
	}
}

func TestSnapshotRejectsChangedPinnedPage(t *testing.T) {
	root := t.TempDir()
	sourceRoot := t.TempDir()
	writeFile(t, filepath.Join(root, "docs", "pods.md"), "first\n")
	context := boundContext(t, root, unboundContext())
	writeFile(t, filepath.Join(root, "docs", "pods.md"), "second\n")
	if _, err := NewSnapshot(context, root, sourceRoot); err == nil {
		t.Fatal("changed page accepted")
	}
}

func TestPinnedKubernetesPodLifecycleSnapshotSmoke(t *testing.T) {
	if os.Getenv("BREAKFIX_DOCUMENTATION_SMOKE") != "1" {
		t.Skip("documentation smoke target was not requested")
	}
	renderedRoot := strings.TrimSpace(os.Getenv("BREAKFIX_DOCUMENTATION_SNAPSHOT_ROOT"))
	sourceRoot := strings.TrimSpace(os.Getenv("BREAKFIX_DOCUMENTATION_SOURCE_ROOT"))
	if renderedRoot == "" || sourceRoot == "" {
		t.Fatal("documentation smoke requires rendered and source roots")
	}
	context := domain.DocumentContext{
		FormatVersion: domain.FormatVersion,
		SourceID:      "kubernetes",
		Repository:    "https://github.com/kubernetes/website.git",
		Commit:        "ce98a43f24257385a9766003a6dadc95e962dc63",
		Version:       "snapshot-ce98a43",
		Language:      "en",
		License:       "CC BY 4.0",
		MirrorOrigin:  "https://docs.breakfix.example",
		PagePath:      "docs/concepts/workloads/pods/pod-lifecycle/index.html",
		Anchor:        "pod-lifetime",
	}
	snapshot, err := NewPinnedSnapshot(context, renderedRoot, sourceRoot)
	if err != nil {
		t.Fatalf("open pinned Kubernetes snapshot: %v", err)
	}
	if snapshot.BuildBaseURL == "" || snapshot.Context.ContentDigest == "" {
		t.Fatalf("pinned page context = %#v", snapshot)
	}
	page, err := snapshot.ReadPage(context.PagePath, context.Anchor)
	if err != nil || !strings.Contains(page.Content, "Pod Lifecycle") {
		t.Fatalf("read Pod lifecycle page = %#v, %v", page, err)
	}
	metadata, err := snapshot.ReadMetadata(context.PagePath)
	if err != nil || metadata.Title != "Pod Lifecycle" || !containsAnchor(metadata.Anchors, context.Anchor) {
		t.Fatalf("Pod lifecycle heading metadata = %#v, %v", metadata, err)
	}
	source, err := snapshot.ReadSource("content/en/docs/concepts/workloads/pods/pod-lifecycle.md", 1, 9)
	if err != nil || !strings.Contains(source.Content, "title: Pod Lifecycle") {
		t.Fatalf("Pod lifecycle source evidence = %#v, %v", source, err)
	}
	include, err := snapshot.ReadInclude("content/en/includes/index.md", 1, 4)
	if err != nil || !strings.Contains(include.Content, "headless: true") {
		t.Fatalf("pinned include evidence = %#v, %v", include, err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func containsAnchor(anchors []string, expected string) bool {
	for _, anchor := range anchors {
		if anchor == expected {
			return true
		}
	}
	return false
}
