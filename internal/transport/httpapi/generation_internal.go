package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/generation"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

const (
	internalGenerateRole = config.InternalWorkerGenerate
	generationMinLease   = 5 * time.Second
	generationMaxLease   = 2 * time.Minute
)

type generationClaimRequest struct {
	WorkerID       string `json:"worker_id"`
	LeaseTTLMillis int64  `json:"lease_ttl_millis"`
}

type generationRenewRequest struct {
	generation.RuntimeActionCredential
	LeaseTTLMillis int64 `json:"lease_ttl_millis"`
}

type generationBuildCompleteRequest struct {
	generation.RuntimeActionCredential
	Output generation.BuildOutput `json:"output"`
}

type generationArtifactCompleteRequest struct {
	generation.RuntimeActionCredential
	Artifact generation.ArtifactReference `json:"artifact"`
}

type generationVerificationEnvironmentRequest struct {
	generation.RuntimeActionCredential
	Environment generation.VerificationEnvironment `json:"environment"`
}

type generationVerificationCompleteRequest struct {
	generation.RuntimeActionCredential
	Report generation.VerificationReport `json:"report"`
}

type generationArtifactFailureRequest struct {
	generation.RuntimeActionCredential
	Failure generation.Failure             `json:"failure"`
	Report  *generation.VerificationReport `json:"report,omitempty"`
}

type generationInfrastructureFailureRequest struct {
	generation.RuntimeActionCredential
	Failure generation.Failure `json:"failure"`
}

type generationResourceReapClaimRequest struct {
	WorkerID       string                      `json:"worker_id"`
	Kind           generation.ResourceReapKind `json:"kind"`
	LeaseTTLMillis int64                       `json:"lease_ttl_millis"`
}

type generationResourceReapCompleteRequest struct {
	Claim   generation.ResourceReapClaim `json:"claim"`
	Failure string                       `json:"failure,omitempty"`
}

// InternalClaimGenerationWorkflow claims exactly one Runtime Worker action.
// Despite the historical method name, it never returns Server Agent states.
func (h *Handler) InternalClaimGenerationWorkflow(c *gin.Context) {
	var request generationClaimRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	leaseTTL := time.Duration(request.LeaseTTLMillis) * time.Millisecond
	if strings.TrimSpace(request.WorkerID) == "" || leaseTTL < generationMinLease || leaseTTL > generationMaxLease {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "worker_id and a lease between 5 seconds and 2 minutes are required"})
		return
	}
	claim, err := h.db.Generation.ClaimGenerationWorkflow(c.Request.Context(), request.WorkerID, leaseTTL, time.Now().UTC())
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	if claim == nil {
		c.JSON(http.StatusOK, struct {
			Action *generation.RuntimeActionContext `json:"action,omitempty"`
		}{})
		return
	}
	action, err := h.db.Generation.LoadGenerationRuntimeAction(c.Request.Context(), *claim, time.Now().UTC())
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Action *generation.RuntimeActionContext `json:"action,omitempty"`
	}{Action: action})
}

func (h *Handler) InternalClaimGenerationResourceReap(c *gin.Context) {
	var request generationResourceReapClaimRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	leaseTTL := time.Duration(request.LeaseTTLMillis) * time.Millisecond
	if strings.TrimSpace(request.WorkerID) == "" || request.Kind.Owner() != "runtime-worker" || leaseTTL < generationMinLease || leaseTTL > generationMaxLease {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "worker_id, a runtime-worker resource kind, and a lease between 5 seconds and 2 minutes are required"})
		return
	}
	claim, err := h.db.Generation.ClaimGenerationResourceReap(c.Request.Context(), request.Kind, request.WorkerID, leaseTTL, time.Now().UTC())
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Claim *generation.ResourceReapClaim `json:"claim,omitempty"`
	}{Claim: claim})
}

