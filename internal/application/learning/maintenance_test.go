package learning

import (
	"context"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/postgres"
	testpostgres "github.com/breakfix/breakfix/internal/testkit/postgres"
)

type projectionTestStore struct {
	repository *postgres.EnvironmentRepository
}

func (s projectionTestStore) RecordScenarioAttempt(ctx context.Context, userID, scenarioID, revisionID, environmentUID, runtimeName string, startedAt time.Time) error {
	return s.repository.RecordScenarioAttempt(ctx, userID, scenarioID, revisionID, environmentUID, runtimeName, startedAt)
}

func (s projectionTestStore) RecordCheckpointFirstPass(ctx context.Context, event CheckpointFirstPass) error {
	return s.repository.RecordCheckpointFirstPass(ctx, postgres.CheckpointFirstPassEvent{
		EnvironmentUID: event.EnvironmentUID, UserID: event.UserID, ScenarioID: event.ScenarioID,
		ScenarioRevision: event.ScenarioRevisionID, CheckpointID: event.CheckpointID,
		FirstPassedAt: event.FirstPassedAt, Summary: event.Summary,
	})
}

func (s projectionTestStore) ListCheckpointFirstPasses(ctx context.Context, environmentUID string) ([]CheckpointFirstPass, error) {
	events, err := s.repository.ListCheckpointFirstPasses(ctx, []string{environmentUID})
	if err != nil {
		return nil, err
	}
	result := make([]CheckpointFirstPass, 0, len(events[environmentUID]))
	for _, event := range events[environmentUID] {
		result = append(result, CheckpointFirstPass{EnvironmentUID: event.EnvironmentUID, UserID: event.UserID, ScenarioID: event.ScenarioID, ScenarioRevisionID: event.ScenarioRevision, CheckpointID: event.CheckpointID, FirstPassedAt: event.FirstPassedAt, Summary: event.Summary})
	}
	return result, nil
}

func (s projectionTestStore) RecordScenarioCompletion(ctx context.Context, userID, scenarioID, revisionID, environmentUID string, completedAt time.Time) error {
	return s.repository.RecordScenarioCompletion(ctx, userID, scenarioID, revisionID, environmentUID, completedAt)
}

func (s projectionTestStore) FinishScenarioAttempt(ctx context.Context, environmentUID, outcome string, finishedAt time.Time) error {
	return s.repository.FinishScenarioAttempt(ctx, environmentUID, outcome, finishedAt)
}

func newProjectionTestService(t *testing.T) (*ProjectionService, *postgres.Store) {
	t.Helper()
	database := testpostgres.New(t)
	service, err := NewProjectionService(projectionTestSource{}, projectionTestStore{repository: database.Environment})
	if err != nil {
		t.Fatal(err)
	}
	return service, database
}

type projectionTestSource struct{}

func (projectionTestSource) ListEnvironmentProjections(context.Context) ([]EnvironmentProjection, error) {
	return nil, nil
}
func (projectionTestSource) DeleteEnvironmentProjection(context.Context, string, string, string) error {
	return nil
}

func learningProjection(uid, runtimeName string, readyAt time.Time) EnvironmentProjection {
	return EnvironmentProjection{
		UID: uid, Name: uid, Runtime: runtimeName, Purpose: purposeLearning,
		UserID: "u-demo", ScenarioID: "chal-r7m4x2q9v6kp", ScenarioRevision: "chrev-aaaaaaaaaaaaaaaa",
		Phase: "Ready", ReadyAt: &readyAt,
	}
}

