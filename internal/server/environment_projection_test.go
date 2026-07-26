package server

import (
	"context"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/db"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/testpostgres"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newProjectionTestHandler(t *testing.T) *Handler {
	t.Helper()
	database := testpostgres.New(t)
	return &Handler{db: database}
}

func TestEnvironmentStatusProjectionRecordsReadyAndCompletionIdempotently(t *testing.T) {
	handler := newProjectionTestHandler(t)
	ctx := context.Background()
	readyAt := metav1.NewTime(time.Date(2026, time.July, 25, 1, 0, 0, 0, time.UTC))
	completedAt := metav1.NewTime(readyAt.Add(2 * time.Minute))
	projection := environmentProjection{
		UID:     "environment-uid",
		Name:    "environment-name",
		Runtime: "container",
		Spec: &breakfixv1.CommonEnvironmentSpec{
			UserRef: "u-demo", ChallengeRef: "cleanup-logs",
		},
		Status: &breakfixv1.CommonEnvironmentStatus{
			Phase: breakfixv1.EnvironmentReady, ReadyAt: &readyAt,
		},
	}

	for range 2 {
		deleteAfter, err := handler.projectEnvironmentRecord(ctx, projection)
		if err != nil || deleteAfter {
			t.Fatalf("ready projection = delete:%v err:%v", deleteAfter, err)
		}
	}
	projection.Status.Phase = breakfixv1.EnvironmentCompleted
	projection.Status.CompletedAt = &completedAt
	for range 2 {
		deleteAfter, err := handler.projectEnvironmentRecord(ctx, projection)
		if err != nil || deleteAfter {
			t.Fatalf("completed projection = delete:%v err:%v", deleteAfter, err)
		}
	}

	summary, err := handler.db.LearningSummary(ctx, "u-demo", completedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if summary.AttemptedCount != 1 || summary.CompletedCount != 1 {
		t.Fatalf("learning summary = %#v", summary)
	}
}

func TestEnvironmentStatusProjectionFinishesDestroyedAttemptBeforeDeletion(t *testing.T) {
	handler := newProjectionTestHandler(t)
	ctx := context.Background()
	readyAt := metav1.NewTime(time.Date(2026, time.July, 25, 2, 0, 0, 0, time.UTC))
	destroyedAt := metav1.NewTime(readyAt.Add(5 * time.Minute))
	projection := environmentProjection{
		UID:     "environment-uid",
		Name:    "environment-name",
		Runtime: "vcluster",
		Spec: &breakfixv1.CommonEnvironmentSpec{
			UserRef: "u-demo", ChallengeRef: "cleanup-logs",
		},
		Status: &breakfixv1.CommonEnvironmentStatus{
			Phase: breakfixv1.EnvironmentReady, ReadyAt: &readyAt,
		},
	}
	if deleteAfter, err := handler.projectEnvironmentRecord(ctx, projection); err != nil || deleteAfter {
		t.Fatalf("ready projection = delete:%v err:%v", deleteAfter, err)
	}
	projection.Status.Phase = breakfixv1.EnvironmentDestroyed
	projection.Status.DestroyedAt = &destroyedAt
	deleteAfter, err := handler.projectEnvironmentRecord(ctx, projection)
	if err != nil || !deleteAfter {
		t.Fatalf("destroyed projection = delete:%v err:%v", deleteAfter, err)
	}

	history, err := handler.db.ListLearningHistory(ctx, "u-demo", db.LearningHistoryFilter{ChallengeIDs: []string{"cleanup-logs"}}, 10, nil, destroyedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Outcome != db.AttemptExpired || history[0].EndedAt == nil || !history[0].EndedAt.Equal(destroyedAt.Time) {
		t.Fatalf("learning history = %#v", history)
	}
}
