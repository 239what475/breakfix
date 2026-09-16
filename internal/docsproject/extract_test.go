package docsproject

import (
	"strings"
	"testing"
)

func TestParsePageRendersRestrictedMarkdown(t *testing.T) {
	page, err := ParsePage(strings.NewReader(`<!doctype html><main>
<nav class="toc"><a href="#gone">Contents</a></nav><div class="breadcrumb">Breadcrumb</div>
<h1>Example page</h1><p>Text <strong>strong</strong>, <em>emphasis</em>, <code>x := 1</code>, and <a href="/docs/next/">next</a>.<br>Continues.</p>
<h2 id="feature">Feature</h2><div class="feature-state-notice feature-beta" title="Feature Gate: Demo"><span class="feature-state-stage">Beta</span> since Kubernetes v1.35; enabled by default</div>
<div class="alert alert-warning"><h4 class="alert-heading">Warning:</h4><p>Pay attention.</p></div>
<pre><code class="language-go">
fmt.Println(` + "`" + `tick` + "`" + `)
</code></pre>
<table><tr><th>Name</th><th>Value</th></tr><tr><td>a|b</td><td><strong>yes</strong></td></tr></table>
<blockquote><p>Quoted.</p></blockquote><details><summary>More</summary><p>Hidden text.</p></details>
<img src="copy.svg" onclick="copy()"><span class="ln">17</span></main>`))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Example page\n\nText **strong**, *emphasis*, `x := 1`, and [next](/docs/next/). Continues.\n\n## Feature\n\n**[FEATURE STATE: Beta | gate: Demo | since: v1.35 | enabled by default]**\n\n> [!WARNING]\n> Pay attention.\n\n```go\nfmt.Println(`tick`)\n```\n\n| Name | Value |\n| --- | --- |\n| a\\|b | **yes** |\n\n> Quoted.\n\n> **More**\n>\n> Hidden text.\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown =\n%s\nwant:\n%s", got, want)
	}
	if page.Title != "Example page" || len(page.Headings) != 1 || page.Headings[0].ID != "feature" {
		t.Fatalf("page headings = %#v, title = %q", page.Headings, page.Title)
	}
	if page.CodeBlocks != 1 || page.DegradedTables != 0 || page.DroppedElements != 0 || len(page.FeatureStates) != 1 {
		t.Fatalf("page metadata = %#v", page)
	}
}

func TestParsePageRendersListsTabsCardsAndDegradedTables(t *testing.T) {
	page, err := ParsePage(strings.NewReader(`<main><h1>Index</h1><ul><li>one<ul><li>nested</li></ul></li><li>two</li></ul>
<div class="card-group"><a href="/docs/a/">A</a><a href="/docs/b/">B</a></div>
<button id="panel-label">Linux</button><div class="tab-pane" aria-labelledby="panel-label"><p>Panel text.</p></div>
<table><tr><td rowspan="2">A</td><td>B</td></tr><tr><td>C</td></tr></table></main>`))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"- one\n  - nested\n- two", "- [A](/docs/a/)\n- [B](/docs/b/)", "**Panel: Linux**\n\nPanel text.", "A | B\nC"} {
		if !strings.Contains(string(page.Markdown), expected) {
			t.Fatalf("markdown missing %q:\n%s", expected, page.Markdown)
		}
	}
	if page.DegradedTables != 1 {
		t.Fatalf("degraded tables = %d", page.DegradedTables)
	}
}

func TestParsePageEmitsFencedCodeBlockWithMinimumFence(t *testing.T) {
	page, err := ParsePage(strings.NewReader(`<main><h1>Fences</h1><pre><code class="language-yaml">behavior:
  scaleDown:
    stabilizationWindowSeconds: 300
</code></pre></main>`))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Fences\n\n```yaml\nbehavior:\n  scaleDown:\n    stabilizationWindowSeconds: 300\n```\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown = %q, want %q", got, want)
	}
}

