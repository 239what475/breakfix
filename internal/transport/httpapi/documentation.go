package httpapi

import (
	"net/http"
	"strings"

	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

// StartDocumentationPractice ignites one page of the pinned library. The
// route is gated by requireAdmin; the caller's identifier becomes the audit
// actor recorded together with the workflow creation.
func (h *Handler) StartDocumentationPractice(c *gin.Context) {
	if h == nil || h.documentation == nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "documentation practice is not configured"})
		return
	}
	var request api.DocumentationPracticeStartRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "page_path is required"})
		return
	}
	pagePath := strings.TrimSpace(request.PagePath)
	if pagePath == "" || strings.HasPrefix(pagePath, "/") || strings.Contains(pagePath, "\\") || pagePath == "." || strings.HasPrefix(pagePath, "../") || strings.Contains(pagePath, "/../") {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "page_path must be a safe relative path"})
		return
	}
	anchor := ""
	if request.Anchor != nil {
		anchor = strings.TrimSpace(*request.Anchor)
	}
	actorID, _ := c.Get("user_id")
	actor, _ := actorID.(string)
	workflow, err := h.documentation.StartDocumentationPractice(c.Request.Context(), actor, pagePath, anchor)
	if err != nil {
		if strings.Contains(err.Error(), "not part of the library") || strings.Contains(err.Error(), "request is invalid") {
			c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "start documentation practice: " + err.Error()})
			return
		}
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "start documentation practice: " + err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, api.DocumentationPracticeStart{WorkflowId: workflow.ID, State: string(workflow.State)})
}
