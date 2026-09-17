package docsproject

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const maxRenderedPageBytes = 2 * 1024 * 1024

// ExtractedPage is the restricted Markdown projection before manifest
// assembly. Its fields are intentionally independent of runtime components.
type ExtractedPage struct {
	Title           string
	Markdown        []byte
	Headings        []ExtractedHeading
	FeatureStates   []FeatureState
	Assets          []Asset
	Links           LinkStats
	CodeBlocks      int
	DegradedTables  int
	DroppedElements int
}

type ExtractedHeading struct {
	ID    string
	Level int
	Title string
}

type FeatureState struct {
	Anchor string `json:"anchor"`
	Gate   string `json:"gate,omitempty"`
	Stage  string `json:"stage"`
	Since  string `json:"since,omitempty"`
	Note   string `json:"note,omitempty"`
}

func extractPage(content []byte) (ExtractedPage, error) {
	return extractPageWithNormalizer(content, nil)
}

func extractPageWithNormalizer(content []byte, normalizer *pageNormalizer) (ExtractedPage, error) {
	if len(content) > maxRenderedPageBytes {
		return ExtractedPage{}, errors.New("rendered page exceeds the read limit")
	}
	document, err := html.Parse(bytes.NewReader(content))
	if err != nil {
		return ExtractedPage{}, fmt.Errorf("parse rendered page: %w", err)
	}
	main := findElement(document, func(node *html.Node) bool { return node.Data == "main" })
	if main == nil {
		return ExtractedPage{}, errors.New("rendered page has no main content")
	}
	renderer := pageRenderer{labels: elementsByID(document), normalizer: normalizer}
	markdown := renderer.blocks(main)
	if renderer.title == "" {
		fallback := fallbackTitle(document, renderer.headings)
		if fallback == "" {
			return ExtractedPage{}, errors.New("rendered page has no usable title")
		}
		renderer.title = fallback
		markdown = "# " + fallback + "\n\n" + markdown
	}
	if renderer.err != nil {
		return ExtractedPage{}, renderer.err
	}
	links, assets := LinkStats{}, []Asset(nil)
	if normalizer != nil {
		links, assets = normalizer.result()
	}
	return ExtractedPage{
		Title:           renderer.title,
		Markdown:        []byte(markdown),
		Headings:        renderer.headings,
		FeatureStates:   renderer.featureStates,
		Assets:          assets,
		Links:           links,
		CodeBlocks:      renderer.codeBlocks,
		DegradedTables:  renderer.degradedTables,
		DroppedElements: renderer.droppedElements,
	}, nil
}

type pageRenderer struct {
	labels          map[string]*html.Node
	title           string
	currentAnchor   string
	headings        []ExtractedHeading
	featureStates   []FeatureState
	codeBlocks      int
	degradedTables  int
	droppedElements int
	normalizer      *pageNormalizer
	err             error
}

func (r *pageRenderer) blocks(root *html.Node) string {
	blocks := r.contentBlocks(root, nil)
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, "\n\n") + "\n"
}

// contentBlocks renders a container's children as a sequence of Markdown
// blocks. Consecutive inline nodes (text plus inline elements) merge into a
// single paragraph — the pinned tree routinely leaves body text unwrapped
// next to headings, inside lead divs, callouts, and definitions, and those
// bare runs must not fragment or vanish. skip hides children the caller
// renders itself (alert headings, summaries).
func (r *pageRenderer) contentBlocks(root *html.Node, skip func(*html.Node) bool) []string {
	var blocks []string
	var pending strings.Builder
	var pendingLast *html.Node
	flush := func() {
		if text := strings.TrimSpace(pending.String()); text != "" {
			blocks = append(blocks, text)
		}
		pending.Reset()
		pendingLast = nil
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.CommentNode || skip != nil && skip(child) {
			continue
		}
		if isTransparentAnchor(child) {
			flush()
			blocks = append(blocks, r.contentBlocks(child, skip)...)
			continue
		}
		if isInlineNode(child) {
			value := r.inline(child)
			if value == "" {
				continue
			}
			if pending.Len() > 0 {
				pending.WriteString(boundary(pendingLast, pending.String(), value, child))
			}
			pending.WriteString(value)
			pendingLast = child
			continue
		}
		flush()
		if block := r.block(child); block != "" {
			blocks = append(blocks, strings.TrimSpace(block))
		}
	}
	flush()
	return blocks
}

