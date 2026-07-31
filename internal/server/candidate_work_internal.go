package server

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/registry"
	"github.com/breakfix/breakfix/internal/worklist"
	"github.com/gin-gonic/gin"
)

type candidateClaimRequest struct {
	WorkerID       string `json:"worker_id"`
	LeaseTTLMillis int64  `json:"lease_ttl_millis"`
}

type candidateLeaseRequest struct {
	worklist.Credential
	LeaseTTLMillis int64 `json:"lease_ttl_millis,omitempty"`
}

type candidateRequeueRequest struct {
	worklist.Credential
	NextRunAt time.Time `json:"next_run_at"`
	Code      string    `json:"code"`
	Summary   string    `json:"summary"`
}

func (h *Handler) InternalClaimCandidateWork(c *gin.Context) {
	kind, ok := candidateWorkKind(c.Param("kind"))
	if !ok {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "unknown candidate work kind"})
		return
	}
	var request candidateClaimRequest
	if !h.decodeCandidateWorkerRequest(c, kind, &request) {
		return
	}
	leaseTTL := time.Duration(request.LeaseTTLMillis) * time.Millisecond
	if strings.TrimSpace(request.WorkerID) == "" || leaseTTL < minimumWorkerLease || leaseTTL > maximumWorkerLease {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "worker_id and a lease between 5 seconds and 2 minutes are required"})
		return
	}
	if kind == worklist.KindArtifactCleanup {
		if err := h.RecoverCandidatePublications(c.Request.Context()); err != nil {
			h.worklistMetrics.recordFailure(kind, "claim")
			h.writeCandidateWorkError(c, fmt.Errorf("recover completed challenge publications before cleanup: %w", err))
			return
		}
	}
	claim, err := h.db.ClaimCandidateWork(c.Request.Context(), kind, request.WorkerID, leaseTTL, time.Now().UTC())
	if err != nil {
		h.worklistMetrics.recordFailure(kind, "claim")
		h.writeCandidateWorkError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Claim *db.CandidateWorkClaim `json:"claim,omitempty"`
	}{Claim: claim})
}

func (h *Handler) InternalRenewCandidateWork(c *gin.Context) {
	kind, request, claim, ok := h.candidateWorkRequest(c)
	if !ok {
		return
	}
	leaseTTL := time.Duration(request.LeaseTTLMillis) * time.Millisecond
	if leaseTTL < minimumWorkerLease || leaseTTL > maximumWorkerLease {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "lease must be between 5 seconds and 2 minutes"})
		return
	}
	err := h.db.RenewWorkItem(c.Request.Context(), claim.Work, leaseTTL, time.Now().UTC())
	if err != nil {
		h.worklistMetrics.recordFailure(kind, "renew")
		h.writeCandidateWorkError(c, err)
		return
	}
	_ = kind
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalRequeueCandidateWork(c *gin.Context) {
	kind, ok := candidateWorkKind(c.Param("kind"))
	if !ok {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "unknown candidate work kind"})
		return
	}
	var request candidateRequeueRequest
	if !h.decodeCandidateWorkerRequest(c, kind, &request) {
		return
	}
	now := time.Now().UTC()
	claim, err := h.db.GetCandidateWorkClaim(c.Request.Context(), kind, request.Credential, now)
	if err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	if request.NextRunAt.Before(now) {
		request.NextRunAt = now
	}
	if claim.Work.Item.DeadlineAt != nil && request.NextRunAt.After(*claim.Work.Item.DeadlineAt) {
		request.NextRunAt = *claim.Work.Item.DeadlineAt
	}
	if err := h.db.RequeueWorkItem(c.Request.Context(), claim.Work, request.NextRunAt, request.Code, request.Summary, now); err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalDownloadCandidateArchive(c *gin.Context) {
	_, _, claim, ok := h.candidateWorkRequest(c)
	if !ok {
		return
	}
	revision, err := h.db.GetCandidateRevision(c.Request.Context(), claim.Candidate.ID)
	if err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	archive, err := candidate.ReadArchive(revision.ArchivePath, revision.ArchiveSHA256)
	if err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}{Archive: archive, SHA256: revision.ArchiveSHA256})
}

