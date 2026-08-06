package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const environmentProjectionInterval = 2 * time.Second

type environmentProjection struct {
	UID     string
	Name    string
	Runtime string
	Spec    *breakfixv1.EnvironmentSpec
	Status  *breakfixv1.EnvironmentStatus
}

// StartEnvironmentStatusProjector makes Server the sole database writer for
// learning history. Verification environments are owned by the verifier and
// are deliberately excluded by their purpose.
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
	nodes, err := h.k8s.ListNodeEnvironments(ctx, h.crdNamespace, "")
	if err != nil {
		return err
	}
	for index := range nodes.Items {
		environment := &nodes.Items[index]
		if err := h.projectEnvironment(ctx, environmentProjection{
			UID: string(environment.UID), Name: environment.Name, Runtime: "node",
			Spec: &environment.Spec.Environment, Status: &environment.Status.Environment,
		}); err != nil {
			return err
		}
	}

	vk8s, err := h.k8s.ListVK8sEnvironments(ctx, h.crdNamespace, "")
	if err != nil {
		return err
	}
	for index := range vk8s.Items {
		environment := &vk8s.Items[index]
		if err := h.projectEnvironment(ctx, environmentProjection{
			UID: string(environment.UID), Name: environment.Name, Runtime: "k8s",
			Spec: &environment.Spec.Environment, Status: &environment.Status.Environment,
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
	if projection.Spec.Purpose != breakfixv1.EnvironmentPurposeLearning {
		return false, nil
	}
	if strings.TrimSpace(projection.UID) == "" {
		return false, fmt.Errorf("environment %q has no uid", projection.Name)
	}
	if projection.Status.ReadyAt != nil && !projection.Status.ReadyAt.IsZero() {
		if err := h.db.Environment.RecordChallengeAttempt(ctx, projection.Spec.UserRef, projection.Spec.Source.Ref, projection.Spec.Source.Revision, projection.UID, projection.Runtime, projection.Status.ReadyAt.UTC()); err != nil {
			return false, fmt.Errorf("record environment attempt: %w", err)
		}
	}
	if projection.Status.Checkpoints != nil {
		for _, checkpoint := range projection.Status.Checkpoints.Results {
			if checkpoint.FirstPassedAt == nil || checkpoint.FirstPassedAt.IsZero() {
				continue
			}
			if err := h.db.Environment.RecordCheckpointFirstPass(ctx, postgres.CheckpointFirstPassEvent{
				EnvironmentUID: projection.UID, UserID: projection.Spec.UserRef,
				ChallengeID: projection.Spec.Source.Ref, ChallengeRevision: projection.Spec.Source.Revision,
				CheckpointID: checkpoint.ID, FirstPassedAt: checkpoint.FirstPassedAt.UTC(), Summary: checkpoint.Summary,
			}); err != nil {
				return false, fmt.Errorf("record checkpoint first pass %q: %w", checkpoint.ID, err)
			}
		}
	}

	switch projection.Status.Phase {
	case breakfixv1.EnvironmentCompleted:
		if err := h.db.Environment.RecordChallengeCompletion(ctx, projection.Spec.UserRef, projection.Spec.Source.Ref, projection.Spec.Source.Revision, projection.UID, lifecycleTime(projection.Status.CompletedAt)); err != nil {
			return false, fmt.Errorf("record environment completion: %w", err)
		}
	case breakfixv1.EnvironmentDestroyed, breakfixv1.EnvironmentFailed:
		if projection.Status.ReadyAt != nil && !projection.Status.ReadyAt.IsZero() {
			if err := h.db.Environment.FinishChallengeAttempt(ctx, projection.UID, postgres.AttemptExpired, lifecycleTime(projection.Status.DestroyedAt)); err != nil {
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
