package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/postgres"
	taxonomyapp "github.com/breakfix/breakfix/internal/application/taxonomy"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/content/challenge"
	taxonomystore "github.com/breakfix/breakfix/internal/content/taxonomy"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/taxonomy"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

const (
	internalTaxonomyRole = config.InternalWorkerTaxonomy
	taxonomyMinLease     = 5 * time.Second
	taxonomyMaxLease     = 2 * time.Minute
)

type taxonomyClaimRequest struct {
	WorkerID       string `json:"worker_id"`
	LeaseTTLMillis int64  `json:"lease_ttl_millis"`
}

type taxonomyLeaseRequest struct {
	taxonomy.LeaseCredential
	LeaseTTLMillis int64 `json:"lease_ttl_millis,omitempty"`
}

func (h *Handler) InternalClaimTaxonomyWorkflow(c *gin.Context) {
	var request taxonomyClaimRequest
	if !h.decodeInternalWorkerRequest(c, internalTaxonomyRole, &request) {
		return
	}
	leaseTTL := time.Duration(request.LeaseTTLMillis) * time.Millisecond
	if strings.TrimSpace(request.WorkerID) == "" || leaseTTL < taxonomyMinLease || leaseTTL > taxonomyMaxLease {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "worker_id and a lease between 5 seconds and 2 minutes are required"})
		return
	}
	claim, err := h.db.Taxonomy.ClaimTaxonomyWorkflow(c.Request.Context(), request.WorkerID, leaseTTL, time.Now().UTC())
	if err != nil {
		h.writeInternalTaxonomyWorkflowError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Claim *taxonomy.Claim `json:"claim,omitempty"`
	}{Claim: claim})
}

func (h *Handler) InternalRenewTaxonomyWorkflow(c *gin.Context) {
	var request taxonomyLeaseRequest
	if !h.decodeInternalWorkerRequest(c, internalTaxonomyRole, &request) {
		return
	}
	leaseTTL := time.Duration(request.LeaseTTLMillis) * time.Millisecond
	if leaseTTL < taxonomyMinLease || leaseTTL > taxonomyMaxLease {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "lease must be between 5 seconds and 2 minutes"})
		return
	}
	claim, err := h.taxonomyWorkflowClaim(c, request.LeaseCredential)
	if err == nil {
		err = h.db.Taxonomy.RenewTaxonomyLease(c.Request.Context(), *claim, leaseTTL, time.Now().UTC())
	}
	if err != nil {
		h.writeInternalTaxonomyWorkflowError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) InternalTaxonomyWorkflowContext(c *gin.Context) {
	var credential taxonomy.LeaseCredential
	if !h.decodeInternalWorkerRequest(c, internalTaxonomyRole, &credential) {
		return
	}
	claim, err := h.taxonomyWorkflowClaim(c, credential)
	if err == nil {
		var snapshot taxonomyapp.Context
		snapshot, err = h.taxonomyContext(c.Request.Context(), *claim)
		if err == nil {
			c.JSON(http.StatusOK, struct {
				Context taxonomyapp.Context `json:"context"`
			}{Context: snapshot})
			return
		}
	}
	h.writeInternalTaxonomyWorkflowError(c, err)
}

func (h *Handler) InternalStartTaxonomyAgentRun(c *gin.Context) {
	var request taxonomyapp.StartAgentRunRequest
	if !h.decodeInternalWorkerRequest(c, internalTaxonomyRole, &request) {
		return
	}
	claim, err := h.taxonomyWorkflowClaim(c, request.LeaseCredential)
	if err != nil {
		h.writeInternalTaxonomyWorkflowError(c, err)
		return
	}
	request.ExpectedState = claim.Workflow.State
	if err := request.Validate(claim.Workflow.ID); err != nil {
		h.writeInternalTaxonomyWorkflowError(c, err)
		return
	}
	if request.Model != h.llm.Model {
		h.writeInternalTaxonomyWorkflowError(c, errors.New("taxonomy agent run model does not match Server configuration"))
		return
	}
	run, err := h.db.Taxonomy.StartTaxonomyAgentRun(c.Request.Context(), *claim, request.Role, h.llm.Model, time.Now().UTC())
	if err != nil {
		h.writeInternalTaxonomyWorkflowError(c, err)
		return
	}
	c.JSON(http.StatusOK, taxonomyapp.StartAgentRunResponse{Run: *run})
}

