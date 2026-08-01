package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

// WorkflowMetrics reports the two durable background aggregates. There is no
// generic work-item metric because the platform has no generic work queue.
func (h *Handler) WorkflowMetrics(c *gin.Context) {
	if h == nil || h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "workflow metrics are unavailable"})
		return
	}
	counts, err := h.db.WorkflowStateCounts(c.Request.Context())
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
	output.WriteString("# TYPE breakfix_taxonomy_workflows gauge\n")
	writeWorkflowStateMetrics(&output, "breakfix_taxonomy_workflows", counts.Taxonomy)
	output.WriteString("# TYPE breakfix_verification_environments gauge\n")
	_, _ = fmt.Fprintf(&output, "breakfix_verification_environments %d\n", activeVerification)
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(output.String()))
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
	selector := "breakfix.dev/purpose=" + string(breakfixv1.EnvironmentPurposeVerification)
	nodes, err := h.k8s.ListNodeEnvironments(ctx, h.crdNamespace, selector)
	if err != nil {
		return 0, fmt.Errorf("list Node verification environments: %w", err)
	}
	vk8s, err := h.k8s.ListVK8sEnvironments(ctx, h.crdNamespace, selector)
	if err != nil {
		return 0, fmt.Errorf("list VK8s verification environments: %w", err)
	}
	count := 0
	for _, environment := range nodes.Items {
		if environment.Status.Environment.Phase != breakfixv1.EnvironmentDestroyed {
			count++
		}
	}
	for _, environment := range vk8s.Items {
		if environment.Status.Environment.Phase != breakfixv1.EnvironmentDestroyed {
			count++
		}
	}
	return count, nil
}
