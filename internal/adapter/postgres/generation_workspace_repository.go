package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/generation"
)

func (d *GenerationRepository) CreateGeneratorWorkspace(ctx context.Context, record generation.Workspace) (*generation.Workspace, error) {
	if record.State == "" {
		record.State = generation.WorkspacePending
	}
	if err := generation.ValidateWorkspace(record); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = now
	}
	_, err := d.conn.ExecContext(ctx, `INSERT INTO generator_workspaces
		(workspace_id, workflow_id, namespace, pvc_name, sandbox_id, active_turn_id, idle_since, state, provision_deadline, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		record.ID, record.WorkflowID, record.Namespace, record.PVCName, record.SandboxID, record.ActiveTurnID, record.IdleSince,
		record.State, record.ProvisionDeadline, record.CreatedAt, record.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create generator workspace: %w", err)
	}
	existing, err := d.GetGeneratorWorkspace(ctx, record.ID)
	matchedRequestedIdentity := err == nil
	if errors.Is(err, generation.ErrWorkspaceNotFound) {
		existing, err = d.GetCurrentGeneratorWorkspace(ctx, record.WorkflowID)
	}
	if err != nil {
		return nil, err
	}
	if existing.WorkflowID != record.WorkflowID || existing.Namespace != record.Namespace || (matchedRequestedIdentity && existing.PVCName != record.PVCName) {
		return nil, fmt.Errorf("generator workspace already has different ownership")
	}
	return existing, nil
}

func (d *GenerationRepository) GetGeneratorWorkspaceForTurn(ctx context.Context, turn generation.WorkspaceTurn) (*generation.Workspace, error) {
	if !turn.Valid() {
		return nil, generation.ErrWorkspaceTurnLost
	}
	record, err := scanGeneratorWorkspace(d.conn.QueryRowContext(ctx, generatorWorkspaceSelect+` WHERE workflow_id = ? AND state = ? AND active_turn_id = ?
		ORDER BY created_at DESC, workspace_id DESC LIMIT 1`, turn.WorkflowID, generation.WorkspaceActive, turn.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrWorkspaceTurnLost
	}
	if err != nil {
		return nil, fmt.Errorf("get generator workspace turn: %w", err)
	}
	return record, nil
}

// AcquireGeneratorWorkspaceTurn is the authoritative single-writer fence for
// Generator workspace tools. The same explicit turn may resume after a
// retried request; a different turn cannot modify the workspace concurrently.
func (d *GenerationRepository) AcquireGeneratorWorkspaceTurn(ctx context.Context, turn generation.WorkspaceTurn, now time.Time) (*generation.Workspace, error) {
	if !turn.Valid() || now.IsZero() {
		return nil, generation.ErrWorkspaceTurnLost
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin acquire generator workspace turn: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := scanGeneratorWorkspace(tx.QueryRowContext(ctx, generatorWorkspaceSelect+` WHERE workflow_id = ? AND state = ?
		ORDER BY created_at DESC, workspace_id DESC LIMIT 1 FOR UPDATE`, turn.WorkflowID, generation.WorkspaceActive))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrWorkspaceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock generator workspace turn: %w", err)
	}
	if record.ActiveTurnID != "" && record.ActiveTurnID != turn.ID {
		return nil, generation.ErrWorkspaceBusy
	}
	if record.ActiveTurnID == "" {
		if _, err := tx.ExecContext(ctx, `UPDATE generator_workspaces SET active_turn_id = ?, idle_since = NULL, updated_at = ? WHERE workspace_id = ?`, turn.ID, now.UTC(), record.ID); err != nil {
			return nil, fmt.Errorf("bind generator workspace turn: %w", err)
		}
		record.ActiveTurnID = turn.ID
		record.IdleSince = nil
		record.UpdatedAt = now.UTC()
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generator workspace turn: %w", err)
	}
	return record, nil
}

func (d *GenerationRepository) ReleaseGeneratorWorkspaceTurn(ctx context.Context, turn generation.WorkspaceTurn, now time.Time) error {
	if !turn.Valid() || now.IsZero() {
		return generation.ErrWorkspaceTurnLost
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE generator_workspaces SET active_turn_id = '', idle_since = NULL, updated_at = ?
		WHERE workflow_id = ? AND state = ? AND active_turn_id = ?`, now.UTC(), turn.WorkflowID, generation.WorkspaceActive, turn.ID)
	if err != nil {
		return fmt.Errorf("release generator workspace turn: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed == 1 {
		return nil
	}
	return generation.ErrWorkspaceTurnLost
}

