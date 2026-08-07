package httpapi

import (
	"net/http"
	"time"

	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

// RequestRoadmapMaintenance is intentionally an internal debugging endpoint,
// not a user-facing Catalog action. The durable request still waits for the
// same idle-window gate before the Server creates a workflow.
func (h *Handler) RequestRoadmapMaintenance(c *gin.Context) {
	if h == nil || h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "roadmap maintenance is unavailable"})
		return
	}
	requested, err := h.db.Roadmap.RequestRoadmapMaintenance(c.Request.Context(), time.Now().UTC())
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"requested": requested})
}