func (h *Handler) InternalTaxonomyWorkflowPhase(c *gin.Context) {
	var request taxonomyapp.PhaseRequest
	if !h.decodeInternalWorkerRequest(c, internalTaxonomyRole, &request) {
		return
	}
	if err := request.Validate(); err != nil {
		h.writeInternalTaxonomyWorkflowError(c, err)
		return
	}
	claim, err := h.taxonomyWorkflowClaim(c, request.LeaseCredential)
	if err != nil {
		h.writeInternalTaxonomyWorkflowError(c, err)
		return
	}
	if claim.Workflow.State != request.ExpectedState {
		h.writeInternalTaxonomyWorkflowError(c, taxonomy.ErrLeaseLost)
		return
	}
	now := time.Now().UTC()
	switch {
	case request.Mapper != nil:
		err = h.completeTaxonomyMapper(c.Request.Context(), *claim, *request.Mapper, now)
	case request.ReviewPair != nil:
		err = h.completeTaxonomyReviewPair(c.Request.Context(), *claim, *request.ReviewPair, now)
	case request.Publication != nil:
		err = h.completeTaxonomyPublication(c.Request.Context(), *claim, now)
	case request.TechnicalFailure != nil:
		_, _, err = h.db.Taxonomy.ReportTaxonomyTechnicalFailure(c.Request.Context(), *claim, request.ExpectedState, request.TechnicalFailure.Message, request.TechnicalFailure.RunIDs, now)
	}
	if err != nil {
		h.writeInternalTaxonomyWorkflowError(c, err)
		return
	}
	refreshed, err := h.refreshTaxonomyWorkflowClaim(c.Request.Context(), *claim)
	if err != nil {
		h.writeInternalTaxonomyWorkflowError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Claim *taxonomy.Claim `json:"claim,omitempty"`
	}{Claim: refreshed})
}

func (h *Handler) taxonomyWorkflowClaim(c *gin.Context, credential taxonomy.LeaseCredential) (*taxonomy.Claim, error) {
	if h.db == nil || strings.TrimSpace(c.Param("id")) == "" || !credential.Valid() {
		return nil, errors.New("valid taxonomy workflow lease credentials are required")
	}
	return h.db.Taxonomy.GetTaxonomyClaim(c.Request.Context(), c.Param("id"), credential, time.Now().UTC())
}

func (h *Handler) refreshTaxonomyWorkflowClaim(ctx context.Context, prior taxonomy.Claim) (*taxonomy.Claim, error) {
	workflow, err := h.db.Taxonomy.GetTaxonomyWorkflow(ctx, prior.Workflow.ID)
	if err != nil {
		return nil, err
	}
	if workflow.State.Terminal() || strings.TrimSpace(workflow.LeaseOwner) == "" {
		return nil, nil
	}
	return h.db.Taxonomy.RefreshTaxonomyClaim(ctx, workflow.ID, prior.LeaseOwner, time.Now().UTC())
}

func (h *Handler) taxonomyContext(ctx context.Context, claim taxonomy.Claim) (taxonomyapp.Context, error) {
	entry, err := h.taxonomyChallenge(ctx, claim.Workflow)
	if err != nil {
		return taxonomyapp.Context{}, err
	}
	base, err := h.taxonomySnapshotForRevision(claim.Workflow.BaseTaxonomyRevision)
	if err != nil {
		return taxonomyapp.Context{}, err
	}
	artifact, err := taxonomystore.ReadChallengeArtifact(entry.Dir)
	if err != nil {
		return taxonomyapp.Context{}, fmt.Errorf("read taxonomy challenge artifact: %w", err)
	}
	result := taxonomyapp.Context{Workflow: claim.Workflow}
	switch claim.Workflow.State {
	case taxonomy.WorkflowMapping:
		result.MapperValidation = &taxonomyapp.MapperValidation{
			Challenge: taxonomy.ChallengeRef{ID: entry.ID, Title: entry.Title, ContentRevision: entry.ContentRevision},
			Base:      base,
		}
		result.Mapper, err = taxonomyapp.MapperModelInput(claim.Workflow, entry.ID, entry.Title, entry.ContentRevision, base, artifact)
	case taxonomy.WorkflowReviewing:
		if claim.Workflow.CandidateChangeSet == nil {
			return taxonomyapp.Context{}, errors.New("taxonomy review has no mapper candidate")
		}
		result.CurriculumReview, err = taxonomyapp.CurriculumReviewModelInput(entry.ID, entry.Title, entry.ContentRevision, base, *claim.Workflow.CandidateChangeSet, artifact)
		if err == nil {
			result.SREReview, err = taxonomyapp.SREReviewModelInput(entry.ID, entry.Title, entry.ContentRevision, base, *claim.Workflow.CandidateChangeSet, artifact)
		}
	case taxonomy.WorkflowPublishing:
		return result, nil
	default:
		return taxonomyapp.Context{}, fmt.Errorf("taxonomy workflow state %s has no worker context", claim.Workflow.State)
	}
	if err != nil {
		return taxonomyapp.Context{}, err
	}
	return result, nil
}

