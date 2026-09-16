package docsproject

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPageNormalizerNormalizesLinksAndAssets(t *testing.T) {
	root := t.TempDir()
	writeNormalizerFile(t, root, "build-info.json", `{"base_url":"http://localhost:1313/"}`)
	writeNormalizerFile(t, root, "_redirects", "/docs/old/ /docs/new/ 301\n")
	writeNormalizerFile(t, root, "docs/images/diagram.svg", "asset")
	context, err := loadNormalization(root, treeState{AllPages: []string{"docs/new/"}, Orphans: []string{"docs/archive/"}})
	if err != nil {
		t.Fatal(err)
	}
	normalizer := context.forPage("docs/current/")
	page, err := extractPageWithNormalizer([]byte(`<main><h1>Current</h1><p><a href="http://localhost:1313/docs/old/#section">Old</a> <a href="#here">Here</a> <a href="https://example.test">External</a> <a href="/docs/archive/">Archive</a> <a href="/docs/missing/">Missing</a> <a href="javascript:bad()">Bad</a><img alt="diagram" src="/docs/images/diagram.svg"></p></main>`), normalizer)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Current\n\n[Old](docs/new/#section) [Here](#here) [External](https://example.test) [Archive](docs/archive/) [Missing](docs/missing/) Bad![diagram](docs/images/diagram.svg)\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown = %q, want %q", got, want)
	}
	if page.Links.Internal != 1 || page.Links.PageInternal != 1 || page.Links.External != 1 || page.Links.ToOrphans != 1 || page.Links.Dropped != 1 || len(page.Links.OutOfTree) != 1 || page.Links.OutOfTree[0] != "docs/missing/" {
		t.Fatalf("links = %#v", page.Links)
	}
	if len(page.Assets) != 1 || page.Assets[0].Path != "docs/images/diagram.svg" || page.Assets[0].Digest != "sha256:d59386e0ae435e292fbe0ebcdb954b75ed5fb3922091277cb19f798fc5d50718" {
		t.Fatalf("assets = %#v", page.Assets)
	}
}

func TestRedirectTableHandlesWildcardAndLoopsWithoutNetwork(t *testing.T) {
	table := redirectTable{exact: map[string]string{"/docs/a/": "/docs/b/", "/docs/b/": "/docs/a/"}, wildcard: []redirectRule{{from: "/docs/old/*", to: "/docs/new/:splat"}}}
	if got := table.resolve("/docs/old/item/"); got != "/docs/new/item/" {
		t.Fatalf("wildcard redirect = %q", got)
	}
	if got := table.resolve("/docs/a/"); got != "/docs/a/" && got != "/docs/b/" {
		t.Fatalf("redirect loop escaped protection: %q", got)
	}
}

func TestPageNormalizerRejectsMissingOrExternalAssets(t *testing.T) {
	root := t.TempDir()
	writeNormalizerFile(t, root, "build-info.json", `{"base_url":"https://docs.example.test/"}`)
	writeNormalizerFile(t, root, "_redirects", "")
	context, err := loadNormalization(root, treeState{})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"/docs/images/missing.svg", "https://example.test/image.svg"} {
		if _, err := extractPageWithNormalizer([]byte(`<main><h1>Page</h1><img src="`+source+`"></main>`), context.forPage("docs/page/")); err == nil {
			t.Fatalf("asset %q was accepted", source)
		}
	}
}

func TestLoadRedirectsCountsOnlyDocsRules(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "_redirects")
	if err := os.WriteFile(filename, []byte("/docs/a/ /docs/b/ 301\n/blog/a/ /blog/b/ 301\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	table, err := loadRedirects(filename)
	if err != nil || table.count != 1 || !strings.Contains(table.exact["/docs/a/"], "/docs/b/") {
		t.Fatalf("redirects = %#v, %v", table, err)
	}
}

func writeNormalizerFile(t *testing.T, root, name, content string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
