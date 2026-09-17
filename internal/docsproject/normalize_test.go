package docsproject

import (
	"net/url"
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
	context, err := loadNormalization(root, treeState{AllPages: []string{"docs/new/"}, Orphans: []string{"docs/archive/"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	normalizer := context.forPage("docs/current/")
	page, err := extractPageWithNormalizer([]byte(`<main><h1>Current</h1><p><a href="http://localhost:1313/docs/old/#section">Old</a> <a href="#here">Here</a> <a href="https://example.test">External</a> <a href="/docs/archive/">Archive</a> <a href="/docs/missing/">Missing</a> <a href="javascript:bad()">Bad</a><img alt="diagram" src="/docs/images/diagram.svg"></p></main>`), normalizer)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Current\n\n[Old](../new/#section) [Here](#here) [External](https://example.test) [Archive](../archive/) [Missing](../missing/) Bad![diagram](../images/diagram.svg)\n"
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

func TestRelativeReferenceEdgeCases(t *testing.T) {
	cases := []struct {
		page      string
		target    string
		directory bool
		want      string
	}{
		{page: "docs/a/page/", target: "docs/a/page/", directory: true, want: "./"},
		{page: "docs/a/page/", target: "docs/reference/glossary/", directory: true, want: "../../reference/glossary/"},
		{page: "docs/home/", target: "images/docs/pod.svg", directory: false, want: "../../images/docs/pod.svg"},
		{page: "docs/a/", target: "docs/a/x.svg", directory: false, want: "x.svg"},
	}
	for _, test := range cases {
		got := relativeReference(test.page, test.target, test.directory)
		if got != test.want {
			t.Fatalf("relativeReference(%q, %q, %v) = %q, want %q", test.page, test.target, test.directory, got, test.want)
		}
	}
}

func TestRedirectTableResolvesTrailingSlashMismatch(t *testing.T) {
	table := redirectTable{exact: map[string]string{"/docs/old/": "/docs/new/"}}
	if got := table.resolve("/docs/old"); got != "/docs/new/" {
		t.Fatalf("slashless source redirect = %q, want %q", got, "/docs/new/")
	}
	if got := table.resolve("/docs/old/"); got != "/docs/new/" {
		t.Fatalf("redirect = %q, want %q", got, "/docs/new/")
	}
	if got := table.resolve("/docs/untouched"); got != "/docs/untouched" {
		t.Fatalf("unknown path changed = %q", got)
	}
}

func TestRedirectTableHandlesWildcardAndLoopsWithoutNetwork(t *testing.T) {
	table := redirectTable{exact: map[string]string{"/docs/a/": "/docs/b/", "/docs/b/": "/docs/a/", "/docs/chain/": "/docs/a/"}, wildcard: []redirectRule{{from: "/docs/old/*", to: "/docs/new/:splat"}}}
	if got := table.resolve("/docs/old/item/"); got != "/docs/new/item/" {
		t.Fatalf("wildcard redirect = %q", got)
	}
	if got := table.resolve("/docs/a/"); got != "/docs/b/" {
		t.Fatalf("redirect did not resolve one hop: %q", got)
	}
	if got := table.resolve("/docs/chain/"); got != "/docs/a/" {
		t.Fatalf("redirect followed more than one hop: %q", got)
	}
}

func TestPageNormalizerRejectsMissingAssetsAndDropsExternalAssets(t *testing.T) {
	root := t.TempDir()
	writeNormalizerFile(t, root, "build-info.json", `{"base_url":"https://docs.example.test/"}`)
	writeNormalizerFile(t, root, "_redirects", "")
	context, err := loadNormalization(root, treeState{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := extractPageWithNormalizer([]byte(`<main><h1>Page</h1><img src="/docs/images/missing.svg"></main>`), context.forPage("docs/page/")); err == nil {
		t.Fatal("missing asset was accepted")
	}
	page, err := extractPageWithNormalizer([]byte(`<main><h1>Page</h1><img src="https://example.test/image.svg"></main>`), context.forPage("docs/page/"))
	if err != nil || strings.Contains(string(page.Markdown), "image.svg") || page.DroppedElements != 1 {
		t.Fatalf("external image page = %#v, error = %v", page, err)
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

func TestPageNormalizerNormalizesCardLinks(t *testing.T) {
	context := normalizationContext{tree: map[string]struct{}{"docs/new/": {}}}
	page, err := extractPageWithNormalizer([]byte(`<main><h1>Index</h1><div class="card-group"><a href="/docs/new/">New</a><a href="javascript:bad()">Bad</a></div></main>`), context.forPage("docs/index/"))
	if err != nil || string(page.Markdown) != "# Index\n\n- [New](../new/)\n" || page.Links.Internal != 1 || page.Links.Dropped != 1 {
		t.Fatalf("page = %#v, error = %v", page, err)
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

func TestPageNormalizerKeepsRedirectTargetFragments(t *testing.T) {
	table := redirectTable{exact: map[string]string{
		"/docs/reference/kubernetes-api/authentication-resources/token-request-v1/": "/docs/reference/kubernetes-api/storage/csi-driver-v1/#TokenRequest",
		"/docs/old/": "/docs/new/",
	}}
	context := normalizationContext{tree: map[string]struct{}{"docs/reference/kubernetes-api/storage/csi-driver-v1/": {}, "docs/new/": {}}, redirects: table}
	normalizer := context.forPage("docs/concepts/configuration/secret/")
	page, err := extractPageWithNormalizer([]byte(`<main><h1>Secrets</h1><p><a href="/docs/reference/kubernetes-api/authentication-resources/token-request-v1/">TokenRequest</a> and <a href="/docs/old/#keep">old anchor</a>.</p></main>`), normalizer)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Secrets\n\n[TokenRequest](../../../reference/kubernetes-api/storage/csi-driver-v1/#TokenRequest) and [old anchor](../../../new/#keep).\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown = %q, want %q", got, want)
	}
}

func TestPageNormalizerKeepsSameOriginNonDocsLinks(t *testing.T) {
	root := t.TempDir()
	writeNormalizerFile(t, root, "build-info.json", `{"base_url":"https://kubernetes.io/"}`)
	writeNormalizerFile(t, root, "_redirects", "")
	context, err := loadNormalization(root, treeState{AllPages: []string{"docs/current/"}}, "https://kubernetes.io")
	if err != nil {
		t.Fatal(err)
	}
	normalizer := context.forPage("docs/current/")
	page, err := extractPageWithNormalizer([]byte(`<main><h1>Current</h1><p>Read the <a href="/blog/2021/05/14/using-finalizers/">blog post</a> and the <a href="/releases/version-skew-policy/">skew policy</a>.</p></main>`), normalizer)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Current\n\nRead the [blog post](https://kubernetes.io/blog/2021/05/14/using-finalizers) and the [skew policy](https://kubernetes.io/releases/version-skew-policy).\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown = %q, want %q", got, want)
	}
	if page.Links.External != 2 || page.Links.Dropped != 0 {
		t.Fatalf("links = %#v", page.Links)
	}
}

func TestPageNormalizerFollowsRedirectsOutOfDocsTree(t *testing.T) {
	table := redirectTable{exact: map[string]string{
		"/docs/setup/release/version-skew-policy/": "/releases/version-skew-policy/",
	}}
	context := normalizationContext{tree: map[string]struct{}{}, redirects: table, siteOrigin: mustURL(t, "https://kubernetes.io")}
	normalizer := context.forPage("docs/concepts/windows/intro/")
	page, err := extractPageWithNormalizer([]byte(`<main><h1>Intro</h1><p>The Kubernetes <a href="/docs/setup/release/version-skew-policy/">version-skew policy</a> also applies.</p></main>`), normalizer)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Intro\n\nThe Kubernetes [version-skew policy](https://kubernetes.io/releases/version-skew-policy) also applies.\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown = %q, want %q", got, want)
	}
	if page.Links.External != 1 || page.Links.Dropped != 0 {
		t.Fatalf("links = %#v", page.Links)
	}
}

func mustURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
