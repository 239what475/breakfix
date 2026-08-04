package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/oci"
	app "github.com/breakfix/breakfix/internal/application/generation"
	roadmapapp "github.com/breakfix/breakfix/internal/application/roadmap"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/generation"
	roadmapdomain "github.com/breakfix/breakfix/internal/domain/roadmap"
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

type generationClassificationTopicSearchRequest struct {
	generation.LeaseCredential
	Query    string `json:"query"`
	DomainID string `json:"domain_id,omitempty"`
	Limit    int    `json:"limit"`
}

type generationClassificationTagSearchRequest struct {
	generation.LeaseCredential
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type generationClassificationReadRequest struct {
	generation.LeaseCredential
	ID string `json:"id"`
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

func (h *Handler) InternalSearchGenerationClassificationTopics(c *gin.Context) {
	var request generationClassificationTopicSearchRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	claim, retrieval, err := h.generationClassificationRetrieval(c, request.LeaseCredential)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	topics, err := retrieval.SearchTopics(roadmapapp.TopicSearch{Query: request.Query, DomainID: request.DomainID, Limit: request.Limit})
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		RoadmapRevision string                  `json:"roadmap_revision"`
		Topics          []roadmapapp.TopicMatch `json:"topics"`
	}{RoadmapRevision: claim.Workflow.ClassificationRoadmapRevision, Topics: topics})
}

func (h *Handler) InternalReadGenerationClassificationTopic(c *gin.Context) {
	var request generationClassificationReadRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	claim, retrieval, err := h.generationClassificationRetrieval(c, request.LeaseCredential)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	topic, err := retrieval.ReadTopic(request.ID)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		RoadmapRevision string              `json:"roadmap_revision"`
		Topic           roadmapdomain.Topic `json:"topic"`
	}{RoadmapRevision: claim.Workflow.ClassificationRoadmapRevision, Topic: *topic})
}

func (h *Handler) InternalSearchGenerationClassificationTags(c *gin.Context) {
	var request generationClassificationTagSearchRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	claim, retrieval, err := h.generationClassificationRetrieval(c, request.LeaseCredential)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	tags, err := retrieval.SearchTags(roadmapapp.TagSearch{Query: request.Query, Limit: request.Limit})
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		RoadmapRevision string                `json:"roadmap_revision"`
		Tags            []roadmapapp.TagMatch `json:"tags"`
	}{RoadmapRevision: claim.Workflow.ClassificationRoadmapRevision, Tags: tags})
}

func (h *Handler) InternalReadGenerationClassificationTag(c *gin.Context) {
	var request generationClassificationReadRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	claim, retrieval, err := h.generationClassificationRetrieval(c, request.LeaseCredential)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	tag, err := retrieval.ReadTag(request.ID)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		RoadmapRevision string            `json:"roadmap_revision"`
		Tag             roadmapdomain.Tag `json:"tag"`
	}{RoadmapRevision: claim.Workflow.ClassificationRoadmapRevision, Tag: *tag})
}

