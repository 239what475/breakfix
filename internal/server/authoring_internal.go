package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/gin-gonic/gin"
)

type internalAuthoringStageRequest struct {
	authoring.LeaseCredential
	StageRevision int64            `json:"stage_revision"`
	Plan          authoring.Plan   `json:"plan"`
	Change        authoring.Change `json:"change"`
}

type internalAuthoringFinalizeRequest struct {
	authoring.LeaseCredential
	Content string `json:"content"`
}

// InternalAuthoringContext returns the private staged Plan for one active
// authoring Run. The stage never appears in the public authoring API until a
// successful finalization makes it into a public Revision.
func (h *Handler) InternalAuthoringContext(c *gin.Context) {
	var credential authoring.LeaseCredential
	if !h.decodeInternalAgentRequest(c, &credential) {
		return
	}
	claim, err := h.internalAuthoringClaim(c.Request.Context(), c.Param("id"), credential)
	if err != nil {
		h.writeInternalAuthoringError(c, err)
		return
	}
	stage, err := h.db.GetAuthoringStage(c.Request.Context(), claim.Run.ID)
	if err != nil {
		h.writeInternalAuthoringError(c, err)
		return
	}
	c.JSON(http.StatusOK, authoring.ExecutionContext{Stage: *stage})
}

// InternalAuthoringStage applies one model tool mutation to the private
// stage. The Server validates the current Run attempt and lease before any
// Authoring-domain write is allowed.
func (h *Handler) InternalAuthoringStage(c *gin.Context) {
	var request internalAuthoringStageRequest
	if !h.decodeInternalAgentRequest(c, &request) {
		return
	}
	claim, err := h.internalAuthoringClaim(c.Request.Context(), c.Param("id"), request.LeaseCredential)
	if err != nil {
		h.writeInternalAuthoringError(c, err)
		return
	}
	stage, err := h.db.UpdateAuthoringStage(c.Request.Context(), *claim, request.StageRevision, request.Plan, request.Change)
	if err != nil {
		h.writeInternalAuthoringError(c, err)
		return
	}
	c.JSON(http.StatusOK, stage)
}

// InternalAuthoringFinalize atomically exposes at most one public Revision,
// stores the final authoring reply, and marks the claimed Run as succeeded.
func (h *Handler) InternalAuthoringFinalize(c *gin.Context) {
	var request internalAuthoringFinalizeRequest
	if !h.decodeInternalAgentRequest(c, &request) {
		return
	}
	claim, err := h.internalAuthoringClaim(c.Request.Context(), c.Param("id"), request.LeaseCredential)
	if err != nil {
		h.writeInternalAuthoringError(c, err)
		return
	}
	if _, err := h.db.FinalizeAuthoringRun(c.Request.Context(), *claim, request.Content); err != nil {
		h.writeInternalAuthoringError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) internalAuthoringClaim(ctx context.Context, runID string, credential authoring.LeaseCredential) (*agentruntime.Claim, error) {
	if h.db == nil {
		return nil, errors.New("authoring runtime is unavailable")
	}
	if strings.TrimSpace(runID) == "" || credential.Attempt < 1 || strings.TrimSpace(credential.LeaseOwner) == "" {
		return nil, errors.New("authoring run lease credentials are required")
	}
	run, err := h.db.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Purpose != "authoring" || run.OwnerKind != "authoring-session" || strings.TrimSpace(run.OwnerRef) == "" || strings.TrimSpace(run.SessionID) == "" {
		return nil, errors.New("agent run is not a current authoring attempt")
	}
	if run.Attempt != credential.Attempt {
		return nil, agentruntime.ErrLeaseLost
	}
	claim := &agentruntime.Claim{Run: *run, LeaseOwner: credential.LeaseOwner}
	if err := h.db.ValidateLease(ctx, *claim, time.Now().UTC()); err != nil {
		return nil, err
	}
	return claim, nil
}

func (h *Handler) writeInternalAuthoringError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, authoring.ErrNotFound), errors.Is(err, agentruntime.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, authoring.ErrVersionConflict), errors.Is(err, agentruntime.ErrLeaseLost), errors.Is(err, authoring.ErrInvalidState):
		status = http.StatusConflict
	}
	c.JSON(status, api.ErrorResponse{Error: err.Error()})
}
