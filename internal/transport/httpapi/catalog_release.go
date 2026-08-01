package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

const catalogReleaseRecoveryInterval = 10 * time.Second

type installCatalogReleaseRequest struct {
	Bundle string `json:"bundle"`
}

// InstallCatalogRelease is intentionally an administrator-only initialization
// endpoint. It receives an immutable OCI reference and never accepts source
// files, a mutable tag, or a user-created candidate archive.
func (h *Handler) InstallCatalogRelease(c *gin.Context) {
	if !h.requireCatalogAdministrator(c) {
		return
	}
	if h.catalogInstaller == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "catalog installation is not configured"})
		return
	}
	var request installCatalogReleaseRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "invalid catalog release install request"})
		return
	}
	release, err := h.catalogInstaller.Install(c.Request.Context(), strings.TrimSpace(request.Bundle))
	if err != nil {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, release)
}

func (h *Handler) GetCatalogRelease(c *gin.Context) {
	if !h.requireCatalogAdministrator(c) {
		return
	}
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "catalog installation is not configured"})
		return
	}
	release, err := h.db.Catalog.GetRelease(c.Request.Context(), c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "catalog release not found"})
		return
	}
	c.JSON(http.StatusOK, release)
}

func (h *Handler) requireCatalogAdministrator(c *gin.Context) bool {
	if h == nil || len(h.catalogAdminToken) == 0 {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "catalog administrator authentication is not configured"})
		return false
	}
	provided := []byte(c.GetHeader("X-Breakfix-Catalog-Token"))
	if len(provided) == 0 || subtle.ConstantTimeCompare(provided, h.catalogAdminToken) != 1 {
		c.JSON(http.StatusForbidden, api.ErrorResponse{Error: "catalog administrator authentication failed"})
		return false
	}
	return true
}

// RecoverCatalogReleases resumes the Server-owned part of installation after
// a restart. Worker-owned side effects remain lease-fenced and are never
// replayed by this loop.
func (h *Handler) RecoverCatalogReleases(ctx context.Context) error {
	if h == nil || h.releaseCoordinator == nil {
		return nil
	}
	return h.releaseCoordinator.Recover(ctx)
}

func (h *Handler) StartCatalogReleaseRecovery(ctx context.Context) {
	if h == nil || h.releaseCoordinator == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(catalogReleaseRecoveryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := h.RecoverCatalogReleases(ctx); err != nil {
					slog.Error("recover catalog releases", "err", err)
				}
			}
		}
	}()
}

func (h *Handler) advanceCatalogRelease(ctx context.Context, workflow generation.Workflow) {
	if h == nil || h.releaseCoordinator == nil || workflow.Source.Kind != generation.SourceRelease {
		return
	}
	entry, err := h.db.Catalog.GetEntry(ctx, workflow.Source.Ref)
	if err == nil {
		err = h.releaseCoordinator.Advance(ctx, entry.ReleaseID)
	}
	if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
		slog.Error("advance catalog release", "workflow_id", workflow.ID, "entry_id", workflow.Source.Ref, "err", err)
	}
}
