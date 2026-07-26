package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	AttemptActive    = "active"
	AttemptCompleted = "completed"
	AttemptStopped   = "stopped"
	AttemptReset     = "reset"
	AttemptExpired   = "expired"
)

type ChallengeAttempt struct {
	EnvironmentUID  string
	UserID          string
	ChallengeID     string
	Runtime         string
	ReadyAt         time.Time
	EndedAt         *time.Time
	Outcome         string
	LearningSeconds int64
}

type TerminalConnection struct {
	ID               string
	EnvironmentUID   string
	UserID           string
	ChallengeID      string
	ServerInstanceID string
	ConnectedAt      time.Time
}

type EnvironmentUsageSession struct {
	ID             string
	EnvironmentUID string
	UserID         string
	ChallengeID    string
	StartedAt      time.Time
	HeartbeatAt    time.Time
	EndedAt        *time.Time
}

// LearningHistoryItem is a durable user-facing attempt. Learning time is
// derived from environment usage sessions rather than duplicated on attempts.
type LearningHistoryItem struct {
	EnvironmentUID  string
	ChallengeID     string
	Runtime         string
	ReadyAt         time.Time
	CompletedAt     *time.Time
	EndedAt         *time.Time
	Outcome         string
	LearningSeconds int64
	LastActivityAt  time.Time
}

// LearningHistoryCursor identifies an attempt in the same order used by the
// history query. It remains internal to the DB; Server turns it into an
// opaque API token.
type LearningHistoryCursor struct {
	ReadyAt        time.Time
	EnvironmentUID string
}

// LearningHistoryFilter limits history to challenge directories that are
// still present in the catalog and optionally to a user-facing state/runtime.
type LearningHistoryFilter struct {
	ChallengeIDs []string
	State        string
	Runtime      string
}

type LearningSummary struct {
	CompletedCount         int
	AttemptedCount         int
	TerminalLearningSecond int64
}

func validAttemptOutcome(outcome string) bool {
	switch outcome {
	case AttemptActive, AttemptCompleted, AttemptStopped, AttemptReset, AttemptExpired:
		return true
	default:
		return false
	}
}

func requiredLearningValue(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("learning %s is required", name)
	}
	return nil
}

// RecordChallengeAttempt persists the point at which a user received a Ready
// environment. Reconciliation retries must not produce a second attempt.
func (d *DB) RecordChallengeAttempt(ctx context.Context, userID, challengeID, environmentUID, runtime string, readyAt time.Time) error {
	for name, value := range map[string]string{
		"user id": userID, "challenge id": challengeID, "environment uid": environmentUID,
	} {
		if err := requiredLearningValue(name, value); err != nil {
			return err
		}
	}
	if readyAt.IsZero() {
		return fmt.Errorf("learning ready time is required")
	}
	_, err := d.conn.ExecContext(ctx, `
		INSERT INTO user_challenge_attempts
			(environment_uid, user_id, challenge_id, runtime, ready_at, outcome)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(environment_uid) DO NOTHING
	`, environmentUID, userID, challengeID, runtime, nowText(readyAt.UTC()), AttemptActive)
	if err != nil {
		return fmt.Errorf("record challenge attempt: %w", err)
	}
	return nil
}

// FinishChallengeAttempt records the final state of a Ready environment. A
// terminal lifecycle event must never overwrite a previously completed result.
func (d *DB) FinishChallengeAttempt(ctx context.Context, environmentUID, outcome string, endedAt time.Time) error {
	if err := requiredLearningValue("environment uid", environmentUID); err != nil {
		return err
	}
	if !validAttemptOutcome(outcome) || outcome == AttemptActive {
		return fmt.Errorf("invalid final attempt outcome %q", outcome)
	}
	if endedAt.IsZero() {
		return fmt.Errorf("learning end time is required")
	}
	_, err := d.conn.ExecContext(ctx, `
		UPDATE user_challenge_attempts
		SET outcome = ?, ended_at = ?
		WHERE environment_uid = ? AND outcome = ?
	`, outcome, nowText(endedAt.UTC()), environmentUID, AttemptActive)
	if err != nil {
		return fmt.Errorf("finish challenge attempt: %w", err)
	}
	return nil
}

