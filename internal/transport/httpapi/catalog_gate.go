package httpapi

import (
	"net/http"

	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

// requireCatalogReady is applied only to routes that consume the current
// Catalog/Roadmap baseline. It intentionally excludes health, authentication,
// authoring discussion, and the Runtime Worker API so an in-progress release
// can finish installing without creating a readiness deadlock.
func (h *Handler) requireCatalogReady(c *gin.Context) {
	if h == nil || h.catalog == nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "catalog service is unavailable"})
		return
	}
	if err := h.catalog.Ready(c.Request.Context()); err != nil {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: err.Error()})
		return
	}
	c.Next()
}
