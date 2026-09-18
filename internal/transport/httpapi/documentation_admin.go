package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/domain/audit"
	documentdomain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

// runnableActionStuckBudget wraps the verification environment's 1800s
// MaxLifetime plus a 300s reconciliation margin before a runnable phase
// counts as stuck by dwell alone.
const runnableActionStuckBudget = 1800*time.Second + 300*time.Second

// requireDocumentationProduct guards every admin documentation endpoint: the
// product is deployment-owned and entirely absent when library_root is unset.
func (h *Handler) requireDocumentationProduct(c *gin.Context) bool {
	if h == nil || h.db == nil || h.documentation == nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "documentation practice is not configured"})
		return false
	}
	return true
}

func (h *Handler) ListAdminDocumentationWorkflows(c *gin.Context) {
	if !h.requireDocumentationProduct(c) {
		return
	}
	observations, err := h.db.DocumentPractice.ListWorkflowObservations(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	now := time.Now().UTC()
	workflows := make([]api.AdminDocumentationWorkflow, 0, len(observations))
	for _, observation := range observations {
		workflows = append(workflows, adminDocumentationWorkflowSummary(observation, now, h.agentStuckAfter))
	}
	c.JSON(http.StatusOK, api.AdminDocumentationWorkflowList{Workflows: workflows})
}

func (h *Handler) GetAdminDocumentationWorkflow(c *gin.Context, workflowID api.DocumentWorkflowID) {
	if !h.requireDocumentationProduct(c) {
		return
	}
	ctx := c.Request.Context()
	observation, err := h.db.DocumentPractice.GetWorkflowObservation(ctx, string(workflowID))
	if errors.Is(err, documentdomain.ErrWorkflowNotFound) {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "document workflow not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	artifacts, err := h.db.DocumentPractice.ListArtifacts(ctx, string(workflowID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	agentAudits, err := h.db.DocumentPractice.ListAgentAudits(ctx, string(workflowID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	publication, err := h.db.DocumentPractice.GetWorkflowPublication(ctx, string(workflowID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	now := time.Now().UTC()
	detail := api.AdminDocumentationWorkflowDetail{
		Id:           observation.Workflow.ID,
		State:        string(observation.Workflow.State),
		StateVersion: int(observation.Workflow.StateVersion),
		Revision:     int(observation.Workflow.Revision),
		UpdatedAt:    observation.Workflow.UpdatedAt,
		DwellSeconds: int(dwellSeconds(observation.Workflow.UpdatedAt, now)),
		Stuck:        deriveDocumentationStuck(observation, now, h.agentStuckAfter),
		Ledger:       make([]api.AdminDocumentationLedgerEntry, 0, len(artifacts)),
		AgentAudits:  make([]api.AdminDocumentationAgentAudit, 0, len(agentAudits)),
	}
	for _, artifact := range artifacts {
		var parentID, policyVersion *string
		if artifact.ParentID != "" {
			parentID = &artifact.ParentID
		}
		if artifact.PolicyVersion != "" {
			policyVersion = &artifact.PolicyVersion
		}
		detail.Ledger = append(detail.Ledger, api.AdminDocumentationLedgerEntry{
			Id:              artifact.ID,
			Kind:            artifact.Kind,
			ParentId:        parentID,
			ContentRevision: artifact.ContentRevision,
			Digest:          artifact.Digest,
			SchemaVersion:   artifact.SchemaVersion,
			OwnerRole:       artifact.OwnerRole,
			PolicyVersion:   policyVersion,
			CreatedAt:       artifact.CreatedAt,
		})
	}
	for _, auditRow := range agentAudits {
		detail.AgentAudits = append(detail.AgentAudits, api.AdminDocumentationAgentAudit{
			RunId:         auditRow.RunID,
			Role:          auditRow.Role,
			Model:         auditRow.Model,
			PromptVersion: auditRow.PromptVersion,
			ToolVersion:   auditRow.ToolVersion,
			PolicyVersion: auditRow.PolicyVersion,
			InputDigest:   auditRow.InputDigest,
			OutputDigest:  auditRow.OutputDigest,
			CreatedAt:     auditRow.CreatedAt,
		})
	}
	if publication != nil {
		var manifestPayload map[string]any
		encoded, err := json.Marshal(publication.Manifest)
		if err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
			return
		}
		if err := json.Unmarshal(encoded, &manifestPayload); err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
			return
		}
		detail.Publication = &api.AdminDocumentationPublication{
			Id:             publication.ID,
			Manifest:       manifestPayload,
			ManifestDigest: publication.Digest,
			CreatedAt:      publication.CreatedAt,
		}
	}
	c.JSON(http.StatusOK, detail)
}

func (h *Handler) ForceFailAdminDocumentationWorkflow(c *gin.Context, workflowID api.DocumentWorkflowID) {
	h.runAdminWorkflowAction(c, string(workflowID), audit.ActionDocumentationWorkflowForceFail, func(ctx context.Context, workflowID, reason string, action *audit.HumanAction) (documentdomain.Workflow, error) {
		return h.documentation.ForceFailDocumentationWorkflow(ctx, workflowID, reason, action)
	})
}