func (h *Handler) InternalDownloadCandidateK8sBase(c *gin.Context) {
	kind, _, claim, ok := h.candidateWorkRequest(c)
	if !ok {
		return
	}
	if kind != worklist.KindBuild || claim.Candidate.Snapshot.Runtime != challenge.RuntimeK8s || claim.Candidate.Snapshot.K8s == nil {
		h.writeCandidateWorkError(c, candidate.ErrInvalidState)
		return
	}
	root, err := os.MkdirTemp("", "breakfix-k8s-base-")
	if err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	defer os.RemoveAll(root) //nolint:errcheck
	path := filepath.Join(root, "base.oci.tar")
	if err := h.registryClient.PullOCIArchive(c.Request.Context(), claim.Candidate.Snapshot.K8s.BaseImageDigest, path); err != nil {
		h.writeCandidateWorkError(c, fmt.Errorf("pull trusted K8s base image: %w", err))
		return
	}
	archive, err := os.ReadFile(path)
	if err != nil {
		h.writeCandidateWorkError(c, fmt.Errorf("read trusted K8s base archive: %w", err))
		return
	}
	c.JSON(http.StatusOK, struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}{Archive: archive, SHA256: candidate.Digest(archive)})
}

func (h *Handler) InternalCompleteCandidateBuild(c *gin.Context) {
	var request struct {
		worklist.Credential
		Output  candidate.BuildOutput `json:"output"`
		Archive []byte                `json:"archive,omitempty"`
	}
	kind, claim, ok := h.decodeCandidateMutation(c, &request, &request.Credential)
	if !ok {
		return
	}
	if kind != worklist.KindBuild {
		h.writeCandidateWorkError(c, candidate.ErrInvalidState)
		return
	}
	output := request.Output
	switch claim.Candidate.Snapshot.Runtime {
	case challenge.RuntimeK8s:
		if len(request.Archive) == 0 || output.OCIArchivePath != "" || output.Incus != nil {
			h.writeCandidateWorkError(c, errors.New("K8s build completion requires exactly one OCI archive"))
			return
		}
		if output.OCIArchiveSHA256 != candidate.Digest(request.Archive) {
			h.writeCandidateWorkError(c, errors.New("K8s build archive digest does not match output"))
			return
		}
		if err := validateOCIArchiveBytes(request.Archive); err != nil {
			h.writeCandidateWorkError(c, fmt.Errorf("validate K8s build archive: %w", err))
			return
		}
		path, digest, err := candidate.SaveBuildArchiveAtomic(h.dataDir, claim.Candidate.ID, claim.Work.Item.ID, int64(claim.Work.Item.Attempt), request.Archive)
		if err != nil {
			h.writeCandidateWorkError(c, err)
			return
		}
		output.OCIArchivePath = path
		output.OCIArchiveSHA256 = digest
	case challenge.RuntimeNode:
		if len(request.Archive) != 0 || output.Incus == nil || output.Incus.Project != h.incusConfig.BuildProject ||
			output.Incus.WorkItemID != claim.Work.Item.ID || output.Incus.Attempt != int64(claim.Work.Item.Attempt) {
			h.writeCandidateWorkError(c, errors.New("node build output does not match its fenced build attempt"))
			return
		}
	default:
		h.writeCandidateWorkError(c, candidate.ErrInvalidState)
		return
	}
	if err := h.db.CompleteCandidateBuild(c.Request.Context(), claim.Work, output, time.Now().UTC()); err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalDownloadCandidateBuildArchive(c *gin.Context) {
	kind, _, claim, ok := h.candidateWorkRequest(c)
	if !ok {
		return
	}
	if kind != worklist.KindArtifactPublish || claim.Candidate.Build == nil || claim.Candidate.Snapshot.Runtime != challenge.RuntimeK8s {
		h.writeCandidateWorkError(c, candidate.ErrInvalidState)
		return
	}
	revision, err := h.db.GetCandidateRevision(c.Request.Context(), claim.Candidate.ID)
	if err != nil || revision.Build == nil {
		h.writeCandidateWorkError(c, errors.Join(err, candidate.ErrInvalidState))
		return
	}
	archive, err := candidate.ReadArchive(revision.Build.OCIArchivePath, revision.Build.OCIArchiveSHA256)
	if err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Archive []byte `json:"archive"`
		SHA256  string `json:"sha256"`
	}{Archive: archive, SHA256: revision.Build.OCIArchiveSHA256})
}

