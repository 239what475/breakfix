package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/worklist"
	"github.com/gin-gonic/gin"
)

const (
	minimumWorkerLease = 5 * time.Second
	maximumWorkerLease = 2 * time.Minute
)

func (h *Handler) InternalClaimAgentWork(c *gin.Context) {
	var request struct {
		WorkerID       string `json:"worker_id"`
		LeaseTTLMillis int64  `json:"lease_ttl_millis"`
	}
	if !h.decodeInternalAgentRequest(c, &request) {
		return
	}
	leaseTTL := time.Duration(request.LeaseTTLMillis) * time.Millisecond
	if strings.TrimSpace(request.WorkerID) == "" || leaseTTL < minimumWorkerLease || leaseTTL > maximumWorkerLease {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "worker_id and a lease between 5 seconds and 2 minutes are required"})
		return
	}
	claim, err := h.db.ClaimNext(c.Request.Context(), request.WorkerID, leaseTTL, time.Now().UTC())
	if err != nil {
		h.worklistMetrics.recordFailure(worklist.KindAgent, "claim")
		h.writeInternalWorkError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Claim *agentruntime.Claim `json:"claim,omitempty"`
	}{Claim: claim})
}

func (h *Handler) InternalAgentRunStatus(c *gin.Context) {
	var request struct{}
	if !h.decodeInternalAgentRequest(c, &request) {
		return
	}
	run, err := h.db.GetRun(c.Request.Context(), c.Param("id"))
	if err != nil {
		h.writeInternalWorkError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Run agentruntime.Run `json:"run"`
	}{Run: *run})
}

func (h *Handler) InternalRenewAgentWork(c *gin.Context) {
	var request struct {
		agentruntime.LeaseCredential
		LeaseTTLMillis int64 `json:"lease_ttl_millis"`
	}
	if !h.decodeInternalAgentRequest(c, &request) {
		return
	}
	leaseTTL := time.Duration(request.LeaseTTLMillis) * time.Millisecond
	if leaseTTL < minimumWorkerLease || leaseTTL > maximumWorkerLease {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "lease must be between 5 seconds and 2 minutes"})
		return
	}
	claim, err := h.agentClaim(c, request.LeaseCredential)
	if err == nil {
		err = h.db.RenewLease(c.Request.Context(), *claim, leaseTTL, time.Now().UTC())
	}
	if err != nil {
		h.worklistMetrics.recordFailure(worklist.KindAgent, "renew")
	}
	h.finishInternalWorkMutation(c, err)
}

func (h *Handler) InternalRequeueAgentWork(c *gin.Context) {
	var request struct {
		agentruntime.LeaseCredential
		NextRunAt time.Time `json:"next_run_at"`
		Error     string    `json:"error"`
	}
	if !h.decodeInternalAgentRequest(c, &request) {
		return
	}
	claim, err := h.agentClaim(c, request.LeaseCredential)
	if err == nil {
		now := time.Now().UTC()
		if request.NextRunAt.Before(now) {
			request.NextRunAt = now
		}
		if claim.Run.DeadlineAt == nil {
			err = agentruntime.ErrLeaseLost
		} else if request.NextRunAt.After(*claim.Run.DeadlineAt) {
			request.NextRunAt = *claim.Run.DeadlineAt
		}
		if err == nil {
			err = h.db.Requeue(c.Request.Context(), *claim, request.NextRunAt, request.Error, now)
		}
	}
	h.finishInternalWorkMutation(c, err)
}

func (h *Handler) InternalCompleteAgentWork(c *gin.Context) {
	var request struct {
		agentruntime.LeaseCredential
		Message agentruntime.Message `json:"message"`
	}
	if !h.decodeInternalAgentRequest(c, &request) {
		return
	}
	claim, err := h.agentClaim(c, request.LeaseCredential)
	if err == nil {
		err = h.db.CompleteWithMessage(c.Request.Context(), *claim, request.Message, time.Now().UTC())
	}
	h.finishInternalWorkMutation(c, err)
}

func (h *Handler) InternalFailAgentWork(c *gin.Context) {
	var request struct {
		agentruntime.LeaseCredential
		Error string `json:"error"`
	}
	if !h.decodeInternalAgentRequest(c, &request) {
		return
	}
	now := time.Now().UTC()
	claim, err := h.agentClaim(c, request.LeaseCredential)
	if err == nil {
		err = h.db.Fail(c.Request.Context(), *claim, request.Error, now)
	}
	if err == nil {
		_, err = h.db.ReconcileFailedGeneratorRuns(c.Request.Context(), now)
	}
	h.finishInternalWorkMutation(c, err)
}

func (h *Handler) agentClaim(c *gin.Context, credential agentruntime.LeaseCredential) (*agentruntime.Claim, error) {
	return h.getAgentClaim(c.Request.Context(), c.Param("id"), credential)
}

func (h *Handler) getAgentClaim(ctx context.Context, runID string, credential agentruntime.LeaseCredential) (*agentruntime.Claim, error) {
	if h.db == nil || !credential.Valid() || strings.TrimSpace(runID) == "" {
		return nil, errors.New("valid agent work item credentials are required")
	}
	claim, err := h.db.GetAgentClaim(ctx, runID, credential.Attempt, credential.LeaseOwner, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if claim.WorkItemID != credential.WorkItemID {
		return nil, agentruntime.ErrLeaseLost
	}
	return claim, nil
}

func (h *Handler) finishInternalWorkMutation(c *gin.Context, err error) {
	if err != nil {
		h.writeInternalWorkError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) writeInternalWorkError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, agentruntime.ErrLeaseLost), errors.Is(err, worklist.ErrLeaseLost):
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "work item lease was lost"})
	case errors.Is(err, agentruntime.ErrNotFound), errors.Is(err, worklist.ErrNotFound):
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "work item was not found"})
	default:
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
	}
}
