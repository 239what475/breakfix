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
	"github.com/breakfix/breakfix/internal/worklist"
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

// ListRunsForOwner returns the durable execution history for one domain owner.
// It is used by operational diagnostics without exposing unrelated Agent Runs.
func (d *DB) ListRunsForOwner(ctx context.Context, ownerKind, ownerRef string) ([]agentruntime.Run, error) {
	if strings.TrimSpace(ownerKind) == "" || strings.TrimSpace(ownerRef) == "" {
		return nil, fmt.Errorf("agent run owner kind and reference are required")
	}
	rows, err := d.conn.QueryContext(ctx, agentRunSelect+` WHERE owner_kind = ? AND owner_ref = ?
		ORDER BY created_at, id`, ownerKind, ownerRef)
	if err != nil {
		return nil, fmt.Errorf("list agent runs for owner: %w", err)
	}
	defer func() { _ = rows.Close() }()
	runs := make([]agentruntime.Run, 0)
	for rows.Next() {
		run, err := scanAgentRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan owner agent run: %w", err)
		}
		runs = append(runs, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate owner agent runs: %w", err)
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
	now = now.UTC()
	if _, err := tx.ExecContext(ctx, `WITH expired AS (
		UPDATE work_items SET state = ?, lease_owner = '', lease_expires_at = NULL,
			error_code = 'deadline_exceeded', error_summary = 'agent run deadline exceeded', updated_at = ?
		WHERE kind = ? AND state IN (?, ?) AND deadline_at <= ?
		RETURNING subject_id
	) UPDATE agent_runs SET status = ?, last_error = 'agent run deadline exceeded', completed_at = ?, updated_at = ?
	WHERE id IN (SELECT subject_id FROM expired) AND status IN (?, ?)`,
		worklist.StateFailed, now, worklist.KindAgent, worklist.StatePending, worklist.StateRunning, now,
		agentruntime.RunFailed, now, now, agentruntime.RunPending, agentruntime.RunRunning); err != nil {
		return nil, fmt.Errorf("expire due agent runs: %w", err)
	}
	var workItemID, runID string
	err = tx.QueryRowContext(ctx, `SELECT id, subject_id FROM work_items
		WHERE kind = ? AND (
			(state = ? AND next_run_at <= ?)
			OR (state = ? AND lease_expires_at <= ?)
		) AND deadline_at > ?
		ORDER BY next_run_at, created_at, id
		FOR UPDATE SKIP LOCKED
		LIMIT 1`, worklist.KindAgent, worklist.StatePending, now, worklist.StateRunning, now, now).Scan(&workItemID, &runID)
	if errors.Is(err, sql.ErrNoRows) {
		// The deadline update above is still meaningful even when no work can
		// be claimed. Commit it so an expired Run cannot remain active forever.
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit expired agent runs: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select claimable agent run: %w", err)
	}
	leaseOwner := strings.TrimSpace(worker) + "-" + worklist.NewID("lease")
	leaseExpiresAt := now.Add(leaseTTL)
	var attempt int
	if err := tx.QueryRowContext(ctx, `UPDATE work_items SET state = ?, attempt = attempt + 1,
		lease_owner = ?, lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND state IN (?, ?) RETURNING attempt`,
		worklist.StateRunning, leaseOwner, leaseExpiresAt, now, workItemID,
		worklist.StatePending, worklist.StateRunning).Scan(&attempt); err != nil {
		return nil, fmt.Errorf("claim agent work item: %w", err)
	}
	run, err := scanAgentRun(tx.QueryRowContext(ctx, `UPDATE agent_runs SET status = ?, updated_at = ?
		WHERE id = ? AND status IN (?, ?) RETURNING `+agentRunColumns,
		agentruntime.RunRunning, now, runID, agentruntime.RunPending, agentruntime.RunRunning))
	if err != nil {
		return nil, fmt.Errorf("claim agent run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit agent run claim: %w", err)
	}
	return &agentruntime.Claim{Run: *run, WorkItemID: workItemID, Attempt: attempt, LeaseOwner: leaseOwner}, nil
}

func (d *DB) RenewLease(ctx context.Context, claim agentruntime.Claim, leaseTTL time.Duration, now time.Time) error {
	if !claim.Valid() || leaseTTL <= 0 || now.IsZero() {
		return fmt.Errorf("valid claim, positive lease ttl, and current time are required")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE work_items SET lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND kind = ? AND subject_id = ? AND state = ? AND attempt = ?
		AND lease_owner = ? AND lease_expires_at > ? AND deadline_at > ?`,
		now.Add(leaseTTL), now, claim.WorkItemID, worklist.KindAgent, claim.Run.ID,
		worklist.StateRunning, claim.Attempt, claim.LeaseOwner, now, now)
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
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin requeue agent run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := lockAgentClaim(ctx, tx, claim, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		agentruntime.RunPending, lastError, now, claim.Run.ID); err != nil {
		return fmt.Errorf("requeue agent run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work_items SET state = ?, next_run_at = ?, lease_owner = '',
		lease_expires_at = NULL, error_code = 'agent_execution', error_summary = ?, updated_at = ? WHERE id = ?`,
		worklist.StatePending, nextAttemptAt, lastError, now, claim.WorkItemID); err != nil {
		return fmt.Errorf("requeue agent work item: %w", err)
	}
	return tx.Commit()
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
	sessionID, err = lockAgentClaim(ctx, tx, claim, now)
	if err != nil {
		return err
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
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, completed_at = ?, updated_at = ?
		WHERE id = ? AND status = ?`, agentruntime.RunSucceeded, now, now, claim.Run.ID, agentruntime.RunRunning)
	if err != nil {
		return fmt.Errorf("complete agent run: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return agentruntime.ErrLeaseLost
	}
	if err := completeAgentWorkItemTx(ctx, tx, claim, worklist.StateSucceeded, "", "", now); err != nil {
		return err
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
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fail agent run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := lockAgentClaim(ctx, tx, claim, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ? WHERE id = ?`,
		agentruntime.RunFailed, lastError, now, now, claim.Run.ID); err != nil {
		return fmt.Errorf("fail agent run: %w", err)
	}
	if err := completeAgentWorkItemTx(ctx, tx, claim, worklist.StateFailed, "agent_execution", lastError, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) Cancel(ctx context.Context, runID string, now time.Time) error {
	if strings.TrimSpace(runID) == "" || now.IsZero() {
		return fmt.Errorf("run id and current time are required")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin cancel agent run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, completed_at = ?, updated_at = ?
		WHERE id = ? AND status IN (?, ?)`, agentruntime.RunCancelled, now, now, runID, agentruntime.RunPending, agentruntime.RunRunning)
	if err != nil {
		return fmt.Errorf("cancel agent run: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return agentruntime.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE work_items SET state = ?, lease_owner = '', lease_expires_at = NULL,
		error_code = 'cancelled', error_summary = 'agent run cancelled', updated_at = ?
		WHERE kind = ? AND subject_type = ? AND subject_id = ? AND state IN (?, ?)`,
		worklist.StateCancelled, now, worklist.KindAgent, worklist.SubjectAgentRun, runID,
		worklist.StatePending, worklist.StateRunning); err != nil {
		return fmt.Errorf("cancel agent work item: %w", err)
	}
	return tx.Commit()
}

func (d *DB) CancelRunsForOwner(ctx context.Context, purpose, ownerKind, ownerRef string, now time.Time) (int64, error) {
	if strings.TrimSpace(purpose) == "" || strings.TrimSpace(ownerKind) == "" || strings.TrimSpace(ownerRef) == "" || now.IsZero() {
		return 0, fmt.Errorf("purpose, owner kind, owner ref, and current time are required")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin cancel owner agent runs: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.raw.QueryContext(ctx, bind(`UPDATE agent_runs SET status = ?, completed_at = ?, updated_at = ?
		WHERE session_id IN (SELECT id FROM agent_sessions WHERE purpose = ? AND owner_kind = ? AND owner_ref = ?)
		AND status IN (?, ?) RETURNING id`),
		agentruntime.RunCancelled, now, now, purpose, ownerKind, ownerRef, agentruntime.RunPending, agentruntime.RunRunning)
	if err != nil {
		return 0, fmt.Errorf("cancel agent runs for owner: %w", err)
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, fmt.Errorf("scan cancelled agent run: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate cancelled agent runs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close cancelled agent runs: %w", err)
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE work_items SET state = ?, lease_owner = '', lease_expires_at = NULL,
			error_code = 'cancelled', error_summary = 'agent owner cancelled', updated_at = ?
			WHERE kind = ? AND subject_type = ? AND subject_id = ? AND state IN (?, ?)`,
			worklist.StateCancelled, now, worklist.KindAgent, worklist.SubjectAgentRun, id,
			worklist.StatePending, worklist.StateRunning); err != nil {
			return 0, fmt.Errorf("cancel owner agent work item: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit owner agent cancellation: %w", err)
	}
	return int64(len(ids)), nil
}

func (d *DB) ValidateLease(ctx context.Context, claim agentruntime.Claim, now time.Time) error {
	if !claim.Valid() || now.IsZero() {
		return fmt.Errorf("valid claim and current time are required")
	}
	var valid bool
	err := d.conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_items w
		JOIN agent_runs r ON r.id = w.subject_id
		WHERE w.id = ? AND w.kind = ? AND w.subject_type = ? AND w.subject_id = ?
		AND w.state = ? AND w.attempt = ? AND w.lease_owner = ? AND w.lease_expires_at > ?
		AND w.deadline_at > ? AND r.status = ?)`, claim.WorkItemID, worklist.KindAgent,
		worklist.SubjectAgentRun, claim.Run.ID, worklist.StateRunning, claim.Attempt,
		claim.LeaseOwner, now, now, agentruntime.RunRunning).Scan(&valid)
	if err != nil {
		return fmt.Errorf("validate agent lease: %w", err)
	}
	if !valid {
		return agentruntime.ErrLeaseLost
	}
	return nil
}

func (d *DB) GetAgentClaim(ctx context.Context, runID string, attempt int, leaseOwner string, now time.Time) (*agentruntime.Claim, error) {
	if strings.TrimSpace(runID) == "" || attempt < 1 || strings.TrimSpace(leaseOwner) == "" || now.IsZero() {
		return nil, fmt.Errorf("agent claim credentials are required")
	}
	run, err := d.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	item, err := d.GetWorkItemForSubject(ctx, worklist.KindAgent, worklist.SubjectAgentRun, runID)
	if errors.Is(err, worklist.ErrNotFound) {
		return nil, agentruntime.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	claim := &agentruntime.Claim{Run: *run, WorkItemID: item.ID, Attempt: attempt, LeaseOwner: leaseOwner}
	if item.Attempt != attempt || item.LeaseOwner != leaseOwner {
		return nil, agentruntime.ErrLeaseLost
	}
	if err := d.ValidateLease(ctx, *claim, now); err != nil {
		return nil, err
	}
	return claim, nil
}

func lockAgentClaim(ctx context.Context, tx *Tx, claim agentruntime.Claim, now time.Time) (sql.NullString, error) {
	var sessionID sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT r.session_id FROM work_items w
		JOIN agent_runs r ON r.id = w.subject_id
		WHERE w.id = ? AND w.kind = ? AND w.subject_type = ? AND w.subject_id = ?
		AND w.state = ? AND w.attempt = ? AND w.lease_owner = ? AND w.lease_expires_at > ?
		AND w.deadline_at > ? AND r.status = ? FOR UPDATE OF w, r`,
		claim.WorkItemID, worklist.KindAgent, worklist.SubjectAgentRun, claim.Run.ID,
		worklist.StateRunning, claim.Attempt, claim.LeaseOwner, now, now, agentruntime.RunRunning).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.NullString{}, agentruntime.ErrLeaseLost
	}
	if err != nil {
		return sql.NullString{}, fmt.Errorf("lock agent claim: %w", err)
	}
	return sessionID, nil
}

func completeAgentWorkItemTx(ctx context.Context, tx *Tx, claim agentruntime.Claim, state worklist.State, code, summary string, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE work_items SET state = ?, lease_owner = '', lease_expires_at = NULL,
		error_code = ?, error_summary = ?, updated_at = ?
		WHERE id = ? AND kind = ? AND subject_id = ? AND state = ? AND attempt = ? AND lease_owner = ?`,
		state, code, summary, now, claim.WorkItemID, worklist.KindAgent, claim.Run.ID,
		worklist.StateRunning, claim.Attempt, claim.LeaseOwner)
	if err != nil {
		return fmt.Errorf("complete agent work item: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
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
		DeadlineAt:    input.DeadlineAt,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	row := tx.QueryRowContext(ctx, `INSERT INTO agent_runs
		(id, session_id, purpose, owner_kind, owner_ref, input_revision, input_json, status, model, prompt_version,
		deadline_at, created_at, updated_at)
		VALUES (?, NULLIF(?, ''), ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?, ?)
		RETURNING `+agentRunColumns,
		run.ID, run.SessionID, run.Purpose, run.OwnerKind, run.OwnerRef, run.InputRevision, agentRunInput(run.Input), run.Status, run.Model, run.PromptVersion,
		run.DeadlineAt, run.CreatedAt, run.UpdatedAt)
	created, err := scanAgentRun(row)
	if err != nil {
		return nil, fmt.Errorf("insert agent run: %w", err)
	}
	deadline := input.DeadlineAt.UTC()
	if _, err := createWorkItemTx(ctx, tx, worklist.CreateItem{
		ID:          worklist.NewID("work"),
		Kind:        worklist.KindAgent,
		SubjectType: worklist.SubjectAgentRun,
		SubjectID:   run.ID,
		NextRunAt:   now.UTC(),
		DeadlineAt:  &deadline,
	}, now); err != nil {
		return nil, fmt.Errorf("enqueue agent run: %w", err)
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
	deadline_at, last_error, created_at, updated_at, completed_at`

const agentRunSelect = `SELECT ` + agentRunColumns + ` FROM agent_runs`

type agentRow interface {
	Scan(...any) error
}

func scanAgentRun(row agentRow) (*agentruntime.Run, error) {
	var run agentruntime.Run
	var completedAt sql.NullTime
	err := row.Scan(&run.ID, &run.SessionID, &run.Purpose, &run.OwnerKind, &run.OwnerRef, &run.InputRevision, &run.Input, &run.Status, &run.Model, &run.PromptVersion,
		&run.DeadlineAt, &run.LastError, &run.CreatedAt, &run.UpdatedAt, &completedAt)
	if err != nil {
		return nil, err
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