func (h *Handler) InternalCompleteCandidateArtifactPublish(c *gin.Context) {
	var request struct {
		worklist.Credential
		Artifact candidate.ArtifactReference `json:"artifact"`
	}
	kind, claim, ok := h.decodeCandidateMutation(c, &request, &request.Credential)
	if !ok {
		return
	}
	if kind != worklist.KindArtifactPublish {
		h.writeCandidateWorkError(c, candidate.ErrInvalidState)
		return
	}
	if err := h.validateCandidateStagingArtifact(claim.Candidate, request.Artifact); err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	if err := h.db.CompleteCandidateArtifactPublish(c.Request.Context(), claim.Work, request.Artifact, time.Now().UTC()); err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalRecordCandidateVerificationEnvironment(c *gin.Context) {
	var request struct {
		worklist.Credential
		Environment candidate.VerificationEnvironment `json:"environment"`
	}
	kind, claim, ok := h.decodeCandidateMutation(c, &request, &request.Credential)
	if !ok {
		return
	}
	if kind != worklist.KindVerify {
		h.writeCandidateWorkError(c, candidate.ErrInvalidState)
		return
	}
	if err := h.db.RecordCandidateVerificationEnvironment(c.Request.Context(), claim.Work, request.Environment, time.Now().UTC()); err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalCompleteCandidateVerification(c *gin.Context) {
	var request struct {
		worklist.Credential
		Report candidate.VerificationReport `json:"report"`
	}
	kind, claim, ok := h.decodeCandidateMutation(c, &request, &request.Credential)
	if !ok {
		return
	}
	if kind != worklist.KindVerify {
		h.writeCandidateWorkError(c, candidate.ErrInvalidState)
		return
	}
	if err := h.db.CompleteCandidateVerification(c.Request.Context(), claim.Work, request.Report, time.Now().UTC()); err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalFailCandidateArtifact(c *gin.Context) {
	var request struct {
		worklist.Credential
		Failure candidate.Failure             `json:"failure"`
		Report  *candidate.VerificationReport `json:"report,omitempty"`
	}
	_, claim, ok := h.decodeCandidateMutation(c, &request, &request.Credential)
	if !ok {
		return
	}
	if err := h.db.FailCandidateArtifact(c.Request.Context(), claim.Work, request.Failure, request.Report, time.Now().UTC()); err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalCompleteCandidateCleanup(c *gin.Context) {
	var credential worklist.Credential
	kind, claim, ok := h.decodeCandidateMutation(c, &credential, &credential)
	if !ok {
		return
	}
	if kind != worklist.KindArtifactCleanup {
		h.writeCandidateWorkError(c, candidate.ErrInvalidState)
		return
	}
	if err := candidate.RemoveBuildArchives(h.dataDir, claim.Candidate.ID); err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	if err := h.db.CompleteCandidateCleanup(c.Request.Context(), claim.Work, time.Now().UTC()); err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalCompleteCandidateChallengePublish(c *gin.Context) {
	var request struct {
		worklist.Credential
		Artifact candidate.ArtifactReference `json:"artifact"`
	}
	kind, claim, ok := h.decodeCandidateMutation(c, &request, &request.Credential)
	if !ok {
		return
	}
	if kind != worklist.KindChallengePublish || claim.Candidate.Publication == nil {
		h.writeCandidateWorkError(c, candidate.ErrInvalidState)
		return
	}
	if err := h.validateCandidateChallengeArtifact(claim.Candidate, request.Artifact); err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	now := time.Now().UTC()
	if err := h.db.RecordCandidateChallengeArtifact(c.Request.Context(), claim.Work, request.Artifact, now); err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	revision, err := h.db.GetCandidateRevision(c.Request.Context(), claim.Candidate.ID)
	if err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	entry, err := h.materializeCandidatePublication(revision)
	if err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	if err := h.db.CompleteCandidateChallengePublish(c.Request.Context(), claim.Work, request.Artifact, entry.Revision, time.Now().UTC()); err != nil {
		h.writeCandidateWorkError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) decodeCandidateMutation(c *gin.Context, value any, credential *worklist.Credential) (worklist.Kind, *db.CandidateWorkClaim, bool) {
	kind, ok := candidateWorkKind(c.Param("kind"))
	if !ok {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "unknown candidate work kind"})
		return "", nil, false
	}
	if !h.decodeCandidateWorkerRequest(c, kind, value) {
		return "", nil, false
	}
	if credential == nil || c.Param("id") != credential.WorkItemID {
		h.writeCandidateWorkError(c, worklist.ErrLeaseLost)
		return "", nil, false
	}
	claim, err := h.db.GetCandidateWorkClaim(c.Request.Context(), kind, *credential, time.Now().UTC())
	if err != nil {
		h.writeCandidateWorkError(c, err)
		return "", nil, false
	}
	return kind, claim, true
}

func validateOCIArchiveBytes(data []byte) error {
	root, err := os.MkdirTemp("", "breakfix-validate-oci-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root) //nolint:errcheck
	path := filepath.Join(root, "image.oci.tar")
	if err := os.WriteFile(path, data, 0o400); err != nil {
		return err
	}
	return registry.ValidateOCIArchive(path)
}

func (h *Handler) candidateWorkRequest(c *gin.Context) (worklist.Kind, candidateLeaseRequest, *db.CandidateWorkClaim, bool) {
	kind, ok := candidateWorkKind(c.Param("kind"))
	if !ok {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "unknown candidate work kind"})
		return "", candidateLeaseRequest{}, nil, false
	}
	var request candidateLeaseRequest
	if !h.decodeCandidateWorkerRequest(c, kind, &request) {
		return "", candidateLeaseRequest{}, nil, false
	}
	if c.Param("id") != request.WorkItemID {
		h.writeCandidateWorkError(c, worklist.ErrLeaseLost)
		return "", candidateLeaseRequest{}, nil, false
	}
	claim, err := h.db.GetCandidateWorkClaim(c.Request.Context(), kind, request.Credential, time.Now().UTC())
	if err != nil {
		h.writeCandidateWorkError(c, err)
		return "", candidateLeaseRequest{}, nil, false
	}
	return kind, request, claim, true
}

func candidateWorkKind(value string) (worklist.Kind, bool) {
	kind := worklist.Kind(strings.TrimSpace(value))
	switch kind {
	case worklist.KindBuild, worklist.KindArtifactPublish, worklist.KindVerify,
		worklist.KindArtifactCleanup, worklist.KindChallengePublish:
		return kind, true
	default:
		return "", false
	}
}

func candidateWorkerRole(kind worklist.Kind) (config.InternalWorkerRole, bool) {
	switch kind {
	case worklist.KindBuild:
		return config.InternalWorkerBuilder, true
	case worklist.KindArtifactPublish, worklist.KindArtifactCleanup, worklist.KindChallengePublish:
		return config.InternalWorkerPublisher, true
	case worklist.KindVerify:
		return config.InternalWorkerVerifier, true
	default:
		return "", false
	}
}

func (h *Handler) decodeCandidateWorkerRequest(c *gin.Context, kind worklist.Kind, value any) bool {
	role, ok := candidateWorkerRole(kind)
	if !ok {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "unknown candidate work kind"})
		return false
	}
	return h.decodeInternalWorkerRequest(c, role, value)
}

func (h *Handler) writeCandidateWorkError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, worklist.ErrLeaseLost):
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "work item lease was lost"})
	case errors.Is(err, worklist.ErrNotFound), errors.Is(err, candidate.ErrNotFound):
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "candidate work item was not found"})
	case errors.Is(err, candidate.ErrInvalidState):
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: err.Error()})
	default:
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
	}
}
