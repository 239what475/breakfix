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

func TestEnvironmentStatusProjectionRecordsCheckpointFirstPassesIdempotently(t *testing.T) {
	handler := newProjectionTestHandler(t)
	ctx := context.Background()
	readyAt := metav1.NewTime(time.Date(2026, time.July, 28, 4, 0, 0, 0, time.UTC))
	firstPassedAt := metav1.NewTime(readyAt.Add(time.Minute))
	projection := environmentProjection{
		UID:     "environment-first-pass",
		Name:    "environment-first-pass",
		Runtime: "container",
		Spec: &breakfixv1.CommonEnvironmentSpec{
			UserRef: "u-demo", ChallengeRef: "cleanup-logs", ChallengeRevision: "revision-one",
		},
		Status: &breakfixv1.CommonEnvironmentStatus{
			Phase:   breakfixv1.EnvironmentReady,
			ReadyAt: &readyAt,
			Checkpoints: &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
				ID: "repair", Passed: true, FirstPassedAt: &firstPassedAt, Summary: "repair complete",
			}}},
		},
	}
	for range 2 {
		if deleteAfter, err := handler.projectEnvironmentRecord(ctx, projection); err != nil || deleteAfter {
			t.Fatalf("project = delete:%v err:%v", deleteAfter, err)
		}
	}

	events, err := handler.db.ListCheckpointFirstPasses(ctx, []string{projection.UID})
	if err != nil {
		t.Fatal(err)
	}
	if got := events[projection.UID]; len(got) != 1 || !got[0].FirstPassedAt.Equal(firstPassedAt.Time) || got[0].ChallengeRevision != "revision-one" {
		t.Fatalf("checkpoint events = %#v", got)
	}

	reset := projection
	reset.UID = "environment-first-pass-reset"
	if deleteAfter, err := handler.projectEnvironmentRecord(ctx, reset); err != nil || deleteAfter {
		t.Fatalf("reset projection = delete:%v err:%v", deleteAfter, err)
	}
	events, err = handler.db.ListCheckpointFirstPasses(ctx, []string{projection.UID, reset.UID})
	if err != nil {
		t.Fatal(err)
	}
	if len(events[projection.UID]) != 1 || len(events[reset.UID]) != 1 {
		t.Fatalf("reset first pass events = %#v", events)
	}
}

func TestEnvironmentStatusProjectionDoesNotPersistVerifyTaskEnvironment(t *testing.T) {
	handler := newProjectionTestHandler(t)
	ctx := context.Background()
	readyAt := metav1.NewTime(time.Date(2026, time.July, 28, 5, 0, 0, 0, time.UTC))
	firstPassedAt := metav1.NewTime(readyAt.Add(time.Minute))
	deleteAfter, err := handler.projectEnvironmentRecord(ctx, environmentProjection{
		UID: "verify-environment", Name: "verify-environment", Runtime: "container", Verification: true,
		Spec: &breakfixv1.CommonEnvironmentSpec{UserRef: "verify-task", ChallengeRef: "verify-task"},
		Status: &breakfixv1.CommonEnvironmentStatus{Phase: breakfixv1.EnvironmentReady, ReadyAt: &readyAt, Checkpoints: &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
			ID: "repair", Passed: true, FirstPassedAt: &firstPassedAt, Summary: "done",
		}}}},
	})
	if err != nil || deleteAfter {
		t.Fatalf("verify environment projection = delete:%v err:%v", deleteAfter, err)
	}
	if events, err := handler.db.ListCheckpointFirstPasses(ctx, []string{"verify-environment"}); err != nil || len(events["verify-environment"]) != 0 {
		t.Fatalf("verify environment wrote learning events: %#v, %v", events, err)
	}
}

func TestEnvironmentStatusProjectionRecordsVClusterCheckpointFirstPass(t *testing.T) {
	handler := newProjectionTestHandler(t)
	ctx := context.Background()
	readyAt := metav1.NewTime(time.Date(2026, time.July, 28, 6, 0, 0, 0, time.UTC))
	firstPassedAt := metav1.NewTime(readyAt.Add(time.Minute))
	projection := environmentProjection{
		UID: "vcluster-first-pass", Name: "vcluster-first-pass", Runtime: "vcluster",
		Spec: &breakfixv1.CommonEnvironmentSpec{UserRef: "u-demo", ChallengeRef: "vcluster-demo", ChallengeRevision: "revision-vcluster"},
		Status: &breakfixv1.CommonEnvironmentStatus{Phase: breakfixv1.EnvironmentReady, ReadyAt: &readyAt, Checkpoints: &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
			ID: "deployment-ready", Passed: true, FirstPassedAt: &firstPassedAt, Summary: "deployment is ready",
		}}}},
	}
	if deleteAfter, err := handler.projectEnvironmentRecord(ctx, projection); err != nil || deleteAfter {
		t.Fatalf("project vcluster = delete:%v err:%v", deleteAfter, err)
	}
	events, err := handler.db.ListCheckpointFirstPasses(ctx, []string{projection.UID})
	if err != nil {
		t.Fatal(err)
	}
	if got := events[projection.UID]; len(got) != 1 || got[0].CheckpointID != "deployment-ready" || !got[0].FirstPassedAt.Equal(firstPassedAt.Time) {
		t.Fatalf("vcluster checkpoint events = %#v", got)
	}
}

func TestEnvironmentStatusProjectionTerminalReconnectDoesNotDuplicateFirstPass(t *testing.T) {
	handler := newProjectionTestHandler(t)
	ctx := context.Background()
	readyAt := metav1.NewTime(time.Date(2026, time.July, 28, 7, 0, 0, 0, time.UTC))
	firstPassedAt := metav1.NewTime(readyAt.Add(time.Minute))
	projection := environmentProjection{
		UID: "reconnect-first-pass", Name: "reconnect-first-pass", Runtime: "container",
		Spec: &breakfixv1.CommonEnvironmentSpec{UserRef: "u-demo", ChallengeRef: "cleanup-logs", ChallengeRevision: "revision-reconnect"},
		Status: &breakfixv1.CommonEnvironmentStatus{Phase: breakfixv1.EnvironmentReady, ReadyAt: &readyAt, Checkpoints: &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
			ID: "repair", Passed: true, FirstPassedAt: &firstPassedAt, Summary: "repair complete",
		}}}},
	}
	if _, err := handler.projectEnvironmentRecord(ctx, projection); err != nil {
		t.Fatal(err)
	}
	firstConnection := db.TerminalConnection{ID: "terminal-first", EnvironmentUID: projection.UID, UserID: "u-demo", ChallengeID: "cleanup-logs", ServerInstanceID: "server-one", ConnectedAt: readyAt.Time}
	if err := handler.db.OpenTerminalConnection(ctx, firstConnection); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.db.CloseTerminalConnection(ctx, firstConnection.ID, readyAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := handler.db.OpenTerminalConnection(ctx, db.TerminalConnection{ID: "terminal-second", EnvironmentUID: projection.UID, UserID: "u-demo", ChallengeID: "cleanup-logs", ServerInstanceID: "server-two", ConnectedAt: readyAt.Add(3 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.projectEnvironmentRecord(ctx, projection); err != nil {
		t.Fatal(err)
	}
	events, err := handler.db.ListCheckpointFirstPasses(ctx, []string{projection.UID})
	if err != nil {
		t.Fatal(err)
	}
	if got := events[projection.UID]; len(got) != 1 || !got[0].FirstPassedAt.Equal(firstPassedAt.Time) {
		t.Fatalf("reconnect checkpoint events = %#v", got)
	}
}