func (r *pageRenderer) block(node *html.Node) string {
	if node == nil || node.Type == html.CommentNode {
		return ""
	}
	if node.Type == html.TextNode {
		return ""
	}
	if node.Type != html.ElementNode {
		return r.blocks(node)
	}
	if shouldStrip(node) {
		return ""
	}
	switch node.Data {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		level := int(node.Data[1] - '0')
		title := strings.TrimSpace(r.inlineChildren(node))
		if title == "" {
			return ""
		}
		if level == 1 && r.title == "" {
			r.title = title
		}
		if level >= 2 {
			id := attribute(node, "id")
			if id == "" {
				r.droppedElements++
			} else {
				r.currentAnchor = id
				r.headings = append(r.headings, ExtractedHeading{ID: id, Level: level, Title: title})
			}
		}
		return strings.Repeat("#", level) + " " + title
	case "p":
		return strings.TrimSpace(r.inlineChildren(node))
	case "ul", "ol":
		return r.list(node, 0, node.Data == "ol")
	case "pre":
		return r.codeBlock(node)
	case "table":
		return r.table(node)
	case "blockquote":
		return quote(r.blocks(node))
	case "details":
		return r.details(node)
	case "hr":
		return "---"
	case "dl":
		return r.definitionList(node)
	case "dt":
		return r.term(node)
	case "dd":
		return r.definition(node)
	case "img", "a", "code", "strong", "b", "em", "i", "br":
		return strings.TrimSpace(r.inline(node))
	case "nav":
		return ""
	case "script", "style", "svg", "canvas", "button", "form", "input", "iframe":
		r.droppedElements++
		return ""
	case "div", "section", "article", "header", "footer", "figure", "figcaption", "aside", "fieldset", "legend":
		if feature := r.featureState(node); feature != nil {
			r.featureStates = append(r.featureStates, *feature)
			return featureMarkdown(*feature)
		}
		if alert := alertKind(node); alert != "" {
			return r.alert(node, alert)
		}
		if hasClass(node, "tab-pane") {
			return r.tab(node)
		}
		if cardLinks := r.cardLinks(node); len(cardLinks) > 0 {
			return strings.Join(cardLinks, "\n")
		}
		return r.blocks(node)
	case "span", "small", "sup", "sub", "kbd", "mark", "time", "abbr", "cite":
		return strings.TrimSpace(r.inline(node))
	default:
		r.droppedElements++
		return ""
	}
}

func (r *pageRenderer) inlineChildren(node *html.Node) string {
	var result strings.Builder
	var last *html.Node
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		value := r.inline(child)
		if value == "" {
			continue
		}
		if result.Len() > 0 {
			result.WriteString(boundary(last, result.String(), value, child))
		}
		result.WriteString(value)
		last = child
	}
	return strings.TrimSpace(result.String())
}

// boundary inserts a separating space between two adjacent inline siblings
// when both are badge-styled containers (label/span, as in the metrics
// reference) and the rendered HTML carries no whitespace of its own there.
func boundary(last *html.Node, previous, next string, node *html.Node) string {
	if badgeNode(last) && badgeNode(node) && !endsWithSpace(previous) && !startsWithSpace(next) {
		return " "
	}
	return ""
}

func badgeNode(node *html.Node) bool {
	return node != nil && node.Type == html.ElementNode && (node.Data == "span" || node.Data == "label")
}

func endsWithSpace(value string) bool {
	last, _ := utf8.DecodeLastRuneInString(value)
	return unicode.IsSpace(last)
}

func startsWithSpace(value string) bool {
	first, _ := utf8.DecodeRuneInString(value)
	return unicode.IsSpace(first)
}

// definitionList keeps definition-list semantics readable in plain Markdown:
// each term renders as a bold line followed by its definition paragraphs.
func (r *pageRenderer) definitionList(node *html.Node) string {
	var blocks []string
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode {
			continue
		}
		switch child.Data {
		case "dt":
			if term := r.term(child); term != "" {
				blocks = append(blocks, term)
			}
		case "dd":
			if definition := strings.TrimSpace(r.definition(child)); definition != "" {
				blocks = append(blocks, definition)
			}
		}
	}
	return strings.Join(blocks, "\n\n")
}

