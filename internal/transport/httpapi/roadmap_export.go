package httpapi

import (
	"errors"
	"mime"
	"net/http"

	"github.com/breakfix/breakfix/internal/adapter/postgres"
	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

// ExportRoadmapRevision is an intentionally internal debugging endpoint. It
// is not part of the browser API or OpenAPI contract.
func (h *Handler) ExportRoadmapRevision(c *gin.Context) {
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "database is not configured"})
		return
	}
	revision, err := h.db.Roadmap.RoadmapRevision(c.Request.Context(), c.Param("revision_id"))
	if errors.Is(err, postgres.ErrRoadmapRevisionNotFound) {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "roadmap revision not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	export, err := appcatalog.PrepareRoadmapRevisionExport(*revision, h.challengesDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	c.Header("Content-Type", "application/gzip")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": export.Filename()}))
	c.Status(http.StatusOK)
	if err := export.WriteArchiveTo(c.Writer); err != nil {
		_ = c.Error(err)
	}
}
