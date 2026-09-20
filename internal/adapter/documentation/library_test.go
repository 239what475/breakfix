package docsource

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/docsproject"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
)

func libraryIdentity(context domain.DocumentContext) LibraryIdentity {
	return LibraryIdentity{SourceID: context.SourceID, Repository: context.Repository, Commit: context.Commit, Version: context.Version, Language: context.Language, License: context.License}
}

func libraryContext() domain.DocumentContext {
	return domain.DocumentContext{FormatVersion: domain.FormatVersion, SourceID: "kubernetes", Repository: "https://github.com/kubernetes/website.git", Commit: strings.Repeat("a", 40), Version: "snapshot-aaa", Language: "en", License: "CC BY 4.0", PagePath: "docs/concepts/workloads/pods/pod-lifecycle", Anchor: "pod-lifetime"}
}

const libraryMarkdown = "# Pod Lifecycle\n\nPods follow a defined lifecycle.\n\n## Pod lifetime\n\nA Pod is mortal.\n\n## Pod phase\n\nThe phase is Pending.\n"

// writeLibraryMaterial builds a minimal real-shape library directory: global
// manifest plus one page directory whose digests are computed from content.
func writeLibraryMaterial(t *testing.T, root string, context domain.DocumentContext, markdown string, mutateGlobal func(*docsproject.GlobalManifest), mutatePage func(*docsproject.PageManifest)) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(context.PagePath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(context.PagePath), "index.md"), []byte(markdown), 0o644); err != nil {
		t.Fatal(err)
	}
	page := docsproject.PageManifest{
		FormatVersion: docsproject.FormatVersion, GeneratorVersion: "docs-project-test",
		Upstream: docsproject.Upstream{Source: context.SourceID, Commit: context.Commit, Version: context.Version, Locale: context.Language},
		Path:     context.PagePath + "/", PageKind: "content", Title: "Pod Lifecycle", Digest: evidenceDigest(markdown),
		Anchors: []docsproject.Anchor{
			{ID: "pod-lifecycle", Level: 1, Title: "Pod Lifecycle", Digest: evidenceDigest("# Pod Lifecycle\n\nPods follow a defined lifecycle.\n\n"), Parent: ""},
			{ID: "pod-lifetime", Level: 2, Title: "Pod lifetime", Digest: evidenceDigest("## Pod lifetime\n\nA Pod is mortal.\n\n"), Parent: "pod-lifecycle"},
			{ID: "pod-phase", Level: 2, Title: "Pod phase", Digest: evidenceDigest("## Pod phase\n\nThe phase is Pending.\n"), Parent: "pod-lifecycle"},
		},
	}
	if err := os.MkdirAll(filepath.Join(root, "images", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	const assetContent = "<svg>pod</svg>\n"
	if err := os.WriteFile(filepath.Join(root, "images", "docs", "pod.svg"), []byte(assetContent), 0o644); err != nil {
		t.Fatal(err)
	}
	page.Assets = []docsproject.Asset{{Path: "images/docs/pod.svg", Digest: evidenceDigest(assetContent)}}
	if mutatePage != nil {
		mutatePage(&page)
	}
	pageBytes, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(context.PagePath), "index.json"), pageBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	global := docsproject.GlobalManifest{
		FormatVersion: docsproject.FormatVersion, GeneratorVersion: "docs-project-test",
		Upstream:  docsproject.Upstream{Source: context.SourceID, Commit: context.Commit, Version: context.Version, Locale: context.Language},
		BuildInfo: json.RawMessage(fmt.Sprintf(`{"source":%q,"repository":%q,"revision":%q,"version":%q,"locale":%q,"base_url":"http://localhost:1313/"}`, context.SourceID, context.Repository, context.Commit, context.Version, context.Language)),
		Tree: docsproject.Tree{Nodes: []docsproject.TreeNode{{
			Title: "Concepts", Path: "docs/concepts/",
			Children: []docsproject.TreeNode{{Title: "Pod Lifecycle", Path: context.PagePath + "/"}},
		}}},
		Pages: []string{context.PagePath + "/"},
	}
	if mutateGlobal != nil {
		mutateGlobal(&global)
	}
	globalBytes, err := json.Marshal(global)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), globalBytes, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPinnedLibraryServesDigestVerifiedAnchorSlices(t *testing.T) {
	root := t.TempDir()
	context := libraryContext()
	writeLibraryMaterial(t, root, context, libraryMarkdown, nil, nil)

	library, err := NewPinnedLibrary(libraryIdentity(context), root)
	if err != nil {
		t.Fatalf("open pinned library: %v", err)
	}
	page, err := library.ReadPage(context.PagePath, context.Anchor)
	if err != nil || page.Content != "## Pod lifetime\n\nA Pod is mortal.\n\n" {
		t.Fatalf("library page = %#v, %v", page, err)
	}
	if page.Digest != evidenceDigest(page.Content) {
		t.Fatalf("page digest %q does not cover the served slice", page.Digest)
	}
	if page.Context.ParserVersion != "docs-project-test" || page.Context.PageDigest != evidenceDigest(libraryMarkdown) {
		t.Fatalf("page context does not carry the evidence triple: %#v", page.Context)
	}
	metadata, err := library.ReadMetadata(context.PagePath)
	if err != nil || metadata.Title != "Pod Lifecycle" || len(metadata.Anchors) != 3 || metadata.Anchors[1] != "pod-lifetime" {
		t.Fatalf("library metadata = %#v, %v", metadata, err)
	}
	if metadata.Context.ParserVersion != "docs-project-test" || metadata.Context.PageDigest != evidenceDigest(libraryMarkdown) {
		t.Fatalf("metadata context does not carry the evidence triple: %#v", metadata.Context)
	}
	parserVersion, upstreamCommit := library.Identity()
	if parserVersion != "docs-project-test" || upstreamCommit != context.Commit {
		t.Fatalf("library identity = %q, %q", parserVersion, upstreamCommit)
	}
	if metadata.Digest != evidenceDigest(libraryMarkdown) {
		t.Fatalf("metadata digest %q is not the page digest", metadata.Digest)
	}
	if _, err := library.ReadPage("docs/other", ""); err == nil {
		t.Fatal("page outside the library was accepted")
	}
	// The deployment no longer pins one anchor: every manifest anchor of the
	// page is readable, each with its digest-verified slice.
	other, err := library.ReadPage(context.PagePath, "pod-phase")
	if err != nil || other.Content != "## Pod phase\n\nThe phase is Pending.\n" || other.Context.Anchor != "pod-phase" {
		t.Fatalf("second anchor = %#v, %v", other, err)
	}
	if other.Context.PagePath != context.PagePath {
		t.Fatalf("anchor context page path = %q", other.Context.PagePath)
	}
}

