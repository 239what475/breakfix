package db

import (
	"context"
	"testing"
	"time"
)

func TestChallengeAttemptCompletionNeverOverwritesTerminalOutcome(t *testing.T) {
	database := newLearningTestDB(t)
	ctx := context.Background()
	readyAt := time.Date(2026, time.July, 24, 1, 0, 0, 0, time.UTC)
	if err := database.RecordChallengeAttempt(ctx, "u-one", "challenge-one", "environment-one", "node", readyAt); err != nil {
		t.Fatal(err)
	}
	if err := database.FinishChallengeAttempt(ctx, "environment-one", AttemptStopped, readyAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordChallengeCompletion(ctx, "u-one", "challenge-one", "environment-one", readyAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	var outcome string
	if err := database.conn.QueryRow(`SELECT outcome FROM user_challenge_attempts WHERE environment_uid = ?`, "environment-one").Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != AttemptStopped {
		t.Fatalf("outcome = %q, want %q", outcome, AttemptStopped)
	}
}

func TestTerminalUsageSessionDeduplicatesConnections(t *testing.T) {
	database := newLearningTestDB(t)
	ctx := context.Background()
	started := time.Date(2026, time.July, 24, 2, 0, 0, 0, time.UTC)
	first := TerminalConnection{ID: "connection-one", EnvironmentUID: "environment-one", UserID: "u-one", ChallengeID: "challenge-one", ServerInstanceID: "server-a", ConnectedAt: started}
	second := TerminalConnection{ID: "connection-two", EnvironmentUID: "environment-one", UserID: "u-one", ChallengeID: "challenge-one", ServerInstanceID: "server-b", ConnectedAt: started.Add(5 * time.Second)}
	if err := database.OpenTerminalConnection(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := database.OpenTerminalConnection(ctx, second); err != nil {
		t.Fatal(err)
	}

	var sessions int
	if err := database.conn.QueryRow(`SELECT COUNT(*) FROM environment_usage_sessions WHERE environment_uid = ?`, first.EnvironmentUID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 {
		t.Fatalf("usage sessions = %d, want 1", sessions)
	}
	last, err := database.CloseTerminalConnection(ctx, first.ID, started.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if last {
		t.Fatal("first close reported last active connection")
	}
	var endedAt string
	if err := database.conn.QueryRow(`SELECT ended_at FROM environment_usage_sessions WHERE environment_uid = ?`, first.EnvironmentUID).Scan(&endedAt); err != nil {
		t.Fatal(err)
	}
	if endedAt != "" {
		t.Fatalf("usage session ended after first close: %q", endedAt)
	}
	last, err = database.CloseTerminalConnection(ctx, second.ID, started.Add(20*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !last {
		t.Fatal("second close did not report last active connection")
	}
	if err := database.conn.QueryRow(`SELECT ended_at FROM environment_usage_sessions WHERE environment_uid = ?`, first.EnvironmentUID).Scan(&endedAt); err != nil {
		t.Fatal(err)
	}
	if endedAt != "" {
		t.Fatalf("usage session ended before settle delay: %q", endedAt)
	}
	closed, err := database.FinishTerminalUsageSession(ctx, first.EnvironmentUID, started.Add(25*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("usage session was not closed after the final settled connection")
	}
	if err := database.conn.QueryRow(`SELECT ended_at FROM environment_usage_sessions WHERE environment_uid = ?`, first.EnvironmentUID).Scan(&endedAt); err != nil {
		t.Fatal(err)
	}
	if endedAt == "" {
		t.Fatal("usage session remained active after settle delay")
	}
}

func TestCleanupTerminalActivityClosesStaleConnectionAndSession(t *testing.T) {
	database := newLearningTestDB(t)
	ctx := context.Background()
	started := time.Date(2026, time.July, 24, 3, 0, 0, 0, time.UTC)
	if err := database.OpenTerminalConnection(ctx, TerminalConnection{ID: "connection-one", EnvironmentUID: "environment-one", UserID: "u-one", ChallengeID: "challenge-one", ServerInstanceID: "server-a", ConnectedAt: started}); err != nil {
		t.Fatal(err)
	}
	if err := database.CleanupTerminalActivity(ctx, started.Add(time.Minute), started.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var active int
	if err := database.conn.QueryRow(`SELECT COUNT(*) FROM terminal_connections WHERE disconnected_at = ''`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("active connections = %d, want 0", active)
	}
	if err := database.conn.QueryRow(`SELECT COUNT(*) FROM environment_usage_sessions WHERE ended_at = ''`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("active sessions = %d, want 0", active)
	}
	if err := database.DeleteClosedTerminalConnections(ctx, started.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := database.conn.QueryRow(`SELECT COUNT(*) FROM terminal_connections`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("connections after retention cleanup = %d, want 0", active)
	}
}

func TestLearningSummaryAndHistoryUseUsageSessions(t *testing.T) {
	database := newLearningTestDB(t)
	ctx := context.Background()
	started := time.Date(2026, time.July, 24, 4, 0, 0, 0, time.UTC)
	if err := database.RecordChallengeAttempt(ctx, "u-one", "challenge-one", "environment-one", "node", started); err != nil {
		t.Fatal(err)
	}
	if err := database.OpenTerminalConnection(ctx, TerminalConnection{ID: "connection-one", EnvironmentUID: "environment-one", UserID: "u-one", ChallengeID: "challenge-one", ServerInstanceID: "server-a", ConnectedAt: started}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CloseTerminalConnection(ctx, "connection-one", started.Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.FinishTerminalUsageSession(ctx, "environment-one", started.Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordChallengeCompletion(ctx, "u-one", "challenge-one", "environment-one", started.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	summary, err := database.LearningSummary(ctx, "u-one", started.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if summary.CompletedCount != 1 || summary.AttemptedCount != 1 || summary.TerminalLearningSecond != 90 {
		t.Fatalf("summary = %#v", summary)
	}
	history, err := database.ListLearningHistory(ctx, "u-one", LearningHistoryFilter{ChallengeIDs: []string{"challenge-one"}}, 10, nil, started.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].LearningSeconds != 90 || history[0].CompletedAt == nil || history[0].Outcome != AttemptCompleted {
		t.Fatalf("history = %#v", history)
	}
}

func TestLearningHistoryIncludesUncompletedAttempt(t *testing.T) {
	database := newLearningTestDB(t)
	ctx := context.Background()
	readyAt := time.Date(2026, time.July, 24, 5, 0, 0, 0, time.UTC)
	if err := database.RecordChallengeAttempt(ctx, "u-one", "challenge-one", "environment-one", "node", readyAt); err != nil {
		t.Fatal(err)
	}
	history, err := database.ListLearningHistory(ctx, "u-one", LearningHistoryFilter{ChallengeIDs: []string{"challenge-one"}}, 10, nil, readyAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].CompletedAt != nil || history[0].Outcome != AttemptActive {
		t.Fatalf("uncompleted history = %#v", history)
	}
}

func TestLearningHistoryPreservesAttemptOutcomeAfterEarlierCompletion(t *testing.T) {
	database := newLearningTestDB(t)
	ctx := context.Background()
	firstReadyAt := time.Date(2026, time.July, 24, 5, 0, 0, 0, time.UTC)
	if err := database.RecordChallengeAttempt(ctx, "u-one", "challenge-one", "environment-first", "node", firstReadyAt); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordChallengeCompletion(ctx, "u-one", "challenge-one", "environment-first", firstReadyAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	secondReadyAt := firstReadyAt.Add(2 * time.Hour)
	if err := database.RecordChallengeAttempt(ctx, "u-one", "challenge-one", "environment-second", "node", secondReadyAt); err != nil {
		t.Fatal(err)
	}
	if err := database.FinishChallengeAttempt(ctx, "environment-second", AttemptStopped, secondReadyAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	history, err := database.ListLearningHistory(ctx, "u-one", LearningHistoryFilter{ChallengeIDs: []string{"challenge-one"}}, 10, nil, secondReadyAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].EnvironmentUID != "environment-second" || history[0].Outcome != AttemptStopped || history[0].CompletedAt != nil {
		t.Fatalf("repeated challenge history = %#v", history)
	}
	if history[1].EnvironmentUID != "environment-first" || history[1].Outcome != AttemptCompleted || history[1].CompletedAt == nil {
		t.Fatalf("completed attempt history = %#v", history)
	}
	if !history[1].CompletedAt.Equal(firstReadyAt.Add(time.Minute)) {
		t.Fatalf("completion time = %s, want %s", history[1].CompletedAt, firstReadyAt.Add(time.Minute))
	}
}

func TestLearningHistoryFiltersAndPaginatesSameTimestampAttempts(t *testing.T) {
	database := newLearningTestDB(t)
	ctx := context.Background()
	readyAt := time.Date(2026, time.July, 24, 6, 0, 0, 0, time.UTC)
	for _, attempt := range []struct {
		environmentUID string
		challengeID    string
		runtime        string
	}{
		{"environment-c", "challenge-node", "node"},
		{"environment-b", "challenge-k8s", "k8s"},
		{"environment-a", "challenge-ended", "node"},
	} {
		if err := database.RecordChallengeAttempt(ctx, "u-one", attempt.challengeID, attempt.environmentUID, attempt.runtime, readyAt); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.FinishChallengeAttempt(ctx, "environment-a", AttemptStopped, readyAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	filter := LearningHistoryFilter{ChallengeIDs: []string{"challenge-node", "challenge-k8s", "challenge-ended"}}
	first, err := database.ListLearningHistory(ctx, "u-one", filter, 2, nil, readyAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].EnvironmentUID != "environment-c" || first[1].EnvironmentUID != "environment-b" {
		t.Fatalf("first page = %#v", first)
	}
	second, err := database.ListLearningHistory(ctx, "u-one", filter, 2, &LearningHistoryCursor{ReadyAt: first[1].ReadyAt, EnvironmentUID: first[1].EnvironmentUID}, readyAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].EnvironmentUID != "environment-a" {
		t.Fatalf("second page = %#v", second)
	}

	ended, err := database.ListLearningHistory(ctx, "u-one", LearningHistoryFilter{ChallengeIDs: filter.ChallengeIDs, State: "ended"}, 10, nil, readyAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(ended) != 1 || ended[0].EnvironmentUID != "environment-a" {
		t.Fatalf("ended attempts = %#v", ended)
	}
	k8s, err := database.ListLearningHistory(ctx, "u-one", LearningHistoryFilter{ChallengeIDs: filter.ChallengeIDs, Runtime: "k8s"}, 10, nil, readyAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(k8s) != 1 || k8s[0].EnvironmentUID != "environment-b" {
		t.Fatalf("k8s attempts = %#v", k8s)
	}
}

func newLearningTestDB(t *testing.T) *DB {
	t.Helper()
	return newTestDB(t)
}
