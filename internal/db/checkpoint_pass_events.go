package db

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CheckpointFirstPassEvent is an immutable learning fact. Current checkpoint
// state continues to live in the Environment CRD; this table only records the
// first successful observation for one environment/checkpoint pair.
type CheckpointFirstPassEvent struct {
	EnvironmentUID    string
	UserID            string
	ChallengeID       string
	ChallengeRevision string
	CheckpointID      string
	FirstPassedAt     time.Time
	Summary           string
}

func (d *DB) RecordCheckpointFirstPass(ctx context.Context, event CheckpointFirstPassEvent) error {
	for name, value := range map[string]string{
		"environment uid": event.EnvironmentUID,
		"user id":         event.UserID,
		"challenge id":    event.ChallengeID,
		"checkpoint id":   event.CheckpointID,
		"summary":         event.Summary,
	} {
		if err := requiredLearningValue(name, value); err != nil {
			return err
		}
	}
	if event.FirstPassedAt.IsZero() {
		return fmt.Errorf("checkpoint first passed time is required")
	}
	_, err := d.conn.ExecContext(ctx, `
		INSERT INTO checkpoint_pass_events
			(environment_uid, checkpoint_id, user_id, challenge_id, challenge_revision, first_passed_at, summary)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(environment_uid, checkpoint_id) DO NOTHING
	`, event.EnvironmentUID, event.CheckpointID, event.UserID, event.ChallengeID, strings.TrimSpace(event.ChallengeRevision), event.FirstPassedAt.UTC(), event.Summary)
	if err != nil {
		return fmt.Errorf("record checkpoint first pass: %w", err)
	}
	return nil
}

// ListCheckpointFirstPasses returns history for the requested attempts in a
// single query so My Space does not turn a learning page into an N+1 lookup.
func (d *DB) ListCheckpointFirstPasses(ctx context.Context, environmentUIDs []string) (map[string][]CheckpointFirstPassEvent, error) {
	result := make(map[string][]CheckpointFirstPassEvent, len(environmentUIDs))
	if len(environmentUIDs) == 0 {
		return result, nil
	}
	placeholders := make([]string, 0, len(environmentUIDs))
	args := make([]any, 0, len(environmentUIDs))
	for _, environmentUID := range environmentUIDs {
		if err := requiredLearningValue("environment uid", environmentUID); err != nil {
			return nil, err
		}
		placeholders = append(placeholders, "?")
		args = append(args, environmentUID)
	}
	rows, err := d.conn.QueryContext(ctx, `
		SELECT environment_uid, user_id, challenge_id, challenge_revision, checkpoint_id, first_passed_at, summary
		FROM checkpoint_pass_events
		WHERE environment_uid IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY environment_uid, first_passed_at, checkpoint_id
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("list checkpoint first passes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var event CheckpointFirstPassEvent
		if err := rows.Scan(&event.EnvironmentUID, &event.UserID, &event.ChallengeID, &event.ChallengeRevision, &event.CheckpointID, &event.FirstPassedAt, &event.Summary); err != nil {
			return nil, fmt.Errorf("scan checkpoint first pass: %w", err)
		}
		event.FirstPassedAt = event.FirstPassedAt.UTC()
		result[event.EnvironmentUID] = append(result[event.EnvironmentUID], event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate checkpoint first passes: %w", err)
	}
	return result, nil
}
