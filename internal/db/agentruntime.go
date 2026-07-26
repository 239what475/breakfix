package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
)

func (d *DB) CreateSession(ctx context.Context, session agentruntime.Session) (*agentruntime.Session, error) {
	if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.Purpose) == "" || strings.TrimSpace(session.OwnerKind) == "" || strings.TrimSpace(session.OwnerRef) == "" {
		return nil, fmt.Errorf("agent session requires id, purpose, owner kind, and owner ref")
	}
	if session.Status == "" {
		session.Status = agentruntime.SessionActive
	}
	if session.Status != agentruntime.SessionActive && session.Status != agentruntime.SessionClosed {
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

func (d *DB) GetSession(ctx context.Context, id string) (*agentruntime.Session, error) {
	var session agentruntime.Session
	err := d.conn.QueryRowContext(ctx, `SELECT id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at
		FROM agent_sessions WHERE id = ?`, id).Scan(
		&session.ID, &session.Purpose, &session.OwnerKind, &session.OwnerRef, &session.UserRef, &session.Status, &session.CreatedAt, &session.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agentruntime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get agent session: %w", err)
	}
	return &session, nil
}

func (d *DB) FindOrCreateSession(ctx context.Context, session agentruntime.Session) (*agentruntime.Session, error) {
	if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.Purpose) == "" || strings.TrimSpace(session.OwnerKind) == "" || strings.TrimSpace(session.OwnerRef) == "" {
		return nil, fmt.Errorf("agent session requires id, purpose, owner kind, and owner ref")
	}
	if session.Status == "" {
		session.Status = agentruntime.SessionActive
	}
	now := time.Now().UTC()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	if session.UpdatedAt.IsZero() {
		session.UpdatedAt = now
	}
	row := d.conn.QueryRowContext(ctx, `INSERT INTO agent_sessions
		(id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (purpose, owner_kind, owner_ref, user_ref) DO UPDATE SET updated_at = agent_sessions.updated_at
		RETURNING id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at`,
		session.ID, session.Purpose, session.OwnerKind, session.OwnerRef, session.UserRef, session.Status, session.CreatedAt, session.UpdatedAt)
	return scanAgentSession(row)
}

func (d *DB) FindSession(ctx context.Context, purpose, ownerKind, ownerRef, userRef string) (*agentruntime.Session, error) {
	return scanAgentSession(d.conn.QueryRowContext(ctx, `SELECT id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at
		FROM agent_sessions WHERE purpose = ? AND owner_kind = ? AND owner_ref = ? AND user_ref = ?`, purpose, ownerKind, ownerRef, userRef))
}

