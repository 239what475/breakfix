package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestLearningMigrationBackfillsHistoricalCompletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breakfix.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
		CREATE TABLE user_challenge_progress (
			user_id TEXT NOT NULL,
			challenge_id TEXT NOT NULL,
			completed_at TEXT NOT NULL,
			environment_uid TEXT NOT NULL,
			PRIMARY KEY (user_id, challenge_id)
		);
		INSERT INTO user_challenge_progress VALUES ('u-one', 'challenge-one', '2026-07-24T01:02:03Z', 'environment-one');
		PRAGMA user_version = 9;
	`)
	if err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	var userID, challengeID, runtime, readyAt, endedAt, outcome string
	if err := database.conn.QueryRow(`
		SELECT user_id, challenge_id, runtime, ready_at, ended_at, outcome
		FROM user_challenge_attempts WHERE environment_uid = 'environment-one'
	`).Scan(&userID, &challengeID, &runtime, &readyAt, &endedAt, &outcome); err != nil {
		t.Fatal(err)
	}
	if userID != "u-one" || challengeID != "challenge-one" || runtime != "" || outcome != AttemptCompleted || readyAt != endedAt {
		t.Fatalf("unexpected backfilled attempt: user=%q challenge=%q runtime=%q ready=%q ended=%q outcome=%q", userID, challengeID, runtime, readyAt, endedAt, outcome)
	}
	columns, err := database.conn.Query(`PRAGMA table_info(user_challenge_attempts)`)
	if err != nil {
		t.Fatal(err)
	}
	defer columns.Close()
	for columns.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := columns.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "learning_seconds" {
			t.Fatal("attempt table retained duplicate learning_seconds column after v11")
		}
	}
	if err := columns.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestLearningMigrationRemovesLegacyChallengeCatalogTable(t *testing.T) {
	database := newLearningTestDB(t)
	var count int
	if err := database.conn.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'challenges'
	`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("legacy challenge catalog table still exists")
	}
}

func TestLearningMigrationAcceptsDevelopmentV10WithoutDuplicateDuration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breakfix.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
		CREATE TABLE user_challenge_attempts (
			environment_uid TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			challenge_id TEXT NOT NULL,
			runtime TEXT NOT NULL DEFAULT '',
			ready_at TEXT NOT NULL,
			ended_at TEXT NOT NULL DEFAULT '',
			outcome TEXT NOT NULL DEFAULT 'active'
		);
		CREATE TABLE terminal_connections (
			id                  TEXT PRIMARY KEY,
			environment_uid     TEXT NOT NULL,
			user_id             TEXT NOT NULL,
			challenge_id        TEXT NOT NULL,
			gateway_instance_id TEXT NOT NULL,
			connected_at        TEXT NOT NULL,
			heartbeat_at        TEXT NOT NULL,
			disconnected_at     TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO user_challenge_attempts
			(environment_uid, user_id, challenge_id, runtime, ready_at, ended_at, outcome)
		VALUES ('environment-one', 'u-one', 'challenge-one', 'container', '2026-07-24T01:02:03Z', '', 'active');
		PRAGMA user_version = 10;
	`)
	if err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	var userID, challengeID string
	if err := database.conn.QueryRow(`
		SELECT user_id, challenge_id FROM user_challenge_attempts WHERE environment_uid = 'environment-one'
	`).Scan(&userID, &challengeID); err != nil {
		t.Fatal(err)
	}
	if userID != "u-one" || challengeID != "challenge-one" {
		t.Fatalf("migrated attempt = (%q, %q)", userID, challengeID)
	}
}

func TestLearningMigrationRenamesGatewayConnectionOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "breakfix.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
		CREATE TABLE terminal_connections (
			id                  TEXT PRIMARY KEY,
			environment_uid     TEXT NOT NULL,
			user_id             TEXT NOT NULL,
			challenge_id        TEXT NOT NULL,
			gateway_instance_id TEXT NOT NULL,
			connected_at        TEXT NOT NULL,
			heartbeat_at        TEXT NOT NULL,
			disconnected_at     TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO terminal_connections
			(id, environment_uid, user_id, challenge_id, gateway_instance_id, connected_at, heartbeat_at)
		VALUES ('connection-one', 'environment-one', 'user-one', 'challenge-one', 'server-before-rename', '2026-07-25T00:00:00Z', '2026-07-25T00:00:00Z');
		PRAGMA user_version = 13;
	`)
	if err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	var owner string
	if err := database.conn.QueryRow(`SELECT server_instance_id FROM terminal_connections WHERE id = 'connection-one'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != "server-before-rename" {
		t.Fatalf("migrated server instance = %q", owner)
	}
}

func TestChallengeAttemptCompletionNeverOverwritesTerminalOutcome(t *testing.T) {
	database := newLearningTestDB(t)
	ctx := context.Background()
	readyAt := time.Date(2026, time.July, 24, 1, 0, 0, 0, time.UTC)
	if err := database.RecordChallengeAttempt(ctx, "u-one", "challenge-one", "environment-one", "container", readyAt); err != nil {
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
	if err := database.RecordChallengeAttempt(ctx, "u-one", "challenge-one", "environment-one", "container", started); err != nil {
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
	if err := database.RecordChallengeAttempt(ctx, "u-one", "challenge-one", "environment-one", "container", readyAt); err != nil {
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
	if err := database.RecordChallengeAttempt(ctx, "u-one", "challenge-one", "environment-first", "container", firstReadyAt); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordChallengeCompletion(ctx, "u-one", "challenge-one", "environment-first", firstReadyAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	secondReadyAt := firstReadyAt.Add(2 * time.Hour)
	if err := database.RecordChallengeAttempt(ctx, "u-one", "challenge-one", "environment-second", "container", secondReadyAt); err != nil {
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
		{"environment-c", "challenge-container", "container"},
		{"environment-b", "challenge-vcluster", "vcluster"},
		{"environment-a", "challenge-ended", "container"},
	} {
		if err := database.RecordChallengeAttempt(ctx, "u-one", attempt.challengeID, attempt.environmentUID, attempt.runtime, readyAt); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.FinishChallengeAttempt(ctx, "environment-a", AttemptStopped, readyAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	filter := LearningHistoryFilter{ChallengeIDs: []string{"challenge-container", "challenge-vcluster", "challenge-ended"}}
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
	vcluster, err := database.ListLearningHistory(ctx, "u-one", LearningHistoryFilter{ChallengeIDs: filter.ChallengeIDs, Runtime: "vcluster"}, 10, nil, readyAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(vcluster) != 1 || vcluster[0].EnvironmentUID != "environment-b" {
		t.Fatalf("vcluster attempts = %#v", vcluster)
	}
}

func newLearningTestDB(t *testing.T) *DB {
	t.Helper()
	database, err := New(filepath.Join(t.TempDir(), "breakfix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}