// OpenTerminalConnection creates a durable connection record. The unique
// active usage-session index makes the first connection transition atomic.
func (d *DB) OpenTerminalConnection(ctx context.Context, connection TerminalConnection) error {
	for name, value := range map[string]string{
		"connection id":      connection.ID,
		"environment uid":    connection.EnvironmentUID,
		"user id":            connection.UserID,
		"challenge id":       connection.ChallengeID,
		"server instance id": connection.ServerInstanceID,
	} {
		if err := requiredLearningValue(name, value); err != nil {
			return err
		}
	}
	if connection.ConnectedAt.IsZero() {
		return fmt.Errorf("connection time is required")
	}
	now := connection.ConnectedAt.UTC()
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin terminal connection: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO terminal_connections
			(id, environment_uid, user_id, challenge_id, server_instance_id, connected_at, heartbeat_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, connection.ID, connection.EnvironmentUID, connection.UserID, connection.ChallengeID, connection.ServerInstanceID, nowText(now), nowText(now)); err != nil {
		return fmt.Errorf("insert terminal connection: %w", err)
	}

	// The partial unique index resolves concurrent opens from multiple Server
	// replicas without treating a process-local counter as global state.
	usageID := fmt.Sprintf("usage-%s-%d", connection.EnvironmentUID, now.UnixNano())
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO environment_usage_sessions
			(id, environment_uid, user_id, challenge_id, started_at, heartbeat_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING
	`, usageID, connection.EnvironmentUID, connection.UserID, connection.ChallengeID, nowText(now), nowText(now)); err != nil {
		return fmt.Errorf("open environment usage session: %w", err)
	}
	return tx.Commit()
}

func (d *DB) TouchTerminalConnection(ctx context.Context, connectionID string, at time.Time) error {
	if err := requiredLearningValue("connection id", connectionID); err != nil {
		return err
	}
	if at.IsZero() {
		return fmt.Errorf("connection heartbeat time is required")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin terminal heartbeat: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var environmentUID string
	err = tx.QueryRowContext(ctx, `SELECT environment_uid FROM terminal_connections WHERE id = ? AND disconnected_at = ''`, connectionID).Scan(&environmentUID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read terminal connection: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE terminal_connections SET heartbeat_at = ? WHERE id = ?`, nowText(at.UTC()), connectionID); err != nil {
		return fmt.Errorf("touch terminal connection: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE environment_usage_sessions SET heartbeat_at = ? WHERE environment_uid = ? AND ended_at = ''`, nowText(at.UTC()), environmentUID); err != nil {
		return fmt.Errorf("touch environment usage session: %w", err)
	}
	return tx.Commit()
}

// CloseTerminalConnection records an individual WebSocket close immediately.
// A usage session is deliberately left open for the Server's settle delay so
// a tab handover does not create a false gap in learning time.
func (d *DB) CloseTerminalConnection(ctx context.Context, connectionID string, at time.Time) (bool, error) {
	if err := requiredLearningValue("connection id", connectionID); err != nil {
		return false, err
	}
	if at.IsZero() {
		return false, fmt.Errorf("connection close time is required")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin terminal close: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var environmentUID string
	err = tx.QueryRowContext(ctx, `SELECT environment_uid FROM terminal_connections WHERE id = ?`, connectionID).Scan(&environmentUID)
	if err == sql.ErrNoRows {
		return false, tx.Commit()
	}
	if err != nil {
		return false, fmt.Errorf("read terminal connection: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE terminal_connections SET disconnected_at = ?, heartbeat_at = ? WHERE id = ? AND disconnected_at = ''`, nowText(at.UTC()), nowText(at.UTC()), connectionID); err != nil {
		return false, fmt.Errorf("close terminal connection: %w", err)
	}

	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM terminal_connections WHERE environment_uid = ? AND disconnected_at = ''`, environmentUID).Scan(&remaining); err != nil {
		return false, fmt.Errorf("count active terminal connections: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return remaining == 0, nil
}

// FinishTerminalUsageSession closes an environment-level usage interval only
// after a local settle delay. It checks all Server connections transactionally
// so a connection hosted by another replica keeps the interval alive.
func (d *DB) FinishTerminalUsageSession(ctx context.Context, environmentUID string, at time.Time) (bool, error) {
	if err := requiredLearningValue("environment uid", environmentUID); err != nil {
		return false, err
	}
	if at.IsZero() {
		return false, fmt.Errorf("usage session end time is required")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin usage session finish: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var active int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM terminal_connections WHERE environment_uid = ? AND disconnected_at = ''
	`, environmentUID).Scan(&active); err != nil {
		return false, fmt.Errorf("count active terminal connections: %w", err)
	}
	if active != 0 {
		return false, tx.Commit()
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE environment_usage_sessions
		SET ended_at = ?, heartbeat_at = ?
		WHERE environment_uid = ? AND ended_at = ''
	`, nowText(at.UTC()), nowText(at.UTC()), environmentUID)
	if err != nil {
		return false, fmt.Errorf("close environment usage session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read usage session close result: %w", err)
	}
	return changed == 1, nil
}

// CleanupTerminalActivity makes Server restarts and lost WebSocket close
// events bounded. It closes stale records and their orphaned usage sessions.
func (d *DB) CleanupTerminalActivity(ctx context.Context, staleBefore, now time.Time) error {
	if staleBefore.IsZero() || now.IsZero() {
		return fmt.Errorf("terminal cleanup times are required")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin terminal cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		UPDATE terminal_connections
		SET disconnected_at = ?
		WHERE disconnected_at = '' AND heartbeat_at < ?
	`, nowText(now.UTC()), nowText(staleBefore.UTC())); err != nil {
		return fmt.Errorf("close stale terminal connections: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE environment_usage_sessions
		SET ended_at = ?, heartbeat_at = ?
		WHERE ended_at = '' AND NOT EXISTS (
			SELECT 1 FROM terminal_connections c
			WHERE c.environment_uid = environment_usage_sessions.environment_uid
				AND c.disconnected_at = ''
		)
	`, nowText(now.UTC()), nowText(now.UTC())); err != nil {
		return fmt.Errorf("close orphaned environment usage sessions: %w", err)
	}
	return tx.Commit()
}

// DeleteClosedTerminalConnections removes low-level operational records once
// they are no longer needed to determine global active connection state.
// Environment usage sessions remain the durable source for learning time.
func (d *DB) DeleteClosedTerminalConnections(ctx context.Context, before time.Time) error {
	if before.IsZero() {
		return fmt.Errorf("terminal connection retention time is required")
	}
	if _, err := d.conn.ExecContext(ctx, `
		DELETE FROM terminal_connections
		WHERE disconnected_at != '' AND disconnected_at < ?
	`, nowText(before.UTC())); err != nil {
		return fmt.Errorf("delete closed terminal connections: %w", err)
	}
	return nil
}

func (d *DB) LearningSummary(ctx context.Context, userID string, now time.Time) (LearningSummary, error) {
	if err := requiredLearningValue("user id", userID); err != nil {
		return LearningSummary{}, err
	}
	if now.IsZero() {
		return LearningSummary{}, fmt.Errorf("learning summary time is required")
	}

	var summary LearningSummary
	if err := d.conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM user_challenge_progress WHERE user_id = ?
	`, userID).Scan(&summary.CompletedCount); err != nil {
		return LearningSummary{}, fmt.Errorf("count completed challenges: %w", err)
	}
	if err := d.conn.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT challenge_id) FROM user_challenge_attempts WHERE user_id = ?
	`, userID).Scan(&summary.AttemptedCount); err != nil {
		return LearningSummary{}, fmt.Errorf("count attempted challenges: %w", err)
	}
	if err := d.conn.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(
			CASE WHEN ended_at = '' THEN GREATEST(0, EXTRACT(EPOCH FROM (?::timestamptz - started_at::timestamptz))::BIGINT)
			ELSE GREATEST(0, EXTRACT(EPOCH FROM (ended_at::timestamptz - started_at::timestamptz))::BIGINT) END
		), 0)::BIGINT
		FROM environment_usage_sessions WHERE user_id = ?
	`, nowText(now.UTC()), userID).Scan(&summary.TerminalLearningSecond); err != nil {
		return LearningSummary{}, fmt.Errorf("sum terminal learning time: %w", err)
	}
	return summary, nil
}

func (d *DB) ListLearningHistory(ctx context.Context, userID string, filter LearningHistoryFilter, limit int, cursor *LearningHistoryCursor, now time.Time) ([]LearningHistoryItem, error) {
	if err := requiredLearningValue("user id", userID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 101 {
		return nil, fmt.Errorf("learning history limit must be between 1 and 101")
	}
	if now.IsZero() {
		return nil, fmt.Errorf("learning history time is required")
	}
	if err := validateLearningHistoryFilter(filter); err != nil {
		return nil, err
	}
	if len(filter.ChallengeIDs) == 0 {
		return []LearningHistoryItem{}, nil
	}

	args := []any{nowText(now.UTC()), nowText(now.UTC()), userID}
	whereParts := []string{"a.user_id = ?"}
	challengePlaceholders := make([]string, 0, len(filter.ChallengeIDs))
	for _, challengeID := range filter.ChallengeIDs {
		challengePlaceholders = append(challengePlaceholders, "?")
		args = append(args, challengeID)
	}
	whereParts = append(whereParts, "a.challenge_id IN ("+strings.Join(challengePlaceholders, ",")+")")
	switch filter.State {
	case "active", "completed":
		whereParts = append(whereParts, "a.outcome = ?")
		args = append(args, filter.State)
	case "ended":
		whereParts = append(whereParts, "a.outcome IN (?, ?, ?)")
		args = append(args, AttemptStopped, AttemptReset, AttemptExpired)
	}
	if filter.Runtime != "" {
		whereParts = append(whereParts, "a.runtime = ?")
		args = append(args, filter.Runtime)
	}
	if cursor != nil {
		if cursor.ReadyAt.IsZero() || strings.TrimSpace(cursor.EnvironmentUID) == "" {
			return nil, fmt.Errorf("learning history cursor is invalid")
		}
		whereParts = append(whereParts, "(a.ready_at < ? OR (a.ready_at = ? AND a.environment_uid < ?))")
		args = append(args, nowText(cursor.ReadyAt.UTC()), nowText(cursor.ReadyAt.UTC()), cursor.EnvironmentUID)
	}
	args = append(args, limit)
	rows, err := d.conn.QueryContext(ctx, `
		SELECT a.environment_uid, a.challenge_id, a.runtime, a.ready_at, a.ended_at, a.outcome,
			CASE WHEN a.outcome = 'completed' AND a.ended_at != '' THEN a.ended_at END AS completed_at,
			COALESCE(SUM(CASE WHEN s.ended_at = '' THEN GREATEST(0, EXTRACT(EPOCH FROM (?::timestamptz - s.started_at::timestamptz))::BIGINT)
				ELSE GREATEST(0, EXTRACT(EPOCH FROM (s.ended_at::timestamptz - s.started_at::timestamptz))::BIGINT) END), 0)::BIGINT AS learning_seconds,
			COALESCE(MAX(CASE WHEN s.ended_at = '' THEN ? ELSE s.ended_at END), a.ended_at, a.ready_at) AS last_activity_at
		FROM user_challenge_attempts a
		LEFT JOIN environment_usage_sessions s ON s.environment_uid = a.environment_uid
		WHERE `+strings.Join(whereParts, " AND ")+`
		GROUP BY a.environment_uid
		ORDER BY a.ready_at DESC, a.environment_uid DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list learning history: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]LearningHistoryItem, 0, limit)
	for rows.Next() {
		var item LearningHistoryItem
		var readyAt, endedAt, lastActivityAt string
		var completedAt sql.NullString
		if err := rows.Scan(&item.EnvironmentUID, &item.ChallengeID, &item.Runtime, &readyAt, &endedAt, &item.Outcome, &completedAt, &item.LearningSeconds, &lastActivityAt); err != nil {
			return nil, fmt.Errorf("scan learning history: %w", err)
		}
		item.ReadyAt = parseLearningTime(readyAt)
		item.EndedAt = optionalLearningTime(endedAt)
		if completedAt.Valid {
			item.CompletedAt = optionalLearningTime(completedAt.String)
		}
		item.LastActivityAt = parseLearningTime(lastActivityAt)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate learning history: %w", err)
	}
	return items, nil
}

func validateLearningHistoryFilter(filter LearningHistoryFilter) error {
	for _, challengeID := range filter.ChallengeIDs {
		if strings.TrimSpace(challengeID) == "" {
			return fmt.Errorf("learning history challenge id is required")
		}
	}
	switch filter.State {
	case "", "active", "completed", "ended":
	default:
		return fmt.Errorf("learning history state %q is invalid", filter.State)
	}
	switch filter.Runtime {
	case "", "container", "vcluster":
	default:
		return fmt.Errorf("learning history runtime %q is invalid", filter.Runtime)
	}
	return nil
}

func parseLearningTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func optionalLearningTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed := parseLearningTime(value)
	if parsed.IsZero() {
		return nil
	}
	return &parsed
}
