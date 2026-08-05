package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/oci"
	app "github.com/breakfix/breakfix/internal/application/generation"
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

type generationLeaseRequest struct {
	generation.LeaseCredential
	LeaseTTLMillis int64 `json:"lease_ttl_millis,omitempty"`
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
	c.JSON(http.StatusOK, struct {
		Claim *generation.Claim `json:"claim,omitempty"`
	}{Claim: claim})
}

func (h *Handler) InternalClaimGenerationResourceReap(c *gin.Context) {
	var request generationResourceReapClaimRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	leaseTTL := time.Duration(request.LeaseTTLMillis) * time.Millisecond
	if strings.TrimSpace(request.WorkerID) == "" || request.Kind.Owner() != "generate-worker" || leaseTTL < generationMinLease || leaseTTL > generationMaxLease {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "worker_id, a generate-worker resource kind, and a lease between 5 seconds and 2 minutes are required"})
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
	if request.Claim.Valid() != nil || request.Claim.Kind.Owner() != "generate-worker" {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "generation resource reap completion is invalid"})
		return
	}
	if err := h.db.Generation.CompleteGenerationResourceReap(c.Request.Context(), request.Claim, request.Failure, time.Now().UTC()); err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalRenewGenerationWorkflow(c *gin.Context) {
	var request generationLeaseRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	leaseTTL := time.Duration(request.LeaseTTLMillis) * time.Millisecond
	if leaseTTL < generationMinLease || leaseTTL > generationMaxLease {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "lease must be between 5 seconds and 2 minutes"})
		return
	}
	claim, err := h.generationClaim(c, request.LeaseCredential)
	if err == nil {
		err = h.db.Generation.RenewGenerationLease(c.Request.Context(), *claim, leaseTTL, time.Now().UTC())
	}
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalGenerationContext(c *gin.Context) {
	var credential generation.LeaseCredential
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &credential) {
		return
	}
	claim, err := h.generationClaim(c, credential)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	context, err := h.db.Generation.LoadGenerationContext(c.Request.Context(), *claim, time.Now().UTC())
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Context generation.Context `json:"context"`
	}{Context: *context})
}

func (h *Handler) InternalGenerationPhase(c *gin.Context) {
	var request app.PhaseRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	if err := request.Validate(); err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	claim, err := h.generationClaim(c, request.LeaseCredential)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	if claim.Workflow.State != request.ExpectedState {
		h.writeInternalGenerationError(c, generation.ErrLeaseLost)
		return
	}
	now := time.Now().UTC()
	switch {
	case request.Build != nil:
		err = h.completeGenerationBuild(c, *claim, *request.Build, now)
	case request.ArtifactPublish != nil:
		err = h.completeGenerationArtifactPublish(c, *claim, request.ArtifactPublish.Artifact, now)
	case request.VerificationEnvironment != nil:
		err = h.db.Generation.RecordGenerationVerificationEnvironment(c.Request.Context(), *claim, request.VerificationEnvironment.Environment, now)
	case request.Verification != nil:
		err = h.validateGenerationVerificationReport(c, *claim, request.Verification.Report)
		if err == nil {
			err = h.db.Generation.CompleteGenerationVerification(c.Request.Context(), *claim, request.Verification.Report, now)
		}
	case request.ChallengePublish != nil:
		err = h.completeGenerationChallengePublish(c, *claim, request.ChallengePublish.Artifact, now)
	case request.InfrastructureFailure != nil:
		_, err = h.db.Generation.ReportGenerationInfrastructureFailure(c.Request.Context(), *claim, request.ExpectedState, request.InfrastructureFailure.Failure, now)
	case request.ArtifactFailure != nil:
		err = h.db.Generation.ReportGenerationArtifactFailure(c.Request.Context(), *claim, request.ExpectedState, request.ArtifactFailure.Failure, request.ArtifactFailure.Report, now)
	}
	if err != nil {
		var artifactErr *generation.ArtifactError
		if !errors.As(err, &artifactErr) {
			h.writeInternalGenerationError(c, err)
			return
		}
		if reportErr := h.db.Generation.ReportGenerationArtifactFailure(c.Request.Context(), *claim, request.ExpectedState, artifactErr.Failure, artifactErr.Report, now); reportErr != nil {
			h.writeInternalGenerationError(c, reportErr)
			return
		}
	}
	refreshed, err := h.refreshGenerationClaim(c, *claim)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Claim *generation.Claim `json:"claim,omitempty"`
	}{Claim: refreshed})
}

