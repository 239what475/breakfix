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
	for _, expected := range []string{"- one\n  - nested\n- two", "- [A](/docs/a/)\n- [B](/docs/b/)", "**Panel: Linux**\n\nPanel text.", "| A | B |\n| --- | --- |\n| C |  |"} {
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

func TestParsePageGroupsBareTextRunsIntoParagraphs(t *testing.T) {
	page, err := ParsePage(strings.NewReader(`<main><h1>Arch</h1>
<div class="lead">The architectural concepts behind Kubernetes.</div>
<h3 id="ccm">cloud-controller-manager</h3>A Kubernetes <a class='glossary-tooltip' href='/docs/reference/glossary/?all=true#term-control-plane'>control plane</a> component that embeds cloud-specific control logic.
<div class="alert alert-secondary callout third-party-content"><strong>Note:</strong> This section links to third party projects. To add a project, read the <a href="/docs/contribute/style/content-guide/">content guide</a> before submitting a change.</div></main>`))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Arch\n\nThe architectural concepts behind Kubernetes.\n\n### cloud-controller-manager\n\nA Kubernetes control plane component that embeds cloud-specific control logic.\n\n**Note:** This section links to third party projects. To add a project, read the [content guide](/docs/contribute/style/content-guide/) before submitting a change.\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown =\n%s\nwant:\n%s", got, want)
	}
}

func TestParsePageKeepsAlertBareTextInOneParagraph(t *testing.T) {
	page, err := ParsePage(strings.NewReader(`<main><h1>Notes</h1><div class="alert alert-info" role="note"><h4 class="alert-heading">Note:</h4>You can also run the manager as a Kubernetes <a href="/docs/concepts/cluster-administration/addons/">addon</a> rather than as part of the control plane.</div></main>`))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Notes\n\n> [!NOTE]\n> You can also run the manager as a Kubernetes [addon](/docs/concepts/cluster-administration/addons/) rather than as part of the control plane.\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown =\n%s\nwant:\n%s", got, want)
	}
}

func TestParsePageUnwrapsSelfClosedAnchors(t *testing.T) {
	page, err := ParsePage(strings.NewReader(`<main><h1>Auth</h1><a id="warning-always-allow" /><div class="alert alert-danger" role="note"><h4 class="alert-heading">Warning:</h4><p>Enabling the mode bypasses authorization.</p></div></main>`))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Auth\n\n> [!CAUTION]\n> Enabling the mode bypasses authorization.\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown =\n%s\nwant:\n%s", got, want)
	}
}

func TestParsePageRendersListItemBlockChildrenInOrder(t *testing.T) {
	page, err := ParsePage(strings.NewReader(`<main><h1>Flow</h1><ol><li>Start the agent:<pre><code class="language-shell">agent --start</code></pre><div class="alert alert-info"><h4 class="alert-heading">Note:</h4>The socket is per-node.</div>Then verify.</li><li>Second.</li></ol><ul><li>Outer text<ul><li>inner</li></ul>Tail after the sublist.</li></ul></main>`))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Flow\n\n" +
		"1. Start the agent:\n" +
		"   ```shell\n" +
		"   agent --start\n" +
		"   ```\n" +
		"   > [!NOTE]\n" +
		"   > The socket is per-node.\n" +
		"   Then verify.\n" +
		"2. Second.\n\n" +
		"- Outer text\n" +
		"  - inner\n" +
		"  Tail after the sublist.\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown =\n%s\nwant:\n%s", got, want)
	}
}

func TestParsePageRendersGlossaryTooltipAsPlainText(t *testing.T) {
	page, err := ParsePage(strings.NewReader(`<main><h1>Gloss</h1><p>A <a class='glossary-tooltip' title='definition' data-bs-toggle='tooltip' href='/docs/reference/glossary/?all=true#term-cluster' target='_blank'>cluster</a> of nodes.</p></main>`))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Gloss\n\nA cluster of nodes.\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown =\n%s\nwant:\n%s", got, want)
	}
}

func TestParsePagePreservesMultibyteSpaceBoundaries(t *testing.T) {
	page, err := ParsePage(strings.NewReader("<main><h1>Steps</h1><p>This is shown as step\u00a0<strong>2</strong>\u00a0in the diagram.</p></main>"))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Steps\n\nThis is shown as step **2** in the diagram.\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown = %q, want %q", got, want)
	}
}

func TestParsePageMapsScriptElementsToUnicode(t *testing.T) {
	page, err := ParsePage(strings.NewReader(`<main><h1>Units</h1><p>2<sup>26</sup> bytes and H<sub>2</sub>O plus <sup>v1.2</sup> fallback.</p></main>`))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Units\n\n2²⁶ bytes and H₂O plus v1.2 fallback.\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown =\n%s\nwant:\n%s", got, want)
	}
}

func TestParsePageSpacesBadgeLabelRuns(t *testing.T) {
	page, err := ParsePage(strings.NewReader(`<main><h1>Metrics</h1><ul><li><label class="metric_detail">Labels:</label><span class="metric_label">name</span><span class="metric_label">verb</span></li></ul></main>`))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Metrics\n\n- Labels: name verb\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown =\n%s\nwant:\n%s", got, want)
	}
}

func TestParsePageRendersMetricNameAndHelp(t *testing.T) {
	page, err := ParsePage(strings.NewReader(`<main><h1>Metrics</h1><div class="metric" data-stability="stable"><div class="metric_name">apiserver_requests_total</div><div class="metric_help">Counter of requests.</div><ul><li><label>Stability Level:</label><span>STABLE</span></li></ul></div></main>`))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Metrics\n\napiserver_requests_total\n\nCounter of requests.\n\n- Stability Level: STABLE\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown =\n%s\nwant:\n%s", got, want)
	}
}

func TestParsePageDropsEmptyTextAnchors(t *testing.T) {
	page, err := ParsePage(strings.NewReader(`<main><h1>Gloss</h1><p>Dockershim<a href="#term-dockershim" class="permalink"></a> is gone.</p></main>`))
	if err != nil {
		t.Fatal(err)
	}
	want := "# Gloss\n\nDockershim is gone.\n"
	if got := string(page.Markdown); got != want {
		t.Fatalf("markdown =\n%s\nwant:\n%s", got, want)
	}
}