func (r *pageRenderer) term(node *html.Node) string {
	title := strings.TrimSpace(r.inlineChildren(node))
	if title == "" {
		return ""
	}
	return "**" + title + "**"
}

func (r *pageRenderer) definition(node *html.Node) string {
	if hasBlockChild(node) {
		return r.blocks(node)
	}
	return strings.TrimSpace(r.inlineChildren(node))
}

func hasBlockChild(node *html.Node) bool {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode {
			continue
		}
		switch child.Data {
		case "p", "ul", "ol", "pre", "table", "blockquote", "dl", "details", "div", "figure", "section":
			return true
		}
	}
	return false
}

func (r *pageRenderer) inline(node *html.Node) string {
	if node == nil || node.Type == html.CommentNode {
		return ""
	}
	if node.Type == html.TextNode {
		return escapeText(collapseWhitespace(node.Data))
	}
	if node.Type != html.ElementNode || shouldStrip(node) {
		return ""
	}
	var content strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		content.WriteString(r.inline(child))
	}
	value := content.String()
	switch node.Data {
	case "br":
		return " "
	case "code", "kbd":
		return inlineCode(strings.TrimSpace(rawText(node)))
	case "strong", "b":
		return wrap("**", strings.TrimSpace(value))
	case "em", "i":
		return wrap("*", strings.TrimSpace(value))
	case "sup":
		return scriptValue(value, superscripts)
	case "sub":
		return scriptValue(value, subscripts)
	case "a":
		if hasClass(node, "glossary-tooltip") {
			// The pinned site renders glossary tooltips as plain terms with
			// a hover definition; they are not navigation links.
			return value
		}
		if href := attribute(node, "href"); href != "" {
			if r.normalizer != nil {
				normalized, keep := r.normalizer.link(href)
				if !keep {
					return value
				}
				href = normalized
			}
			text := strings.TrimSpace(value)
			if text == "" {
				// Anchors without visible text (permalink hooks and the
				// like) render as nothing in a browser.
				return ""
			}
			return "[" + text + "](" + href + ")"
		}
		return value
	case "img":
		if attribute(node, "onclick") != "" {
			return ""
		}
		src := attribute(node, "src")
		if r.normalizer != nil {
			var err error
			src, err = r.normalizer.image(src)
			if err != nil {
				var external *externalAssetError
				if errors.As(err, &external) {
					r.droppedElements++
					return ""
				}
				r.err = err
				return ""
			}
		}
		return "![" + escapeText(attribute(node, "alt")) + "](" + src + ")"
	case "span", "small", "mark", "time", "abbr", "cite", "label":
		return value
	default:
		return value
	}
}

// superscripts and subscripts map script content to the Unicode characters
// plain Markdown needs to keep exponents (2<sup>26</sup> bytes) readable.
var superscripts = map[rune]rune{
	'0': '⁰', '1': '¹', '2': '²', '3': '³', '4': '⁴', '5': '⁵', '6': '⁶', '7': '⁷', '8': '⁸', '9': '⁹',
	'+': '⁺', '-': '⁻', '=': '⁼', '(': '⁽', ')': '⁾', 'n': 'ⁿ', 'i': 'ⁱ',
}

var subscripts = map[rune]rune{
	'0': '₀', '1': '₁', '2': '₂', '3': '₃', '4': '₄', '5': '₅', '6': '₆', '7': '₇', '8': '₈', '9': '₉',
	'+': '₊', '-': '₋', '=': '₌', '(': '₍', ')': '₎', 'a': 'ₐ', 'e': 'ₑ', 'h': 'ₕ', 'i': 'ᵢ', 'j': 'ⱼ',
	'k': 'ₖ', 'l': 'ₗ', 'm': 'ₘ', 'n': 'ₙ', 'o': 'ₒ', 'p': 'ₚ', 'r': 'ᵣ', 's': 'ₛ', 't': 'ₜ', 'u': 'ᵤ',
	'v': 'ᵥ', 'x': 'ₓ',
}