func TestPinnedLibraryRejectsManifestContextMismatch(t *testing.T) {
	root := t.TempDir()
	context := libraryContext()
	writeLibraryMaterial(t, root, context, libraryMarkdown, func(global *docsproject.GlobalManifest) {
		global.Upstream.Commit = strings.Repeat("b", 40)
	}, nil)
	if _, err := NewPinnedLibrary(libraryIdentity(context), root); err == nil {
		t.Fatal("library with a different upstream commit was accepted")
	}

	root = t.TempDir()
	writeLibraryMaterial(t, root, context, libraryMarkdown, func(global *docsproject.GlobalManifest) {
		global.BuildInfo = json.RawMessage(`{"source":"kubernetes","repository":"https://github.com/other/website.git"}`)
	}, nil)
	if _, err := NewPinnedLibrary(libraryIdentity(context), root); err == nil {
		t.Fatal("library with a different repository was accepted")
	}

	root = t.TempDir()
	writeLibraryMaterial(t, root, context, libraryMarkdown, func(global *docsproject.GlobalManifest) {
		global.Pages = []string{"docs/tasks/other/"}
	}, nil)
	if _, err := NewPinnedLibrary(libraryIdentity(context), root); err == nil {
		t.Fatal("library manifest referencing an absent page was accepted")
	}
}

