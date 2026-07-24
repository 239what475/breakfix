package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/authoring"
)

// AuthoringSpaceSession is the database-owned portion of an author's card.
// Challenge presentation remains a filesystem concern in Server.
type AuthoringSpaceSession struct {
	ID                 string
	Title              string
	State              authoring.SessionState
	PublishChallengeID string
	UpdatedAt          time.Time
}

type ChallengeAudienceCounts struct {
	AttemptedUsers int
	CompletedUsers int
}

func (d *DB) ListAuthoringSpaceSessions(ctx context.Context, userID string) ([]AuthoringSpaceSession, error) {
	if err := requiredLearningValue("user id", userID); err != nil {
		return nil, err
	}
	rows, err := d.conn.QueryContext(ctx, `
		SELECT s.id, s.state, s.publish_challenge_id, s.updated_at, r.plan_json
		FROM authoring_sessions s
		JOIN authoring_revisions r ON r.session_id = s.id AND r.revision = s.current_revision
		WHERE s.user_id = ?
		ORDER BY s.updated_at DESC, s.id DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list authoring space sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	sessions := make([]AuthoringSpaceSession, 0)
	for rows.Next() {
		var session AuthoringSpaceSession
		var state, updatedAt, planJSON string
		if err := rows.Scan(&session.ID, &state, &session.PublishChallengeID, &updatedAt, &planJSON); err != nil {
			return nil, fmt.Errorf("scan authoring space session: %w", err)
		}
		var plan authoring.Plan
		if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
			return nil, fmt.Errorf("decode authoring plan for %q: %w", session.ID, err)
		}
		session.Title = strings.TrimSpace(plan.Metadata.Title)
		session.State = authoring.SessionState(state)
		session.UpdatedAt = parseAuthoringTime(updatedAt)
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate authoring space sessions: %w", err)
	}
	return sessions, nil
}

func (d *DB) ChallengeAudienceCounts(ctx context.Context, challengeIDs []string) (map[string]ChallengeAudienceCounts, error) {
	counts := make(map[string]ChallengeAudienceCounts, len(challengeIDs))
	if len(challengeIDs) == 0 {
		return counts, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(challengeIDs)), ",")
	args := make([]any, len(challengeIDs))
	for i, id := range challengeIDs {
		args[i] = id
		counts[id] = ChallengeAudienceCounts{}
	}

	rows, err := d.conn.QueryContext(ctx, `
		SELECT challenge_id, COUNT(DISTINCT user_id)
		FROM user_challenge_attempts
		WHERE challenge_id IN (`+placeholders+`)
		GROUP BY challenge_id
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("count challenge attempts: %w", err)
	}
	for rows.Next() {
		var challengeID string
		var attempted int
		if err := rows.Scan(&challengeID, &attempted); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan challenge attempts: %w", err)
		}
		current := counts[challengeID]
		current.AttemptedUsers = attempted
		counts[challengeID] = current
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close challenge attempts: %w", err)
	}

	rows, err = d.conn.QueryContext(ctx, `
		SELECT challenge_id, COUNT(DISTINCT user_id)
		FROM user_challenge_progress
		WHERE challenge_id IN (`+placeholders+`)
		GROUP BY challenge_id
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("count challenge completions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var challengeID string
		var completed int
		if err := rows.Scan(&challengeID, &completed); err != nil {
			return nil, fmt.Errorf("scan challenge completions: %w", err)
		}
		current := counts[challengeID]
		current.CompletedUsers = completed
		counts[challengeID] = current
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate challenge completions: %w", err)
	}
	return counts, nil
}
