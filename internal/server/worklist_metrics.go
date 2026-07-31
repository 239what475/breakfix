package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/breakfix/breakfix/internal/api"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/worklist"
	"github.com/gin-gonic/gin"
)

type worklistOperationKey struct {
	Kind      worklist.Kind
	Operation string
}

type worklistOperationMetrics struct {
	mu       sync.Mutex
	failures map[worklistOperationKey]uint64
}

func newWorklistOperationMetrics() *worklistOperationMetrics {
	return &worklistOperationMetrics{failures: make(map[worklistOperationKey]uint64)}
}

func (m *worklistOperationMetrics) recordFailure(kind worklist.Kind, operation string) {
	if m == nil || !worklist.ValidKind(kind) || strings.TrimSpace(operation) == "" {
		return
	}
	m.mu.Lock()
	m.failures[worklistOperationKey{Kind: kind, Operation: operation}]++
	m.mu.Unlock()
}

func (m *worklistOperationMetrics) snapshot() map[worklistOperationKey]uint64 {
	result := make(map[worklistOperationKey]uint64)
	if m == nil {
		return result
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, value := range m.failures {
		result[key] = value
	}
	return result
}

func (h *Handler) InternalInspectWorkItems(c *gin.Context) {
	var filter worklist.InspectionFilter
	if !h.decodeInternalAgentRequest(c, &filter) {
		return
	}
	items, err := h.db.InspectWorkItems(c.Request.Context(), filter)
	if err != nil {
		h.writeInternalWorkError(c, err)
		return
	}
	snapshot, err := h.db.WorklistSnapshot(c.Request.Context(), time.Now().UTC())
	if err != nil {
		h.writeInternalWorkError(c, err)
		return
	}
	c.JSON(http.StatusOK, struct {
		Items    []worklist.InspectionItem `json:"items"`
		Snapshot worklist.Snapshot         `json:"snapshot"`
	}{Items: items, Snapshot: snapshot})
}

func (h *Handler) WorklistMetrics(c *gin.Context) {
	if h == nil || h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "worklist metrics are unavailable"})
		return
	}
	snapshot, err := h.db.WorklistSnapshot(c.Request.Context(), time.Now().UTC())
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
	output.WriteString("# TYPE breakfix_work_items gauge\n")
	output.WriteString("# TYPE breakfix_work_item_oldest_pending_seconds gauge\n")
	output.WriteString("# TYPE breakfix_work_item_duration_seconds summary\n")
	var cleanupBacklog int64
	for _, item := range snapshot.Kinds {
		states := []struct {
			name  worklist.State
			value int64
		}{
			{worklist.StatePending, item.Pending}, {worklist.StateRunning, item.Running},
			{worklist.StateSucceeded, item.Succeeded}, {worklist.StateFailed, item.Failed},
			{worklist.StateCancelled, item.Cancelled},
		}
		for _, state := range states {
			_, _ = fmt.Fprintf(&output, "breakfix_work_items{kind=%q,state=%q} %d\n", item.Kind, state.name, state.value)
		}
		_, _ = fmt.Fprintf(&output, "breakfix_work_item_oldest_pending_seconds{kind=%q} %g\n", item.Kind, item.OldestPendingAgeSeconds)
		_, _ = fmt.Fprintf(&output, "breakfix_work_item_duration_seconds_sum{kind=%q} %g\n", item.Kind, item.TerminalDurationSeconds)
		_, _ = fmt.Fprintf(&output, "breakfix_work_item_duration_seconds_count{kind=%q} %d\n", item.Kind, item.TerminalCount)
		if item.Kind == worklist.KindArtifactCleanup {
			cleanupBacklog = item.Pending + item.Running
		}
	}
	output.WriteString("# TYPE breakfix_work_item_failures_total counter\n")
	for _, failure := range snapshot.Failures {
		_, _ = fmt.Fprintf(&output, "breakfix_work_item_failures_total{kind=%q,class=%q} %d\n", failure.Kind, failure.Class, failure.Count)
	}
	output.WriteString("# TYPE breakfix_work_item_operation_failures_total counter\n")
	operationFailures := h.worklistMetrics.snapshot()
	operationKeys := make([]worklistOperationKey, 0, len(operationFailures))
	for key := range operationFailures {
		operationKeys = append(operationKeys, key)
	}
	slices.SortFunc(operationKeys, func(left, right worklistOperationKey) int {
		if compared := strings.Compare(string(left.Kind), string(right.Kind)); compared != 0 {
			return compared
		}
		return strings.Compare(left.Operation, right.Operation)
	})
	for _, key := range operationKeys {
		count := operationFailures[key]
		_, _ = fmt.Fprintf(&output, "breakfix_work_item_operation_failures_total{kind=%q,operation=%q} %d\n", key.Kind, key.Operation, count)
	}
	output.WriteString("# TYPE breakfix_cleanup_backlog gauge\n")
	_, _ = fmt.Fprintf(&output, "breakfix_cleanup_backlog %d\n", cleanupBacklog)
	output.WriteString("# TYPE breakfix_verification_environments gauge\n")
	_, _ = fmt.Fprintf(&output, "breakfix_verification_environments %d\n", activeVerification)
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(output.String()))
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
