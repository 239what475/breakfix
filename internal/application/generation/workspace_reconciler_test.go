package generation

import (
	"context"
	"testing"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

func TestWorkspaceReconcilerDropsDeletingCRAndCompletesRow(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	pvcs := &memoryWorkspacePVCs{}
	owners := &memoryWorkspaceOwners{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-one"}
	manager := newWorkspaceManagerWithOwners(t, repo, pvcs, sandboxes, owners, &now)

	record, err := manager.Ensure(context.Background(), "workflow-one", []byte("seed"))
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	if err := manager.Retire(context.Background(), "workflow-one"); err != nil {
		t.Fatalf("retire workspace: %v", err)
	}
	reconcile(t, manager)

	deleted, err := repo.GetGeneratorWorkspace(context.Background(), record.ID)
	if err != nil || deleted.State != domain.WorkspaceDeleted {
		t.Fatalf("dropped row = %#v, err=%v", deleted, err)
	}
	if _, exists := owners.owners[record.ID]; exists {
		t.Fatal("owner survived the drop")
	}
	if sandboxes.deleted != 1 {
		t.Fatalf("sandbox deletion count = %d, want 1", sandboxes.deleted)
	}
	if pvcs.ownerPatches != 1 {
		t.Fatalf("pvc owner hardening count = %d, want 1", pvcs.ownerPatches)
	}
}

// A row that completed while the owner delete failed must not leak its CR:
// the reconciler retries the external cleanup before removing the owner.
func TestWorkspaceReconcilerDropsResidueOwnerForDeletedRow(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	pvcs := &memoryWorkspacePVCs{}
	owners := &memoryWorkspaceOwners{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-one"}
	manager := newWorkspaceManagerWithOwners(t, repo, pvcs, sandboxes, owners, &now)

	record, err := manager.Ensure(context.Background(), "workflow-one", []byte("seed"))
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	if err := repo.MarkGeneratorWorkspaceDeleted(context.Background(), record.ID, now); err != nil {
		t.Fatalf("mark row deleted: %v", err)
	}
	reconcile(t, manager)

	if _, exists := owners.owners[record.ID]; exists {
		t.Fatal("residue owner survived the reconciler")
	}
	if sandboxes.deleted != 1 {
		t.Fatalf("residue sandbox deletion count = %d, want 1", sandboxes.deleted)
	}
}

func TestWorkspaceReconcilerBackfillsMissingOwnerFromLiveRow(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	pvcs := &memoryWorkspacePVCs{}
	owners := &memoryWorkspaceOwners{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-one"}
	manager := newWorkspaceManagerWithOwners(t, repo, pvcs, sandboxes, owners, &now)

	record, err := manager.Ensure(context.Background(), "workflow-one", []byte("seed"))
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	// Simulate a workspace that predates the ownership transfer: the row is
	// live but has no CR.
	delete(owners.owners, record.ID)
	owners.created = 0

	reconcile(t, manager)

	if owners.created != 1 {
		t.Fatalf("backfilled owner count = %d, want 1", owners.created)
	}
	owner := owners.owners[record.ID]
	if owner.State != domain.WorkspaceActive || owner.SandboxID != "sandbox-one" || owner.WorkflowID != "workflow-one" {
		t.Fatalf("backfilled owner = %#v", owner)
	}
	intact, err := repo.GetGeneratorWorkspace(context.Background(), record.ID)
	if err != nil || intact.State != domain.WorkspaceActive {
		t.Fatalf("backfill disturbed the live row: %#v, err=%v", intact, err)
	}
}

// A CR without ownership facts has no Server-provisioned resources; an
// explicit delete must converge instead of wedging on the cleanup finalizer.
func TestWorkspaceReconcilerReleasesTerminatingOwnerWithoutFacts(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	pvcs := &memoryWorkspacePVCs{}
	owners := &memoryWorkspaceOwners{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-one"}
	manager := newWorkspaceManagerWithOwners(t, repo, pvcs, sandboxes, owners, &now)

	owners.owners = map[string]domain.WorkspaceOwner{
		"generator-workspace-broken": {ID: "generator-workspace-broken", Terminating: true},
	}

	reconcile(t, manager)

	if _, exists := owners.owners["generator-workspace-broken"]; exists {
		t.Fatal("terminating owner without facts survived the reconciler")
	}
	if owners.deleted != 1 {
		t.Fatalf("owner deletion count = %d, want 1", owners.deleted)
	}
	if sandboxes.deleted != 0 || pvcs.ownerPatches != 0 {
		t.Fatalf("a fact-less owner must not trigger resource drops: sandboxes=%d patches=%d", sandboxes.deleted, pvcs.ownerPatches)
	}
}

func TestWorkspaceLeakSanitizerDeletesOnlyUnownedSandboxes(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	pvcs := &memoryWorkspacePVCs{}
	owners := &memoryWorkspaceOwners{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-owned"}
	manager := newWorkspaceManagerWithOwners(t, repo, pvcs, sandboxes, owners, &now)

	record, err := manager.Ensure(context.Background(), "workflow-one", []byte("seed"))
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	// A workspace the database forgot leaves its Sandbox behind; the CR set
	// no longer claims it, so the sanitizer deletes it. An unrelated live
	// workspace's Sandbox stays untouched.
	sandboxes.workspaces["generator-workspace-stray"] = "sandbox-stray"

	sanitizer := &WorkspaceLeakSanitizer{manager: manager}
	if err := sanitizer.SanitizeOnce(context.Background()); err != nil {
		t.Fatalf("sanitize: %v", err)
	}
	if sandboxes.deleted != 1 {
		t.Fatalf("sanitizer deletion count = %d, want 1", sandboxes.deleted)
	}
	if _, exists := sandboxes.workspaces[record.ID]; !exists {
		t.Fatal("owned sandbox was deleted by the sanitizer")
	}
	// Deleting the same stray again is a no-op once it is gone.
	if err := sanitizer.SanitizeOnce(context.Background()); err != nil {
		t.Fatalf("second sanitize: %v", err)
	}
	if sandboxes.deleted != 1 {
		t.Fatalf("second sanitizer deletion count = %d, want 1", sandboxes.deleted)
	}
}

func TestWorkspaceReconcilerTickJoinsReconcileAndSanitize(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	pvcs := &memoryWorkspacePVCs{}
	owners := &memoryWorkspaceOwners{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-one"}
	manager := newWorkspaceManagerWithOwners(t, repo, pvcs, sandboxes, owners, &now)

	record, err := manager.Ensure(context.Background(), "workflow-one", []byte("seed"))
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	if err := manager.Retire(context.Background(), "workflow-one"); err != nil {
		t.Fatalf("retire workspace: %v", err)
	}
	sandboxes.workspaces["generator-workspace-stray"] = "sandbox-stray"

	reconciler, err := NewWorkspaceReconciler(manager)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.tick(context.Background()); err != nil {
		t.Fatalf("reconciler tick: %v", err)
	}
	deleted, err := repo.GetGeneratorWorkspace(context.Background(), record.ID)
	if err != nil || deleted.State != domain.WorkspaceDeleted {
		t.Fatalf("dropped row = %#v, err=%v", deleted, err)
	}
	if _, exists := owners.owners[record.ID]; exists {
		t.Fatal("owner survived the tick")
	}
	if _, exists := sandboxes.workspaces["generator-workspace-stray"]; exists {
		t.Fatal("stray sandbox survived the tick")
	}
	if sandboxes.deleted != 2 {
		t.Fatalf("sandbox deletion count = %d, want 2 (drop plus stray)", sandboxes.deleted)
	}
}