func (d *DB) ListMessages(ctx context.Context, sessionID string) ([]agentruntime.Message, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT id, session_id, sequence, role, content, metadata_json, created_at
		FROM agent_messages WHERE session_id = ? ORDER BY sequence`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list agent messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	messages := make([]agentruntime.Message, 0)
	for rows.Next() {
		message, err := scanAgentMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, *message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent messages: %w", err)
	}
	return messages, nil
}

func (d *DB) CreateMessageAndRun(ctx context.Context, message agentruntime.Message, run agentruntime.CreateRun) (*agentruntime.Run, error) {
	if strings.TrimSpace(run.SessionID) == "" || run.SessionID != message.SessionID {
		return nil, fmt.Errorf("agent message and run must use the same session")
	}
	if strings.TrimSpace(message.ID) == "" || strings.TrimSpace(message.Content) == "" || message.Role != "user" {
		return nil, fmt.Errorf("agent user message requires id and content")
	}
	if len(message.Metadata) > 0 && !json.Valid(message.Metadata) {
		return nil, fmt.Errorf("agent message metadata must be valid JSON")
	}
	if err := agentruntime.ValidateCreateRun(run); err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin agent message run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var sessionStatus agentruntime.SessionStatus
	err = tx.QueryRowContext(ctx, `SELECT status FROM agent_sessions WHERE id = ? FOR UPDATE`, run.SessionID).Scan(&sessionStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agentruntime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock agent session: %w", err)
	}
	if sessionStatus != agentruntime.SessionActive {
		return nil, fmt.Errorf("agent session is not active")
	}
	if err := ensureNoActiveSessionRun(ctx, tx, run.SessionID); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if message.CreatedAt.IsZero() {
		message.CreatedAt = now
	}
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

func (d *DB) CreateRun(ctx context.Context, run agentruntime.CreateRun) (*agentruntime.Run, error) {
	if err := agentruntime.ValidateCreateRun(run); err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create agent run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if run.SessionID != "" {
		var sessionStatus agentruntime.SessionStatus
		err = tx.QueryRowContext(ctx, `SELECT status FROM agent_sessions WHERE id = ? FOR UPDATE`, run.SessionID).Scan(&sessionStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, agentruntime.ErrNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("lock agent session: %w", err)
		}
		if sessionStatus != agentruntime.SessionActive {
			return nil, fmt.Errorf("agent session is not active")
		}
		if err := ensureNoActiveSessionRun(ctx, tx, run.SessionID); err != nil {
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

func (d *DB) GetRun(ctx context.Context, id string) (*agentruntime.Run, error) {
	run, err := scanAgentRun(d.conn.QueryRowContext(ctx, agentRunSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agentruntime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get agent run: %w", err)
	}
	return run, nil
}

func (d *DB) GetActiveRunForSession(ctx context.Context, sessionID string) (*agentruntime.Run, error) {
	run, err := scanAgentRun(d.conn.QueryRowContext(ctx, agentRunSelect+` WHERE session_id = ? AND status IN (?, ?)
		ORDER BY created_at DESC, id DESC LIMIT 1`, sessionID, agentruntime.RunPending, agentruntime.RunRunning))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agentruntime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get active agent run: %w", err)
	}
	return run, nil
}

func (d *DB) ListActiveRunsForPurpose(ctx context.Context, purpose string) ([]agentruntime.Run, error) {
	if strings.TrimSpace(purpose) == "" {
		return nil, fmt.Errorf("agent run purpose is required")
	}
	rows, err := d.conn.QueryContext(ctx, agentRunSelect+` WHERE purpose = ? AND status IN (?, ?)
		ORDER BY created_at, id`, purpose, agentruntime.RunPending, agentruntime.RunRunning)
	if err != nil {
		return nil, fmt.Errorf("list active agent runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	runs := make([]agentruntime.Run, 0)
	for rows.Next() {
		run, err := scanAgentRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan active agent run: %w", err)
		}
		runs = append(runs, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active agent runs: %w", err)
	}
	return runs, nil
}

func (d *DB) ClaimNext(ctx context.Context, worker string, leaseTTL time.Duration, now time.Time) (*agentruntime.Claim, error) {
	if strings.TrimSpace(worker) == "" || leaseTTL <= 0 || now.IsZero() {
		return nil, fmt.Errorf("worker, positive lease ttl, and current time are required")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin claim agent run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM agent_runs
		WHERE (
			(status = ? AND next_attempt_at <= ?)
			OR (status = ? AND lease_expires_at <= ?)
		) AND deadline_at > ?
		ORDER BY next_attempt_at, created_at, id
		FOR UPDATE SKIP LOCKED
		LIMIT 1`, agentruntime.RunPending, now, agentruntime.RunRunning, now, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select claimable agent run: %w", err)
	}
	leaseOwner := strings.TrimSpace(worker) + "-" + agentruntime.NewID("lease")
	leaseExpiresAt := now.Add(leaseTTL)
	row := tx.QueryRowContext(ctx, `UPDATE agent_runs
		SET status = ?, attempt = attempt + 1, lease_owner = ?, lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND status IN (?, ?)
		RETURNING `+agentRunColumns,
		agentruntime.RunRunning, leaseOwner, leaseExpiresAt, now, id, agentruntime.RunPending, agentruntime.RunRunning)
	run, err := scanAgentRun(row)
	if err != nil {
		return nil, fmt.Errorf("claim agent run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit agent run claim: %w", err)
	}
	return &agentruntime.Claim{Run: *run, LeaseOwner: leaseOwner}, nil
}