func (h *Handler) InternalCompleteGenerationResourceReap(c *gin.Context) {
	var request generationResourceReapCompleteRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	if request.Claim.Valid() != nil || request.Claim.Kind.Owner() != "runtime-worker" {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "runtime resource reap completion is invalid"})
		return
	}
	if err := h.db.Generation.CompleteGenerationResourceReap(c.Request.Context(), request.Claim, request.Failure, time.Now().UTC()); err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalRenewGenerationWorkflow(c *gin.Context) {
	var request generationRenewRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	leaseTTL := time.Duration(request.LeaseTTLMillis) * time.Millisecond
	if leaseTTL < generationMinLease || leaseTTL > generationMaxLease {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "lease must be between 5 seconds and 2 minutes"})
		return
	}
	action, err := h.runtimeAction(c, request.RuntimeActionCredential)
	if err == nil {
		err = h.db.Generation.RenewGenerationLease(c.Request.Context(), action.Claim, leaseTTL, time.Now().UTC())
	}
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalDownloadGenerationCandidateArchive(c *gin.Context) {
	var credential generation.RuntimeActionCredential
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &credential) {
		return
	}
	action, err := h.runtimeAction(c, credential)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	if action.Identity.State != generation.StateBuilding {
		h.writeInternalGenerationError(c, generation.ErrCandidateInvalidState)
		return
	}
	revision, err := h.db.Generation.GetCandidateRevision(c.Request.Context(), action.Candidate.ID)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	archive, err := candidate.ReadArchive(revision.ArchivePath, revision.ArchiveSHA256)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}{Archive: archive, SHA256: revision.ArchiveSHA256})
}

func (h *Handler) InternalCompleteGenerationBuild(c *gin.Context) {
	var request generationBuildCompleteRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	h.completeRuntimeAction(c, request.RuntimeActionCredential, generation.StateBuilding, func(action *generation.RuntimeActionContext) error {
		if err := h.validateGenerationBuildOutput(*action, request.Output); err != nil {
			return generation.NewArtifactError("BUILD_OUTPUT_INVALID", err.Error())
		}
		return h.db.Generation.CompleteGenerationBuild(c.Request.Context(), action.Claim, request.Output, time.Now().UTC())
	})
}

func (h *Handler) InternalCompleteGenerationArtifactPublish(c *gin.Context) {
	var request generationArtifactCompleteRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	h.completeRuntimeAction(c, request.RuntimeActionCredential, generation.StateArtifactPublishing, func(action *generation.RuntimeActionContext) error {
		if err := h.validateCandidateStagingArtifact(action.Candidate, request.Artifact); err != nil {
			return generation.NewArtifactError("ARTIFACT_REFERENCE_INVALID", err.Error())
		}
		return h.db.Generation.CompleteGenerationArtifactPublish(c.Request.Context(), action.Claim, request.Artifact, time.Now().UTC())
	})
}

func (h *Handler) InternalRecordGenerationVerificationEnvironment(c *gin.Context) {
	var request generationVerificationEnvironmentRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	h.completeRuntimeAction(c, request.RuntimeActionCredential, generation.StateVerifying, func(action *generation.RuntimeActionContext) error {
		if err := request.Environment.Validate(action.Candidate.Snapshot.Runtime); err != nil {
			return generation.NewArtifactError("VERIFICATION_ENVIRONMENT_INVALID", err.Error())
		}
		return h.db.Generation.RecordGenerationVerificationEnvironment(c.Request.Context(), action.Claim, request.Environment, time.Now().UTC())
	})
}

func (h *Handler) InternalCompleteGenerationVerification(c *gin.Context) {
	var request generationVerificationCompleteRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	h.completeRuntimeAction(c, request.RuntimeActionCredential, generation.StateVerifying, func(action *generation.RuntimeActionContext) error {
		if err := request.Report.Validate(action.Candidate.Snapshot); err != nil {
			return generation.NewArtifactError("VERIFICATION_REPORT_INVALID", err.Error())
		}
		return h.db.Generation.CompleteGenerationVerification(c.Request.Context(), action.Claim, request.Report, time.Now().UTC())
	})
}

func (h *Handler) InternalRecordGenerationChallengePublication(c *gin.Context) {
	var request generationArtifactCompleteRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	h.completeRuntimeAction(c, request.RuntimeActionCredential, generation.StateChallengePublishing, func(action *generation.RuntimeActionContext) error {
		if err := h.validateCandidateChallengeArtifact(action.Candidate, request.Artifact); err != nil {
			return generation.NewArtifactError("CHALLENGE_ARTIFACT_INVALID", err.Error())
		}
		return h.db.Generation.RecordGenerationChallengePublicationResult(c.Request.Context(), action.Claim, request.Artifact, time.Now().UTC())
	})
}

