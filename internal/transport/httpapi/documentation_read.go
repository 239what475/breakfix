package httpapi

import (
	"net/http"
	"path"
	"regexp"
	"strings"

	docsource "github.com/breakfix/breakfix/internal/adapter/documentation"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

// documentationLibrary serves the parsed library for the reader. Reads are
// verified against the offline digests before anything is served.
type documentationLibrary interface {
	ReadDocumentPage(string) (docsource.DocumentPage, error)
	// DocumentPageTitle resolves one page's title from the library manifests;
	// an unknown page yields an empty string.
	DocumentPageTitle(string) string
	// DocumentTitles lists every library page path with its title.
	DocumentTitles() map[string]string
	ReadDocumentTree(string) ([]docsource.DocumentTreeChild, error)
	ReadDocumentAsset(string) ([]byte, string, error)
	// PinnedContext is the verified source/commit/language identity reader
	// queries must be scoped to.
	PinnedContext() docsource.DocumentContext
}

// GetDocumentationPage serves one parsed page. Parsed pages are public read
// content, exactly like the catalog projection: the page carries no user data
// and its bytes are covered by the served digest.
func (h *Handler) GetDocumentationPage(c *gin.Context) {
	if h.documentationLibrary == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "documentation library is unavailable"})
		return
	}
	page, err := h.documentationLibrary.ReadDocumentPage(c.Query("path"))
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "documentation page is unavailable"})
		return
	}
	page.Markdown = rewriteAssetReferences(page.Markdown, page.Path, page.Assets)
	response := api.DocumentationPageResponse{
		Path:     page.Path,
		PageKind: page.PageKind,
		Title:    page.Title,
		Digest:   page.Digest,
		Markdown: page.Markdown,
		Anchors:  make([]api.DocumentationAnchor, 0, len(page.Anchors)),
	}
	for _, asset := range page.Assets {
		response.Assets = append(response.Assets, api.DocumentationAsset{Path: asset.Path, Digest: asset.Digest})
	}
	for _, anchor := range page.Anchors {
		response.Anchors = append(response.Anchors, api.DocumentationAnchor{Id: anchor.ID, Level: anchor.Level, Title: anchor.Title})
	}
	c.Header("ETag", `"`+page.Digest+`"`)
	if c.Request.Header.Get("If-None-Match") == `"`+page.Digest+`"` {
		// 304 carries no body; flush the status explicitly so it does not
		// depend on a later write.
		c.Status(http.StatusNotModified)
		c.Writer.WriteHeaderNow()
		return
	}
	c.JSON(http.StatusOK, response)
}

// GetDocumentationTree serves one level of the library tree so navigation
// loads a section at a time instead of the full corpus.
func (h *Handler) GetDocumentationTree(c *gin.Context) {
	if h.documentationLibrary == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "documentation library is unavailable"})
		return
	}
	children, err := h.documentationLibrary.ReadDocumentTree(c.Query("path"))
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "documentation tree node is unavailable"})
		return
	}
	response := api.DocumentationTreeResponse{Nodes: make([]api.DocumentationTreeNode, 0, len(children))}
	for _, child := range children {
		response.Nodes = append(response.Nodes, api.DocumentationTreeNode{Title: child.Title, Path: child.Path, HasChildren: child.HasChildren})
	}
	c.JSON(http.StatusOK, response)
}

// GetDocumentationAsset serves one static asset whose digest the page
// manifests pin. Content is served inline with its media type.
func (h *Handler) GetDocumentationAsset(c *gin.Context) {
	if h.documentationLibrary == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "documentation library is unavailable"})
		return
	}
	content, contentType, err := h.documentationLibrary.ReadDocumentAsset(c.Query("path"))
	if err != nil {
		if strings.Contains(err.Error(), "not part of the library") {
			c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "documentation asset is unavailable"})
			return
		}
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "documentation asset failed digest verification"})
		return
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Data(http.StatusOK, contentType, content)
}

var imageReferencePattern = regexp.MustCompile(`(!\[[^\]]*\]\()([^)\s]+)(\)| "[^"]*"\))`)

// rewriteAssetReferences points page-relative image references at the asset
// endpoint, resolving them to library-relative asset paths. References the
// generator did not record as assets are left untouched.
func rewriteAssetReferences(markdown, pagePath string, assets []docsource.DocumentAsset) string {
	known := make(map[string]bool, len(assets))
	for _, asset := range assets {
		known[asset.Path] = true
	}
	pageDir := path.Dir(strings.TrimSuffix(pagePath, "/"))
	return imageReferencePattern.ReplaceAllStringFunc(markdown, func(reference string) string {
		match := imageReferencePattern.FindStringSubmatch(reference)
		if match == nil {
			return reference
		}
		target := match[2]
		if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "/") || strings.HasPrefix(target, "data:") {
			return reference
		}
		resolved := path.Join(pageDir, target)
		if !known[resolved] {
			return reference
		}
		return match[1] + "/api/documentation/asset?path=" + resolved + match[3]
	})
}
