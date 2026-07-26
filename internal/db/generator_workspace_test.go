package db

import (
	"context"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/workspace"
)

func TestGeneratorWorkspacePersistsAgainstAgentSession(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	if _, err := database.CreateSession(ctx, agentruntime.Session{
		ID: "generator-session-one", Purpose: "generator", OwnerKind: "authoring-session", OwnerRef: "authoring-one", UserRef: "user-one",
	}); err != nil {
		t.Fatalf("create generator session: %v", err)
	}
	now := time.Now().UTC().Round(time.Microsecond)
	record, err := database.CreateGeneratorWorkspace(ctx, workspace.Record{
		GeneratorSessionID: "generator-session-one", Namespace: "opensandbox", PVCName: workspace.NewPVCName("generator-session-one"),
		State: workspace.StatePending, ProvisionDeadline: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create generator workspace: %v", err)
	}
	if record.State != workspace.StatePending {
		t.Fatalf("workspace state = %q", record.State)
	}
	if err := database.ActivateGeneratorWorkspace(ctx, record.GeneratorSessionID, "sandbox-one", now.Add(time.Second)); err != nil {
		t.Fatalf("activate workspace: %v", err)
	}
	if _, err := database.BeginGeneratorWorkspaceCleanup(ctx, record.GeneratorSessionID, now.Add(2*time.Second)); err != nil {
		t.Fatalf("begin workspace cleanup: %v", err)
	}
	if err := database.MarkGeneratorWorkspaceDeleted(ctx, record.GeneratorSessionID, now.Add(3*time.Second)); err != nil {
		t.Fatalf("mark workspace deleted: %v", err)
	}
	stored, err := database.GetGeneratorWorkspace(ctx, record.GeneratorSessionID)
	if err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if stored.State != workspace.StateDeleted || stored.SandboxID != "sandbox-one" || stored.DeletedAt == nil {
		t.Fatalf("stored workspace = %#v", stored)
	}
}