func (h *Handler) InternalReportGenerationInfrastructureFailure(c *gin.Context) {
	var request generationInfrastructureFailureRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	h.completeRuntimeAction(c, request.RuntimeActionCredential, request.Identity.State, func(action *generation.RuntimeActionContext) error {
		if request.Failure.Class != generation.FailureInfrastructure || request.Failure.Validate() != nil {
			return errors.New("runtime infrastructure failure is invalid")
		}
		_, err := h.db.Generation.ReportGenerationInfrastructureFailure(c.Request.Context(), action.Claim, action.Identity.State, request.Failure, time.Now().UTC())
		return err
	})
}

func (h *Handler) InternalReportGenerationArtifactFailure(c *gin.Context) {
	var request generationArtifactFailureRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	h.completeRuntimeAction(c, request.RuntimeActionCredential, request.Identity.State, func(action *generation.RuntimeActionContext) error {
		if request.Failure.Class != generation.FailureArtifact || request.Failure.Validate() != nil {
			return errors.New("runtime artifact failure is invalid")
		}
		return h.db.Generation.ReportGenerationArtifactFailure(c.Request.Context(), action.Claim, action.Identity.State, request.Failure, request.Report, time.Now().UTC())
	})
}

func (h *Handler) completeRuntimeAction(c *gin.Context, credential generation.RuntimeActionCredential, expected generation.WorkflowState, complete func(*generation.RuntimeActionContext) error) {
	action, err := h.runtimeAction(c, credential)
	if err == nil && action.Identity.State != expected {
		err = generation.ErrLeaseLost
	}
	if err == nil {
		err = complete(action)
	}
	if err == nil {
		c.Status(http.StatusNoContent)
		return
	}
	var artifact *generation.ArtifactError
	if action != nil && errors.As(err, &artifact) {
		if reportErr := h.db.Generation.ReportGenerationArtifactFailure(c.Request.Context(), action.Claim, action.Identity.State, artifact.Failure, artifact.Report, time.Now().UTC()); reportErr == nil {
			c.Status(http.StatusNoContent)
			return
		} else {
			err = reportErr
		}
	}
	h.writeInternalGenerationError(c, err)
}

func (h *Handler) runtimeAction(c *gin.Context, credential generation.RuntimeActionCredential) (*generation.RuntimeActionContext, error) {
	if h.db == nil || !credential.Valid() || strings.TrimSpace(c.Param("id")) == "" || credential.Identity.WorkflowID != c.Param("id") {
		return nil, errors.New("valid runtime action credentials are required")
	}
	claim, err := h.db.Generation.GetGenerationClaim(c.Request.Context(), c.Param("id"), credential.LeaseCredential, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	action, err := h.db.Generation.LoadGenerationRuntimeAction(c.Request.Context(), *claim, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if action.Identity != credential.Identity {
		return nil, generation.ErrLeaseLost
	}
	return action, nil
}

func (h *Handler) validateGenerationBuildOutput(action generation.RuntimeActionContext, output generation.BuildOutput) error {
	if err := output.Validate(action.Candidate.Snapshot.Runtime); err != nil {
		return err
	}
	switch action.Candidate.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		expected, err := candidate.BuildOCIRepository(h.registryRepository, action.Identity.WorkflowID, action.Candidate.ID, action.Identity.StateVersion)
		if err != nil {
			return err
		}
		actual, err := candidate.OCIRepository(output.OCIReference)
		if err != nil {
			return err
		}
		if actual != expected {
			return errors.New("K8s build artifact OCI repository does not belong to this runtime action")
		}
		return nil
	case challenge.RuntimeNode:
		if output.Incus == nil || output.Incus.Project != h.incusConfig.BuildProject ||
			output.Incus.WorkflowID != action.Identity.WorkflowID || output.Incus.CandidateRevisionID != action.Candidate.ID ||
			output.Incus.Attempt != action.Identity.StateVersion {
			return errors.New("Node build output does not match its fenced runtime action")
		}
		return nil
	default:
		return generation.ErrCandidateInvalidState
	}
}

func (h *Handler) writeInternalGenerationError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, generation.ErrWorkflowNotFound), errors.Is(err, generation.ErrCandidateNotFound), errors.Is(err, agent.ErrNotFound), errors.Is(err, generation.ErrWorkspaceNotFound):
		status = http.StatusNotFound
	case errors.Is(err, generation.ErrLeaseLost):
		status = http.StatusConflict
	case strings.Contains(err.Error(), "unavailable"):
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, api.ErrorResponse{Error: err.Error()})
}
