package db

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RecordChallengeCompletion stores the user's first successful completion of
// a challenge. Repeated controller reconciliations must not overwrite it.
func (d *DB) RecordChallengeCompletion(ctx context.Context, userID, challengeID, environmentUID string, completedAt time.Time) error {
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("completion user id is required")
	}
	if strings.TrimSpace(challengeID) == "" {
		return fmt.Errorf("completion challenge id is required")
	}
	if strings.TrimSpace(environmentUID) == "" {
		return fmt.Errorf("completion environment uid is required")
	}
	if completedAt.IsZero() {
		return fmt.Errorf("completion time is required")
	}

	_, err := d.conn.ExecContext(ctx, `
		INSERT INTO user_challenge_progress (user_id, challenge_id, completed_at, environment_uid)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, challenge_id) DO NOTHING
	`, userID, challengeID, completedAt.UTC().Format(time.RFC3339Nano), environmentUID)
	if err != nil {
		return fmt.Errorf("record challenge completion: %w", err)
	}
	return nil
}

// ListCompletedChallengeIDs returns the user's durable completion history.
func (d *DB) ListCompletedChallengeIDs(ctx context.Context, userID string) (map[string]struct{}, error) {
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
	defer rows.Close()

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
