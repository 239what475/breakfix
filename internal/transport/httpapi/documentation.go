package httpapi

import (
	"net/http"

	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

// StartDocumentationPractice ignites the fixed workflow. The route is gated
// by requireAdmin; the caller's identifier becomes the audit actor recorded
// together with the workflow creation.
func (h *Handler) StartDocumentationPractice(c *gin.Context) {
	if h == nil || h.documentation == nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "documentation practice is not configured"})
		return
	}
	actorID, _ := c.Get("user_id")
	actor, _ := actorID.(string)
	workflow, err := h.documentation.StartDocumentationPractice(c.Request.Context(), actor)
	if err != nil {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "start documentation practice: " + err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, api.DocumentationPracticeStart{WorkflowId: workflow.ID, State: string(workflow.State)})
}
