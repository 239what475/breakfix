package postgres

import (
	"context"
	"testing"
	"time"
)

func TestScenarioCompletionPersistsFirstCompletion(t *testing.T) {
	database := newTestDB(t)

	first := time.Date(2026, time.July, 24, 9, 30, 0, 0, time.UTC)
	if err := database.Environment.RecordScenarioCompletion(context.Background(), "user-a", "scenario-a", "chrev-aaaaaaaaaaaaaaaa", "environment-a", first); err != nil {
		t.Fatal(err)
	}
	if err := database.Environment.RecordScenarioCompletion(context.Background(), "user-a", "scenario-a", "chrev-aaaaaaaaaaaaaaaa", "environment-b", first.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	completed, err := database.Environment.ListCompletedScenarioIDs(context.Background(), "user-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := completed["scenario-a"]; !ok || len(completed) != 1 {
		t.Fatalf("completed scenarios = %#v", completed)
	}

	var completedAt, environmentUID string
	if err := database.conn.QueryRow(`
		SELECT completed_at, environment_uid
		FROM user_scenario_progress
		WHERE user_id = ? AND scenario_id = ?
	`, "user-a", "scenario-a").Scan(&completedAt, &environmentUID); err != nil {
		t.Fatal(err)
	}
	if completedAt != first.Format(time.RFC3339Nano) || environmentUID != "environment-a" {
		t.Fatalf("stored completion = (%q, %q)", completedAt, environmentUID)
	}
}
