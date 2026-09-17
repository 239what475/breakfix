package httpapi

import (
	"net/http"

	"github.com/breakfix/breakfix/internal/adapter/postgres"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

// ListAdminRunnableActions observes the public runnable queue. The summary
// always describes the whole queue; the state/phase filters apply to items
// only. Flags name the two conditions an operator must react to:
// attempt-high marks actions nearing their retry ceiling, and
// failed-unreconciled marks the precise "action is dead while its workflow
// still waits" signal.
func (h *Handler) ListAdminRunnableActions(c *gin.Context, params api.ListAdminRunnableActionsParams) {
	if h == nil || h.db == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "runnable queue is unavailable"})
		return
	}
	state := ""
	if params.State != nil {
		if !params.State.Valid() {
			c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "runnable action state filter is invalid"})
			return
		}
		state = string(*params.State)
	}
	phase := ""
	if params.Phase != nil {
		if !params.Phase.Valid() {
			c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "runnable action phase filter is invalid"})
			return
		}
		phase = string(*params.Phase)
	}
	ctx := c.Request.Context()
	byState, err := h.db.Runnable.CountRunnableActionsByState(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	byAttempt, err := h.db.Runnable.CountRunnableActionsByAttempt(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	observations, err := h.db.Runnable.ListRunnableActionObservations(ctx, state, phase)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	items := make([]api.AdminRunnableActionItem, 0, len(observations))
	for _, observation := range observations {
		items = append(items, adminRunnableActionItem(observation))
	}
	c.JSON(http.StatusOK, api.AdminRunnableActionPage{
		Summary: api.AdminRunnableActionSummary{ByState: intSummary(byState), ByAttempt: intSummary(byAttempt)},
		Items:   items,
	})
}

func intSummary(counts map[string]int64) map[string]int {
	summary := make(map[string]int, len(counts))
	for key, count := range counts {
		summary[key] = int(count)
	}
	return summary
}

func adminRunnableActionItem(observation postgres.RunnableActionObservation) api.AdminRunnableActionItem {
	flag := ""
	if observation.State == "failed" && observation.DocumentWorkflowID != nil && observation.ReconciledAt == nil {
		flag = "failed-unreconciled"
	} else if observation.Attempt >= 4 {
		flag = "attempt-high"
	}
	return api.AdminRunnableActionItem{
		ActionKey:          observation.ActionKey,
		ContentKind:        observation.ContentKind,
		ContentId:          observation.ContentID,
		ContentRevision:    observation.ContentRevision,
		Phase:              string(observation.Phase),
		State:              observation.State,
		Attempt:            observation.Attempt,
		LeaseExpiresAt:     observation.LeaseExpiresAt,
		NextRunAt:          observation.NextRunAt,
		FailureClass:       observation.FailureClass,
		FailureCode:        observation.FailureCode,
		FailureSummary:     observation.FailureSummary,
		DocumentWorkflowId: observation.DocumentWorkflowID,
		Reconciled:         observation.ReconciledAt,
		Flag:               api.AdminRunnableActionItemFlag(flag),
	}
}