func (h *Handler) completeTaxonomyMapper(ctx context.Context, claim taxonomy.Claim, result taxonomyapp.MapperResult, now time.Time) error {
	entry, err := h.taxonomyChallenge(ctx, claim.Workflow)
	if err != nil {
		return err
	}
	base, err := h.taxonomySnapshotForRevision(claim.Workflow.BaseTaxonomyRevision)
	if err != nil {
		return err
	}
	if _, err := taxonomy.ValidateWorkflowChangeSet(result.ChangeSet, taxonomy.ChallengeRef{ID: entry.ID, Title: entry.Title, ContentRevision: entry.ContentRevision}, base); err != nil {
		return fmt.Errorf("mapper candidate failed deterministic validation: %w", err)
	}
	return h.db.Taxonomy.FinalizeTaxonomyMapper(ctx, claim, result.RunID, result.ChangeSet, now)
}

func (h *Handler) completeTaxonomyReviewPair(ctx context.Context, claim taxonomy.Claim, result taxonomyapp.ReviewPairResult, now time.Time) error {
	if err := taxonomy.ValidateReview(result.Curriculum); err != nil {
		return err
	}
	if err := taxonomy.ValidateReview(result.SRE); err != nil {
		return err
	}
	current, err := h.currentTaxonomySnapshot()
	if err != nil {
		return err
	}
	return h.db.Taxonomy.FinalizeTaxonomyReviewPair(ctx, claim, result.CurriculumRunID, result.SRERunID, result.Curriculum, result.SRE, current.Revision, now)
}

func (h *Handler) completeTaxonomyPublication(ctx context.Context, claim taxonomy.Claim, now time.Time) error {
	h.taxonomyPublishMu.Lock()
	defer h.taxonomyPublishMu.Unlock()

	entry, err := h.taxonomyChallenge(ctx, claim.Workflow)
	if err != nil {
		return err
	}
	if claim.Workflow.CandidateChangeSet == nil || claim.Workflow.CurriculumReview == nil || claim.Workflow.SREReview == nil ||
		claim.Workflow.CurriculumReview.Decision != taxonomy.ReviewApprove || claim.Workflow.SREReview.Decision != taxonomy.ReviewApprove {
		return errors.New("taxonomy publication requires an approved review pair")
	}
	base, err := h.taxonomySnapshotForRevision(claim.Workflow.BaseTaxonomyRevision)
	if err != nil {
		return err
	}
	current, err := h.currentTaxonomySnapshot()
	if err != nil {
		return err
	}
	changes := *claim.Workflow.CandidateChangeSet
	if current.Revision != claim.Workflow.BaseTaxonomyRevision {
		if !changes.MappingOnlyFor(entry.ID) || !taxonomy.ReferencedDefinitionsUnchanged(base, current, changes) {
			return h.db.Taxonomy.ResetTaxonomyForLatest(ctx, claim, current.Revision, "taxonomy 已变化，需要基于最新 revision 重新进行完整审查", now)
		}
		base = current
	}
	next, err := taxonomy.ValidateWorkflowChangeSet(changes, taxonomy.ChallengeRef{ID: entry.ID, Title: entry.Title, ContentRevision: entry.ContentRevision}, base)
	if err != nil {
		return fmt.Errorf("validate approved taxonomy changeset: %w", err)
	}
	expected, err := h.taxonomy.PreviewRevision(next)
	if err != nil {
		return fmt.Errorf("preview taxonomy snapshot: %w", err)
	}
	if err := h.db.Taxonomy.SetTaxonomyExpectedSnapshot(ctx, claim, expected, now); err != nil {
		return err
	}
	published, err := h.taxonomy.Publish(next)
	if err != nil {
		return fmt.Errorf("publish taxonomy snapshot: %w", err)
	}
	if published.Revision != expected {
		return fmt.Errorf("published taxonomy revision %q differs from expected %q", published.Revision, expected)
	}
	return h.db.Taxonomy.CompleteTaxonomyPublication(ctx, claim, published.Revision, now)
}

