package db

import (
	"context"
	"errors"
	"testing"

	"github.com/breakfix/breakfix/internal/authoring"
)

func TestAuthoringOnlyExposesVerifiedArtifactAndPublishesExplicitly(t *testing.T) {
	ctx := context.Background()
	database := newTestDB(t)

	const sessionID = "author-test"
	const userID = "user-test"
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: sessionID, UserID: userID, AgentSessionID: "claude-session", WorkflowSessionID: "workflow-session"}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	first, err := database.ReplaceAuthoringPlan(ctx, sessionID, userID, 0, validAuthoringPlan("first overview"), authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.BeginGeneration(ctx, sessionID, userID, first.Number, "gen-first"); err != nil {
		t.Fatal(err)
	}
	if err := database.AttachVerificationTask(ctx, sessionID, "gen-first", first.Number, "vt-first"); err != nil {
		t.Fatal(err)
	}
	verification := authoring.Verification{TaskID: "vt-first", Phase: "Succeeded", Message: "verification passed", Report: &authoring.VerificationReport{BuildPassed: true, AnswerPassed: true, CheckpointsPassed: true}}
	artifact := authoring.Artifact{SubmissionID: "sub-first", Directory: "authoring/author-test/revisions/1/artifact", GenerationID: "gen-first"}
	if err := database.CompleteVerification(ctx, sessionID, "gen-first", artifact, verification); err != nil {
		t.Fatal(err)
	}

	session, err := database.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if session.State != authoring.StateAwaitingVerifiedReview || session.CurrentRevision != first.Number || session.VisibleRevision != first.Number {
		t.Fatalf("verified revision not made visible: %#v", session)
	}
	if session.WorkflowSessionID != "workflow-session" || session.WorkflowStarted {
		t.Fatalf("workflow session was not persisted correctly: %#v", session)
	}
	if err := database.SetAuthoringWorkflowStarted(ctx, sessionID); err != nil {
		t.Fatal(err)
	}
	session, err = database.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if !session.WorkflowStarted {
		t.Fatal("workflow session start was not persisted")
	}
	latest, err := database.GetLatestOpenAuthoringSession(ctx, userID)
	if err != nil || latest.ID != sessionID {
		t.Fatalf("open authoring session cannot be resumed: %#v, %v", latest, err)
	}
	stored, err := database.GetAuthoringRevision(ctx, sessionID, first.Number)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Artifact == nil || stored.Verification == nil || stored.Verification.Phase != "Succeeded" {
		t.Fatalf("verified artifact was not stored atomically: %#v", stored)
	}

	second, err := database.ReplaceAuthoringPlan(ctx, sessionID, userID, first.Number, validAuthoringPlan("revised overview"), authoring.StateRevisingAndVerifying)
	if err != nil {
		t.Fatal(err)
	}
	session, err = database.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if session.CurrentRevision != second.Number || session.VisibleRevision != first.Number || session.State != authoring.StateRevisingAndVerifying || session.GenerationID != "" || session.VerifyTaskID != "" {
		t.Fatalf("unverified revision leaked into visible state: %#v", session)
	}
	if err := database.CompleteVerification(ctx, sessionID, "gen-first", artifact, verification); !errors.Is(err, authoring.ErrInvalidState) {
		t.Fatalf("old VerifyTask must not verify a new revision, got %v", err)
	}

	if _, err := database.BeginGeneration(ctx, sessionID, userID, second.Number, "gen-second"); err != nil {
		t.Fatal(err)
	}
	if err := database.AttachVerificationTask(ctx, sessionID, "gen-second", second.Number, "vt-second"); err != nil {
		t.Fatal(err)
	}
	if err := database.CompleteVerification(ctx, sessionID, "gen-second", authoring.Artifact{SubmissionID: "sub-second", Directory: "authoring/author-test/revisions/2/artifact", GenerationID: "gen-second"}, authoring.Verification{TaskID: "vt-second", Phase: "Succeeded"}); err != nil {
		t.Fatal(err)
	}

	publish, err := database.BeginPublish(ctx, sessionID, userID, second.Number, "chal-opaque")
	if err != nil {
		t.Fatal(err)
	}
	if publish.Artifact == nil || publish.Verification == nil {
		t.Fatalf("publish did not lock verified artifact: %#v", publish)
	}
	if err := database.CompletePublish(ctx, sessionID, userID, second.Number, "chal-opaque"); err != nil {
		t.Fatal(err)
	}
	session, err = database.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if session.State != authoring.StatePublished || session.PublishChallengeID != "chal-opaque" {
		t.Fatalf("published state is inconsistent: %#v", session)
	}
	stored, err = database.GetAuthoringRevision(ctx, sessionID, second.Number)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Verification == nil || stored.Verification.ChallengeID != "chal-opaque" {
		t.Fatalf("published catalog reference was not retained: %#v", stored)
	}
	if _, err := database.GetLatestOpenAuthoringSession(ctx, userID); err != authoring.ErrNotFound {
		t.Fatalf("published session must not be resumed as open: %v", err)
	}
}

func TestAuthoringVerificationFailureRestartsExactlyOnceWithoutArtifact(t *testing.T) {
	ctx := context.Background()
	database := newTestDB(t)

	const sessionID = "author-repair"
	const userID = "user-repair"
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: sessionID, UserID: userID, AgentSessionID: "claude-session"}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	revision, err := database.ReplaceAuthoringPlan(ctx, sessionID, userID, 0, validAuthoringPlan("overview"), authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.BeginGeneration(ctx, sessionID, userID, revision.Number, "gen-failed"); err != nil {
		t.Fatal(err)
	}
	if err := database.AttachVerificationTask(ctx, sessionID, "gen-failed", revision.Number, "vt-failed"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.RestartGeneration(ctx, sessionID, "gen-failed", "vt-failed", "gen-repair", "checkpoints did not pass"); err != nil {
		t.Fatal(err)
	}
	session, err := database.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if session.GenerationID != "gen-repair" || session.VerifyTaskID != "" || session.PendingFeedback != "checkpoints did not pass" || session.VisibleRevision != 0 || session.State != authoring.StateGeneratingAndVerifying {
		t.Fatalf("failure recovery exposed an artifact or left stale task state: %#v", session)
	}
	if _, err := database.RestartGeneration(ctx, sessionID, "gen-failed", "vt-failed", "gen-duplicate", "duplicate"); err == nil {
		t.Fatal("same failed VerifyTask started a duplicate generation")
	}
}

func validAuthoringPlan(overview string) authoring.Plan {
	return authoring.Plan{
		Metadata: authoring.Metadata{
			Title: "Repair service configuration", Description: "Repair a service configuration and verify health.",
			Difficulty: "easy", Runtime: "container",
		},
		Overview:    overview,
		Checkpoints: []authoring.Checkpoint{{ID: "health", Title: "Health endpoint works", Markdown: "The health endpoint returns success.", Position: 1}},
	}
}
