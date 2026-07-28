package db

import (
	"context"
	"testing"
	"time"
)

func TestCheckpointFirstPassEventsAreImmutablePerEnvironment(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	first := time.Date(2026, time.July, 28, 3, 4, 5, 0, time.UTC)
	event := CheckpointFirstPassEvent{
		EnvironmentUID:    "environment-one",
		UserID:            "user-one",
		ChallengeID:       "challenge-one",
		ChallengeRevision: "revision-one",
		CheckpointID:      "repair",
		FirstPassedAt:     first,
		Summary:           "repair is ready",
	}
	if err := database.RecordCheckpointFirstPass(ctx, event); err != nil {
		t.Fatal(err)
	}
	event.FirstPassedAt = event.FirstPassedAt.Add(time.Hour)
	event.Summary = "later summary must not replace the learning fact"
	if err := database.RecordCheckpointFirstPass(ctx, event); err != nil {
		t.Fatal(err)
	}

	event.EnvironmentUID = "environment-two"
	if err := database.RecordCheckpointFirstPass(ctx, event); err != nil {
		t.Fatal(err)
	}

	events, err := database.ListCheckpointFirstPasses(ctx, []string{"environment-one", "environment-two"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events["environment-one"]) != 1 || !events["environment-one"][0].FirstPassedAt.Equal(first) || events["environment-one"][0].Summary != "repair is ready" {
		t.Fatalf("environment one events = %#v", events["environment-one"])
	}
	if len(events["environment-two"]) != 1 || !events["environment-two"][0].FirstPassedAt.Equal(first.Add(time.Hour)) {
		t.Fatalf("environment two events = %#v", events["environment-two"])
	}
}
