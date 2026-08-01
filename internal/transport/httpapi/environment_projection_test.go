package httpapi

import (
	"context"
	"testing"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	testpostgres "github.com/breakfix/breakfix/internal/testkit/postgres"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newProjectionTestHandler(t *testing.T) *Handler {
	t.Helper()
	return &Handler{db: testpostgres.New(t)}
}

func learningProjection(uid, runtime string, readyAt time.Time) environmentProjection {
	ready := metav1.NewTime(readyAt)
	return environmentProjection{
		UID: uid, Name: uid, Runtime: runtime,
		Spec: &breakfixv1.EnvironmentSpec{
			Purpose: breakfixv1.EnvironmentPurposeLearning,
			Source:  breakfixv1.EnvironmentSourceSpec{Kind: breakfixv1.EnvironmentSourcePublished, Ref: "chal-r7m4x2q9v6kp", Revision: "sha256:revision"},
			UserRef: "u-demo",
		},
		Status: &breakfixv1.EnvironmentStatus{Phase: breakfixv1.EnvironmentReady, ReadyAt: &ready},
	}
}

func TestEnvironmentStatusProjectionRecordsReadyAndCompletionIdempotently(t *testing.T) {
	handler := newProjectionTestHandler(t)
	ctx := context.Background()
	readyAt := time.Date(2026, time.July, 25, 1, 0, 0, 0, time.UTC)
	projection := learningProjection("environment-uid", "node", readyAt)
	for range 2 {
		if deleteAfter, err := handler.projectEnvironmentRecord(ctx, projection); err != nil || deleteAfter {
			t.Fatalf("ready projection = delete:%v err:%v", deleteAfter, err)
		}
	}
	completed := metav1.NewTime(readyAt.Add(2 * time.Minute))
	projection.Status.Phase = breakfixv1.EnvironmentCompleted
	projection.Status.CompletedAt = &completed
	for range 2 {
		if deleteAfter, err := handler.projectEnvironmentRecord(ctx, projection); err != nil || deleteAfter {
			t.Fatalf("completed projection = delete:%v err:%v", deleteAfter, err)
		}
	}
	summary, err := handler.db.Environment.LearningSummary(ctx, "u-demo", completed.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if summary.AttemptedCount != 1 || summary.CompletedCount != 1 {
		t.Fatalf("learning summary = %#v", summary)
	}
}

func TestEnvironmentStatusProjectionFinishesDestroyedAttempt(t *testing.T) {
	handler := newProjectionTestHandler(t)
	ctx := context.Background()
	readyAt := time.Date(2026, time.July, 25, 2, 0, 0, 0, time.UTC)
	projection := learningProjection("environment-destroyed", "k8s", readyAt)
	if _, err := handler.projectEnvironmentRecord(ctx, projection); err != nil {
		t.Fatal(err)
	}
	destroyed := metav1.NewTime(readyAt.Add(5 * time.Minute))
	projection.Status.Phase = breakfixv1.EnvironmentDestroyed
	projection.Status.DestroyedAt = &destroyed
	deleteAfter, err := handler.projectEnvironmentRecord(ctx, projection)
	if err != nil || !deleteAfter {
		t.Fatalf("destroyed projection = delete:%v err:%v", deleteAfter, err)
	}
	history, err := handler.db.Environment.ListLearningHistory(ctx, "u-demo", postgres.LearningHistoryFilter{ChallengeIDs: []string{"chal-r7m4x2q9v6kp"}}, 10, nil, destroyed.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Outcome != postgres.AttemptExpired {
		t.Fatalf("learning history = %#v", history)
	}
}

func TestEnvironmentStatusProjectionRecordsCheckpointFirstPassOnce(t *testing.T) {
	handler := newProjectionTestHandler(t)
	ctx := context.Background()
	readyAt := time.Date(2026, time.July, 28, 4, 0, 0, 0, time.UTC)
	passedAt := metav1.NewTime(readyAt.Add(time.Minute))
	projection := learningProjection("environment-checkpoint", "node", readyAt)
	projection.Status.Checkpoints = &breakfixv1.CheckpointStatus{Results: []breakfixv1.CheckpointResultStatus{{
		ID: "proxy-ready", Passed: true, FirstPassedAt: &passedAt, Summary: "proxy is ready",
	}}}
	for range 2 {
		if _, err := handler.projectEnvironmentRecord(ctx, projection); err != nil {
			t.Fatal(err)
		}
	}
	events, err := handler.db.Environment.ListCheckpointFirstPasses(ctx, []string{projection.UID})
	if err != nil {
		t.Fatal(err)
	}
	if got := events[projection.UID]; len(got) != 1 || got[0].ChallengeRevision != "sha256:revision" || got[0].CheckpointID != "proxy-ready" {
		t.Fatalf("checkpoint events = %#v", got)
	}
}

func TestVerificationEnvironmentIsNotProjectedIntoLearningHistory(t *testing.T) {
	handler := newProjectionTestHandler(t)
	ctx := context.Background()
	readyAt := time.Date(2026, time.July, 28, 5, 0, 0, 0, time.UTC)
	projection := learningProjection("verification-environment", "node", readyAt)
	projection.Spec.Purpose = breakfixv1.EnvironmentPurposeVerification
	projection.Spec.Source.Kind = breakfixv1.EnvironmentSourceCandidate
	if deleteAfter, err := handler.projectEnvironmentRecord(ctx, projection); err != nil || deleteAfter {
		t.Fatalf("verification projection = delete:%v err:%v", deleteAfter, err)
	}
	summary, err := handler.db.Environment.LearningSummary(ctx, "u-demo", readyAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if summary.AttemptedCount != 0 || summary.CompletedCount != 0 {
		t.Fatalf("verification environment wrote learning facts: %#v", summary)
	}
}