func TestProjectionRecordsReadyAndCompletionIdempotently(t *testing.T) {
	service, database := newProjectionTestService(t)
	ctx := context.Background()
	readyAt := time.Date(2026, time.July, 25, 1, 0, 0, 0, time.UTC)
	projection := learningProjection("environment-uid", "node", readyAt)
	for range 2 {
		if deleteAfter, err := service.Project(ctx, projection); err != nil || deleteAfter {
			t.Fatalf("ready projection = delete:%v err:%v", deleteAfter, err)
		}
	}
	completed := readyAt.Add(2 * time.Minute)
	projection.Phase = phaseCompleted
	projection.CompletedAt = &completed
	for range 2 {
		if deleteAfter, err := service.Project(ctx, projection); err != nil || deleteAfter {
			t.Fatalf("completed projection = delete:%v err:%v", deleteAfter, err)
		}
	}
	summary, err := database.Environment.LearningSummary(ctx, "u-demo", completed.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if summary.AttemptedCount != 1 || summary.CompletedCount != 1 {
		t.Fatalf("learning summary = %#v", summary)
	}
}

func TestProjectionFinishesDestroyedAttempt(t *testing.T) {
	service, database := newProjectionTestService(t)
	ctx := context.Background()
	readyAt := time.Date(2026, time.July, 25, 2, 0, 0, 0, time.UTC)
	projection := learningProjection("environment-destroyed", "k8s", readyAt)
	if _, err := service.Project(ctx, projection); err != nil {
		t.Fatal(err)
	}
	destroyed := readyAt.Add(5 * time.Minute)
	projection.Phase = phaseDestroyed
	projection.DestroyedAt = &destroyed
	deleteAfter, err := service.Project(ctx, projection)
	if err != nil || !deleteAfter {
		t.Fatalf("destroyed projection = delete:%v err:%v", deleteAfter, err)
	}
	history, err := database.Environment.ListLearningHistory(ctx, "u-demo", postgres.LearningHistoryFilter{ScenarioIDs: []string{"chal-r7m4x2q9v6kp"}}, 10, nil, destroyed.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Outcome != postgres.AttemptExpired {
		t.Fatalf("learning history = %#v", history)
	}
}

func TestProjectionRecordsCheckpointFirstPassOnce(t *testing.T) {
	service, database := newProjectionTestService(t)
	ctx := context.Background()
	readyAt := time.Date(2026, time.July, 28, 4, 0, 0, 0, time.UTC)
	passedAt := readyAt.Add(time.Minute)
	projection := learningProjection("environment-checkpoint", "node", readyAt)
	projection.Checkpoints = []Checkpoint{{ID: "proxy-ready", FirstPassedAt: &passedAt, Summary: "proxy is ready"}}
	for range 2 {
		if _, err := service.Project(ctx, projection); err != nil {
			t.Fatal(err)
		}
	}
	events, err := database.Environment.ListCheckpointFirstPasses(ctx, []string{projection.UID})
	if err != nil {
		t.Fatal(err)
	}
	if got := events[projection.UID]; len(got) != 1 || got[0].ScenarioRevision != "chrev-aaaaaaaaaaaaaaaa" || got[0].CheckpointID != "proxy-ready" {
		t.Fatalf("checkpoint events = %#v", got)
	}
}

func TestProjectionCompletesOnlyWhenEveryCheckpointPasses(t *testing.T) {
	service, database := newProjectionTestService(t)
	ctx := context.Background()
	readyAt := time.Date(2026, time.July, 28, 6, 0, 0, 0, time.UTC)
	passedAt := readyAt.Add(time.Minute)
	projection := learningProjection("environment-checkpoint-completion", "node", readyAt)
	projection.Checkpoints = []Checkpoint{
		{ID: "first", Passed: true, FirstPassedAt: &passedAt, Summary: "first passed"},
		{ID: "second", Passed: false, Summary: "second pending"},
	}
	if _, err := service.Project(ctx, projection); err != nil {
		t.Fatal(err)
	}
	summary, err := database.Environment.LearningSummary(ctx, "u-demo", passedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if summary.CompletedCount != 0 {
		t.Fatalf("partial checkpoint result completed scenario: %#v", summary)
	}
	projection.Checkpoints[0].Passed = false
	projection.Checkpoints[0].FirstPassedAt = nil
	projection.Checkpoints[1].Passed = true
	projection.Checkpoints[1].FirstPassedAt = &passedAt
	if _, err := service.Project(ctx, projection); err != nil {
		t.Fatal(err)
	}
	summary, err = database.Environment.LearningSummary(ctx, "u-demo", passedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if summary.CompletedCount != 1 {
		t.Fatalf("completed checkpoint result was not recorded: %#v", summary)
	}
}

func TestVerificationEnvironmentIsNotProjected(t *testing.T) {
	service, database := newProjectionTestService(t)
	ctx := context.Background()
	readyAt := time.Date(2026, time.July, 28, 5, 0, 0, 0, time.UTC)
	projection := learningProjection("verification-environment", "node", readyAt)
	projection.Purpose = "verification"
	if deleteAfter, err := service.Project(ctx, projection); err != nil || deleteAfter {
		t.Fatalf("verification projection = delete:%v err:%v", deleteAfter, err)
	}
	summary, err := database.Environment.LearningSummary(ctx, "u-demo", readyAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if summary.AttemptedCount != 0 || summary.CompletedCount != 0 {
		t.Fatalf("verification environment wrote learning facts: %#v", summary)
	}
}
