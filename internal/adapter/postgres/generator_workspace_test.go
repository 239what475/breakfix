package postgres

import (
	"context"
	"errors"
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

func TestGeneratorWorkspaceTurnIsSingleWriterAndRestartRetiresIt(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Round(time.Microsecond)
	workflow, _, _ := createGenerationWorkflowFixture(t, database, now)
	record, err := database.Generation.CreateGeneratorWorkspace(ctx, generation.Workspace{
		ID: "workspace-turn", WorkflowID: workflow.ID, Namespace: "opensandbox", PVCName: generation.NewWorkspacePVCName("workspace-turn"),
		State: generation.WorkspacePending, ProvisionDeadline: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := database.Generation.ActivateGeneratorWorkspace(ctx, record.ID, "sandbox-turn", now); err != nil {
		t.Fatalf("activate workspace: %v", err)
	}
	first := generation.WorkspaceTurn{WorkflowID: workflow.ID, ID: "turn-one"}
	if _, err := database.Generation.AcquireGeneratorWorkspaceTurn(ctx, first, now); err != nil {
		t.Fatalf("acquire first workspace turn: %v", err)
	}
	if _, err := database.Generation.AcquireGeneratorWorkspaceTurn(ctx, generation.WorkspaceTurn{WorkflowID: workflow.ID, ID: "turn-two"}, now); !errors.Is(err, generation.ErrWorkspaceBusy) {
		t.Fatalf("acquire concurrent workspace turn = %v, want busy", err)
	}
	retired, err := database.Generation.RetireIncompleteGeneratorWorkspaces(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatalf("retire restarted workspace: %v", err)
	}
	if len(retired) != 1 || retired[0].ID != record.ID || retired[0].State != generation.WorkspaceDeleting || retired[0].ActiveTurnID != "" {
		t.Fatalf("retired workspaces = %#v", retired)
	}
	if _, err := database.Generation.GetGeneratorWorkspaceForTurn(ctx, first); !errors.Is(err, generation.ErrWorkspaceTurnLost) {
		t.Fatalf("read retired workspace turn = %v, want lost", err)
	}
}

func TestSubmitGenerationCandidateIsIdempotentAndReleasesWorkspaceTurn(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Round(time.Microsecond)
	workflow, sessionID, userID := createGenerationWorkflowFixture(t, database, now)
	record, err := database.Generation.CreateGeneratorWorkspace(ctx, generation.Workspace{
		ID: "workspace-submit", WorkflowID: workflow.ID, Namespace: "opensandbox", PVCName: generation.NewWorkspacePVCName("workspace-submit"),
		State: generation.WorkspacePending, ProvisionDeadline: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := database.Generation.ActivateGeneratorWorkspace(ctx, record.ID, "sandbox-submit", now); err != nil {
		t.Fatalf("activate workspace: %v", err)
	}
	submission := generation.CandidateSubmission{WorkflowID: workflow.ID, TurnID: "turn-submit", IdempotencyKey: "submit-candidate"}
	if _, err := database.Generation.AcquireGeneratorWorkspaceTurn(ctx, generation.WorkspaceTurn{WorkflowID: workflow.ID, ID: submission.TurnID}, now); err != nil {
		t.Fatalf("acquire workspace turn: %v", err)
	}
	revision := generation.Revision{
		ID: "candidate-submit", ArchivePath: "/tmp/candidate-submit.tar.gz", ArchiveSHA256: workflowTestDigest, Snapshot: generationTestSnapshot(),
	}
	first, err := database.Generation.SubmitGenerationCandidate(ctx, sessionID, userID, submission, revision, now)
	if err != nil {
		t.Fatalf("submit candidate: %v", err)
	}
	second, err := database.Generation.SubmitGenerationCandidate(ctx, sessionID, userID, submission, generation.Revision{
		ID: "candidate-ignored", ArchivePath: "/tmp/candidate-ignored.tar.gz", ArchiveSHA256: workflowTestDigest, Snapshot: generationTestSnapshot(),
	}, now.Add(time.Second))
	if err != nil {
		t.Fatalf("repeat candidate submission: %v", err)
	}
	if first.ID != "candidate-submit" || second.ID != first.ID || first.Source != workflow.Source || first.SourceRevision != workflow.SourceRevision {
		t.Fatalf("submitted revisions = first:%#v second:%#v", first, second)
	}
	stored, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("read submitted workflow: %v", err)
	}
	if stored.State != generation.StateJudging || stored.CandidateRevisionID != first.ID {
		t.Fatalf("submitted workflow = %#v", stored)
	}
	workspace, err := database.Generation.GetCurrentGeneratorWorkspace(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("read submitted workspace: %v", err)
	}
	if workspace.ActiveTurnID != "" {
		t.Fatalf("submitted workspace remains bound to %q", workspace.ActiveTurnID)
	}
}
