package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

type runnableExecutionOutputRequest struct {
	Credential runnable.LeaseCredential `json:"credential"`
	Capture    runnable.OutputCapture   `json:"capture"`
}

const maxRunnableExecutionOutputRequestBytes = 1024 * 1024

type runnableMaterializationCompleteRequest struct {
	Credential runnable.LeaseCredential `json:"credential"`
	Revision   runnable.StoredRevision  `json:"revision"`
}

type runnableVerificationCompleteRequest struct {
	Credential runnable.LeaseCredential          `json:"credential"`
	Report     runnable.StoredVerificationReport `json:"report"`
}

type runnableVerificationEnvironmentRequest struct {
	Request runnable.VerifyRequest `json:"request"`
}

type runnableVerificationEnvironmentReleaseRequest struct {
	Credential  runnable.LeaseCredential     `json:"credential"`
	Environment runnable.EnvironmentIdentity `json:"environment"`
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

func (h *Handler) InternalStoreRunnableExecutionOutput(c *gin.Context) {
	var request runnableExecutionOutputRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRunnableExecutionOutputRequestBytes)
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	if err := request.Credential.Validate(); err != nil || request.Credential.Identity.Phase != runnable.ActionVerify || request.Capture.Validate() != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "runnable execution output request is invalid"})
		return
	}
	if h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "runnable execution output store is unavailable"})
		return
	}
	reference, err := h.db.Runnable.StoreRunnableExecutionOutput(c.Request.Context(), request.Credential, request.Capture, time.Now().UTC())
	if err != nil {
		h.writeInternalRuntimeError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Reference runnable.ImmutableReference `json:"reference"`
	}{Reference: reference})
}

// InternalCreateRunnableVerificationEnvironment is the sole verification
// environment spec creation path. The Worker submits a complete lease-fenced
// request, while Server validates it against durable action state and creates
// or adopts the deterministic RuntimeEnvironment on its behalf.
func (h *Handler) InternalCreateRunnableVerificationEnvironment(c *gin.Context) {
	var request runnableVerificationEnvironmentRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	if err := request.Request.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "verification environment request is invalid"})
		return
	}
	if h.db == nil || h.k8s == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "verification environment control plane is unavailable"})
		return
	}
	now := time.Now().UTC()
	if err := h.db.Runnable.ValidateRunnableVerificationLease(c.Request.Context(), request.Request.Credential, request.Request.RunnableRevisionRef, request.Request.Attempt, now); err != nil {
		h.writeInternalRuntimeError(c, err)
		return
	}
	name := runnable.VerificationEnvironmentName(request.Request.RunnableRevisionRef, request.Request.Attempt)
	environment := &runtimev2.RuntimeEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: runtimev2.RuntimeEnvironmentSpec{
			RunnableRevisionRef: runtimev2.RunnableRevisionReference{ID: request.Request.RunnableRevisionRef.ID, Digest: request.Request.RunnableRevisionDigest},
			Purpose:             runtimev2.PurposeVerification,
			Lease:               runtimev2.LeaseSpec{RenewedAt: metav1.NewTime(now)},
		},
	}
	created, err := h.k8s.CreateRuntimeEnvironment(c.Request.Context(), h.crdNamespace, environment)
	if apierrors.IsAlreadyExists(err) {
		created, err = h.k8s.GetRuntimeEnvironment(c.Request.Context(), h.crdNamespace, name)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "create verification environment: " + err.Error()})
		return
	}
	if created == nil || created.UID == "" || created.Spec.RunnableRevisionRef.ID != request.Request.RunnableRevisionRef.ID || created.Spec.RunnableRevisionRef.Digest != request.Request.RunnableRevisionDigest || created.Spec.Purpose != runtimev2.PurposeVerification {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "verification environment is bound to another runnable revision"})
		return
	}
	// Recheck after a potentially slow Kubernetes request. If the lease was
	// lost, release the newly observed object rather than leaving an orphan.
	if err := h.db.Runnable.ValidateRunnableVerificationLease(c.Request.Context(), request.Request.Credential, request.Request.RunnableRevisionRef, request.Request.Attempt, time.Now().UTC()); err != nil {
		if releaseErr := h.markVerificationEnvironmentReleasable(c.Request.Context(), string(created.UID), request.Request.RunnableRevisionDigest); releaseErr != nil {
			slog.Warn("release verification environment after lost lease", "environment", created.UID, "err", releaseErr)
		}
		h.writeInternalRuntimeError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Environment *runtimev2.RuntimeEnvironment `json:"environment"`
	}{Environment: created})
}

func (h *Handler) InternalRequestRunnableVerificationEnvironmentRelease(c *gin.Context) {
	var request runnableVerificationEnvironmentReleaseRequest
	if !h.decodeInternalWorkerRequest(c, internalRuntimeRole, &request) {
		return
	}
	if err := request.Credential.Validate(); err != nil || request.Credential.Identity.Phase != runnable.ActionVerify || request.Environment.Validate() != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "verification environment release request is invalid"})
		return
	}
	if h.db == nil || h.k8s == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "verification environment control plane is unavailable"})
		return
	}
	reference, _, err := h.db.Runnable.ResolveRunnableVerificationLease(c.Request.Context(), request.Credential, time.Now().UTC())
	if err != nil {
		h.writeInternalRuntimeError(c, err)
		return
	}
	if err := h.markVerificationEnvironmentReleasable(c.Request.Context(), request.Environment.ID, reference.Digest); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "request verification environment release: " + err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
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
	if err := h.markVerificationEnvironmentReleasable(c.Request.Context(), request.Report.Report.Environment.ID, request.Report.Report.RunnableRevisionDigest); err != nil {
		// Report persistence is the workflow boundary. Reaper handoff failures
		// remain observable but cannot roll the immutable report back.
		slog.Warn("request verification environment release", "environment", request.Report.Report.Environment.ID, "err", err)
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) markVerificationEnvironmentReleasable(ctx context.Context, environmentID, revisionDigest string) error {
	if h == nil || h.k8s == nil || strings.TrimSpace(environmentID) == "" || !runnable.ValidDigest(revisionDigest) {
		return errors.New("verification environment release request is invalid")
	}
	items, err := h.k8s.ListRuntimeEnvironments(ctx, h.crdNamespace, "")
	if err != nil {
		return err
	}
	for index := range items.Items {
		environment := items.Items[index].DeepCopy()
		if string(environment.UID) != environmentID {
			continue
		}
		if environment.Spec.Purpose != runtimev2.PurposeVerification || environment.Spec.RunnableRevisionRef.Digest != revisionDigest {
			return errors.New("verification environment is bound to another runnable revision")
		}
		if environment.Spec.Lease.ReleaseAt != nil {
			return nil
		}
		now := metav1.NewTime(time.Now().UTC())
		environment.Spec.Lease.ReleaseAt = &now
		_, err := h.k8s.UpdateRuntimeEnvironment(ctx, h.crdNamespace, environment)
		return err
	}
	return errors.New("verification environment was not found")
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
