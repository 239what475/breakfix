package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/runnable"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

type runnableActionClaimRequest struct {
	WorkerID       string `json:"worker_id"`
	LeaseTTLMillis int64  `json:"lease_ttl_millis"`
}

type runnableActionRenewRequest struct {
	Credential     runnable.LeaseCredential `json:"credential"`
	LeaseTTLMillis int64                    `json:"lease_ttl_millis"`
}

type runnableSourceRequest struct {
	Credential runnable.LeaseCredential `json:"credential"`
}

type runnableMaterializationCompleteRequest struct {
	Credential runnable.LeaseCredential `json:"credential"`
	Revision   runnable.StoredRevision  `json:"revision"`
}

type runnableVerificationCompleteRequest struct {
	Credential runnable.LeaseCredential          `json:"credential"`
	Report     runnable.StoredVerificationReport `json:"report"`
}

type runnableActionFailureRequest struct {
	Credential runnable.LeaseCredential `json:"credential"`
	Class      runnable.FailureClass    `json:"class"`
	Code       string                   `json:"code"`
	Summary    string                   `json:"summary"`
}

func (h *Handler) InternalClaimRunnableAction(c *gin.Context) {
	var request runnableActionClaimRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	leaseTTL := time.Duration(request.LeaseTTLMillis) * time.Millisecond
	if strings.TrimSpace(request.WorkerID) == "" || leaseTTL < runtimeMinLease || leaseTTL > runtimeMaxLease {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "worker_id and a lease between 5 seconds and 2 minutes are required"})
		return
	}
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "runnable action store is unavailable"})
		return
	}
	action, err := h.db.Runnable.ClaimRunnableAction(c.Request.Context(), request.WorkerID, leaseTTL, time.Now().UTC())
	if err != nil {
		h.writeInternalRuntimeError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Action *runnable.ActionContext `json:"action,omitempty"`
	}{Action: action})
}

func (h *Handler) InternalRenewRunnableAction(c *gin.Context) {
	var request runnableActionRenewRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	leaseTTL := time.Duration(request.LeaseTTLMillis) * time.Millisecond
	if err := request.Credential.Validate(); err != nil || leaseTTL < runtimeMinLease || leaseTTL > runtimeMaxLease {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "valid credential and a lease between 5 seconds and 2 minutes are required"})
		return
	}
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "runnable action store is unavailable"})
		return
	}
	if err := h.db.Runnable.RenewRunnableAction(c.Request.Context(), request.Credential, leaseTTL, time.Now().UTC()); err != nil {
		h.writeInternalRuntimeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalDownloadRunnableSource(c *gin.Context) {
	var request runnableSourceRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	if err := request.Credential.Validate(); err != nil || request.Credential.Identity.Phase != runnable.ActionMaterializeArtifact {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "runnable source request is invalid"})
		return
	}
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "runnable source store is unavailable"})
		return
	}
	archive, err := h.db.Runnable.ReadRunnableActionSource(c.Request.Context(), request.Credential, time.Now().UTC())
	if err != nil {
		h.writeInternalRuntimeError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Archive []byte `json:"archive"`
	}{Archive: archive})
}

func (h *Handler) InternalCompleteRunnableMaterialization(c *gin.Context) {
	var request runnableMaterializationCompleteRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	request.Revision.CreatedAt = time.Now().UTC()
	if err := request.Credential.Validate(); err != nil || request.Credential.Identity.Phase != runnable.ActionMaterializeArtifact || request.Revision.Validate() != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "materialization completion is invalid"})
		return
	}
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "runnable action store is unavailable"})
		return
	}
	if err := h.db.Runnable.CompleteRunnableMaterialization(c.Request.Context(), request.Credential, request.Revision, request.Revision.CreatedAt); err != nil {
		h.writeInternalRuntimeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalCompleteRunnableVerification(c *gin.Context) {
	var request runnableVerificationCompleteRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	request.Report.CreatedAt = time.Now().UTC()
	if err := request.Credential.Validate(); err != nil || request.Credential.Identity.Phase != runnable.ActionVerify || request.Report.Validate() != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "verification completion is invalid"})
		return
	}
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "runnable action store is unavailable"})
		return
	}
	if err := h.db.Runnable.CompleteRunnableVerification(c.Request.Context(), request.Credential, request.Report, request.Report.CreatedAt); err != nil {
		h.writeInternalRuntimeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalReportRunnableActionFailure(c *gin.Context) {
	var request runnableActionFailureRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	if err := request.Credential.Validate(); err != nil || !request.Class.Valid() || strings.TrimSpace(request.Code) == "" || strings.TrimSpace(request.Summary) == "" {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "runnable action failure is invalid"})
		return
	}
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "runnable action store is unavailable"})
		return
	}
	if err := h.db.Runnable.ReportRunnableActionFailure(c.Request.Context(), request.Credential, request.Class, request.Code, request.Summary, time.Now().UTC()); err != nil {
		h.writeInternalRuntimeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