// scriptValue maps every rune through the script table or falls back to the
// plain value, so unmappable exponents degrade to text instead of noise.
func scriptValue(value string, mapping map[rune]rune) string {
	if value == "" {
		return ""
	}
	var out strings.Builder
	for _, character := range value {
		mapped, ok := mapping[character]
		if !ok {
			return value
		}
		out.WriteRune(mapped)
	}
	return out.String()
}

func fallbackTitle(document *html.Node, headings []ExtractedHeading) string {
	meta := findElement(document, func(node *html.Node) bool {
		return node.Data == "meta" && attribute(node, "property") == "og:title"
	})
	if title := attribute(meta, "content"); title != "" && title != "Kubernetes" {
		return title
	}
	if len(headings) > 0 {
		return headings[0].Title
	}
	return ""
}

func (r *pageRenderer) list(list *html.Node, depth int, ordered bool) string {
	var lines []string
	itemNumber := 0
	for child := list.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != html.ElementNode || child.Data != "li" || shouldStrip(child) {
			continue
		}
		itemNumber++
		prefix := "- "
		if ordered {
			prefix = fmt.Sprintf("%d. ", itemNumber)
		}
		lines = append(lines, r.listItem(child, depth, prefix)...)
	}
	return strings.Join(lines, "\n")
}

// listItem renders one item's children in document order as a block
// sequence: paragraphs and fenced code stay on their own lines (indented to
// the content column), nested lists keep their position instead of being
// hoisted behind the item text. Only consecutive paragraphs need a blank
// line between them; every other block boundary starts unambiguously.
func (r *pageRenderer) listItem(item *html.Node, depth int, prefix string) []string {
	type itemBlock struct {
		text       string
		paragraph  bool
		selfIndent bool // nested lists already carry their own indentation
	}
	var itemBlocks []itemBlock
	var pending strings.Builder
	var pendingLast *html.Node
	flush := func() {
		if text := strings.TrimSpace(pending.String()); text != "" {
			itemBlocks = append(itemBlocks, itemBlock{text: text, paragraph: true})
		}
		pending.Reset()
		pendingLast = nil
	}
	for child := item.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.CommentNode {
			continue
		}
		if isTransparentAnchor(child) {
			flush()
			for _, block := range r.contentBlocks(child, nil) {
				itemBlocks = append(itemBlocks, itemBlock{text: block})
			}
			continue
		}
		if child.Type == html.ElementNode && (child.Data == "ul" || child.Data == "ol") {
			flush()
			itemBlocks = append(itemBlocks, itemBlock{text: r.list(child, depth+1, child.Data == "ol"), selfIndent: true})
			continue
		}
		if isInlineNode(child) {
			value := r.inline(child)
			if value == "" {
				continue
			}
			if pending.Len() > 0 {
				pending.WriteString(boundary(pendingLast, pending.String(), value, child))
			}
			pending.WriteString(value)
			pendingLast = child
			continue
		}
		flush()
		if block := r.block(child); block != "" {
			itemBlocks = append(itemBlocks, itemBlock{text: strings.TrimSpace(block), paragraph: child.Data == "p"})
		}
	}
	flush()

	var lines []string
	marker := strings.Repeat("  ", depth) + prefix
	indent := strings.Repeat("  ", depth) + strings.Repeat(" ", len(prefix))
	previousParagraph := false
	for index, block := range itemBlocks {
		text := strings.TrimRight(block.text, "\n")
		if text == "" {
			continue
		}
		if index > 0 && previousParagraph && block.paragraph && len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		previousParagraph = block.paragraph
		for lineIndex, line := range strings.Split(text, "\n") {
			switch {
			case block.selfIndent:
				if index == 0 && lineIndex == 0 {
					lines = append(lines, strings.TrimRight(marker, " "))
				}
				lines = append(lines, line)
			case index == 0 && lineIndex == 0:
				lines = append(lines, marker+line)
			case line == "":
				lines = append(lines, "")
			default:
				lines = append(lines, indent+line)
			}
		}
	}
	return lines
}

