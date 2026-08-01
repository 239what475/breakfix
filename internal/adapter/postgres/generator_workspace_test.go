package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

func TestGeneratorWorkspacePersistsAgainstGeneratorRun(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	if _, err := database.Agent.CreateRun(ctx, agent.CreateRun{
		ID: "generator-run-one", Purpose: "generator", OwnerKind: "authoring-session", OwnerRef: "authoring-one",
		Model: "test", PromptVersion: "test",
	}); err != nil {
		t.Fatalf("create generator run: %v", err)
	}
	now := time.Now().UTC().Round(time.Microsecond)
	record, err := database.Generation.CreateGeneratorWorkspace(ctx, generation.Workspace{
		GeneratorRunID: "generator-run-one", Namespace: "opensandbox", PVCName: generation.NewWorkspacePVCName("generator-run-one"),
		State: generation.WorkspacePending, ProvisionDeadline: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create generator workspace: %v", err)
	}
	if record.State != generation.WorkspacePending {
		t.Fatalf("workspace state = %q", record.State)
	}
	if err := database.Generation.ActivateGeneratorWorkspace(ctx, record.GeneratorRunID, "sandbox-one", now.Add(time.Second)); err != nil {
		t.Fatalf("activate workspace: %v", err)
	}
	if _, err := database.Generation.BeginGeneratorWorkspaceCleanup(ctx, record.GeneratorRunID, now.Add(2*time.Second)); err != nil {
		t.Fatalf("begin workspace cleanup: %v", err)
	}
	if err := database.Generation.MarkGeneratorWorkspaceDeleted(ctx, record.GeneratorRunID, now.Add(3*time.Second)); err != nil {
		t.Fatalf("mark workspace deleted: %v", err)
	}
	stored, err := database.Generation.GetGeneratorWorkspace(ctx, record.GeneratorRunID)
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if stored.State != generation.WorkspaceDeleted || stored.SandboxID != "sandbox-one" || stored.DeletedAt == nil {
		t.Fatalf("stored workspace = %#v", stored)
	}
}

func TestListTerminalGeneratorWorkspacesIncludesPendingWorkspace(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Round(time.Microsecond)
	run, err := database.Agent.CreateRun(ctx, agent.CreateRun{
		ID: "generator-run-pending", Purpose: "generator", OwnerKind: "authoring-session", OwnerRef: "authoring-one",
		Model: "test", PromptVersion: "test",
	})
	if err != nil {
		t.Fatalf("create generator run: %v", err)
	}
	if _, err := database.Generation.CreateGeneratorWorkspace(ctx, generation.Workspace{
		GeneratorRunID: "generator-run-pending", Namespace: "opensandbox", PVCName: generation.NewWorkspacePVCName("generator-run-pending"),
		State: generation.WorkspacePending, ProvisionDeadline: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create pending workspace: %v", err)
	}
	if err := database.Agent.FailRun(ctx, run.ID, "workspace cleanup", now.Add(time.Second)); err != nil {
		t.Fatalf("finish generator run: %v", err)
	}
	workspaces, err := database.Generation.ListTerminalGeneratorWorkspaces(ctx)
	if err != nil {
		t.Fatalf("list terminal workspaces: %v", err)
	}
	if len(workspaces) != 1 || workspaces[0].GeneratorRunID != "generator-run-pending" || workspaces[0].State != generation.WorkspacePending {
		t.Fatalf("terminal workspaces = %#v", workspaces)
	}
}
