package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RecordScenarioCompletion stores the user's first successful completion of
// a scenario. Repeated controller reconciliations must not overwrite it.
func (d *EnvironmentRepository) RecordScenarioCompletion(ctx context.Context, userID, scenarioID, scenarioRevision, environmentUID string, completedAt time.Time) error {
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("completion user id is required")
	}
	if strings.TrimSpace(scenarioID) == "" {
		return fmt.Errorf("completion scenario id is required")
	}
	if strings.TrimSpace(scenarioRevision) == "" {
		return fmt.Errorf("completion scenario revision is required")
	}
	if strings.TrimSpace(environmentUID) == "" {
		return fmt.Errorf("completion environment uid is required")
	}
	if completedAt.IsZero() {
		return fmt.Errorf("completion time is required")
	}

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin scenario completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_scenario_progress (user_id, scenario_id, completed_at, environment_uid)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, scenario_id) DO NOTHING
	`, userID, scenarioID, completedAt.UTC().Format(time.RFC3339Nano), environmentUID); err != nil {
		return fmt.Errorf("record scenario completion: %w", err)
	}
	// Ready reconciliation normally creates the attempt first. Keeping this
	// fallback in the same transaction preserves attempt >= completion even for
	// pre-existing environments created before activity tracking was enabled.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_scenario_attempts
			(environment_uid, user_id, scenario_id, scenario_revision, runtime, ready_at, ended_at, outcome)
		VALUES (?, ?, ?, ?, '', ?, ?, ?)
		ON CONFLICT(environment_uid) DO NOTHING
	`, environmentUID, userID, scenarioID, scenarioRevision, completedAt.UTC().Format(time.RFC3339Nano), completedAt.UTC().Format(time.RFC3339Nano), AttemptCompleted); err != nil {
		return fmt.Errorf("backfill completed scenario attempt: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE user_scenario_attempts
		SET outcome = ?, ended_at = ?
		WHERE environment_uid = ? AND outcome = ?
	`, AttemptCompleted, completedAt.UTC().Format(time.RFC3339Nano), environmentUID, AttemptActive); err != nil {
		return fmt.Errorf("complete scenario attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit scenario completion: %w", err)
	}
	return nil
}

// ListCompletedScenarioIDs returns the user's durable completion history.
func (d *EnvironmentRepository) ListCompletedScenarioIDs(ctx context.Context, userID string) (map[string]struct{}, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("completion user id is required")
	}

	rows, err := d.conn.QueryContext(ctx, `
		SELECT scenario_id
		FROM user_scenario_progress
		WHERE user_id = ?
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list completed scenarios: %w", err)
	}
	defer func() { _ = rows.Close() }()

	completed := make(map[string]struct{})
	for rows.Next() {
		var scenarioID string
		if err := rows.Scan(&scenarioID); err != nil {
			return nil, fmt.Errorf("scan completed scenario: %w", err)
		}
		completed[scenarioID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate completed scenarios: %w", err)
	}
	return completed, nil
}
