package server

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/db"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const environmentProjectionInterval = 2 * time.Second

type environmentProjection struct {
	UID     string
	Name    string
	Runtime string
	Spec    *breakfixv1.CommonEnvironmentSpec
	Status  *breakfixv1.CommonEnvironmentStatus
}

// StartEnvironmentStatusProjector makes the Server the sole database writer
// for Environment lifecycle facts. Each operation is idempotent so a restart
// simply replays current CRD status.
func (h *Handler) StartEnvironmentStatusProjector(ctx context.Context) {
	if h == nil || h.db == nil || h.k8s == nil {
		return
	}
	project := func() {
		if err := h.projectEnvironmentStatuses(ctx); err != nil {
			slog.Warn("project environment statuses", "err", err)
		}
	}
	project()
	go func() {
		ticker := time.NewTicker(environmentProjectionInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				project()
			}
		}
	}()
}

func (h *Handler) projectEnvironmentStatuses(ctx context.Context) error {
	containers, err := h.k8s.ListContainerEnvironments(ctx, h.crdNamespace, "")
	if err != nil {
		return err
	}
	for i := range containers.Items {
		env := &containers.Items[i]
		if err := h.projectEnvironment(ctx, environmentProjection{
			UID: string(env.UID), Name: env.Name, Runtime: "container", Spec: &env.Spec, Status: &env.Status,
		}); err != nil {
			return err
		}
	}

	vclusters, err := h.k8s.ListVClusterEnvironments(ctx, h.crdNamespace, "")
	if err != nil {
		return err
	}
	for i := range vclusters.Items {
		env := &vclusters.Items[i]
		if err := h.projectEnvironment(ctx, environmentProjection{
			UID: string(env.UID), Name: env.Name, Runtime: "vcluster", Spec: &env.Spec.CommonEnvironmentSpec, Status: &env.Status.CommonEnvironmentStatus,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handler) projectEnvironment(ctx context.Context, projection environmentProjection) error {
	deleteAfterProjection, err := h.projectEnvironmentRecord(ctx, projection)
	if err != nil || !deleteAfterProjection {
		return err
	}
	adapter, err := h.environmentRuntimeAdapter(projection.Runtime)
	if err != nil {
		return err
	}
	if err := adapter.requestDeletion(ctx, projection.Name); err != nil {
		return fmt.Errorf("delete projected %s environment %q: %w", projection.Runtime, projection.Name, err)
	}
	return nil
}

func (h *Handler) projectEnvironmentRecord(ctx context.Context, projection environmentProjection) (bool, error) {
	if h == nil || h.db == nil || projection.Spec == nil || projection.Status == nil {
		return false, nil
	}
	if strings.TrimSpace(projection.UID) == "" {
		return false, fmt.Errorf("environment %q has no uid", projection.Name)
	}
	if projection.Status.ReadyAt != nil && !projection.Status.ReadyAt.IsZero() {
		if err := h.db.RecordChallengeAttempt(
			ctx,
			projection.Spec.UserRef,
			projection.Spec.ChallengeRef,
			projection.UID,
			projection.Runtime,
			projection.Status.ReadyAt.UTC(),
		); err != nil {
			return false, fmt.Errorf("record environment attempt: %w", err)
		}
	}

	switch projection.Status.Phase {
	case breakfixv1.EnvironmentCompleted:
		completedAt := lifecycleTime(projection.Status.CompletedAt)
		if err := h.db.RecordChallengeCompletion(ctx, projection.Spec.UserRef, projection.Spec.ChallengeRef, projection.UID, completedAt); err != nil {
			return false, fmt.Errorf("record environment completion: %w", err)
		}
	case breakfixv1.EnvironmentDestroyed, breakfixv1.EnvironmentFailed:
		if projection.Status.ReadyAt != nil && !projection.Status.ReadyAt.IsZero() {
			if err := h.db.FinishChallengeAttempt(ctx, projection.UID, db.AttemptExpired, lifecycleTime(projection.Status.DestroyedAt)); err != nil {
				return false, fmt.Errorf("finish environment attempt: %w", err)
			}
		}
		return true, nil
	}
	return false, nil
}

func lifecycleTime(value *metav1.Time) time.Time {
	if value != nil && !value.IsZero() {
		return value.UTC()
	}
	return time.Now().UTC()
}