func (r *pageRenderer) codeBlock(pre *html.Node) string {
	code := findElement(pre, func(node *html.Node) bool { return node.Data == "code" })
	if code == nil {
		code = pre
	}
	content := trimCodeNewlines(rawText(code))
	// A code fence needs at least three backticks; one longer than any run
	// inside the content when the content itself contains backticks.
	fenceLength := longestBacktickRun(content) + 1
	if fenceLength < 3 {
		fenceLength = 3
	}
	fence := strings.Repeat("`", fenceLength)
	language := ""
	for _, class := range strings.Fields(attribute(code, "class")) {
		if strings.HasPrefix(class, "language-") {
			language = strings.TrimPrefix(class, "language-")
			break
		}
	}
	r.codeBlocks++
	return fence + language + "\n" + content + "\n" + fence
}

func (r *pageRenderer) table(table *html.Node) string {
	rows := tableRows(table)
	if len(rows) == 0 {
		return ""
	}
	degraded := false
	for _, row := range rows {
		for _, cell := range row.cells {
			if attribute(cell, "rowspan") != "" || attribute(cell, "colspan") != "" {
				degraded = true
			}
		}
	}
	if degraded {
		r.degradedTables++
		// Merged-cell tables still emit GFM shape so renderers recognize
		// them; rowspan/colspan content flattens into the owning cell.
		width := 0
		for _, row := range rows {
			if len(row.cells) > width {
				width = len(row.cells)
			}
		}
		if width == 0 {
			return ""
		}
		cells := func(row tableRow) []string {
			values := make([]string, 0, len(row.cells))
			for _, cell := range row.cells {
				values = append(values, strings.ReplaceAll(flattenMarkdown(r.inlineChildren(cell)), "|", "\\|"))
			}
			return values
		}
		var lines []string
		for index, row := range rows {
			row := cells(row)
			for len(row) < width {
				row = append(row, "")
			}
			lines = append(lines, "| "+strings.Join(row, " | ")+" |")
			if index == 0 {
				lines = append(lines, "| "+strings.TrimRight(strings.Repeat("--- | ", width), " "))
			}
		}
		return strings.Join(lines, "\n")
	}
	width := 0
	for _, row := range rows {
		if len(row.cells) > width {
			width = len(row.cells)
		}
	}
	if width == 0 {
		return ""
	}
	formatRow := func(row tableRow) string {
		cells := make([]string, width)
		for index, cell := range row.cells {
			cells[index] = strings.ReplaceAll(flattenMarkdown(r.inlineChildren(cell)), "|", "\\|")
		}
		return "| " + strings.Join(cells, " | ") + " |"
	}
	lines := []string{formatRow(rows[0]), "| " + strings.TrimRight(strings.Repeat("--- | ", width), " ")}
	for _, row := range rows[1:] {
		lines = append(lines, formatRow(row))
	}
	return strings.Join(lines, "\n")
}

type tableRow struct{ cells []*html.Node }

func tableRows(root *html.Node) []tableRow {
	var rows []tableRow
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node != root && node.Type == html.ElementNode && node.Data == "table" {
			return
		}
		if node.Type == html.ElementNode && node.Data == "tr" {
			row := tableRow{}
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == html.ElementNode && (child.Data == "th" || child.Data == "td") {
					row.cells = append(row.cells, child)
				}
			}
			rows = append(rows, row)
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return rows
}

func (r *pageRenderer) alert(node *html.Node, kind string) string {
	blocks := r.contentBlocks(node, func(child *html.Node) bool {
		return hasClass(child, "alert-heading")
	})
	if len(blocks) == 0 {
		return ""
	}
	return "> [!" + kind + "]\n" + quote(strings.Join(blocks, "\n\n"))
}

func (r *pageRenderer) details(node *html.Node) string {
	var summary *html.Node
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == "summary" {
			summary = child
			break
		}
	}
	parts := []string{}
	if summary != nil {
		if title := strings.TrimSpace(r.inlineChildren(summary)); title != "" {
			parts = append(parts, "**"+title+"**")
		}
	}
	parts = append(parts, r.contentBlocks(node, func(child *html.Node) bool {
		return child == summary
	})...)
	if len(parts) == 0 {
		return ""
	}
	return quote(strings.Join(parts, "\n\n"))
}