func TestParsePageRendersDefinitionLists(t *testing.T) {
	page, err := ParsePage(strings.NewReader(`<main><h1>Demo</h1><dl>
<dt><code>IfNotPresent</code></dt><dd>the image is pulled only if it is not already present locally.</dd>
<dt><code>Always</code></dt><dd>every time the kubelet requests the <a href="/docs/runtime/">container runtime</a> to pull the image.</dd>
<dt><code>Never</code></dt><dd><p>first block</p><ul><li>item</li></ul></dd>
</dl><dt>Orphan term</dt><dd>orphan definition</dd></main>`))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Demo\n\n**`IfNotPresent`**\n\nthe image is pulled only if it is not already present locally.\n\n" +
		"**`Always`**\n\nevery time the kubelet requests the [container runtime](/docs/runtime/) to pull the image.\n\n" +
		"**`Never`**\n\nfirst block\n\n- item\n\n**Orphan term**\n\norphan definition\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown =\n%s\nwant:\n%s", got, want)
	}
}

func TestParsePageRejectsMissingMainTitleAndOversizedInput(t *testing.T) {
	if _, err := ParsePage(strings.NewReader("<html><body><h1>Outside</h1></body></html>")); err == nil {
		t.Fatal("page without main was accepted")
	}
	if _, err := ParsePage(strings.NewReader("<main><p>No title</p></main>")); err == nil {
		t.Fatal("page without title was accepted")
	}
	oversized := "<main><h1>X</h1>" + strings.Repeat("x", maxRenderedPageBytes) + "</main>"
	if _, err := ParsePage(strings.NewReader(oversized)); err == nil {
		t.Fatal("oversized page was accepted")
	}
}

func TestParsePageSynthesizesTitleForEmptyUpstreamH1(t *testing.T) {
	page, err := ParsePage(strings.NewReader(`<html><head><meta property="og:title" content="Kubernetes Documentation"></head><main><h1></h1><h2 id="start">Start</h2></main></html>`))
	if err != nil || page.Title != "Kubernetes Documentation" || !strings.HasPrefix(string(page.Markdown), "# Kubernetes Documentation\n\n## Start") {
		t.Fatalf("page = %#v, error = %v", page, err)
	}
	page, err = ParsePage(strings.NewReader(`<html><head><meta property="og:title" content="Kubernetes"></head><main><h1></h1><h3 id="requirements">Requirements</h3></main></html>`))
	if err != nil || page.Title != "Requirements" || !strings.HasPrefix(string(page.Markdown), "# Requirements\n\n### Requirements") {
		t.Fatalf("fallback page = %#v, error = %v", page, err)
	}
}

func TestParsePageStripsHeadingSelfLinks(t *testing.T) {
	page, err := ParsePage(strings.NewReader(`<main><h1>Page<a class="td-heading-self-link" href="#page"></a></h1><h2 id="section">Section<a class="td-heading-self-link" href="#section"></a></h2></main>`))
	if err != nil || string(page.Markdown) != "# Page\n\n## Section\n" || len(page.Headings) != 1 || page.Headings[0].Title != "Section" {
		t.Fatalf("page = %#v, error = %v", page, err)
	}
}

func TestParsePageStripsFeedbackAndRejectsMalformedTitleMarkup(t *testing.T) {
	page, err := ParsePage(strings.NewReader(`<main><h1>Page</h1><p>Keep.</p><div id="pre-footer"><h2 id="feedback">Feedback</h2><p class="feedback--prompt">Rate this.</p></div></main>`))
	if err != nil || string(page.Markdown) != "# Page\n\nKeep.\n" {
		t.Fatalf("page = %#v, error = %v", page, err)
	}
	if _, err := ParsePage(strings.NewReader(`<main><h1 title="unterminated`)); err == nil {
		t.Fatal("malformed title markup was accepted")
	}
}