func (d *GenerationRepository) GetGeneratorWorkspace(ctx context.Context, workspaceID string) (*generation.Workspace, error) {
	record, err := scanGeneratorWorkspace(d.conn.QueryRowContext(ctx, generatorWorkspaceSelect+` WHERE workspace_id = ?`, strings.TrimSpace(workspaceID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrWorkspaceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get generator workspace: %w", err)
	}
	return record, nil
}

func (d *GenerationRepository) GetCurrentGeneratorWorkspace(ctx context.Context, workflowID string) (*generation.Workspace, error) {
	record, err := scanGeneratorWorkspace(d.conn.QueryRowContext(ctx, generatorWorkspaceSelect+` WHERE workflow_id = ? AND state IN (?, ?)
		ORDER BY created_at DESC, workspace_id DESC LIMIT 1`, strings.TrimSpace(workflowID), generation.WorkspacePending, generation.WorkspaceActive))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrWorkspaceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get current generator workspace: %w", err)
	}
	return record, nil
}

func (d *GenerationRepository) RecordGeneratorWorkspaceSandbox(ctx context.Context, workspaceID, sandboxID string, now time.Time) error {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(sandboxID) == "" || now.IsZero() {
		return errors.New("generator workspace id, sandbox id, and current time are required")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE generator_workspaces
		SET sandbox_id = ?, updated_at = ?
		WHERE workspace_id = ? AND state = ? AND (sandbox_id = '' OR sandbox_id = ?)`,
		sandboxID, now.UTC(), workspaceID, generation.WorkspacePending, sandboxID)
	if err != nil {
		return fmt.Errorf("record generator workspace sandbox: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed == 1 {
		return nil
	}
	record, err := d.GetGeneratorWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	if record.State == generation.WorkspacePending && record.SandboxID == sandboxID {
		return nil
	}
	return fmt.Errorf("generator workspace cannot record sandbox from state %q", record.State)
}

func (d *GenerationRepository) ActivateGeneratorWorkspace(ctx context.Context, workspaceID, sandboxID string, now time.Time) error {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(sandboxID) == "" || now.IsZero() {
		return errors.New("generator workspace id, sandbox id, and current time are required")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE generator_workspaces
		SET sandbox_id = ?, state = ?, updated_at = ?
		WHERE workspace_id = ? AND state IN (?, ?) AND (sandbox_id = '' OR sandbox_id = ?)`,
		sandboxID, generation.WorkspaceActive, now.UTC(), workspaceID, generation.WorkspacePending, generation.WorkspaceActive, sandboxID)
	if err != nil {
		return fmt.Errorf("activate generator workspace: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed == 1 {
		return nil
	}
	record, err := d.GetGeneratorWorkspace(ctx, workspaceID)
	if err != nil {
		return err
	}
	if record.State == generation.WorkspaceActive && record.SandboxID == sandboxID {
		return nil
	}
	return fmt.Errorf("generator workspace cannot become active from state %q", record.State)
}

func (d *GenerationRepository) BeginGeneratorWorkspaceCleanup(ctx context.Context, workspaceID string, now time.Time) (*generation.Workspace, error) {
	if strings.TrimSpace(workspaceID) == "" || now.IsZero() {
		return nil, errors.New("generator workspace id and current time are required")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generator workspace cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := scanGeneratorWorkspace(tx.QueryRowContext(ctx, generatorWorkspaceSelect+` WHERE workspace_id = ? FOR UPDATE`, workspaceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrWorkspaceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock generator workspace cleanup: %w", err)
	}
	if record.State != generation.WorkspaceDeleted && record.State != generation.WorkspaceDeleting {
		if _, err := tx.ExecContext(ctx, `UPDATE generator_workspaces SET state = ?, active_turn_id = '', idle_since = NULL, updated_at = ? WHERE workspace_id = ?`, generation.WorkspaceDeleting, now.UTC(), workspaceID); err != nil {
			return nil, fmt.Errorf("mark generator workspace deleting: %w", err)
		}
		record.State = generation.WorkspaceDeleting
		record.ActiveTurnID = ""
		record.IdleSince = nil
		record.UpdatedAt = now.UTC()
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generator workspace cleanup: %w", err)
	}
	return record, nil
}

func (d *GenerationRepository) RetireCurrentGeneratorWorkspace(ctx context.Context, workflowID string, now time.Time) (*generation.Workspace, error) {
	if strings.TrimSpace(workflowID) == "" || now.IsZero() {
		return nil, errors.New("generation workflow id and current time are required")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin retire generator workspace: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := scanGeneratorWorkspace(tx.QueryRowContext(ctx, generatorWorkspaceSelect+` WHERE workflow_id = ? AND state IN (?, ?)
		ORDER BY created_at DESC, workspace_id DESC LIMIT 1 FOR UPDATE`, workflowID, generation.WorkspacePending, generation.WorkspaceActive))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrWorkspaceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock current generator workspace: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generator_workspaces SET state = ?, active_turn_id = '', idle_since = NULL, updated_at = ? WHERE workspace_id = ?`, generation.WorkspaceDeleting, now.UTC(), record.ID); err != nil {
		return nil, fmt.Errorf("retire generator workspace: %w", err)
	}
	record.State = generation.WorkspaceDeleting
	record.ActiveTurnID = ""
	record.IdleSince = nil
	record.UpdatedAt = now.UTC()
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit retire generator workspace: %w", err)
	}
	return record, nil
}

// RetireIncompleteGeneratorWorkspaces is startup recovery. Server never
// resumes a partially completed Generator call, so every non-terminal
// workspace is made eligible for asynchronous Sandbox/PVC cleanup.
func (d *GenerationRepository) RetireIncompleteGeneratorWorkspaces(ctx context.Context, now time.Time) ([]generation.Workspace, error) {
	if now.IsZero() {
		return nil, errors.New("current time is required")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin retire incomplete generator workspaces: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	records, err := listGeneratorWorkspaces(ctx, tx, `SELECT w.workspace_id, w.workflow_id, w.namespace, w.pvc_name, w.sandbox_id, w.active_turn_id, w.idle_since, w.state,
		w.provision_deadline, w.created_at, w.updated_at, w.deleted_at
		FROM generator_workspaces w
		JOIN generation_workflows workflow ON workflow.id = w.workflow_id
		WHERE w.state IN (?, ?) AND workflow.state NOT IN (?, ?, ?)
		ORDER BY w.created_at, w.workspace_id FOR UPDATE`,
		generation.WorkspacePending, generation.WorkspaceActive,
		generation.StatePublished, generation.StateFailed, generation.StateCancelled)
	if err != nil {
		return nil, err
	}
	for index := range records {
		if _, err := tx.ExecContext(ctx, `UPDATE generator_workspaces SET state = ?, active_turn_id = '', idle_since = NULL, updated_at = ? WHERE workspace_id = ?`,
			generation.WorkspaceDeleting, now.UTC(), records[index].ID); err != nil {
			return nil, fmt.Errorf("retire incomplete generator workspace: %w", err)
		}
		records[index].State = generation.WorkspaceDeleting
		records[index].ActiveTurnID = ""
		records[index].IdleSince = nil
		records[index].UpdatedAt = now.UTC()
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit retire incomplete generator workspaces: %w", err)
	}
	return records, nil
}

func (d *GenerationRepository) MarkGeneratorWorkspaceDeleted(ctx context.Context, workspaceID string, now time.Time) error {
	if strings.TrimSpace(workspaceID) == "" || now.IsZero() {
		return errors.New("generator workspace id and current time are required")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE generator_workspaces
		SET state = ?, updated_at = ?, deleted_at = ?
		WHERE workspace_id = ? AND state IN (?, ?)`,
		generation.WorkspaceDeleted, now.UTC(), now.UTC(), workspaceID, generation.WorkspaceDeleting, generation.WorkspaceDeleted)
	if err != nil {
		return fmt.Errorf("mark generator workspace deleted: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return generation.ErrWorkspaceNotFound
	}
	return nil
}

func (d *GenerationRepository) ListExpiredPendingGeneratorWorkspaces(ctx context.Context, now time.Time) ([]generation.Workspace, error) {
	if now.IsZero() {
		return nil, errors.New("current time is required")
	}
	return listGeneratorWorkspaces(ctx, d.conn, generatorWorkspaceSelect+` WHERE state = ? AND provision_deadline <= ? ORDER BY provision_deadline, workspace_id`, generation.WorkspacePending, now.UTC())
}

func (d *GenerationRepository) ListDeletingGeneratorWorkspaces(ctx context.Context) ([]generation.Workspace, error) {
	return listGeneratorWorkspaces(ctx, d.conn, generatorWorkspaceSelect+` WHERE state = ? ORDER BY updated_at, workspace_id`, generation.WorkspaceDeleting)
}

func (d *GenerationRepository) ListTerminalGeneratorWorkspaces(ctx context.Context) ([]generation.Workspace, error) {
	return listGeneratorWorkspaces(ctx, d.conn, `SELECT w.workspace_id, w.workflow_id, w.namespace, w.pvc_name, w.sandbox_id, w.active_turn_id, w.idle_since, w.state,
		w.provision_deadline, w.created_at, w.updated_at, w.deleted_at
		FROM generator_workspaces w
		JOIN generation_workflows workflow ON workflow.id = w.workflow_id
	WHERE w.state IN (?, ?) AND workflow.state IN (?, ?, ?)
		ORDER BY w.updated_at, w.workspace_id`,
		generation.WorkspacePending, generation.WorkspaceActive,
		generation.StatePublished, generation.StateFailed, generation.StateCancelled)
}

func (d *GenerationRepository) ListGeneratorWorkspaceSnapshotTargets(ctx context.Context) ([]generation.WorkspaceSnapshotTarget, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT w.workspace_id, w.workflow_id, w.namespace, w.pvc_name, w.sandbox_id, w.active_turn_id, w.idle_since,
		w.state, w.provision_deadline, w.created_at, w.updated_at, w.deleted_at, workflow.workspace_snapshot_digest
		FROM generator_workspaces w
		JOIN generation_workflows workflow ON workflow.id = w.workflow_id
		WHERE w.state = ? AND workflow.state = ?
		ORDER BY w.created_at, w.workspace_id`, generation.WorkspaceActive, generation.StateGenerating)
	if err != nil {
		return nil, fmt.Errorf("list generator workspace snapshot targets: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]generation.WorkspaceSnapshotTarget, 0)
	for rows.Next() {
		target, err := scanGeneratorWorkspaceSnapshotTarget(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate generator workspace snapshot targets: %w", err)
	}
	return result, nil
}

// AcquireGeneratorWorkspaceSnapshot uses the same row lock as a user turn.
// A current snapshot that is already counting idle time must not be replaced,
// because doing so would indefinitely refresh its retirement deadline.
func (d *GenerationRepository) AcquireGeneratorWorkspaceSnapshot(ctx context.Context, workflowID, holderID string, now time.Time) (*generation.WorkspaceSnapshotTarget, error) {
	if strings.TrimSpace(workflowID) == "" || strings.TrimSpace(holderID) == "" || now.IsZero() {
		return nil, generation.ErrWorkspaceTurnLost
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generator workspace snapshot acquire: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND state = ? FOR UPDATE`, workflowID, generation.StateGenerating))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrWorkspaceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock generator workspace snapshot workflow: %w", err)
	}
	record, err := scanGeneratorWorkspace(tx.QueryRowContext(ctx, generatorWorkspaceSelect+` WHERE workflow_id = ? AND state = ?
		ORDER BY created_at DESC, workspace_id DESC LIMIT 1 FOR UPDATE`, workflow.ID, generation.WorkspaceActive))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrWorkspaceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock generator workspace snapshot: %w", err)
	}
	if record.ActiveTurnID != "" {
		return nil, generation.ErrWorkspaceBusy
	}
	if record.IdleSince != nil && workflow.WorkspaceSnapshotDigest != "" {
		return nil, generation.ErrWorkspaceSnapshotCurrent
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generator_workspaces SET active_turn_id = ?, idle_since = NULL, updated_at = ? WHERE workspace_id = ?`,
		holderID, now.UTC(), record.ID); err != nil {
		return nil, fmt.Errorf("bind generator workspace snapshot holder: %w", err)
	}
	record.ActiveTurnID = holderID
	record.IdleSince = nil
	record.UpdatedAt = now.UTC()
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generator workspace snapshot acquire: %w", err)
	}
	return &generation.WorkspaceSnapshotTarget{Workspace: *record, SnapshotDigest: workflow.WorkspaceSnapshotDigest}, nil
}

// PublishGeneratorWorkspaceSnapshot atomically releases its holder and makes
// the immutable snapshot reachable. The holder remains active until this
// transaction succeeds, so no user turn can observe a half-published archive.
func (d *GenerationRepository) PublishGeneratorWorkspaceSnapshot(ctx context.Context, workspaceID, holderID, digest string, now time.Time) error {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(holderID) == "" || !generation.ValidSHA256(digest) || now.IsZero() {
		return generation.ErrWorkspaceTurnLost
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin generator workspace snapshot publish: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var workflowID string
	if err := tx.QueryRowContext(ctx, `SELECT workflow_id FROM generator_workspaces WHERE workspace_id = ?`, workspaceID).Scan(&workflowID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return generation.ErrWorkspaceTurnLost
		}
		return fmt.Errorf("read generator workspace snapshot publication owner: %w", err)
	}
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND state = ? FOR UPDATE`, workflowID, generation.StateGenerating))
	if errors.Is(err, sql.ErrNoRows) {
		return generation.ErrWorkspaceTurnLost
	}
	if err != nil {
		return fmt.Errorf("lock generator workflow snapshot publication: %w", err)
	}
	record, err := scanGeneratorWorkspace(tx.QueryRowContext(ctx, generatorWorkspaceSelect+` WHERE workspace_id = ? FOR UPDATE`, workspaceID))
	if errors.Is(err, sql.ErrNoRows) {
		return generation.ErrWorkspaceTurnLost
	}
	if err != nil {
		return fmt.Errorf("lock generator workspace snapshot publication: %w", err)
	}
	if record.WorkflowID != workflow.ID || record.State != generation.WorkspaceActive || record.ActiveTurnID != holderID {
		return generation.ErrWorkspaceTurnLost
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET workspace_snapshot_digest = ?, updated_at = ? WHERE id = ?`, digest, now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("publish generator workspace snapshot digest: %w", err)
	}
	idleSince := now.UTC()
	if workflow.WorkspaceSnapshotDigest == digest && record.IdleSince != nil {
		idleSince = record.IdleSince.UTC()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generator_workspaces SET active_turn_id = '', idle_since = ?, updated_at = ? WHERE workspace_id = ? AND active_turn_id = ?`,
		idleSince, now.UTC(), record.ID, holderID); err != nil {
		return fmt.Errorf("release generator workspace snapshot holder: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit generator workspace snapshot publish: %w", err)
	}
	return nil
}

