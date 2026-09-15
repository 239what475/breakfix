package httpapi

import (
	"net/http"

	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

func (h *Handler) StartDocumentationPractice(c *gin.Context) {
	if h == nil || h.documentation == nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "documentation practice is not configured"})
		return
	}
	workflow, err := h.documentation.StartDocumentationPractice(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "start documentation practice: " + err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, api.DocumentationPracticeStart{WorkflowId: workflow.ID, State: string(workflow.State)})
}
