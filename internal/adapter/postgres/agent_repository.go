package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/agent"
)

const defaultAgentRunDeadline = 15 * time.Minute

func (d *AgentRepository) CreateSession(ctx context.Context, session agent.Session) (*agent.Session, error) {
	if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.Purpose) == "" || strings.TrimSpace(session.OwnerKind) == "" || strings.TrimSpace(session.OwnerRef) == "" {
		return nil, errors.New("agent session requires id, purpose, owner kind, and owner ref")
	}
	if session.Status == "" {
		session.Status = agent.SessionActive
	}
	if session.Status != agent.SessionActive && session.Status != agent.SessionClosed {
		return nil, fmt.Errorf("invalid agent session status %q", session.Status)
	}
	now := time.Now().UTC()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = now
	}
	_, err := d.conn.ExecContext(ctx, `INSERT INTO agent_sessions
		(id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.Purpose, session.OwnerKind, session.OwnerRef, session.UserRef, session.Status, session.CreatedAt, session.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create agent session: %w", err)
	}
	return &session, nil
}

func (d *AgentRepository) GetSession(ctx context.Context, id string) (*agent.Session, error) {
	return scanAgentSession(d.conn.QueryRowContext(ctx, `SELECT id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at
		FROM agent_sessions WHERE id = ?`, id))
}

func (d *AgentRepository) FindOrCreateSession(ctx context.Context, session agent.Session) (*agent.Session, error) {
	if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.Purpose) == "" || strings.TrimSpace(session.OwnerKind) == "" || strings.TrimSpace(session.OwnerRef) == "" {
		return nil, errors.New("agent session requires id, purpose, owner kind, and owner ref")
	}
	if session.Status == "" {
		session.Status = agent.SessionActive
	}
	now := time.Now().UTC()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = now
	}
	return scanAgentSession(d.conn.QueryRowContext(ctx, `INSERT INTO agent_sessions
		(id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (purpose, owner_kind, owner_ref, user_ref) DO UPDATE SET updated_at = EXCLUDED.updated_at
		RETURNING id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at`,
		session.ID, session.Purpose, session.OwnerKind, session.OwnerRef, session.UserRef, session.Status, session.CreatedAt, session.UpdatedAt))
}

func (d *AgentRepository) FindSession(ctx context.Context, purpose, ownerKind, ownerRef, userRef string) (*agent.Session, error) {
	return scanAgentSession(d.conn.QueryRowContext(ctx, `SELECT id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at
		FROM agent_sessions WHERE purpose = ? AND owner_kind = ? AND owner_ref = ? AND user_ref = ?`, purpose, ownerKind, ownerRef, userRef))
}

func (d *AgentRepository) ListMessages(ctx context.Context, sessionID string) ([]agent.Message, error) {
	return listAgentMessages(ctx, d.conn, sessionID)
}

func listAgentMessages(ctx context.Context, conn *Conn, sessionID string) ([]agent.Message, error) {
	rows, err := conn.QueryContext(ctx, `SELECT id, session_id, sequence, role, content, metadata_json, created_at
		FROM agent_messages WHERE session_id = ? ORDER BY sequence`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list agent messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]agent.Message, 0)
	for rows.Next() {
		message, err := scanAgentMessage(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent messages: %w", err)
	}
	return result, nil
}

// CreateMessageAndRun persists one interactive user message and immediately
// marks its AgentRun running. Server owns this direct call; it is not queued.
func (d *AgentRepository) CreateMessageAndRun(ctx context.Context, message agent.Message, run agent.CreateRun) (*agent.Run, error) {
	if strings.TrimSpace(run.SessionID) == "" || run.SessionID != message.SessionID || message.Role != "user" || strings.TrimSpace(message.Content) == "" {
		return nil, errors.New("agent message and run require the same user session")
	}
	if err := agent.ValidateCreateRun(run); err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin agent message run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockActiveSessionTx(ctx, tx, run.SessionID); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if message.ID == "" {
		message.ID = agent.NewID("agent-message")
	}
	message.CreatedAt = now
	if err := insertAgentMessageTx(ctx, tx, &message); err != nil {
		return nil, err
	}
	created, err := createRunTx(ctx, tx, run, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit agent message run: %w", err)
	}
	return created, nil
}

// CreateRun stores one already-running background or direct AgentRun. A
// workflow, not AgentRun, decides whether and when it is executed.
func (d *AgentRepository) CreateRun(ctx context.Context, run agent.CreateRun) (*agent.Run, error) {
	if err := agent.ValidateCreateRun(run); err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create agent run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if run.SessionID != "" {
		if err := lockActiveSessionTx(ctx, tx, run.SessionID); err != nil {
			return nil, err
		}
	}
	created, err := createRunTx(ctx, tx, run, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit agent run: %w", err)
	}
	return created, nil
}

func (d *AgentRepository) GetRun(ctx context.Context, id string) (*agent.Run, error) {
	run, err := scanAgentRun(d.conn.QueryRowContext(ctx, agentRunSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agent.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get agent run: %w", err)
	}
	return run, nil
}

func (d *AgentRepository) GetActiveRunForSession(ctx context.Context, sessionID string) (*agent.Run, error) {
	run, err := scanAgentRun(d.conn.QueryRowContext(ctx, agentRunSelect+` WHERE session_id = ? AND status = ? ORDER BY created_at DESC, id DESC LIMIT 1`, sessionID, agent.RunRunning))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agent.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get active agent run: %w", err)
	}
	return run, nil
}

func (d *AgentRepository) ListRunsForOwner(ctx context.Context, ownerKind, ownerRef string) ([]agent.Run, error) {
	if strings.TrimSpace(ownerKind) == "" || strings.TrimSpace(ownerRef) == "" {
		return nil, errors.New("agent run owner kind and owner reference are required")
	}
	rows, err := d.conn.QueryContext(ctx, agentRunSelect+` WHERE owner_kind = ? AND owner_ref = ? ORDER BY created_at, id`, ownerKind, ownerRef)
	if err != nil {
		return nil, fmt.Errorf("list agent runs for owner: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]agent.Run, 0)
	for rows.Next() {
		run, err := scanAgentRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *run)
	}
	return result, rows.Err()
}

func (d *AgentRepository) ListActiveRunsForPurpose(ctx context.Context, purpose string) ([]agent.Run, error) {
	if strings.TrimSpace(purpose) == "" {
		return nil, errors.New("agent run purpose is required")
	}
	rows, err := d.conn.QueryContext(ctx, agentRunSelect+` WHERE purpose = ? AND status = ? ORDER BY created_at, id`, purpose, agent.RunRunning)
	if err != nil {
		return nil, fmt.Errorf("list active agent runs for purpose: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]agent.Run, 0)
	for rows.Next() {
		run, err := scanAgentRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active agent runs: %w", err)
	}
	return result, nil
}

func (d *AgentRepository) CompleteRunWithMessage(ctx context.Context, runID string, message agent.Message, now time.Time) error {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(message.Content) == "" || message.Role != "assistant" || now.IsZero() {
		return errors.New("complete agent run requires a run and assistant message")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin complete agent run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	run, err := scanAgentRun(tx.QueryRowContext(ctx, agentRunSelect+` WHERE id = ? FOR UPDATE`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return agent.ErrNotFound
	}
	if err != nil {
		return err
	}
	if run.Status != agent.RunRunning {
		return agent.ErrRunActive
	}
	if run.SessionID != "" {
		if message.ID == "" {
			message.ID = agent.NewID("agent-message")
		}
		message.SessionID = run.SessionID
		message.CreatedAt = now.UTC()
		if err := insertAgentMessageTx(ctx, tx, &message); err != nil {
			return err
		}
	}
	if err := completeRunTx(ctx, tx, runID, now.UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *AgentRepository) CompleteRun(ctx context.Context, runID string, now time.Time) error {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin complete agent run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := completeRunTx(ctx, tx, runID, now.UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func completeRunTx(ctx context.Context, tx *Tx, runID string, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, completed_at = ?, updated_at = ?, last_error = ''
		WHERE id = ? AND status = ?`, agent.RunSucceeded, now, now, runID, agent.RunRunning)
	if err != nil {
		return fmt.Errorf("complete agent run: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return agent.ErrRunActive
	}
	return nil
}

func (d *AgentRepository) FailRun(ctx context.Context, runID, message string, now time.Time) error {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(message) == "" || now.IsZero() {
		return errors.New("fail agent run requires a run, error, and current time")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
		WHERE id = ? AND status = ?`, agent.RunFailed, strings.TrimSpace(message), now.UTC(), now.UTC(), runID, agent.RunRunning)
	if err != nil {
		return fmt.Errorf("fail agent run: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return agent.ErrRunActive
	}
	return nil
}

// RetryRun records a known technical failure and fences the prior Eino
// instance before the caller constructs a fresh one for the same logical run.
// A nil Run means the fifth attempt or the Run deadline has been exhausted and
// the logical Run is now Failed. It never creates an AgentAttempt aggregate or
// changes the business owner.
func (d *AgentRepository) RetryRun(ctx context.Context, runID string, expectedAttempt int, message string, now time.Time) (*agent.Run, error) {
	if strings.TrimSpace(runID) == "" || expectedAttempt < 1 || strings.TrimSpace(message) == "" || now.IsZero() {
		return nil, errors.New("retry agent run requires id, attempt, error, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin agent run retry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	run, err := scanAgentRun(tx.QueryRowContext(ctx, agentRunSelect+` WHERE id = ? FOR UPDATE`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agent.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if run.Status != agent.RunRunning || run.Attempt != expectedAttempt {
		return nil, agent.ErrRunActive
	}
	if run.Attempt < agent.MaxAttempts && run.DeadlineAt.After(now.UTC()) {
		next, err := scanAgentRun(tx.QueryRowContext(ctx, `UPDATE agent_runs
			SET attempt = attempt + 1, last_error = ?, updated_at = ?
			WHERE id = ? AND status = ? AND attempt = ?
			RETURNING `+agentRunColumns,
			strings.TrimSpace(message), now.UTC(), runID, agent.RunRunning, expectedAttempt))
		if err != nil {
			return nil, fmt.Errorf("advance agent run attempt: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return next, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
		WHERE id = ? AND status = ? AND attempt = ?`, agent.RunFailed, strings.TrimSpace(message), now.UTC(), now.UTC(), runID, agent.RunRunning, expectedAttempt); err != nil {
		return nil, fmt.Errorf("fail exhausted agent run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return nil, nil
}

// InterruptRun records a process or lease interruption. It deliberately does
// not turn the event into a sixth technical attempt: the owner creates a new
// AgentRun from its durable business inputs when it is ready to continue.
func (d *AgentRepository) InterruptRun(ctx context.Context, runID, reason string, now time.Time) error {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(reason) == "" || now.IsZero() {
		return errors.New("interrupt agent run requires id, reason, and current time")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
		WHERE id = ? AND status = ?`, agent.RunInterrupted, strings.TrimSpace(reason), now.UTC(), now.UTC(), runID, agent.RunRunning)
	if err != nil {
		return fmt.Errorf("interrupt agent run: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return agent.ErrRunActive
	}
	return nil
}

// RestartRun atomically records a Server interruption and creates a fresh
// logical Run from the same durable session input. It is only for interactive
// sessions; workflow-owned roles create their replacement through their own
// aggregate so they can re-establish their state fence in the same transaction.
func (d *AgentRepository) RestartRun(ctx context.Context, runID, reason string, now time.Time) (*agent.Run, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(reason) == "" || now.IsZero() {
		return nil, errors.New("restart agent run requires id, reason, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin agent run restart: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	prior, err := scanAgentRun(tx.QueryRowContext(ctx, agentRunSelect+` WHERE id = ? FOR UPDATE`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agent.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if prior.Status != agent.RunRunning || strings.TrimSpace(prior.SessionID) == "" {
		return nil, agent.ErrRunActive
	}
	if err := lockAgentSessionTx(ctx, tx, prior.SessionID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
		WHERE id = ? AND status = ?`, agent.RunInterrupted, strings.TrimSpace(reason), now.UTC(), now.UTC(), prior.ID, agent.RunRunning); err != nil {
		return nil, fmt.Errorf("interrupt prior agent run: %w", err)
	}
	replacement, err := createRunTx(ctx, tx, agent.CreateRun{
		ID:            agent.NewID(prior.Purpose + "-run"),
		SessionID:     prior.SessionID,
		Purpose:       prior.Purpose,
		OwnerKind:     prior.OwnerKind,
		OwnerRef:      prior.OwnerRef,
		InputRevision: prior.InputRevision,
		Input:         prior.Input,
		Model:         prior.Model,
		PromptVersion: prior.PromptVersion,
	}, now.UTC())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return replacement, nil
}

func (d *AgentRepository) CancelRunsForOwner(ctx context.Context, purpose, ownerKind, ownerRef, reason string, now time.Time) (int64, error) {
	if strings.TrimSpace(purpose) == "" || strings.TrimSpace(ownerKind) == "" || strings.TrimSpace(ownerRef) == "" || strings.TrimSpace(reason) == "" {
		return 0, errors.New("cancel agent runs requires owner and reason")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
		WHERE purpose = ? AND owner_kind = ? AND owner_ref = ? AND status = ?`,
		agent.RunCancelled, reason, now.UTC(), now.UTC(), purpose, ownerKind, ownerRef, agent.RunRunning)
	if err != nil {
		return 0, fmt.Errorf("cancel agent runs: %w", err)
	}
	return result.RowsAffected()
}

func lockActiveSessionTx(ctx context.Context, tx *Tx, sessionID string) error {
	if err := lockAgentSessionTx(ctx, tx, sessionID); err != nil {
		return err
	}
	return ensureNoActiveSessionRun(ctx, tx, sessionID)
}

func lockAgentSessionTx(ctx context.Context, tx *Tx, sessionID string) error {
	var status agent.SessionStatus
	err := tx.QueryRowContext(ctx, `SELECT status FROM agent_sessions WHERE id = ? FOR UPDATE`, sessionID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return agent.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock agent session: %w", err)
	}
	if status != agent.SessionActive {
		return errors.New("agent session is not active")
	}
	return nil
}

func ensureNoActiveSessionRun(ctx context.Context, tx *Tx, sessionID string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE session_id = ? AND status = ?)`, sessionID, agent.RunRunning).Scan(&exists); err != nil {
		return fmt.Errorf("check active agent session run: %w", err)
	}
	if exists {
		return agent.ErrRunActive
	}
	return nil
}

func createRunTx(ctx context.Context, tx *Tx, input agent.CreateRun, now time.Time) (*agent.Run, error) {
	deadline := input.DeadlineAt.UTC()
	if deadline.IsZero() {
		deadline = now.UTC().Add(defaultAgentRunDeadline)
	}
	row := tx.QueryRowContext(ctx, `INSERT INTO agent_runs
		(id, session_id, purpose, owner_kind, owner_ref, input_revision, input_json, status, model, prompt_version, attempt, deadline_at, created_at, updated_at)
		VALUES (?, NULLIF(?, ''), ?, ?, ?, ?, ?::jsonb, ?, ?, ?, 1, ?, ?, ?)
		RETURNING `+agentRunColumns,
		input.ID, input.SessionID, input.Purpose, input.OwnerKind, input.OwnerRef, input.InputRevision, agentRunInput(input.Input),
		agent.RunRunning, input.Model, input.PromptVersion, deadline, now, now)
	run, err := scanAgentRun(row)
	if err != nil {
		return nil, fmt.Errorf("insert agent run: %w", err)
	}
	return run, nil
}

func insertAgentMessageTx(ctx context.Context, tx *Tx, message *agent.Message) error {
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM agent_messages WHERE session_id = ?`, message.SessionID).Scan(&sequence); err != nil {
		return fmt.Errorf("allocate agent message sequence: %w", err)
	}
	message.Sequence = sequence
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_messages (id, session_id, sequence, role, content, metadata_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?::jsonb, ?)`, message.ID, message.SessionID, message.Sequence, message.Role, message.Content, agentMessageMetadata(message.Metadata), message.CreatedAt); err != nil {
		return fmt.Errorf("insert agent message: %w", err)
	}
	return nil
}

func agentRunInput(value []byte) string {
	if len(value) == 0 {
		return `{}`
	}
	return string(value)
}

func agentMessageMetadata(value []byte) string {
	if len(value) == 0 {
		return `{}`
	}
	return string(value)
}

const agentRunColumns = `id, COALESCE(session_id, ''), purpose, owner_kind, owner_ref, input_revision, input_json, status, model, prompt_version,
	attempt, deadline_at, last_error, created_at, updated_at, completed_at`
const agentRunSelect = `SELECT ` + agentRunColumns + ` FROM agent_runs`

type agentRow interface{ Scan(...any) error }

func scanAgentRun(row agentRow) (*agent.Run, error) {
	var run agent.Run
	var completedAt sql.NullTime
	err := row.Scan(&run.ID, &run.SessionID, &run.Purpose, &run.OwnerKind, &run.OwnerRef, &run.InputRevision, &run.Input, &run.Status,
		&run.Model, &run.PromptVersion, &run.Attempt, &run.DeadlineAt, &run.LastError, &run.CreatedAt, &run.UpdatedAt, &completedAt)
	if err != nil {
		return nil, err
	}
	if completedAt.Valid {
		value := completedAt.Time.UTC()
		run.CompletedAt = &value
	}
	return &run, nil
}

func scanAgentSession(row agentRow) (*agent.Session, error) {
	var session agent.Session
	err := row.Scan(&session.ID, &session.Purpose, &session.OwnerKind, &session.OwnerRef, &session.UserRef, &session.Status, &session.CreatedAt, &session.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agent.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan agent session: %w", err)
	}
	return &session, nil
}

func scanAgentMessage(row agentRow) (*agent.Message, error) {
	var message agent.Message
	if err := row.Scan(&message.ID, &message.SessionID, &message.Sequence, &message.Role, &message.Content, &message.Metadata, &message.CreatedAt); err != nil {
		return nil, fmt.Errorf("scan agent message: %w", err)
	}
	return &message, nil
}

// marshalJSON is shared by workflow repositories for JSONB values.
func marshalJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