func (h *Handler) InternalDownloadGenerationCandidateArchive(c *gin.Context) {
	var credential generation.LeaseCredential
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &credential) {
		return
	}
	claim, revision, err := h.generationCandidateForClaim(c, credential)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	_ = claim
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

func (h *Handler) InternalDownloadGenerationK8sBase(c *gin.Context) {
	var credential generation.LeaseCredential
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &credential) {
		return
	}
	claim, revision, err := h.generationCandidateForClaim(c, credential)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	if claim.Workflow.State != generation.StateBuilding || revision.Snapshot.Runtime != challenge.RuntimeK8s || revision.Snapshot.K8s == nil {
		h.writeInternalGenerationError(c, generation.ErrCandidateInvalidState)
		return
	}
	root, err := os.MkdirTemp("", "breakfix-generation-k8s-base-")
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	defer os.RemoveAll(root) //nolint:errcheck
	path := filepath.Join(root, "base.oci.tar")
	if err := h.registryClient.PullOCIArchive(c.Request.Context(), revision.Snapshot.K8s.BaseImageDigest, path); err != nil {
		h.writeInternalGenerationError(c, fmt.Errorf("pull trusted K8s base image: %w", err))
		return
	}
	archive, err := os.ReadFile(path)
	if err != nil {
		h.writeInternalGenerationError(c, fmt.Errorf("read trusted K8s base archive: %w", err))
		return
	}
	c.JSON(http.StatusOK, struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}{Archive: archive, SHA256: candidate.Digest(archive)})
}

func (h *Handler) InternalDownloadGenerationBuildArchive(c *gin.Context) {
	var credential generation.LeaseCredential
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &credential) {
		return
	}
	claim, revision, err := h.generationCandidateForClaim(c, credential)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	if claim.Workflow.State != generation.StateArtifactPublishing || revision.Snapshot.Runtime != challenge.RuntimeK8s || revision.Build == nil {
		h.writeInternalGenerationError(c, generation.ErrCandidateInvalidState)
		return
	}
	archive, err := candidate.ReadArchive(revision.Build.OCIArchivePath, revision.Build.OCIArchiveSHA256)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}{Archive: archive, SHA256: revision.Build.OCIArchiveSHA256})
}

func (h *Handler) generationClaim(c *gin.Context, credential generation.LeaseCredential) (*generation.Claim, error) {
	if h.db == nil || !credential.Valid() || strings.TrimSpace(c.Param("id")) == "" {
		return nil, errors.New("valid generation workflow lease credentials are required")
	}
	return h.db.Generation.GetGenerationClaim(c.Request.Context(), c.Param("id"), credential, time.Now().UTC())
}

func (h *Handler) generationCandidateForClaim(c *gin.Context, credential generation.LeaseCredential) (*generation.Claim, *generation.Revision, error) {
	claim, err := h.generationClaim(c, credential)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(claim.Workflow.CandidateRevisionID) == "" {
		return nil, nil, generation.ErrCandidateInvalidState
	}
	revision, err := h.db.Generation.GetCandidateRevision(c.Request.Context(), claim.Workflow.CandidateRevisionID)
	if err != nil {
		return nil, nil, err
	}
	return claim, revision, nil
}

func (h *Handler) refreshGenerationClaim(c *gin.Context, prior generation.Claim) (*generation.Claim, error) {
	workflow, err := h.db.Generation.GetGenerationWorkflow(c.Request.Context(), prior.Workflow.ID)
	if err != nil {
		return nil, err
	}
	if workflow.State.Terminal() || workflow.State.Review() || strings.TrimSpace(workflow.LeaseOwner) == "" {
		return nil, nil
	}
	return h.db.Generation.RefreshGenerationClaim(c.Request.Context(), workflow.ID, prior.LeaseOwner, time.Now().UTC())
}

