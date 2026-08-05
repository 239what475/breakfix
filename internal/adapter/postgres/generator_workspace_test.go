package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/generation"
)

func TestGeneratorWorkspacePersistsAgainstWorkflow(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Round(time.Microsecond)
	workflow, _, _ := createGenerationWorkflowFixture(t, database, now)
	record, err := database.Generation.CreateGeneratorWorkspace(ctx, generation.Workspace{
		ID: "workspace-one", WorkflowID: workflow.ID, Namespace: "opensandbox", PVCName: generation.NewWorkspacePVCName("workspace-one"),
		State: generation.WorkspacePending, ProvisionDeadline: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create generator workspace: %v", err)
	}
	if err := database.Generation.ActivateGeneratorWorkspace(ctx, record.ID, "sandbox-one", now.Add(time.Second)); err != nil {
		t.Fatalf("activate workspace: %v", err)
	}
	current, err := database.Generation.GetCurrentGeneratorWorkspace(ctx, workflow.ID)
	if err != nil || current.ID != record.ID || current.SandboxID != "sandbox-one" {
		t.Fatalf("current workspace = %#v, err=%v", current, err)
	}
	retired, err := database.Generation.RetireCurrentGeneratorWorkspace(ctx, workflow.ID, now.Add(2*time.Second))
	if err != nil || retired.ID != record.ID || retired.State != generation.WorkspaceDeleting {
		t.Fatalf("retired workspace = %#v, err=%v", retired, err)
	}
	if err := database.Generation.MarkGeneratorWorkspaceDeleted(ctx, record.ID, now.Add(3*time.Second)); err != nil {
		t.Fatalf("mark workspace deleted: %v", err)
	}
	stored, err := database.Generation.GetGeneratorWorkspace(ctx, record.ID)
	if err != nil || stored.State != generation.WorkspaceDeleted || stored.DeletedAt == nil {
		t.Fatalf("stored workspace = %#v, err=%v", stored, err)
	}
}

func TestListTerminalGeneratorWorkspacesIncludesWorkflowWorkspace(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Round(time.Microsecond)
	workflow, _, _ := createGenerationWorkflowFixture(t, database, now)
	if _, err := database.Generation.CreateGeneratorWorkspace(ctx, generation.Workspace{
		ID: "workspace-terminal", WorkflowID: workflow.ID, Namespace: "opensandbox", PVCName: generation.NewWorkspacePVCName("workspace-terminal"),
		State: generation.WorkspacePending, ProvisionDeadline: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create pending workspace: %v", err)
	}
	if _, err := database.conn.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1, runtime_attempt = 0 WHERE id = ?`, generation.StateFailed, workflow.ID); err != nil {
		t.Fatalf("finish generation workflow: %v", err)
	}
	workspaces, err := database.Generation.ListTerminalGeneratorWorkspaces(ctx)
	if err != nil {
		t.Fatalf("list terminal workspaces: %v", err)
	}
	if len(workspaces) != 1 || workspaces[0].ID != "workspace-terminal" || workspaces[0].WorkflowID != workflow.ID {
		t.Fatalf("terminal workspaces = %#v", workspaces)
	}
}
