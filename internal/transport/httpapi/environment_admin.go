package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/domain/audit"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ListAdminEnvironments observes every RuntimeEnvironment across users, both
// learning and verification. Failed environments surface their controller
// failure so accumulation is visible at a glance.
func (h *Handler) ListAdminEnvironments(c *gin.Context) {
	if h == nil || h.k8s == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "environment observation is unavailable"})
		return
	}
	environments, err := h.k8s.ListRuntimeEnvironments(c.Request.Context(), h.crdNamespace, "")
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	items := make([]api.AdminEnvironment, 0, len(environments.Items))
	for _, environment := range environments.Items {
		stringPointer := func(value string) *string {
			if value == "" {
				return nil
			}
			return &value
		}
		item := api.AdminEnvironment{
			Name:        environment.Name,
			Namespace:   environment.Namespace,
			Phase:       string(environment.Status.Phase),
			Purpose:     api.AdminEnvironmentPurpose(environment.Spec.Purpose),
			User:        stringPointer(environment.Labels["breakfix.dev/user"]),
			ContentKind: stringPointer(environment.Labels["breakfix.dev/content-kind"]),
			ContentId:   stringPointer(environment.Labels["breakfix.dev/content-id"]),
			CreatedAt:   environment.CreationTimestamp.UTC(),
		}
		if expires := environment.Status.Lifecycle.ExpiresAt; expires != nil {
			expiresAt := expires.Time.UTC()
			item.ExpiresAt = &expiresAt
		}
		if failure := environment.Status.Failure; failure != nil {
			message := failure.Message
			item.Failure = &api.AdminEnvironmentFailure{
				Class:     api.AdminEnvironmentFailureClass(failure.Class),
				Component: failure.Component,
				Reason:    failure.Reason,
				Message:   &message,
				At:        failure.At.UTC(),
			}
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].Name < items[j].Name
	})
	c.JSON(http.StatusOK, api.AdminEnvironmentList{Environments: items})
}

// ReleaseAdminEnvironment requests an administrative release by setting
// spec.lease.releaseAt; the existing Draining controller path and Reaper take
// over from there. The handler never tears down resources itself, and a
// release request that is already in flight is an idempotent success.
func (h *Handler) ReleaseAdminEnvironment(c *gin.Context, name string) {
	if h == nil || h.k8s == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "environment release is unavailable"})
		return
	}
	ctx := c.Request.Context()
	environment, err := h.k8s.GetRuntimeEnvironment(ctx, h.crdNamespace, name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "runtime environment not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	if environment.Status.Phase == runtimev2.PhaseReleased {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "runtime environment is already released"})
		return
	}
	phase := string(environment.Status.Phase)
	if environment.Spec.Lease.ReleaseAt == nil {
		now := metav1.NewTime(time.Now().UTC())
		environment.Spec.Lease.ReleaseAt = &now
		if _, err := h.k8s.UpdateRuntimeEnvironment(ctx, h.crdNamespace, environment); err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
			return
		}
	}
	actorID, _ := c.Get("user_id")
	actor, _ := actorID.(string)
	if err := h.recordEnvironmentRelease(ctx, actor, environment.Name, phase); err != nil {
		slog.Warn("record environment release audit", "environment", environment.Name, "err", err)
	}
	c.JSON(http.StatusOK, api.AdminEnvironmentRelease{Id: environment.Name, Phase: phase})
}

func (h *Handler) recordEnvironmentRelease(ctx context.Context, actor, environmentName, phase string) error {
	if h == nil || h.db == nil || actor == "" {
		return nil
	}
	now := time.Now().UTC()
	detail, err := json.Marshal(map[string]string{"phase": phase})
	if err != nil {
		return err
	}
	action := audit.HumanAction{
		ID:         audit.NewID(now),
		UserID:     actor,
		Action:     audit.ActionEnvironmentRelease,
		TargetType: audit.TargetRuntimeEnvironment,
		TargetID:   environmentName,
		Detail:     detail,
		CreatedAt:  now,
	}
	return h.db.Audit.RecordHumanAction(ctx, action)
}