func (d *DB) RenewLease(ctx context.Context, claim agentruntime.Claim, leaseTTL time.Duration, now time.Time) error {
	if !claim.Valid() || leaseTTL <= 0 || now.IsZero() {
		return fmt.Errorf("valid claim, positive lease ttl, and current time are required")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE agent_runs
		SET lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND status = ? AND attempt = ? AND lease_owner = ? AND deadline_at > ?`,
		now.Add(leaseTTL), now, claim.Run.ID, agentruntime.RunRunning, claim.Run.Attempt, claim.LeaseOwner, now)
	if err != nil {
		return fmt.Errorf("renew agent lease: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return agentruntime.ErrLeaseLost
	}
	return nil
}

func (d *DB) Requeue(ctx context.Context, claim agentruntime.Claim, nextAttemptAt time.Time, lastError string, now time.Time) error {
	if !claim.Valid() || nextAttemptAt.IsZero() || now.IsZero() || strings.TrimSpace(lastError) == "" {
		return fmt.Errorf("valid claim, next attempt, error, and current time are required")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE agent_runs
		SET status = ?, next_attempt_at = ?, lease_owner = '', lease_expires_at = NULL, last_error = ?, updated_at = ?
		WHERE id = ? AND status = ? AND attempt = ? AND lease_owner = ? AND deadline_at > ?`,
		agentruntime.RunPending, nextAttemptAt, lastError, now, claim.Run.ID, agentruntime.RunRunning, claim.Run.Attempt, claim.LeaseOwner, now)
	if err != nil {
		return fmt.Errorf("requeue agent run: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return agentruntime.ErrLeaseLost
	}
	return nil
}

func (d *DB) CompleteWithMessage(ctx context.Context, claim agentruntime.Claim, message agentruntime.Message, now time.Time) error {
	if !claim.Valid() || strings.TrimSpace(message.ID) == "" || strings.TrimSpace(message.Content) == "" || message.Role != "assistant" || now.IsZero() {
		return fmt.Errorf("valid claim, non-empty assistant message, and current time are required")
	}
	if len(message.Metadata) > 0 && !json.Valid(message.Metadata) {
		return fmt.Errorf("agent message metadata must be valid JSON")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin complete agent run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var sessionID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT session_id FROM agent_runs
		WHERE id = ? AND status = ? AND attempt = ? AND lease_owner = ? AND deadline_at > ? FOR UPDATE`,
		claim.Run.ID, agentruntime.RunRunning, claim.Run.Attempt, claim.LeaseOwner, now).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return agentruntime.ErrLeaseLost
	}
	if err != nil {
		return fmt.Errorf("lock completing agent run: %w", err)
	}
	if !sessionID.Valid || sessionID.String == "" || message.SessionID != sessionID.String {
		return fmt.Errorf("agent run does not own the assistant message session")
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = now
	}
	if err := insertAgentMessageTx(ctx, tx, &message); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs
		SET status = ?, lease_owner = '', lease_expires_at = NULL, completed_at = ?, updated_at = ?
		WHERE id = ? AND status = ? AND attempt = ? AND lease_owner = ?`,
		agentruntime.RunSucceeded, now, now, claim.Run.ID, agentruntime.RunRunning, claim.Run.Attempt, claim.LeaseOwner)
	if err != nil {
		return fmt.Errorf("complete agent run: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return agentruntime.ErrLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit completed agent run: %w", err)
	}
	return nil
}

func (d *DB) Fail(ctx context.Context, claim agentruntime.Claim, lastError string, now time.Time) error {
	if !claim.Valid() || strings.TrimSpace(lastError) == "" || now.IsZero() {
		return fmt.Errorf("valid claim, error, and current time are required")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE agent_runs
		SET status = ?, lease_owner = '', lease_expires_at = NULL, last_error = ?, completed_at = ?, updated_at = ?
		WHERE id = ? AND status = ? AND attempt = ? AND lease_owner = ?`,
		agentruntime.RunFailed, lastError, now, now, claim.Run.ID, agentruntime.RunRunning, claim.Run.Attempt, claim.LeaseOwner)
	if err != nil {
		return fmt.Errorf("fail agent run: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return agentruntime.ErrLeaseLost
	}
	return nil
}

func (d *DB) Cancel(ctx context.Context, runID string, now time.Time) error {
	if strings.TrimSpace(runID) == "" || now.IsZero() {
		return fmt.Errorf("run id and current time are required")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE agent_runs
		SET status = ?, lease_owner = '', lease_expires_at = NULL, completed_at = ?, updated_at = ?
		WHERE id = ? AND status IN (?, ?)`,
		agentruntime.RunCancelled, now, now, runID, agentruntime.RunPending, agentruntime.RunRunning)
	if err != nil {
		return fmt.Errorf("cancel agent run: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return agentruntime.ErrNotFound
	}
	return nil
}

func (d *DB) CancelRunsForOwner(ctx context.Context, purpose, ownerKind, ownerRef string, now time.Time) (int64, error) {
	if strings.TrimSpace(purpose) == "" || strings.TrimSpace(ownerKind) == "" || strings.TrimSpace(ownerRef) == "" || now.IsZero() {
		return 0, fmt.Errorf("purpose, owner kind, owner ref, and current time are required")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE agent_runs
		SET status = ?, lease_owner = '', lease_expires_at = NULL, completed_at = ?, updated_at = ?
		WHERE session_id IN (
			SELECT id FROM agent_sessions WHERE purpose = ? AND owner_kind = ? AND owner_ref = ?
		) AND status IN (?, ?)`,
		agentruntime.RunCancelled, now, now, purpose, ownerKind, ownerRef, agentruntime.RunPending, agentruntime.RunRunning)
	if err != nil {
		return 0, fmt.Errorf("cancel agent runs for owner: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count cancelled agent runs: %w", err)
	}
	return count, nil
}

func (d *DB) ValidateLease(ctx context.Context, claim agentruntime.Claim, now time.Time) error {
	if !claim.Valid() || now.IsZero() {
		return fmt.Errorf("valid claim and current time are required")
	}
	var valid bool
	err := d.conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs
		WHERE id = ? AND status = ? AND attempt = ? AND lease_owner = ? AND lease_expires_at > ? AND deadline_at > ?)`,
		claim.Run.ID, agentruntime.RunRunning, claim.Run.Attempt, claim.LeaseOwner, now, now).Scan(&valid)
	if err != nil {
		return fmt.Errorf("validate agent lease: %w", err)
	}
	if !valid {
		return agentruntime.ErrLeaseLost
	}
	return nil
}

func ensureNoActiveSessionRun(ctx context.Context, tx *Tx, sessionID string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs
		WHERE session_id = ? AND status IN (?, ?))`, sessionID, agentruntime.RunPending, agentruntime.RunRunning).Scan(&exists); err != nil {
		return fmt.Errorf("check active agent run: %w", err)
	}
	if exists {
		return agentruntime.ErrRunActive
	}
	return nil
}

func createRunTx(ctx context.Context, tx *Tx, input agentruntime.CreateRun, now time.Time) (*agentruntime.Run, error) {
	run := agentruntime.Run{
		ID:            input.ID,
		SessionID:     input.SessionID,
		Purpose:       input.Purpose,
		OwnerKind:     input.OwnerKind,
		OwnerRef:      input.OwnerRef,
		InputRevision: input.InputRevision,
		Input:         append([]byte(nil), input.Input...),
		Status:        agentruntime.RunPending,
		Model:         input.Model,
		PromptVersion: input.PromptVersion,
		NextAttemptAt: input.DeadlineAt,
		DeadlineAt:    input.DeadlineAt,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	// A freshly created Run is immediately claimable, but the explicit value
	// makes a caller-provided deadline independent from the scheduler clock.
	run.NextAttemptAt = now
	row := tx.QueryRowContext(ctx, `INSERT INTO agent_runs
		(id, session_id, purpose, owner_kind, owner_ref, input_revision, input_json, status, model, prompt_version, attempt,
		next_attempt_at, deadline_at, created_at, updated_at)
		VALUES (?, NULLIF(?, ''), ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING `+agentRunColumns,
		run.ID, run.SessionID, run.Purpose, run.OwnerKind, run.OwnerRef, run.InputRevision, agentRunInput(run.Input), run.Status, run.Model, run.PromptVersion,
		run.Attempt, run.NextAttemptAt, run.DeadlineAt, run.CreatedAt, run.UpdatedAt)
	created, err := scanAgentRun(row)
	if err != nil {
		return nil, fmt.Errorf("insert agent run: %w", err)
	}
	return created, nil
}

func insertAgentMessageTx(ctx context.Context, tx *Tx, message *agentruntime.Message) error {
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
	attempt, next_attempt_at, lease_owner, lease_expires_at, deadline_at, last_error, created_at, updated_at, completed_at`

const agentRunSelect = `SELECT ` + agentRunColumns + ` FROM agent_runs`

type agentRow interface {
	Scan(...any) error
}

func scanAgentRun(row agentRow) (*agentruntime.Run, error) {
	var run agentruntime.Run
	var leaseExpiresAt, completedAt sql.NullTime
	err := row.Scan(&run.ID, &run.SessionID, &run.Purpose, &run.OwnerKind, &run.OwnerRef, &run.InputRevision, &run.Input, &run.Status, &run.Model, &run.PromptVersion,
		&run.Attempt, &run.NextAttemptAt, &run.LeaseOwner, &leaseExpiresAt, &run.DeadlineAt, &run.LastError, &run.CreatedAt, &run.UpdatedAt, &completedAt)
	if err != nil {
		return nil, err
	}
	if leaseExpiresAt.Valid {
		value := leaseExpiresAt.Time.UTC()
		run.LeaseExpiresAt = &value
	}
	if completedAt.Valid {
		value := completedAt.Time.UTC()
		run.CompletedAt = &value
	}
	return &run, nil
}

func scanAgentSession(row agentRow) (*agentruntime.Session, error) {
	var session agentruntime.Session
	err := row.Scan(&session.ID, &session.Purpose, &session.OwnerKind, &session.OwnerRef, &session.UserRef, &session.Status, &session.CreatedAt, &session.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agentruntime.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan agent session: %w", err)
	}
	return &session, nil
}

func scanAgentMessage(row agentRow) (*agentruntime.Message, error) {
	var message agentruntime.Message
	if err := row.Scan(&message.ID, &message.SessionID, &message.Sequence, &message.Role, &message.Content, &message.Metadata, &message.CreatedAt); err != nil {
		return nil, fmt.Errorf("scan agent message: %w", err)
	}
	return &message, nil
}
