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
		(workspace_id, workflow_id, namespace, pvc_name, sandbox_id, state, provision_deadline, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (workspace_id) DO NOTHING`,
		record.ID, record.WorkflowID, record.Namespace, record.PVCName, record.SandboxID, record.State,
		record.ProvisionDeadline, record.CreatedAt, record.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create generator workspace: %w", err)
	}
	existing, err := d.GetGeneratorWorkspace(ctx, record.ID)
	if err != nil {
		return nil, err
	}
	if existing.WorkflowID != record.WorkflowID || existing.Namespace != record.Namespace || existing.PVCName != record.PVCName {
		return nil, fmt.Errorf("generator workspace already has different ownership")
	}
	return existing, nil
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
		if _, err := tx.ExecContext(ctx, `UPDATE generator_workspaces SET state = ?, updated_at = ? WHERE workspace_id = ?`, generation.WorkspaceDeleting, now.UTC(), workspaceID); err != nil {
			return nil, fmt.Errorf("mark generator workspace deleting: %w", err)
		}
		record.State = generation.WorkspaceDeleting
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
	if _, err := tx.ExecContext(ctx, `UPDATE generator_workspaces SET state = ?, updated_at = ? WHERE workspace_id = ?`, generation.WorkspaceDeleting, now.UTC(), record.ID); err != nil {
		return nil, fmt.Errorf("retire generator workspace: %w", err)
	}
	record.State = generation.WorkspaceDeleting
	record.UpdatedAt = now.UTC()
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit retire generator workspace: %w", err)
	}
	return record, nil
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
	return listGeneratorWorkspaces(ctx, d.conn, `SELECT w.workspace_id, w.workflow_id, w.namespace, w.pvc_name, w.sandbox_id, w.state,
		w.provision_deadline, w.created_at, w.updated_at, w.deleted_at
		FROM generator_workspaces w
		JOIN generation_workflows workflow ON workflow.id = w.workflow_id
	WHERE w.state IN (?, ?) AND workflow.state IN (?, ?, ?)
		ORDER BY w.updated_at, w.workspace_id`,
		generation.WorkspacePending, generation.WorkspaceActive,
		generation.StatePublished, generation.StateFailed, generation.StateCancelled)
}

const generatorWorkspaceSelect = `SELECT workspace_id, workflow_id, namespace, pvc_name, sandbox_id, state, provision_deadline, created_at, updated_at, deleted_at FROM generator_workspaces`

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
	var deletedAt sql.NullTime
	if err := row.Scan(&record.ID, &record.WorkflowID, &record.Namespace, &record.PVCName, &record.SandboxID, &record.State,
		&record.ProvisionDeadline, &record.CreatedAt, &record.UpdatedAt, &deletedAt); err != nil {
		return nil, err
	}
	if deletedAt.Valid {
		value := deletedAt.Time.UTC()
		record.DeletedAt = &value
	}
	return &record, nil
}
