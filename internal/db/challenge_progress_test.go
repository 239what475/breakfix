package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestChallengeCompletionPersistsFirstCompletion(t *testing.T) {
	database, err := New(filepath.Join(t.TempDir(), "breakfix.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	first := time.Date(2026, time.July, 24, 9, 30, 0, 0, time.UTC)
	if err := database.RecordChallengeCompletion(context.Background(), "user-a", "challenge-a", "environment-a", first); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordChallengeCompletion(context.Background(), "user-a", "challenge-a", "environment-b", first.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	completed, err := database.ListCompletedChallengeIDs(context.Background(), "user-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := completed["challenge-a"]; !ok || len(completed) != 1 {
		t.Fatalf("completed challenges = %#v", completed)
	}

	var completedAt, environmentUID string
	if err := database.conn.QueryRow(`
		SELECT completed_at, environment_uid
		FROM user_challenge_progress
		WHERE user_id = ? AND challenge_id = ?
	`, "user-a", "challenge-a").Scan(&completedAt, &environmentUID); err != nil {
		t.Fatal(err)
	}
	if completedAt != first.Format(time.RFC3339Nano) || environmentUID != "environment-a" {
		t.Fatalf("stored completion = (%q, %q)", completedAt, environmentUID)
	}
}
