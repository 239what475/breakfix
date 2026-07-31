package server

import (
	"context"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/generator"
	"github.com/breakfix/breakfix/internal/testpostgres"
)

func TestWorkDeadlineRecoveryReleasesExpiredGeneratorRunWithoutWorkerPolling(t *testing.T) {
	database := testpostgres.New(t)
	ctx := context.Background()
	const sessionID = "deadline-recovery-authoring"
	const userID = "deadline-recovery-user"
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: sessionID, UserID: userID}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	revision, err := database.ReplaceAuthoringPlan(ctx, sessionID, userID, 0, authoring.Plan{}, authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := database.StartGeneratorRun(ctx, sessionID, userID, revision.Number, agentruntime.CreateRun{
		ID: "deadline-recovery-run", Purpose: generator.RuntimePurpose, OwnerKind: "authoring-session", OwnerRef: sessionID,
		Model: "test-model", PromptVersion: "test-prompt", ExecutionTimeout: time.Second,
	}, generator.RunInput{AuthoringSessionID: sessionID, Revision: revision.Number})
	if err != nil {
		t.Fatal(err)
	}
	claimAt := time.Now().UTC().Add(time.Second).Truncate(time.Microsecond)
	claim, err := database.ClaimNext(ctx, "agent-worker", time.Minute, claimAt)
	if err != nil || claim == nil || claim.Run.ID != run.ID {
		t.Fatalf("claim agent run = %#v, %v", claim, err)
	}

	handler := NewHandler(database, nil, config.Config{})
	if err := handler.recoverExpiredWorkAt(ctx, claimAt.Add(2*time.Second)); err != nil {
		t.Fatalf("recover expired work = %v", err)
	}
	run, err = database.GetRun(ctx, claim.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != agentruntime.RunFailed || run.CompletedAt == nil || run.LastError != "agent run deadline exceeded" {
		t.Fatalf("expired run = %#v", run)
	}
	session, err := database.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if session.State != authoring.StateInfrastructureFailed || session.GeneratorRunID != "" || session.LastError != "agent run deadline exceeded" {
		t.Fatalf("expired generator authoring session = %#v", session)
	}
}