func (h *Handler) InternalStartGenerationAgentRun(c *gin.Context) {
	var request app.StartAgentRunRequest
	if !h.decodeInternalWorkerRequest(c, internalGenerateRole, &request) {
		return
	}
	claim, err := h.generationClaim(c, request.LeaseCredential)
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	request.ExpectedState = claim.Workflow.State
	if err := request.Validate(claim.Workflow.ID); err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	expectedPrompt := app.GeneratorPromptVersion
	switch request.Purpose {
	case app.JudgePurpose:
		expectedPrompt = app.JudgePromptVersion
	case app.ClassifierPurpose:
		expectedPrompt = app.ClassifierPromptVersion
	}
	if request.Model != h.llm.Model || request.PromptVersion != expectedPrompt {
		h.writeInternalGenerationError(c, errors.New("generation agent run metadata does not match Server configuration"))
		return
	}
	run, err := h.db.Generation.StartGenerationAgentRun(c.Request.Context(), *claim, agent.CreateRun{
		ID:            agent.NewID("generation-agent-run"),
		Purpose:       request.Purpose,
		OwnerKind:     "generation-workflow",
		OwnerRef:      claim.Workflow.ID,
		Model:         h.llm.Model,
		PromptVersion: expectedPrompt,
	}, time.Now().UTC())
	if err != nil {
		h.writeInternalGenerationError(c, err)
		return
	}
	c.JSON(http.StatusOK, app.StartAgentRunResponse{Run: *run})
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
	cleanupGeneratorWorkspace := claim.Workflow.State == generation.StateGenerating && claim.Workflow.ActiveAgentRunID != ""
	failureRecorded := request.InfrastructureFailure != nil || request.ArtifactFailure != nil
	switch {
	case request.GeneratedCandidate != nil:
		err = h.finalizeGeneratedCandidate(c, *claim, *request.GeneratedCandidate, now)
	case request.Judgement != nil:
		err = h.db.Generation.FinalizeGenerationJudgement(c.Request.Context(), *claim, request.Judgement.RunID, request.Judgement.Approved, request.Judgement.Feedback, now)
	case request.Classification != nil:
		err = h.db.Generation.FinalizeGenerationClassification(c.Request.Context(), *claim, request.Classification.RunID, request.Classification.Output, now)
	case request.ClassificationAdjustment != nil:
		var contentFeedback *generation.ClassificationContentFeedback
		contentFeedback, err = h.db.Generation.FinalizeGenerationClassificationAdjustment(c.Request.Context(), *claim, *request.ClassificationAdjustment, now)
		if err == nil && contentFeedback != nil {
			err = h.routeClassificationContentFeedback(c.Request.Context(), *contentFeedback)
		}
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
		failureRecorded = true
	}
	if failureRecorded && cleanupGeneratorWorkspace {
		h.scheduleGeneratorWorkspaceCleanup(claim.Workflow.ActiveAgentRunID)
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

// routeClassificationContentFeedback preserves the classification boundary:
// the Classifying role only decides scope, while Authoring owns the private
// Plan draft for a content change. The resulting conversation is durable even
// though this internal Worker request has no user-facing stream.
func (h *Handler) routeClassificationContentFeedback(ctx context.Context, feedback generation.ClassificationContentFeedback) error {
	if h == nil || h.authoring == nil {
		return errors.New("authoring runtime is unavailable for classification content feedback")
	}
	_, run, err := h.authoring.StartTurn(ctx, feedback.UserID, feedback.SessionID, feedback.Content)
	if err != nil {
		return fmt.Errorf("start authoring content feedback: %w", err)
	}
	if _, err := h.authoring.RunTurn(ctx, run.ID, nil); err != nil {
		if failErr := h.authoring.FailTurn(context.Background(), run.ID, err.Error()); failErr != nil && !errors.Is(failErr, agent.ErrRunActive) {
			slog.Error("finalize routed authoring content feedback", "run_id", run.ID, "err", errors.Join(err, failErr))
		}
		return fmt.Errorf("run authoring content feedback: %w", err)
	}
	return nil
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

// generationClassificationRetrieval binds every tool call to the leased
// classifier run and the workflow's immutable revision. No request field can
// redirect a Worker to a newer or arbitrary RoadmapRevision.
func (h *Handler) generationClassificationRetrieval(c *gin.Context, credential generation.LeaseCredential) (*generation.Claim, *roadmapapp.Retrieval, error) {
	claim, err := h.generationClaim(c, credential)
	if err != nil {
		return nil, nil, err
	}
	if claim.Workflow.State != generation.StateClassifying || strings.TrimSpace(claim.Workflow.ActiveAgentRunID) == "" ||
		strings.TrimSpace(claim.Workflow.ClassificationRoadmapRevision) == "" {
		return nil, nil, generation.ErrCandidateInvalidState
	}
	run, err := h.db.Agent.GetRun(c.Request.Context(), claim.Workflow.ActiveAgentRunID)
	if err != nil {
		return nil, nil, err
	}
	if run.Status != agent.RunRunning || run.Purpose != app.ClassifierPurpose || run.OwnerKind != "generation-workflow" || run.OwnerRef != claim.Workflow.ID {
		return nil, nil, generation.ErrLeaseLost
	}
	revision, err := h.db.Roadmap.RoadmapRevision(c.Request.Context(), claim.Workflow.ClassificationRoadmapRevision)
	if err != nil {
		return nil, nil, err
	}
	retrieval, err := roadmapapp.NewRetrieval(*revision)
	if err != nil {
		return nil, nil, err
	}
	return claim, retrieval, nil
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

func (h *Handler) finalizeGeneratedCandidate(c *gin.Context, claim generation.Claim, result generation.GeneratedCandidate, now time.Time) error {
	run, err := h.db.Agent.GetRun(c.Request.Context(), result.RunID)
	if err != nil {
		return err
	}
	if run.ID != claim.Workflow.ActiveAgentRunID || run.Status != agent.RunRunning || run.Purpose != app.GeneratorPurpose || run.OwnerKind != "generation-workflow" || run.OwnerRef != claim.Workflow.ID || strings.TrimSpace(run.SessionID) == "" {
		return generation.ErrLeaseLost
	}
	inspected, err := app.InspectCandidateArchive(result.Archive)
	if err != nil {
		return generation.NewArtifactError("CANDIDATE_INVALID", err.Error())
	}
	snapshot, err := candidateExecutionSnapshot(inspected.Entry, h.runtimeConfig, h.incusConfig)
	if err != nil {
		return generation.NewArtifactError("CANDIDATE_RUNTIME_INVALID", err.Error())
	}
	id := generation.IDForGeneratorRun(run.ID)
	path, digest, err := candidate.SaveArchiveAtomic(h.dataDir, id, inspected.Archive)
	if err != nil {
		return err
	}
	revision := generation.Revision{
		ID:                 id,
		Source:             claim.Workflow.Source,
		SourceRevision:     claim.Workflow.SourceRevision,
		GeneratorSessionID: run.SessionID,
		GeneratorRunID:     run.ID,
		ArchivePath:        path,
		ArchiveSHA256:      digest,
		Snapshot:           snapshot,
	}
	if err := h.db.Generation.FinalizeGeneratedCandidate(c.Request.Context(), claim, run.ID, revision, now); err != nil {
		return err
	}
	h.scheduleGeneratorWorkspaceCleanup(run.ID)
	return nil
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
