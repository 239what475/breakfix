package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

func (d *DB) CreateGeneratorWorkspace(ctx context.Context, record generation.Workspace) (*generation.Workspace, error) {
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
		(generator_run_id, namespace, pvc_name, sandbox_id, state, provision_deadline, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (generator_run_id) DO NOTHING`,
		record.GeneratorRunID, record.Namespace, record.PVCName, record.SandboxID, record.State,
		record.ProvisionDeadline, record.CreatedAt, record.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("create generator workspace: %w", err)
	}
	existing, err := d.GetGeneratorWorkspace(ctx, record.GeneratorRunID)
	if err != nil {
		return nil, err
	}
	if existing.Namespace != record.Namespace || existing.PVCName != record.PVCName {
		return nil, fmt.Errorf("generator run already has a different workspace pvc")
	}
	return existing, nil
}

func (d *DB) GetGeneratorWorkspace(ctx context.Context, generatorRunID string) (*generation.Workspace, error) {
	record, err := scanGeneratorWorkspace(d.conn.QueryRowContext(ctx, generatorWorkspaceSelect+` WHERE generator_run_id = ?`, strings.TrimSpace(generatorRunID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrWorkspaceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get generator workspace: %w", err)
	}
	return record, nil
}

// RecordGeneratorWorkspaceSandbox persists the ID returned by the lifecycle
// API before Server waits for readiness. A retry therefore reconnects to this
// exact Sandbox rather than issuing another create request.
func (d *DB) RecordGeneratorWorkspaceSandbox(ctx context.Context, generatorRunID, sandboxID string, now time.Time) error {
	if strings.TrimSpace(generatorRunID) == "" || strings.TrimSpace(sandboxID) == "" || now.IsZero() {
		return errors.New("generator workspace run, sandbox id, and current time are required")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE generator_workspaces
		SET sandbox_id = ?, updated_at = ?
		WHERE generator_run_id = ? AND state = ? AND (sandbox_id = '' OR sandbox_id = ?)`,
		sandboxID, now, generatorRunID, generation.WorkspacePending, sandboxID)
	if err != nil {
		return fmt.Errorf("record generator workspace sandbox: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed == 1 {
		return nil
	}
	record, err := d.GetGeneratorWorkspace(ctx, generatorRunID)
	if err != nil {
		return err
	}
	if record.State == generation.WorkspacePending && record.SandboxID == sandboxID {
		return nil
	}
	return fmt.Errorf("generator workspace cannot record sandbox from state %q", record.State)
}

func (d *DB) ActivateGeneratorWorkspace(ctx context.Context, generatorRunID, sandboxID string, now time.Time) error {
	if strings.TrimSpace(generatorRunID) == "" || strings.TrimSpace(sandboxID) == "" || now.IsZero() {
		return errors.New("generator workspace run, sandbox id, and current time are required")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE generator_workspaces
		SET sandbox_id = ?, state = ?, updated_at = ?
		WHERE generator_run_id = ? AND state IN (?, ?) AND (sandbox_id = '' OR sandbox_id = ?)`,
		sandboxID, generation.WorkspaceActive, now, generatorRunID, generation.WorkspacePending, generation.WorkspaceActive, sandboxID)
	if err != nil {
		return fmt.Errorf("activate generator workspace: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed == 1 {
		return nil
	}
	record, err := d.GetGeneratorWorkspace(ctx, generatorRunID)
	if err != nil {
		return err
	}
	if record.State == generation.WorkspaceActive && record.SandboxID == sandboxID {
		return nil
	}
	return fmt.Errorf("generator workspace cannot become active from state %q", record.State)
}

func (d *DB) BeginGeneratorWorkspaceCleanup(ctx context.Context, generatorRunID string, now time.Time) (*generation.Workspace, error) {
	if strings.TrimSpace(generatorRunID) == "" || now.IsZero() {
		return nil, errors.New("generator workspace run and current time are required")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generator workspace cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := scanGeneratorWorkspace(tx.QueryRowContext(ctx, generatorWorkspaceSelect+` WHERE generator_run_id = ? FOR UPDATE`, generatorRunID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrWorkspaceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock generator workspace cleanup: %w", err)
	}
	if record.State != generation.WorkspaceDeleted && record.State != generation.WorkspaceDeleting {
		if _, err := tx.ExecContext(ctx, `UPDATE generator_workspaces SET state = ?, updated_at = ? WHERE generator_run_id = ?`, generation.WorkspaceDeleting, now, generatorRunID); err != nil {
			return nil, fmt.Errorf("mark generator workspace deleting: %w", err)
		}
		record.State = generation.WorkspaceDeleting
		record.UpdatedAt = now
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generator workspace cleanup: %w", err)
	}
	return record, nil
}

func (d *DB) MarkGeneratorWorkspaceDeleted(ctx context.Context, generatorRunID string, now time.Time) error {
	if strings.TrimSpace(generatorRunID) == "" || now.IsZero() {
		return errors.New("generator workspace run and current time are required")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE generator_workspaces
		SET state = ?, updated_at = ?, deleted_at = ?
		WHERE generator_run_id = ? AND state IN (?, ?)`,
		generation.WorkspaceDeleted, now, now, generatorRunID, generation.WorkspaceDeleting, generation.WorkspaceDeleted)
	if err != nil {
		return fmt.Errorf("mark generator workspace deleted: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return generation.ErrWorkspaceNotFound
	}
	return nil
}

func (d *DB) ListExpiredPendingGeneratorWorkspaces(ctx context.Context, now time.Time) ([]generation.Workspace, error) {
	if now.IsZero() {
		return nil, errors.New("current time is required")
	}
	return listGeneratorWorkspaces(ctx, d.conn, generatorWorkspaceSelect+` WHERE state = ? AND provision_deadline <= ? ORDER BY provision_deadline, generator_run_id`, generation.WorkspacePending, now)
}

func (d *DB) ListDeletingGeneratorWorkspaces(ctx context.Context) ([]generation.Workspace, error) {
	return listGeneratorWorkspaces(ctx, d.conn, generatorWorkspaceSelect+` WHERE state = ? ORDER BY updated_at, generator_run_id`, generation.WorkspaceDeleting)
}

func (d *DB) ListTerminalGeneratorWorkspaces(ctx context.Context) ([]generation.Workspace, error) {
	return listGeneratorWorkspaces(ctx, d.conn, `SELECT w.generator_run_id, w.namespace, w.pvc_name, w.sandbox_id, w.state,
		w.provision_deadline, w.created_at, w.updated_at, w.deleted_at
		FROM generator_workspaces w
		JOIN agent_runs r ON r.id = w.generator_run_id
		WHERE w.state IN (?, ?) AND r.status IN (?, ?, ?)
		ORDER BY w.updated_at, w.generator_run_id`,
		generation.WorkspacePending, generation.WorkspaceActive, agent.RunSucceeded, agent.RunFailed, agent.RunCancelled)
}

const generatorWorkspaceSelect = `SELECT generator_run_id, namespace, pvc_name, sandbox_id, state, provision_deadline, created_at, updated_at, deleted_at FROM generator_workspaces`

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
	if err := row.Scan(&record.GeneratorRunID, &record.Namespace, &record.PVCName, &record.SandboxID, &record.State,
		&record.ProvisionDeadline, &record.CreatedAt, &record.UpdatedAt, &deletedAt); err != nil {
		return nil, err
	}
	if deletedAt.Valid {
		value := deletedAt.Time
		record.DeletedAt = &value
	}
	return &record, nil
}
