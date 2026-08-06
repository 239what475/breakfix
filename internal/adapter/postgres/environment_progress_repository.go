package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RecordChallengeCompletion stores the user's first successful completion of
// a challenge. Repeated controller reconciliations must not overwrite it.
func (d *EnvironmentRepository) RecordChallengeCompletion(ctx context.Context, userID, challengeID, challengeRevision, environmentUID string, completedAt time.Time) error {
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("completion user id is required")
	}
	if strings.TrimSpace(challengeID) == "" {
		return fmt.Errorf("completion challenge id is required")
	}
	if strings.TrimSpace(challengeRevision) == "" {
		return fmt.Errorf("completion challenge revision is required")
	}
	if strings.TrimSpace(environmentUID) == "" {
		return fmt.Errorf("completion environment uid is required")
	}
	if completedAt.IsZero() {
		return fmt.Errorf("completion time is required")
	}

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin challenge completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_challenge_progress (user_id, challenge_id, completed_at, environment_uid)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, challenge_id) DO NOTHING
	`, userID, challengeID, completedAt.UTC().Format(time.RFC3339Nano), environmentUID); err != nil {
		return fmt.Errorf("record challenge completion: %w", err)
	}
	// Ready reconciliation normally creates the attempt first. Keeping this
	// fallback in the same transaction preserves attempt >= completion even for
	// pre-existing environments created before activity tracking was enabled.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_challenge_attempts
			(environment_uid, user_id, challenge_id, challenge_revision, runtime, ready_at, ended_at, outcome)
		VALUES (?, ?, ?, ?, '', ?, ?, ?)
		ON CONFLICT(environment_uid) DO NOTHING
	`, environmentUID, userID, challengeID, challengeRevision, completedAt.UTC().Format(time.RFC3339Nano), completedAt.UTC().Format(time.RFC3339Nano), AttemptCompleted); err != nil {
		return fmt.Errorf("backfill completed challenge attempt: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE user_challenge_attempts
		SET outcome = ?, ended_at = ?
		WHERE environment_uid = ? AND outcome = ?
	`, AttemptCompleted, completedAt.UTC().Format(time.RFC3339Nano), environmentUID, AttemptActive); err != nil {
		return fmt.Errorf("complete challenge attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit challenge completion: %w", err)
	}
	return nil
}

// ListCompletedChallengeIDs returns the user's durable completion history.
func (d *EnvironmentRepository) ListCompletedChallengeIDs(ctx context.Context, userID string) (map[string]struct{}, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("completion user id is required")
	}

	rows, err := d.conn.QueryContext(ctx, `
		SELECT challenge_id
		FROM user_challenge_progress
		WHERE user_id = ?
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list completed challenges: %w", err)
	}
	defer func() { _ = rows.Close() }()

	completed := make(map[string]struct{})
	for rows.Next() {
		var challengeID string
		if err := rows.Scan(&challengeID); err != nil {
			return nil, fmt.Errorf("scan completed challenge: %w", err)
		}
		completed[challengeID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate completed challenges: %w", err)
	}
	return completed, nil
}