func (r *pageRenderer) tab(node *html.Node) string {
	label := strings.TrimSpace(attribute(node, "title"))
	if label == "" {
		for _, id := range strings.Fields(attribute(node, "aria-labelledby")) {
			if source := r.labels[id]; source != nil {
				label = normalizedText(source)
				break
			}
		}
	}
	if label == "" {
		label = strings.TrimSpace(attribute(node, "id"))
	}
	content := r.blocks(node)
	if label == "" {
		return content
	}
	if content == "" {
		return "**Panel: " + escapeText(label) + "**"
	}
	return "**Panel: " + escapeText(label) + "**\n\n" + strings.TrimSpace(content)
}

func (r *pageRenderer) cardLinks(node *html.Node) []string {
	class := attribute(node, "class")
	if !strings.Contains(class, "card-group") && !strings.Contains(class, "card-deck") && !strings.Contains(class, "card-grid") {
		return nil
	}
	var links []string
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current.Type == html.ElementNode && current.Data == "a" && attribute(current, "href") != "" {
			label := strings.TrimSpace(r.inlineChildren(current))
			if label != "" {
				href := attribute(current, "href")
				if r.normalizer != nil {
					var keep bool
					href, keep = r.normalizer.link(href)
					if !keep {
						return
					}
				}
				links = append(links, "- ["+label+"]("+href+")")
			}
			return
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return links
}

func (r *pageRenderer) featureState(node *html.Node) *FeatureState {
	if !hasClass(node, "feature-state-notice") {
		return nil
	}
	stage := ""
	for _, class := range strings.Fields(attribute(node, "class")) {
		if strings.HasPrefix(class, "feature-") && class != "feature-state-notice" && class != "feature-state-name" && class != "feature-state-details" && class != "feature-state-stage" {
			stage = strings.TrimPrefix(class, "feature-")
			break
		}
	}
	if stage == "" {
		stage = strings.ToLower(strings.TrimSpace(normalizedText(findElement(node, func(child *html.Node) bool {
			return hasClass(child, "feature-state-stage")
		}))))
	}
	if stage == "" {
		r.droppedElements++
		return nil
	}
	text := normalizedText(node)
	state := FeatureState{Anchor: r.currentAnchor, Stage: stage}
	if title := attribute(node, "title"); strings.HasPrefix(title, "Feature Gate:") {
		state.Gate = strings.TrimSpace(strings.TrimPrefix(title, "Feature Gate:"))
	}
	if since := sincePattern.FindStringSubmatch(text); len(since) == 2 {
		state.Since = since[1]
	}
	for _, note := range []string{"enabled by default", "disabled by default"} {
		if strings.Contains(strings.ToLower(text), note) {
			state.Note = note
			break
		}
	}
	return &state
}

var sincePattern = regexp.MustCompile(`(?i)since(?: Kubernetes)?\s+(v[0-9][A-Za-z0-9.\-]+)`)

func featureMarkdown(feature FeatureState) string {
	parts := []string{"FEATURE STATE: " + strings.Title(feature.Stage)}
	if feature.Gate != "" {
		parts = append(parts, "gate: "+feature.Gate)
	}
	if feature.Since != "" {
		parts = append(parts, "since: "+feature.Since)
	}
	if feature.Note != "" {
		parts = append(parts, feature.Note)
	}
	return "**[" + strings.Join(parts, " | ") + "]**"
}

func alertKind(node *html.Node) string {
	if !hasClass(node, "alert") {
		return ""
	}
	switch {
	case hasClass(node, "alert-warning"):
		return "WARNING"
	case hasClass(node, "alert-danger"), hasClass(node, "alert-caution"):
		return "CAUTION"
	case hasClass(node, "alert-success"):
		return "TIP"
	case hasClass(node, "alert-info"):
		return "NOTE"
	default:
		return ""
	}
}

func shouldStrip(node *html.Node) bool {
	classes := attribute(node, "class")
	if attribute(node, "id") == "pre-footer" || strings.Contains(classes, "feedback") || strings.Contains(classes, "rating") {
		return true
	}
	if hasClass(node, "icon-copycode") || hasClass(node, "td-heading-self-link") || hasClass(node, "breadcrumb") {
		return true
	}
	if node.Data == "nav" && strings.Contains(attribute(node, "class"), "toc") {
		return true
	}
	if node.Data == "img" && attribute(node, "onclick") != "" {
		return true
	}
	return (node.Data == "span" || node.Data == "div") && (hasClass(node, "ln") || hasClass(node, "lineno"))
}

func hasClass(node *html.Node, wanted string) bool {
	for _, class := range strings.Fields(attribute(node, "class")) {
		if class == wanted {
			return true
		}
	}
	return false
}

func elementsByID(root *html.Node) map[string]*html.Node {
	result := map[string]*html.Node{}
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if id := attribute(node, "id"); id != "" {
			result[id] = node
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return result
}

func rawText(root *html.Node) string {
	var text strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			text.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return text.String()
}

func collapseWhitespace(value string) string {
	if value == "" {
		return ""
	}
	first, _ := utf8.DecodeRuneInString(value)
	last, _ := utf8.DecodeLastRuneInString(value)
	leading := unicode.IsSpace(first)
	trailing := unicode.IsSpace(last)
	result := strings.Join(strings.Fields(value), " ")
	if result == "" {
		return " "
	}
	if leading {
		result = " " + result
	}
	if trailing {
		result += " "
	}
	return result
}

func escapeText(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "`", "\\`")
	trimmed := strings.TrimLeft(value, " ")
	prefix := value[:len(value)-len(trimmed)]
	if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, ">") {
		return prefix + "\\" + trimmed
	}
	return value
}