func TestPinnedLibraryRejectsTamperedPageBytes(t *testing.T) {
	root := t.TempDir()
	context := libraryContext()
	writeLibraryMaterial(t, root, context, libraryMarkdown, nil, nil)
	library, err := NewPinnedLibrary(libraryIdentity(context), root)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, filepath.FromSlash(context.PagePath), "index.md")
	if err := os.WriteFile(target, []byte(strings.Replace(libraryMarkdown, "mortal", "immortal", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := library.ReadPage(context.PagePath, context.Anchor); err == nil {
		t.Fatal("tampered page bytes passed the page digest")
	}
	if _, err := library.ReadMetadata(context.PagePath); err == nil {
		t.Fatal("tampered page bytes passed the page digest for metadata")
	}
}

func TestPinnedLibraryRejectsAnchorDigestMismatch(t *testing.T) {
	root := t.TempDir()
	context := libraryContext()
	writeLibraryMaterial(t, root, context, libraryMarkdown, nil, func(page *docsproject.PageManifest) {
		for index := range page.Anchors {
			if page.Anchors[index].ID == "pod-lifetime" {
				page.Anchors[index].Digest = evidenceDigest("different bytes")
			}
		}
	})
	library, err := NewPinnedLibrary(libraryIdentity(context), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := library.ReadPage(context.PagePath, context.Anchor); err == nil {
		t.Fatal("anchor slice with a mismatching pinned digest was served")
	}
	if _, err := library.ReadPage(context.PagePath, "absent-anchor"); err == nil {
		t.Fatal("anchor absent from the page manifest was served")
	}
}

func TestPinnedLibraryRejectsPageManifestMismatch(t *testing.T) {
	root := t.TempDir()
	context := libraryContext()
	writeLibraryMaterial(t, root, context, libraryMarkdown, nil, func(page *docsproject.PageManifest) {
		page.Upstream.Commit = strings.Repeat("b", 40)
	})
	library, err := NewPinnedLibrary(libraryIdentity(context), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := library.ReadPage(context.PagePath, context.Anchor); err == nil {
		t.Fatal("page manifest with a different upstream identity was served")
	}

	root = t.TempDir()
	writeLibraryMaterial(t, root, context, libraryMarkdown, nil, func(page *docsproject.PageManifest) {
		page.Digest = evidenceDigest("not the markdown")
	})
	library, err = NewPinnedLibrary(libraryIdentity(context), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := library.ReadPage(context.PagePath, context.Anchor); err == nil {
		t.Fatal("page manifest with a stale page digest was served")
	}
}

func TestPinnedKubernetesPodLifecycleLibrarySmoke(t *testing.T) {
	if os.Getenv("BREAKFIX_DOCUMENTATION_SMOKE") != "1" {
		t.Skip("documentation smoke target was not requested")
	}
	libraryRoot := strings.TrimSpace(os.Getenv("BREAKFIX_DOCUMENTATION_LIBRARY_ROOT"))
	if libraryRoot == "" {
		t.Fatal("documentation library smoke requires the library root")
	}
	// DOCS_REVISION/DOCS_VERSION follow the docs-site.sh override convention:
	// the upstream canary runs this smoke against the latest dev-* corpus, so
	// the pinned identity stays the default but can be replaced wholesale.
	commit := strings.TrimSpace(os.Getenv("DOCS_REVISION"))
	if commit == "" {
		commit = "ce98a43f24257385a9766003a6dadc95e962dc63"
	}
	version := strings.TrimSpace(os.Getenv("DOCS_VERSION"))
	if version == "" {
		version = "snapshot-ce98a43"
	}
	context := domain.DocumentContext{
		FormatVersion: domain.FormatVersion,
		SourceID:      "kubernetes",
		Repository:    "https://github.com/kubernetes/website.git",
		Commit:        commit,
		Version:       version,
		Language:      "en",
		License:       "CC BY 4.0",
		PagePath:      "docs/concepts/workloads/pods/pod-lifecycle",
		Anchor:        "pod-lifetime",
	}
	library, err := NewPinnedLibrary(libraryIdentity(context), libraryRoot)
	if err != nil {
		t.Fatalf("open pinned Kubernetes library: %v", err)
	}
	page, err := library.ReadPage(context.PagePath, context.Anchor)
	if err != nil || !strings.Contains(page.Content, "Pod lifetime") {
		t.Fatalf("read Pod lifecycle page from library = %#v, %v", page, err)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(libraryRoot, filepath.FromSlash(context.PagePath), "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := docsproject.DecodePageManifest(manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, anchor := range manifest.Anchors {
		if anchor.ID != context.Anchor {
			continue
		}
		if page.Digest != anchor.Digest {
			t.Fatalf("served evidence digest %s does not match the manifest anchor digest %s", page.Digest, anchor.Digest)
		}
		break
	}
	sections, err := library.ReadDocumentTree("")
	if err != nil || len(sections) == 0 {
		t.Fatalf("library tree sections = %#v, %v", sections, err)
	}
	document, err := library.ReadDocumentPage(context.PagePath)
	if err != nil || document.Title != "Pod Lifecycle" || document.Digest != manifest.Digest || len(document.Anchors) != len(manifest.Anchors) {
		t.Fatalf("document page = %#v, %v", document, err)
	}
	for index, anchor := range document.Anchors {
		if anchor.ID != manifest.Anchors[index].ID || anchor.Level != manifest.Anchors[index].Level || anchor.Title != manifest.Anchors[index].Title {
			t.Fatalf("document anchor %d = %#v does not match the page manifest", index, anchor)
		}
	}
	for _, asset := range document.Assets {
		if _, contentType, assetErr := library.ReadDocumentAsset(asset.Path); assetErr != nil || contentType == "" {
			t.Fatalf("library asset %s = %v", asset.Path, assetErr)
		}
	}
}

func TestPinnedLibraryFollowsProjectedVolumeSymlinks(t *testing.T) {
	// Kubernetes ConfigMap volumes expose each key as a symlink into a ..data
	// directory. The library must read through such symlinks while rejecting
	// any symlink that escapes the library root.
	root := t.TempDir()
	context := libraryContext()
	pageDir := filepath.Join(root, filepath.FromSlash(context.PagePath))
	if err := os.MkdirAll(filepath.Join(root, ".data", filepath.FromSlash(context.PagePath)), 0o755); err != nil {
		t.Fatal(err)
	}
	writeLibraryMaterial(t, filepath.Join(root, ".data"), context, libraryMarkdown, nil, nil)
	for _, name := range []string{"manifest.json"} {
		if err := os.Symlink(filepath.Join(root, ".data", name), filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(pageDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, ".data", filepath.FromSlash(context.PagePath)), pageDir); err != nil {
		t.Fatal(err)
	}
	library, err := NewPinnedLibrary(libraryIdentity(context), root)
	if err != nil {
		t.Fatalf("open library through projected symlinks: %v", err)
	}
	if _, err := library.ReadPage(context.PagePath, context.Anchor); err != nil {
		t.Fatalf("read page through projected symlinks: %v", err)
	}

	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, "docs", "escape")), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(outside, "manifest.json"), "{}")
	if err := os.Remove(filepath.Join(root, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "manifest.json"), filepath.Join(root, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewPinnedLibrary(libraryIdentity(context), root); err == nil {
		t.Fatal("symlink escaping the library root was accepted")
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

func TestLibraryServesParsedPagesTreeAndAssets(t *testing.T) {
	root := t.TempDir()
	context := libraryContext()
	writeLibraryMaterial(t, root, context, libraryMarkdown, nil, nil)
	library, err := NewPinnedLibrary(libraryIdentity(context), root)
	if err != nil {
		t.Fatal(err)
	}
	page, err := library.ReadDocumentPage("docs/concepts/workloads/pods/pod-lifecycle/")
	if err != nil {
		t.Fatalf("read document page: %v", err)
	}
	if page.Path != context.PagePath || page.PageKind != "content" || page.Title != "Pod Lifecycle" || page.Digest != evidenceDigest(libraryMarkdown) || page.Markdown != libraryMarkdown {
		t.Fatalf("document page = %#v", page)
	}
	if len(page.Anchors) != 3 || page.Anchors[0].ID != "pod-lifecycle" || page.Anchors[0].Level != 1 || page.Anchors[0].Title != "Pod Lifecycle" {
		t.Fatalf("document anchors = %#v", page.Anchors)
	}
	if len(page.Assets) != 1 || page.Assets[0].Path != "images/docs/pod.svg" {
		t.Fatalf("document assets = %#v", page.Assets)
	}
	if _, err := library.ReadDocumentPage("docs/tasks/absent"); err == nil {
		t.Fatal("page outside the library was served")
	}

	sections, err := library.ReadDocumentTree("")
	if err != nil || len(sections) != 1 || sections[0].Title != "Concepts" || !sections[0].HasChildren || sections[0].Path != "docs/concepts" {
		t.Fatalf("root tree = %#v, %v", sections, err)
	}
	children, err := library.ReadDocumentTree("docs/concepts")
	if err != nil || len(children) != 1 || children[0].Path != context.PagePath || children[0].HasChildren {
		t.Fatalf("concept children = %#v, %v", children, err)
	}
	if _, err := library.ReadDocumentTree("docs/absent"); err == nil {
		t.Fatal("tree node outside the library was served")
	}

	content, contentType, err := library.ReadDocumentAsset("images/docs/pod.svg")
	if err != nil || string(content) != "<svg>pod</svg>\n" || contentType != "image/svg+xml" {
		t.Fatalf("asset = %q, %q, %v", content, contentType, err)
	}
	if _, _, err := library.ReadDocumentAsset("images/docs/absent.svg"); err == nil {
		t.Fatal("asset outside the library was served")
	}
	if err := os.WriteFile(filepath.Join(root, "images", "docs", "pod.svg"), []byte("<svg>tampered</svg>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := library.ReadDocumentAsset("images/docs/pod.svg"); err == nil {
		t.Fatal("tampered asset bytes passed the pinned digest")
	}
}

func TestLibraryRejectsTamperedDocumentPage(t *testing.T) {
	root := t.TempDir()
	context := libraryContext()
	writeLibraryMaterial(t, root, context, libraryMarkdown, nil, nil)
	library, err := NewPinnedLibrary(libraryIdentity(context), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(context.PagePath), "index.md"), []byte(strings.Replace(libraryMarkdown, "mortal", "immortal", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := library.ReadDocumentPage(context.PagePath); err == nil {
		t.Fatal("tampered document page bytes passed the page digest")
	}
}

func TestLibraryResolvesPageTitlesFromManifests(t *testing.T) {
	root := t.TempDir()
	context := libraryContext()
	writeLibraryMaterial(t, root, context, libraryMarkdown, nil, nil)
	library, err := NewPinnedLibrary(libraryIdentity(context), root)
	if err != nil {
		t.Fatal(err)
	}
	if title := library.DocumentPageTitle(context.PagePath); title != "Pod Lifecycle" {
		t.Fatalf("page title = %q, want the manifest title", title)
	}
	if title := library.DocumentPageTitle("docs/concepts/workloads/pods/pod-lifecycle/"); title != "Pod Lifecycle" {
		t.Fatalf("normalized page title = %q", title)
	}
	if title := library.DocumentPageTitle("docs/absent"); title != "" {
		t.Fatalf("unknown page title = %q, want empty", title)
	}
}
