package docsproject

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"

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
	var blocks []string
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if block := r.block(child); block != "" {
			blocks = append(blocks, strings.TrimSpace(block))
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, "\n\n") + "\n"
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
	case "img", "a", "code", "strong", "b", "em", "i", "br":
		return strings.TrimSpace(r.inline(node))
	case "nav":
		return ""
	case "script", "style", "svg", "canvas", "button", "form", "input", "iframe":
		r.droppedElements++
		return ""
	case "div", "section", "article", "header", "footer", "figure", "figcaption", "aside", "dl", "dt", "dd", "fieldset", "legend":
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
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		result.WriteString(r.inline(child))
	}
	return strings.TrimSpace(result.String())
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
	case "a":
		if href := attribute(node, "href"); href != "" {
			if r.normalizer != nil {
				normalized, keep := r.normalizer.link(href)
				if !keep {
					return value
				}
				href = normalized
			}
			return "[" + strings.TrimSpace(value) + "](" + href + ")"
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
	case "span", "small", "sup", "sub", "mark", "time", "abbr", "cite", "label":
		return value
	default:
		return value
	}
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
		var body strings.Builder
		var nested []string
		for itemChild := child.FirstChild; itemChild != nil; itemChild = itemChild.NextSibling {
			if itemChild.Type == html.ElementNode && (itemChild.Data == "ul" || itemChild.Data == "ol") {
				nested = append(nested, r.list(itemChild, depth+1, itemChild.Data == "ol"))
				continue
			}
			if itemChild.Type == html.ElementNode && isBlockElement(itemChild.Data) {
				body.WriteString(r.block(itemChild))
			} else {
				body.WriteString(r.inline(itemChild))
			}
		}
		line := strings.Repeat("  ", depth) + prefix + strings.TrimSpace(body.String())
		lines = append(lines, strings.TrimRight(line, " \t\n"))
		for _, nestedList := range nested {
			lines = append(lines, nestedList)
		}
	}
	return strings.Join(lines, "\n")
}

func (r *pageRenderer) codeBlock(pre *html.Node) string {
	code := findElement(pre, func(node *html.Node) bool { return node.Data == "code" })
	if code == nil {
		code = pre
	}
	content := trimCodeNewlines(rawText(code))
	fence := strings.Repeat("`", longestBacktickRun(content)+1)
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
		var lines []string
		for _, row := range rows {
			var cells []string
			for _, cell := range row.cells {
				cells = append(cells, flattenMarkdown(r.inlineChildren(cell)))
			}
			lines = append(lines, strings.Join(cells, " | "))
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
	var parts []string
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if hasClass(child, "alert-heading") {
			continue
		}
		if block := r.block(child); block != "" {
			parts = append(parts, block)
		} else if text := strings.TrimSpace(r.inline(child)); text != "" {
			parts = append(parts, text)
		}
	}
	return "> [!" + kind + "]\n" + quote(strings.Join(parts, "\n\n"))
}

func (r *pageRenderer) details(node *html.Node) string {
	var parts []string
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && child.Data == "summary" {
			parts = append(parts, "**"+strings.TrimSpace(r.inlineChildren(child))+"**")
			continue
		}
		if block := r.block(child); block != "" {
			parts = append(parts, block)
		}
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
	leading := unicode.IsSpace(rune(value[0]))
	trailing := unicode.IsSpace(rune(value[len(value)-1]))
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

// ParsePage is exported for consumers that need to validate fixture inputs
// without running a complete projection.
func ParsePage(content io.Reader) (ExtractedPage, error) {
	bytes, err := io.ReadAll(io.LimitReader(content, maxRenderedPageBytes+1))
	if err != nil {
		return ExtractedPage{}, err
	}
	return extractPage(bytes)
}