func (h *Handler) RestartAdminDocumentationWorkflow(c *gin.Context, workflowID api.DocumentWorkflowID) {
	h.runAdminWorkflowAction(c, string(workflowID), audit.ActionDocumentationWorkflowRestart, func(ctx context.Context, workflowID, reason string, action *audit.HumanAction) (documentdomain.Workflow, error) {
		return h.documentation.RestartDocumentationWorkflow(ctx, workflowID, reason, action)
	})
}

type runAdminWorkflowTransition func(ctx context.Context, workflowID, reason string, action *audit.HumanAction) (documentdomain.Workflow, error)

// runAdminWorkflowAction validates the required reason, records the acting
// administrator through the human action audit, executes the transition, and
// answers with a fresh observation summary.
func (h *Handler) runAdminWorkflowAction(c *gin.Context, workflowID string, auditAction string, run runAdminWorkflowTransition) {
	if !h.requireDocumentationProduct(c) {
		return
	}
	var request api.AdminWorkflowReasonRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "reason is required"})
		return
	}
	reason := request.Reason
	if len(reason) == 0 || len(reason) > 500 {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "reason must be between 1 and 500 characters"})
		return
	}
	actorID, _ := c.Get("user_id")
	actor, _ := actorID.(string)
	now := time.Now().UTC()
	action := audit.HumanAction{
		ID:         audit.NewID(now),
		UserID:     actor,
		Action:     auditAction,
		TargetType: audit.TargetDocumentWorkflow,
		TargetID:   workflowID,
		Detail:     []byte(`{}`),
		CreatedAt:  now,
	}
	if _, err := run(c.Request.Context(), workflowID, reason, &action); err != nil {
		if errors.Is(err, documentdomain.ErrWorkflowNotFound) {
			c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "document workflow not found"})
			return
		}
		if errors.Is(err, documentdomain.ErrWorkflowConflict) {
			c.JSON(http.StatusConflict, api.ErrorResponse{Error: err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	observation, err := h.db.DocumentPractice.GetWorkflowObservation(c.Request.Context(), workflowID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, adminDocumentationWorkflowSummary(observation, time.Now().UTC(), h.agentStuckAfter))
}

func adminDocumentationWorkflowSummary(observation postgres.DocumentWorkflowObservation, now time.Time, agentStuckAfter time.Duration) api.AdminDocumentationWorkflow {
	return api.AdminDocumentationWorkflow{
		Id:           observation.Workflow.ID,
		State:        string(observation.Workflow.State),
		StateVersion: int(observation.Workflow.StateVersion),
		Revision:     int(observation.Workflow.Revision),
		UpdatedAt:    observation.Workflow.UpdatedAt,
		DwellSeconds: int(dwellSeconds(observation.Workflow.UpdatedAt, now)),
		Stuck:        deriveDocumentationStuck(observation, now, agentStuckAfter),
	}
}

// deriveDocumentationStuck is a read-only derivation; it never writes state.
// Runnable phases consult the public action bound to the current state
// version (a completed review phase with no binding falls back to dwell);
// Agent phases use the configured dwell threshold.
func deriveDocumentationStuck(observation postgres.DocumentWorkflowObservation, now time.Time, agentStuckAfter time.Duration) api.AdminDocumentationWorkflowStuck {
	state := observation.Workflow.State
	if state.Terminal() {
		return api.AdminDocumentationWorkflowStuck{Flag: false}
	}
	dwell := time.Duration(dwellSeconds(observation.Workflow.UpdatedAt, now)) * time.Second
	switch state {
	case documentdomain.MaterializingArtifact, documentdomain.Verifying, documentdomain.VerificationReviewing:
		if action := observation.Action; action != nil {
			if action.State == "failed" {
				return api.AdminDocumentationWorkflowStuck{
					Flag:           true,
					Reason:         stuckReasonPointer("action_failed"),
					FailureClass:   stringPointer(action.FailureClass),
					FailureCode:    stringPointer(action.FailureCode),
					FailureSummary: stringPointer(action.FailureSummary),
				}
			}
			if action.Attempt >= 5 && (action.State == "queued" || action.State == "running") {
				return api.AdminDocumentationWorkflowStuck{Flag: true, Reason: stuckReasonPointer("attempts_exhausted")}
			}
		}
		if dwell > runnableActionStuckBudget {
			return api.AdminDocumentationWorkflowStuck{Flag: true, Reason: stuckReasonPointer("dwell_timeout")}
		}
	default:
		if dwell > agentStuckAfter {
			return api.AdminDocumentationWorkflowStuck{Flag: true, Reason: stuckReasonPointer("dwell_timeout")}
		}
	}
	return api.AdminDocumentationWorkflowStuck{Flag: false}
}

func dwellSeconds(stateEntered, now time.Time) int64 {
	if now.Before(stateEntered) {
		return 0
	}
	return int64(now.Sub(stateEntered).Seconds())
}

func stuckReasonPointer(reason string) *api.AdminDocumentationWorkflowStuckReason {
	value := api.AdminDocumentationWorkflowStuckReason(reason)
	return &value
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
