package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/domain/audit"
	"github.com/breakfix/breakfix/internal/domain/doclinks"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

// ListDocumentationLinks serves the shared aggregation list. It is public
// read content: the list carries no user data, matching the catalog
// projection's read model.
func (h *Handler) ListDocumentationLinks(c *gin.Context) {
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "documentation links are unavailable"})
		return
	}
	links, err := h.db.Documentation.ListDocumentationLinks(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "documentation links could not be listed"})
		return
	}
	response := api.DocumentationLinkList{Links: make([]api.DocumentationLink, 0, len(links))}
	for _, link := range links {
		response.Links = append(response.Links, documentationLinkAPI(link))
	}
	c.JSON(http.StatusOK, response)
}

// CreateDocumentationLink adds one link. The key is generated here so the
// identifier is opaque and rename-stable; the admin supplies title, URL, and
// the embed verdict.
func (h *Handler) CreateDocumentationLink(c *gin.Context) {
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "documentation links are unavailable"})
		return
	}
	var input api.AdminDocumentationLinkInput
	if bindDocumentationLinkInput(c, &input) {
		return
	}
	key, err := doclinks.NewKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "documentation link key could not be generated"})
		return
	}
	now := time.Now().UTC()
	link := doclinks.Link{Key: key, Title: input.Title, URL: input.Url, Embed: input.Embed, CreatedAt: now, UpdatedAt: now}
	if respondDocumentationLinkInvalid(c, link.Validate()) {
		return
	}
	if err := h.db.Documentation.CreateDocumentationLink(c.Request.Context(), link); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "documentation link could not be stored"})
		return
	}
	h.recordDocumentationLinkAudit(c, "create", link)
	c.JSON(http.StatusOK, documentationLinkAPI(link))
}

// UpdateDocumentationLink replaces the mutable fields wholesale. PATCH keeps
// the key path stable so a rename never invalidates saved ?doc=<key> links.
func (h *Handler) UpdateDocumentationLink(c *gin.Context, key string) {
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "documentation links are unavailable"})
		return
	}
	var input api.AdminDocumentationLinkInput
	if bindDocumentationLinkInput(c, &input) {
		return
	}
	link, err := h.db.Documentation.UpdateDocumentationLink(c.Request.Context(), key, input.Title, input.Url, input.Embed, time.Now().UTC())
	if respondDocumentationLinkInvalid(c, err) {
		return
	}
	if respondDocumentationLinkMissing(c, err) {
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "documentation link could not be updated"})
		return
	}
	h.recordDocumentationLinkAudit(c, "update", link)
	c.JSON(http.StatusOK, documentationLinkAPI(link))
}

// DeleteDocumentationLink removes one link; deleting an already-gone link is
// a 404 so double clicks surface instead of silently succeeding.
func (h *Handler) DeleteDocumentationLink(c *gin.Context, key string) {
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "documentation links are unavailable"})
		return
	}
	previous, err := h.db.Documentation.GetDocumentationLink(c.Request.Context(), key)
	if respondDocumentationLinkMissing(c, err) {
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "documentation link could not be read"})
		return
	}
	deleted, err := h.db.Documentation.DeleteDocumentationLink(c.Request.Context(), key)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "documentation link could not be deleted"})
		return
	}
	if !deleted {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "documentation link not found"})
		return
	}
	h.recordDocumentationLinkAudit(c, "delete", previous)
	c.JSON(http.StatusOK, api.AdminDocumentationLinkDeletion{Key: key})
}

func documentationLinkAPI(link doclinks.Link) api.DocumentationLink {
	return api.DocumentationLink{Key: link.Key, Title: link.Title, Url: link.URL, Embed: link.Embed}
}

// bindDocumentationLinkInput decodes the write body and answers whether the
// response is already written.
func bindDocumentationLinkInput(c *gin.Context, input *api.AdminDocumentationLinkInput) bool {
	if err := c.ShouldBindJSON(input); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "invalid documentation link request"})
		return true
	}
	return false
}

func respondDocumentationLinkInvalid(c *gin.Context, err error) bool {
	if errors.Is(err, doclinks.ErrLinkInvalid) {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return true
	}
	return false
}

func respondDocumentationLinkMissing(c *gin.Context, err error) bool {
	if errors.Is(err, postgres.ErrDocumentationLinkNotFound) {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "documentation link not found"})
		return true
	}
	return false
}

// recordDocumentationLinkAudit appends one ledger row per admin write verb.
// Audit failures never fail the mutation; the endpoint already committed.
func (h *Handler) recordDocumentationLinkAudit(c *gin.Context, op string, link doclinks.Link) {
	actorID, _ := c.Get("user_id")
	actor, _ := actorID.(string)
	if h == nil || h.db == nil || actor == "" {
		return
	}
	now := time.Now().UTC()
	detail, err := json.Marshal(map[string]string{"op": op, "title": link.Title, "url": link.URL})
	if err != nil {
		slog.Warn("marshal documentation link audit", "key", link.Key, "err", err)
		return
	}
	action := audit.HumanAction{
		ID:         audit.NewID(now),
		UserID:     actor,
		Action:     audit.ActionDocumentationLinkWrite,
		TargetType: audit.TargetDocumentationLink,
		TargetID:   link.Key,
		Detail:     detail,
		CreatedAt:  now,
	}
	if err := h.db.Audit.RecordHumanAction(c.Request.Context(), action); err != nil {
		slog.Warn("record documentation link audit", "key", link.Key, "op", op, "err", err)
	}
}