func inlineCode(value string) string {
	if value == "" {
		return "``"
	}
	fence := strings.Repeat("`", longestBacktickRun(value)+1)
	return fence + value + fence
}

func longestBacktickRun(value string) int {
	longest, current := 0, 0
	for _, character := range value {
		if character == '`' {
			current++
			if current > longest {
				longest = current
			}
		} else {
			current = 0
		}
	}
	return longest
}

func trimCodeNewlines(value string) string {
	value = strings.TrimPrefix(value, "\r\n")
	value = strings.TrimPrefix(value, "\n")
	value = strings.TrimSuffix(value, "\r\n")
	return strings.TrimSuffix(value, "\n")
}

func quote(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		if line == "" {
			lines[index] = ">"
		} else {
			lines[index] = "> " + line
		}
	}
	return strings.Join(lines, "\n")
}

func flattenMarkdown(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func wrap(marker, value string) string {
	if value == "" {
		return ""
	}
	return marker + value + marker
}

func isBlockElement(name string) bool {
	switch name {
	case "p", "div", "section", "article", "blockquote", "pre", "table", "ul", "ol", "details", "figure", "dl":
		return true
	default:
		return false
	}
}

// isInlineNode reports whether a node participates in an inline run during
// block iteration: text plus the inline-level elements of the pinned tree.
func isInlineNode(node *html.Node) bool {
	if node == nil {
		return false
	}
	if node.Type == html.TextNode {
		return true
	}
	if node.Type != html.ElementNode {
		return false
	}
	switch node.Data {
	case "a", "abbr", "b", "bdi", "bdo", "br", "cite", "code", "data", "del", "dfn", "em", "i", "img", "ins", "kbd", "label", "mark", "q", "s", "samp", "small", "span", "strong", "sub", "sup", "time", "tt", "u", "var":
		return true
	default:
		return false
	}
}

// isTransparentAnchor matches the XHTML-style deep-link anchors upstream
// pages write as <a id="x" />. The HTML5 parser keeps them open and swallows
// following blocks, so block iteration unwraps them instead of letting the
// swallowed content collapse into one inline run.
func isTransparentAnchor(node *html.Node) bool {
	return node != nil && node.Type == html.ElementNode && node.Data == "a" && attribute(node, "href") == ""
}

// ParsePage is exported for consumers that need to validate fixture inputs
// without running a complete projection.
func ParsePage(content io.Reader) (ExtractedPage, error) {
	bytes, err := io.ReadAll(io.LimitReader(content, maxRenderedPageBytes+1))
	if err != nil {
		return ExtractedPage{}, err
	}
	return extractPage(bytes)
}