func (h *Handler) taxonomyChallenge(ctx context.Context, workflow taxonomy.Workflow) (*challenge.Entry, error) {
	entry, err := challenge.Get(h.challengesDir, workflow.ChallengeID)
	if err == nil && entry.ContentRevision == workflow.ChallengeContentRevision {
		return entry, nil
	}
	reason := "目标 challenge artifact 已不存在或 revision 已变化"
	if cancelErr := h.db.Taxonomy.CancelTaxonomyWorkflow(ctx, workflow.ID, reason, time.Now().UTC()); cancelErr != nil && !errors.Is(cancelErr, postgres.ErrTaxonomyWorkflowNotFound) {
		return nil, fmt.Errorf("cancel stale taxonomy workflow: %w", cancelErr)
	}
	return nil, taxonomy.ErrLeaseLost
}

func (h *Handler) taxonomySnapshotForRevision(revision string) (taxonomy.Snapshot, error) {
	if strings.TrimSpace(revision) == "" {
		return taxonomy.Snapshot{}, nil
	}
	snapshot, err := h.taxonomy.LoadRevision(revision)
	if err != nil {
		return taxonomy.Snapshot{}, fmt.Errorf("load taxonomy revision %s: %w", revision, err)
	}
	return *snapshot, nil
}

func (h *Handler) currentTaxonomySnapshot() (taxonomy.Snapshot, error) {
	snapshot, err := h.taxonomy.LoadCurrent()
	if errors.Is(err, taxonomy.ErrNoCurrentRevision) {
		return taxonomy.Snapshot{}, nil
	}
	if err != nil {
		return taxonomy.Snapshot{}, fmt.Errorf("load current taxonomy: %w", err)
	}
	return *snapshot, nil
}

// StartTaxonomyWorkflowMaintenance only discovers missing mappings and repairs
// a publish record after a Server crash. It never calls models or writes
// taxonomy content directly.
func (h *Handler) StartTaxonomyWorkflowMaintenance(ctx context.Context) {
	if h.db == nil || h.taxonomy == nil || strings.TrimSpace(h.challengesDir) == "" {
		return
	}
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			if err := h.ReconcileTaxonomyWorkflows(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("reconcile taxonomy workflows", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (h *Handler) ReconcileTaxonomyWorkflows(ctx context.Context) error {
	if err := h.RecoverTaxonomyPublications(ctx); err != nil {
		return err
	}
	entries, err := challenge.List(h.challengesDir)
	if err != nil {
		return fmt.Errorf("list published challenges for taxonomy: %w", err)
	}
	byID := make(map[string]challenge.Entry, len(entries))
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	active, err := h.db.Taxonomy.ListActiveTaxonomyWorkflows(ctx)
	if err != nil {
		return err
	}
	for _, workflow := range active {
		entry, exists := byID[workflow.ChallengeID]
		if exists && entry.ContentRevision == workflow.ChallengeContentRevision {
			continue
		}
		if err := h.db.Taxonomy.CancelTaxonomyWorkflow(ctx, workflow.ID, "目标 challenge artifact 已不存在或 revision 已变化", time.Now().UTC()); err != nil && !errors.Is(err, postgres.ErrTaxonomyWorkflowNotFound) {
			return err
		}
	}
	current, err := h.currentTaxonomySnapshot()
	if err != nil {
		return err
	}
	index, err := taxonomystore.NewCatalogIndex(current, entries)
	if err != nil && current.Revision != "" {
		return fmt.Errorf("index current taxonomy: %w", err)
	}
	for _, entry := range entries {
		if current.Revision != "" {
			if mapping, mapped := index.Mapping(entry.ID); mapped && mapping.Challenge.ContentRevision == entry.ContentRevision {
				continue
			}
		}
		if _, _, err := h.db.Taxonomy.CreateOrGetTaxonomyWorkflow(ctx, entry.ID, entry.ContentRevision, current.Revision, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) RecoverTaxonomyPublications(ctx context.Context) error {
	workflows, err := h.db.Taxonomy.ListPublishingTaxonomyWorkflows(ctx)
	if err != nil {
		return err
	}
	if len(workflows) == 0 {
		return nil
	}
	current, err := h.currentTaxonomySnapshot()
	if err != nil {
		return err
	}
	for _, workflow := range workflows {
		if workflow.ExpectedSnapshotRevision == "" || workflow.ExpectedSnapshotRevision != current.Revision {
			continue
		}
		if err := h.db.Taxonomy.RecoverTaxonomyPublication(ctx, workflow.ID, workflow.ExpectedSnapshotRevision, time.Now().UTC()); err != nil && !errors.Is(err, taxonomy.ErrLeaseLost) {
			return err
		}
	}
	return nil
}

func (h *Handler) writeInternalTaxonomyWorkflowError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, postgres.ErrTaxonomyWorkflowNotFound), errors.Is(err, agent.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, taxonomy.ErrLeaseLost):
		status = http.StatusConflict
	}
	c.JSON(status, api.ErrorResponse{Error: err.Error()})
}
