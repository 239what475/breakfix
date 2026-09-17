package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

// WorkflowMetrics reports durable background aggregates. There is no
// generic work-item metric because the platform has no generic work queue.
func (h *Handler) WorkflowMetrics(c *gin.Context) {
	if h == nil || h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "workflow metrics are unavailable"})
		return
	}
	counts, err := h.db.Reporting.WorkflowStateCounts(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: err.Error()})
		return
	}
	documentCounts, err := h.db.DocumentPractice.CountDocumentWorkflowsByState(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: err.Error()})
		return
	}
	queueStateCounts, err := h.db.Runnable.CountRunnableActionsByState(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: err.Error()})
		return
	}
	activeVerification, err := h.activeVerificationEnvironmentCount(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: err.Error()})
		return
	}
	var output strings.Builder
	output.WriteString("# TYPE breakfix_generation_workflows gauge\n")
	writeWorkflowStateMetrics(&output, "breakfix_generation_workflows", counts.Generation)
	output.WriteString("# TYPE breakfix_document_workflows gauge\n")
	writeFullStateMetrics(&output, "breakfix_document_workflows", documentCounts, []string{
		"Planning", "PlanReviewing", "Generating", "ArtifactReviewing", "MaterializingArtifact", "Verifying",
		"VerificationReviewing", "Publishing", "Published", "NoPractice", "Rejected", "Failed",
	})
	output.WriteString("# TYPE breakfix_runnable_actions gauge\n")
	writeFullStateMetrics(&output, "breakfix_runnable_actions", queueStateCounts, []string{"queued", "running", "completed", "failed"})
	output.WriteString("# TYPE breakfix_verification_environments gauge\n")
	_, _ = fmt.Fprintf(&output, "breakfix_verification_environments %d\n", activeVerification)
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(output.String()))
}

// writeFullStateMetrics emits every known state even when its count is zero
// so monitoring series stay stable across deployments.
func writeFullStateMetrics(output *strings.Builder, name string, counts map[string]int64, states []string) {
	for _, state := range states {
		_, _ = fmt.Fprintf(output, "%s{state=%q} %d\n", name, state, counts[state])
	}
}

func writeWorkflowStateMetrics(output *strings.Builder, name string, counts map[string]int64) {
	states := make([]string, 0, len(counts))
	for state := range counts {
		states = append(states, state)
	}
	sort.Strings(states)
	for _, state := range states {
		_, _ = fmt.Fprintf(output, "%s{state=%q} %d\n", name, state, counts[state])
	}
}

func (h *Handler) activeVerificationEnvironmentCount(ctx context.Context) (int, error) {
	if h.k8s == nil {
		return 0, nil
	}
	selector := "breakfix.dev/purpose=" + string(runtimev2.PurposeVerification)
	environments, err := h.k8s.ListRuntimeEnvironments(ctx, h.crdNamespace, selector)
	if err != nil {
		return 0, fmt.Errorf("list runtime verification environments: %w", err)
	}
	count := 0
	for _, environment := range environments.Items {
		if environment.Status.Phase != runtimev2.PhaseReleased {
			count++
		}
	}
	return count, nil
}
