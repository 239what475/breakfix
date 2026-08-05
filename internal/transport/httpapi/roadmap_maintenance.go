package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

const roadmapRunInterruptionReason = "server restarted before roadmap agent completion"

// RecoverRoadmapAgentRuns establishes the same startup fence as the other
// Server-owned AgentRun roles before maintenance starts claiming tasks.
func (h *Handler) RecoverRoadmapAgentRuns(ctx context.Context) error {
	if h == nil || h.db == nil {
		return nil
	}
	if err := h.db.Roadmap.RecoverInterruptedRoadmapAgentRuns(ctx, roadmapRunInterruptionReason, time.Now().UTC()); err != nil {
		return fmt.Errorf("recover roadmap agent runs: %w", err)
	}
	return nil
}

// StartRoadmapMaintenance starts the one Server-owned incremental Roadmap
// loop. Its PostgreSQL workflow/task leases make this safe in every Server
// replica without a separate Roadmap deployment.
func (h *Handler) StartRoadmapMaintenance(ctx context.Context) {
	if h == nil || h.roadmapMaintenance == nil {
		return
	}
	go h.roadmapMaintenance.Run(ctx)
}

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
