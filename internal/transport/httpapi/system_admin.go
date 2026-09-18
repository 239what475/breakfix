package httpapi

import (
	"errors"
	"net/http"

	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

// GetAdminSystem reports the Server's own state: build metadata, the pinned
// documentation configuration, the live catalog integrity verdict, and the
// background service registry. External binaries (controller, runtime worker)
// are deliberately absent: the Server cannot see their heartbeats.
func (h *Handler) GetAdminSystem(c *gin.Context) {
	if h == nil || h.systemReport == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "system status is unavailable"})
		return
	}
	report, err := h.systemReport(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	status := api.AdminSystemStatus{
		Version:   report.Version,
		Commit:    report.Commit,
		BuildTime: report.BuildTime,
		Services:  make([]api.AdminBackgroundService, 0, len(report.Services)),
	}
	if report.CatalogReleaseReference != "" {
		status.CatalogReleaseReference = &report.CatalogReleaseReference
	}
	for _, service := range report.Services {
		status.Services = append(status.Services, api.AdminBackgroundService{
			Name:       service.Name,
			StartedAt:  service.StartedAt,
			LastTickAt: service.LastTickAt,
			LastError:  service.LastError,
		})
	}
	integrity := api.AdminCatalogIntegrity{State: "ok"}
	var integrityErr error
	if h.catalog != nil {
		integrityErr = h.catalog.CheckIntegrity(c.Request.Context())
	} else {
		integrityErr = errors.New("catalog service is unavailable")
	}
	if integrityErr != nil {
		integrity.State = "failed"
		detail := integrityErr.Error()
		integrity.Detail = &detail
	}
	status.CatalogIntegrity = integrity
	if report.Documentation != nil {
		documentation := &api.AdminDocumentationDeployment{
			SourceId:   report.Documentation.SourceID,
			Repository: report.Documentation.Repository,
			Revision:   report.Documentation.Revision,
			Version:    report.Documentation.Version,
			Language:   report.Documentation.Language,
			PagePath:   report.Documentation.PagePath,
			Anchor:     report.Documentation.Anchor,
		}
		if report.Documentation.ParserVersion != "" {
			documentation.ParserVersion = &report.Documentation.ParserVersion
		}
		if report.Documentation.UpstreamCommit != "" {
			documentation.UpstreamCommit = &report.Documentation.UpstreamCommit
		}
		status.Documentation = documentation
	}
	c.JSON(http.StatusOK, status)
}
