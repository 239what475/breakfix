package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/authoring"
)

// AuthoringSpaceSession is the database-owned portion of an author's card.
// Scenario presentation remains a filesystem concern in Server.
type AuthoringSpaceSession struct {
	ID                string
	Title             string
	State             authoring.SessionState
	PublishScenarioID string
	UpdatedAt         time.Time
}

type ScenarioAudienceCounts struct {
	AttemptedUsers int
	CompletedUsers int
}

func (d *ReportingRepository) ListAuthoringSpaceSessions(ctx context.Context, userID string) ([]AuthoringSpaceSession, error) {
	if err := requiredLearningValue("user id", userID); err != nil {
		return nil, err
	}
	rows, err := d.conn.QueryContext(ctx, `
		SELECT s.id, s.state, s.publish_scenario_id, s.updated_at, r.plan_json
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
		if err := rows.Scan(&session.ID, &state, &session.PublishScenarioID, &updatedAt, &planJSON); err != nil {
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

func (d *ReportingRepository) ScenarioAudienceCounts(ctx context.Context, scenarioIDs []string) (map[string]ScenarioAudienceCounts, error) {
	counts := make(map[string]ScenarioAudienceCounts, len(scenarioIDs))
	if len(scenarioIDs) == 0 {
		return counts, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(scenarioIDs)), ",")
	args := make([]any, len(scenarioIDs))
	for i, id := range scenarioIDs {
		args[i] = id
		counts[id] = ScenarioAudienceCounts{}
	}

	rows, err := d.conn.QueryContext(ctx, `
		SELECT scenario_id, COUNT(DISTINCT user_id)
		FROM user_scenario_attempts
		WHERE scenario_id IN (`+placeholders+`)
		GROUP BY scenario_id
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("count scenario attempts: %w", err)
	}
	for rows.Next() {
		var scenarioID string
		var attempted int
		if err := rows.Scan(&scenarioID, &attempted); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan scenario attempts: %w", err)
		}
		current := counts[scenarioID]
		current.AttemptedUsers = attempted
		counts[scenarioID] = current
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close scenario attempts: %w", err)
	}

	rows, err = d.conn.QueryContext(ctx, `
		SELECT scenario_id, COUNT(DISTINCT user_id)
		FROM user_scenario_progress
		WHERE scenario_id IN (`+placeholders+`)
		GROUP BY scenario_id
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("count scenario completions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var scenarioID string
		var completed int
		if err := rows.Scan(&scenarioID, &completed); err != nil {
			return nil, fmt.Errorf("scan scenario completions: %w", err)
		}
		current := counts[scenarioID]
		current.CompletedUsers = completed
		counts[scenarioID] = current
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scenario completions: %w", err)
	}
	return counts, nil
}