// ClearGeneratorWorkspaceSnapshot drops one known-bad pointer. A mismatch is
// benign: another writer already published a newer snapshot.
func (d *GenerationRepository) ClearGeneratorWorkspaceSnapshot(ctx context.Context, workflowID, expectedDigest string, now time.Time) (bool, error) {
	if strings.TrimSpace(workflowID) == "" || !generation.ValidSHA256(expectedDigest) || now.IsZero() {
		return false, errors.New("generator workspace snapshot identity and current time are required")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin clear generator workspace snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND state = ? FOR UPDATE`, workflowID, generation.StateGenerating))
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit absent generator workspace snapshot: %w", err)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock generator workflow snapshot clear: %w", err)
	}
	if workflow.WorkspaceSnapshotDigest != expectedDigest {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit unchanged generator workspace snapshot: %w", err)
		}
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET workspace_snapshot_digest = '', updated_at = ? WHERE id = ?`, now.UTC(), workflow.ID); err != nil {
		return false, fmt.Errorf("clear generator workspace snapshot digest: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generator_workspaces SET idle_since = NULL, updated_at = ?
		WHERE workflow_id = ? AND state = ?`, now.UTC(), workflow.ID, generation.WorkspaceActive); err != nil {
		return false, fmt.Errorf("clear generator workspace snapshot idle state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit clear generator workspace snapshot: %w", err)
	}
	return true, nil
}

// RetireIdleGeneratorWorkspace only transitions durable state. The reaper is
// still solely responsible for deleting the corresponding Sandbox and PVC.
func (d *GenerationRepository) RetireIdleGeneratorWorkspace(ctx context.Context, workspaceID, expectedDigest string, staleBefore, now time.Time) (*generation.Workspace, error) {
	if strings.TrimSpace(workspaceID) == "" || !generation.ValidSHA256(expectedDigest) || staleBefore.IsZero() || now.IsZero() {
		return nil, generation.ErrWorkspaceNotIdle
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin retire idle generator workspace: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var workflowID string
	if err := tx.QueryRowContext(ctx, `SELECT workflow_id FROM generator_workspaces WHERE workspace_id = ?`, workspaceID).Scan(&workflowID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, generation.ErrWorkspaceNotIdle
		}
		return nil, fmt.Errorf("read idle generator workspace owner: %w", err)
	}
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND state = ? FOR UPDATE`, workflowID, generation.StateGenerating))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrWorkspaceNotIdle
	}
	if err != nil {
		return nil, fmt.Errorf("lock idle generator workspace workflow: %w", err)
	}
	record, err := scanGeneratorWorkspace(tx.QueryRowContext(ctx, generatorWorkspaceSelect+` WHERE workspace_id = ? FOR UPDATE`, workspaceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrWorkspaceNotIdle
	}
	if err != nil {
		return nil, fmt.Errorf("lock idle generator workspace: %w", err)
	}
	if record.WorkflowID != workflow.ID || workflow.WorkspaceSnapshotDigest != expectedDigest || record.State != generation.WorkspaceActive || record.ActiveTurnID != "" || record.IdleSince == nil || record.IdleSince.After(staleBefore.UTC()) {
		return nil, generation.ErrWorkspaceNotIdle
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generator_workspaces SET state = ?, active_turn_id = '', idle_since = NULL, updated_at = ? WHERE workspace_id = ?`,
		generation.WorkspaceDeleting, now.UTC(), record.ID); err != nil {
		return nil, fmt.Errorf("retire idle generator workspace: %w", err)
	}
	record.State = generation.WorkspaceDeleting
	record.ActiveTurnID = ""
	record.IdleSince = nil
	record.UpdatedAt = now.UTC()
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit retire idle generator workspace: %w", err)
	}
	return record, nil
}

func (d *GenerationRepository) ListGeneratorWorkspaceSnapshotReferences(ctx context.Context) ([]generation.WorkspaceSnapshotReference, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT id, workspace_snapshot_digest FROM generation_workflows
		WHERE workspace_snapshot_digest <> '' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list generator workspace snapshot references: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]generation.WorkspaceSnapshotReference, 0)
	for rows.Next() {
		var reference generation.WorkspaceSnapshotReference
		if err := rows.Scan(&reference.WorkflowID, &reference.Digest); err != nil {
			return nil, fmt.Errorf("scan generator workspace snapshot reference: %w", err)
		}
		result = append(result, reference)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate generator workspace snapshot references: %w", err)
	}
	return result, nil
}

const generatorWorkspaceColumns = `workspace_id, workflow_id, namespace, pvc_name, sandbox_id, active_turn_id, idle_since, state, provision_deadline, created_at, updated_at, deleted_at`
const generatorWorkspaceSelect = `SELECT ` + generatorWorkspaceColumns + ` FROM generator_workspaces`

type generatorWorkspaceQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listGeneratorWorkspaces(ctx context.Context, query generatorWorkspaceQuerier, statement string, args ...any) ([]generation.Workspace, error) {
	rows, err := query.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("list generator workspaces: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]generation.Workspace, 0)
	for rows.Next() {
		record, err := scanGeneratorWorkspace(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate generator workspaces: %w", err)
	}
	return result, nil
}

type generatorWorkspaceScanner interface {
	Scan(...any) error
}

func scanGeneratorWorkspace(row generatorWorkspaceScanner) (*generation.Workspace, error) {
	var record generation.Workspace
	var idleSince, deletedAt sql.NullTime
	if err := row.Scan(&record.ID, &record.WorkflowID, &record.Namespace, &record.PVCName, &record.SandboxID, &record.ActiveTurnID, &idleSince, &record.State,
		&record.ProvisionDeadline, &record.CreatedAt, &record.UpdatedAt, &deletedAt); err != nil {
		return nil, err
	}
	if idleSince.Valid {
		value := idleSince.Time.UTC()
		record.IdleSince = &value
	}
	if deletedAt.Valid {
		value := deletedAt.Time.UTC()
		record.DeletedAt = &value
	}
	return &record, nil
}

func scanGeneratorWorkspaceSnapshotTarget(row generatorWorkspaceScanner) (*generation.WorkspaceSnapshotTarget, error) {
	var target generation.WorkspaceSnapshotTarget
	var idleSince, deletedAt sql.NullTime
	if err := row.Scan(&target.Workspace.ID, &target.Workspace.WorkflowID, &target.Workspace.Namespace, &target.Workspace.PVCName,
		&target.Workspace.SandboxID, &target.Workspace.ActiveTurnID, &idleSince, &target.Workspace.State,
		&target.Workspace.ProvisionDeadline, &target.Workspace.CreatedAt, &target.Workspace.UpdatedAt, &deletedAt, &target.SnapshotDigest); err != nil {
		return nil, fmt.Errorf("scan generator workspace snapshot target: %w", err)
	}
	if idleSince.Valid {
		value := idleSince.Time.UTC()
		target.Workspace.IdleSince = &value
	}
	if deletedAt.Valid {
		value := deletedAt.Time.UTC()
		target.Workspace.DeletedAt = &value
	}
	return &target, nil
}
