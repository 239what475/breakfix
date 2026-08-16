package generation

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/workspacearchive"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

func TestWorkspaceSnapshotterSnapshotsAfterTurnAndRetiresOnlyAfterTTL(t *testing.T) {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	manager := newWorkspaceManager(t, repo, &memoryWorkspacePVCs{}, &memoryWorkspaceSandboxes{nextID: "sandbox-snapshot"}, &now)
	workspace, err := manager.Ensure(context.Background(), "workflow-snapshot", nil)
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	archive := snapshotArchive(t, "draft: one\n")
	archiver := &snapshotArchiver{archive: archive}
	snapshotter, err := NewWorkspaceSnapshotter(manager, archiver, WorkspaceSnapshotterConfig{DataDir: t.TempDir(), IdleTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	snapshotter.now = func() time.Time { return now }

	if err := snapshotter.SnapshotWorkflow(context.Background(), "workflow-snapshot"); err != nil {
		t.Fatalf("snapshot workspace: %v", err)
	}
	targets, err := repo.ListGeneratorWorkspaceSnapshotTargets(context.Background())
	if err != nil || len(targets) != 1 || targets[0].SnapshotDigest == "" || targets[0].Workspace.IdleSince == nil {
		t.Fatalf("snapshot target = %#v, err=%v", targets, err)
	}
	firstIdleSince := *targets[0].Workspace.IdleSince
	if archiver.calls != 1 {
		t.Fatalf("archive calls = %d, want 1", archiver.calls)
	}
	if _, err := snapshotter.store.Read("workflow-snapshot", targets[0].SnapshotDigest); err != nil {
		t.Fatalf("read published snapshot: %v", err)
	}

	now = now.Add(30 * time.Minute)
	if err := snapshotter.SnapshotDue(context.Background()); err != nil {
		t.Fatalf("snapshot current workspace: %v", err)
	}
	targets, _ = repo.ListGeneratorWorkspaceSnapshotTargets(context.Background())
	if archiver.calls != 1 || !targets[0].Workspace.IdleSince.Equal(firstIdleSince) {
		t.Fatalf("current snapshot was refreshed: calls=%d idle=%v", archiver.calls, targets[0].Workspace.IdleSince)
	}

	turn := domain.WorkspaceTurn{WorkflowID: "workflow-snapshot", ID: "authoring-turn"}
	if _, err := repo.AcquireGeneratorWorkspaceTurn(context.Background(), turn, now); err != nil {
		t.Fatalf("acquire authoring turn: %v", err)
	}
	if _, err := repo.RetireIdleGeneratorWorkspace(context.Background(), workspace.ID, targets[0].SnapshotDigest, now, now); !errors.Is(err, domain.ErrWorkspaceNotIdle) {
		t.Fatalf("retire active turn = %v, want not idle", err)
	}
	if err := repo.ReleaseGeneratorWorkspaceTurn(context.Background(), turn, now); err != nil {
		t.Fatalf("release authoring turn: %v", err)
	}
	if err := snapshotter.SnapshotWorkflow(context.Background(), "workflow-snapshot"); err != nil {
		t.Fatalf("snapshot after turn release: %v", err)
	}
	targets, _ = repo.ListGeneratorWorkspaceSnapshotTargets(context.Background())
	if archiver.calls != 2 || targets[0].Workspace.IdleSince == nil || !targets[0].Workspace.IdleSince.Equal(now) {
		t.Fatalf("resnapshot target = %#v calls=%d", targets[0], archiver.calls)
	}

	now = now.Add(time.Hour)
	if err := snapshotter.SnapshotDue(context.Background()); err != nil {
		t.Fatalf("retire idle workspace: %v", err)
	}
	record, err := repo.GetGeneratorWorkspace(context.Background(), workspace.ID)
	if err != nil || record.State != domain.WorkspaceDeleting {
		t.Fatalf("retired workspace = %#v, err=%v", record, err)
	}
}

func TestWorkspaceSnapshotterReleasesHolderWhenArchivingFails(t *testing.T) {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	manager := newWorkspaceManager(t, repo, &memoryWorkspacePVCs{}, &memoryWorkspaceSandboxes{nextID: "sandbox-failing"}, &now)
	workspace, err := manager.Ensure(context.Background(), "workflow-failing-snapshot", nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, err := NewWorkspaceSnapshotter(manager, &snapshotArchiver{err: errors.New("provider unavailable")}, WorkspaceSnapshotterConfig{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	snapshotter.now = func() time.Time { return now }
	if err := snapshotter.SnapshotWorkflow(context.Background(), "workflow-failing-snapshot"); err == nil {
		t.Fatal("snapshot unexpectedly succeeded")
	}
	record, err := repo.GetGeneratorWorkspace(context.Background(), workspace.ID)
	if err != nil || record.ActiveTurnID != "" || record.IdleSince != nil {
		t.Fatalf("workspace after failed snapshot = %#v, err=%v", record, err)
	}
}

func TestWorkspaceSnapshotterContinuesAfterOneWorkspaceFails(t *testing.T) {
	now := time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-unavailable"}
	manager := newWorkspaceManager(t, repo, &memoryWorkspacePVCs{}, sandboxes, &now)
	if _, err := manager.Ensure(context.Background(), "workflow-unavailable", nil); err != nil {
		t.Fatalf("create unavailable workspace: %v", err)
	}
	sandboxes.nextID = "sandbox-available"
	if _, err := manager.Ensure(context.Background(), "workflow-available", nil); err != nil {
		t.Fatalf("create available workspace: %v", err)
	}
	archiver := &snapshotArchiverBySandbox{
		archives: map[string][]byte{"sandbox-available": snapshotArchive(t, "saved\n")},
		errors:   map[string]error{"sandbox-unavailable": errors.New("provider unavailable")},
	}
	snapshotter, err := NewWorkspaceSnapshotter(manager, archiver, WorkspaceSnapshotterConfig{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	snapshotter.now = func() time.Time { return now }

	if err := snapshotter.SnapshotDue(context.Background()); err == nil {
		t.Fatal("periodic snapshot unexpectedly hid provider failure")
	}
	targets, err := repo.ListGeneratorWorkspaceSnapshotTargets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		switch target.Workspace.WorkflowID {
		case "workflow-unavailable":
			if target.SnapshotDigest != "" || target.Workspace.IdleSince != nil {
				t.Fatalf("failed workspace was published: %#v", target)
			}
		case "workflow-available":
			if target.SnapshotDigest == "" || target.Workspace.IdleSince == nil {
				t.Fatalf("healthy workspace was not published: %#v", target)
			}
		}
	}
}

type snapshotArchiver struct {
	archive []byte
	err     error
	calls   int
}

type snapshotArchiverBySandbox struct {
	archives map[string][]byte
	errors   map[string]error
}

func (a *snapshotArchiverBySandbox) ArchiveWorkspace(_ context.Context, sandboxID string) ([]byte, error) {
	if err := a.errors[sandboxID]; err != nil {
		return nil, err
	}
	return append([]byte(nil), a.archives[sandboxID]...), nil
}

func (a *snapshotArchiver) ArchiveWorkspace(context.Context, string) ([]byte, error) {
	a.calls++
	if a.err != nil {
		return nil, a.err
	}
	return append([]byte(nil), a.archive...), nil
}

func snapshotArchive(t *testing.T, content string) []byte {
	t.Helper()
	archive, err := workspacearchive.Encode([]workspacearchive.Entry{{Path: "draft.md", Content: []byte(content)}})
	if err != nil {
		t.Fatal(err)
	}
	return archive
}

func TestWorkspaceSnapshotStoreCanonicalizesProviderBytes(t *testing.T) {
	store, err := workspacearchive.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	archive := snapshotArchive(t, "draft\n")
	canonical, digest, err := store.Save("workflow-canonical", archive)
	if err != nil {
		t.Fatal(err)
	}
	read, err := store.Read("workflow-canonical", digest)
	if err != nil || !bytes.Equal(read, canonical) {
		t.Fatalf("stored snapshot = %q, err=%v", read, err)
	}
}
