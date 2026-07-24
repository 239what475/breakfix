package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/breakfix/breakfix/internal/assistant"
)

func (d *DB) CreateAssistantSession(ctx context.Context, session assistant.Session) (*assistant.Session, error) {
	now := time.Now().UTC()
	session.CreatedAt = now
	session.UpdatedAt = now
	result, err := d.conn.ExecContext(ctx, `INSERT OR IGNORE INTO assistant_sessions
		(id, user_id, environment_uid, environment_name, runtime, challenge_id, agent_session_id, agent_started, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.UserID, session.EnvironmentUID, session.EnvironmentName, session.Runtime, session.ChallengeID,
		session.AgentSessionID, false, nowText(now), nowText(now))
	if err != nil {
		return nil, fmt.Errorf("create assistant session: %w", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return d.GetAssistantSession(ctx, session.UserID, session.EnvironmentUID, session.ChallengeID)
	}
	return &session, nil
}

func (d *DB) GetAssistantSession(ctx context.Context, userID, environmentUID, challengeID string) (*assistant.Session, error) {
	return d.readAssistantSession(ctx, `SELECT id, user_id, environment_uid, environment_name, runtime, challenge_id,
		agent_session_id, agent_started, created_at, updated_at
		FROM assistant_sessions WHERE user_id = ? AND environment_uid = ? AND challenge_id = ?`, userID, environmentUID, challengeID)
}

func (d *DB) ListAssistantSessions(ctx context.Context) ([]assistant.Session, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT id, user_id, environment_uid, environment_name, runtime, challenge_id,
		agent_session_id, agent_started, created_at, updated_at FROM assistant_sessions ORDER BY updated_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list assistant sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	sessions := make([]assistant.Session, 0)
	for rows.Next() {
		session, err := scanAssistantSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, *session)
	}
	return sessions, rows.Err()
}

func (d *DB) readAssistantSession(ctx context.Context, query string, args ...any) (*assistant.Session, error) {
	row := d.conn.QueryRowContext(ctx, query, args...)
	return scanAssistantSession(row)
}

type assistantSessionScanner interface {
	Scan(...any) error
}

func scanAssistantSession(scanner assistantSessionScanner) (*assistant.Session, error) {
	var session assistant.Session
	var createdAt, updatedAt string
	err := scanner.Scan(&session.ID, &session.UserID, &session.EnvironmentUID, &session.EnvironmentName, &session.Runtime,
		&session.ChallengeID, &session.AgentSessionID, &session.AgentStarted, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, assistant.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read assistant session: %w", err)
	}
	session.CreatedAt = parseAuthoringTime(createdAt)
	session.UpdatedAt = parseAuthoringTime(updatedAt)
	return &session, nil
}

func (d *DB) ListAssistantMessages(ctx context.Context, sessionID string) ([]assistant.Message, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT id, role, content, evidence_json, created_at
		FROM assistant_messages WHERE session_id = ? ORDER BY created_at, id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list assistant messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	messages := make([]assistant.Message, 0)
	for rows.Next() {
		var message assistant.Message
		var evidenceJSON, createdAt string
		if err := rows.Scan(&message.ID, &message.Role, &message.Content, &evidenceJSON, &createdAt); err != nil {
			return nil, fmt.Errorf("scan assistant message: %w", err)
		}
		if err := json.Unmarshal([]byte(evidenceJSON), &message.Evidence); err != nil {
			return nil, fmt.Errorf("decode assistant evidence: %w", err)
		}
		message.CreatedAt = parseAuthoringTime(createdAt)
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (d *DB) AppendAssistantMessage(ctx context.Context, sessionID string, message assistant.Message) error {
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}
	evidence, err := json.Marshal(message.Evidence)
	if err != nil {
		return fmt.Errorf("marshal assistant evidence: %w", err)
	}
	result, err := d.conn.ExecContext(ctx, `INSERT INTO assistant_messages
		(id, session_id, role, content, evidence_json, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		message.ID, sessionID, message.Role, message.Content, string(evidence), nowText(message.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert assistant message: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return fmt.Errorf("insert assistant message affected %d rows", count)
	}
	return nil
}

func (d *DB) SetAssistantAgentStarted(ctx context.Context, sessionID string) error {
	_, err := d.conn.ExecContext(ctx, `UPDATE assistant_sessions SET agent_started = 1, updated_at = ? WHERE id = ?`, nowText(time.Now().UTC()), sessionID)
	return err
}

func (d *DB) DeleteAssistantSessionsForEnvironment(ctx context.Context, environmentUID string) error {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM assistant_messages
		WHERE session_id IN (SELECT id FROM assistant_sessions WHERE environment_uid = ?)`, environmentUID); err != nil {
		return fmt.Errorf("delete assistant messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM assistant_sessions WHERE environment_uid = ?`, environmentUID); err != nil {
		return fmt.Errorf("delete assistant sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit assistant deletion: %w", err)
	}
	return nil
}
