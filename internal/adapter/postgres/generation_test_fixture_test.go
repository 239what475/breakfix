package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

const workflowTestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func createGenerationWorkflowFixture(t *testing.T, database *Store, now time.Time) (*generation.Workflow, string, string) {
	t.Helper()
	ctx := context.Background()
	userID := authoring.NewID("workflow-user")
	if _, err := database.Identity.CreateUserWithAuth(userID, userID, "", ""); err != nil {
		t.Fatalf("create author: %v", err)
	}
	plan := authoring.Plan{Metadata: authoring.Metadata{Title: "Workspace workflow", Description: "Exercise durable generator workspace lifecycle.", Runtime: "node"}, Overview: "Generate an Operations scenario.", Checkpoints: []authoring.Checkpoint{{ID: "ready", Title: "Ready", Markdown: "The workspace is ready.", Position: 1}}}
	session, err := database.Authoring.CreateAuthoringSession(ctx, authoring.Session{ID: authoring.NewID("authoring"), UserID: userID}, plan)
	if err != nil {
		t.Fatalf("create authoring session: %v", err)
	}
	if _, err := database.Authoring.ReplaceAuthoringPlan(ctx, session.ID, userID, 0, plan, authoring.StateIntentReview); err != nil {
		t.Fatalf("confirm authoring plan: %v", err)
	}
	workflow, err := database.Generation.CreateGenerationWorkflow(ctx, session.ID, userID, generation.StartConfirmation{PlanRevision: 1, IdempotencyKey: "start-workflow"}, now)
	if err != nil {
		t.Fatalf("create generation workflow: %v", err)
	}
	return workflow, session.ID, userID
}

func workspaceTestSource() runnable.SourceArchive {
	return runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "runnable-sources/workspace-test.tar.gz", Digest: workflowTestDigest}
}
