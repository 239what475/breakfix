package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/gin-gonic/gin"
)

func (h *Handler) DownloadVerifySubmissionArtifact(c *gin.Context) {
	if h.internalAPIKey == "" {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "internal download disabled"})
		return
	}
	if c.GetHeader("X-Breakfix-Internal-Key") != h.internalAPIKey {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: "invalid internal key"})
		return
	}

	submissionID := strings.TrimSpace(c.Param("id"))
	if submissionID == "" {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "missing submission id"})
		return
	}
	path := challenge.SubmissionPath(h.dataDir, submissionID)
	f, err := os.Open(path)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "submission artifact not found"})
		return
	}
	defer f.Close()

	c.Header("Content-Type", "application/gzip")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s.tar.gz", submissionID))
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, f)
}