func (h *Handler) completeGenerationBuild(c *gin.Context, claim generation.Claim, result generation.BuildResult, now time.Time) error {
	revision, err := h.db.Generation.GetCandidateRevision(c.Request.Context(), claim.Workflow.CandidateRevisionID)
	if err != nil {
		return err
	}
	output := result.Output
	switch revision.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		if len(result.Archive) == 0 || output.OCIArchivePath != "" || output.Incus != nil || output.OCIArchiveSHA256 != candidate.Digest(result.Archive) {
			return generation.NewArtifactError("BUILD_OUTPUT_INVALID", "K8s generation build completion requires one matching OCI archive")
		}
		if err := validateGenerationOCIArchiveBytes(result.Archive); err != nil {
			return generation.NewArtifactError("BUILD_ARCHIVE_INVALID", err.Error())
		}
		path, digest, err := candidate.SaveBuildArchiveAtomic(h.dataDir, revision.ID, claim.Workflow.ID, int64(claim.StateAttempt+1), result.Archive)
		if err != nil {
			return err
		}
		output.OCIArchivePath = path
		output.OCIArchiveSHA256 = digest
	case challenge.RuntimeNode:
		if len(result.Archive) != 0 || output.Incus == nil || output.Incus.Project != h.incusConfig.BuildProject ||
			output.Incus.WorkflowID != claim.Workflow.ID || output.Incus.CandidateRevisionID != revision.ID || output.Incus.Attempt != int64(claim.StateAttempt+1) {
			return generation.NewArtifactError("BUILD_OUTPUT_INVALID", "node build output does not match its fenced generation attempt")
		}
	default:
		return generation.NewArtifactError("BUILD_RUNTIME_INVALID", "candidate runtime is unsupported")
	}
	return h.db.Generation.CompleteGenerationBuild(c.Request.Context(), claim, output, now)
}

func (h *Handler) completeGenerationArtifactPublish(c *gin.Context, claim generation.Claim, artifact generation.ArtifactReference, now time.Time) error {
	revision, err := h.db.Generation.GetCandidateRevision(c.Request.Context(), claim.Workflow.CandidateRevisionID)
	if err != nil {
		return err
	}
	if err := h.validateCandidateStagingArtifact(revision.WorkerView(), artifact); err != nil {
		return generation.NewArtifactError("ARTIFACT_REFERENCE_INVALID", err.Error())
	}
	return h.db.Generation.CompleteGenerationArtifactPublish(c.Request.Context(), claim, artifact, now)
}

func (h *Handler) validateGenerationVerificationReport(c *gin.Context, claim generation.Claim, report generation.VerificationReport) error {
	revision, err := h.db.Generation.GetCandidateRevision(c.Request.Context(), claim.Workflow.CandidateRevisionID)
	if err != nil {
		return err
	}
	if err := report.Validate(revision.Snapshot); err != nil {
		return generation.NewArtifactError("VERIFICATION_REPORT_INVALID", err.Error())
	}
	return nil
}

func (h *Handler) completeGenerationChallengePublish(c *gin.Context, claim generation.Claim, artifact generation.ArtifactReference, now time.Time) error {
	revision, err := h.db.Generation.GetCandidateRevision(c.Request.Context(), claim.Workflow.CandidateRevisionID)
	if err != nil {
		return err
	}
	if err := h.validateCandidateChallengeArtifact(revision.WorkerView(), artifact); err != nil {
		return generation.NewArtifactError("CHALLENGE_ARTIFACT_INVALID", err.Error())
	}
	if revision.Publication == nil {
		return generation.NewArtifactError("PUBLICATION_INTENT_INVALID", "generation challenge publication has no publication intent")
	}
	copyRevision := *revision
	publication := *revision.Publication
	publication.Artifact = &artifact
	copyRevision.Publication = &publication
	published, err := h.materializeCandidatePublication(&copyRevision)
	if err != nil {
		return err
	}
	return h.db.Generation.CompleteGenerationChallengePublish(c.Request.Context(), claim, artifact, published.ContentRevision, now)
}

func validateGenerationOCIArchiveBytes(data []byte) error {
	root, err := os.MkdirTemp("", "breakfix-validate-oci-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root) //nolint:errcheck
	path := filepath.Join(root, "image.oci.tar")
	if err := os.WriteFile(path, data, 0o400); err != nil {
		return err
	}
	return oci.ValidateOCIArchive(path)
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
