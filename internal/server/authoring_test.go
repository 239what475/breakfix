package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/generator"
	"github.com/breakfix/breakfix/internal/testpostgres"
	"github.com/gin-gonic/gin"
)

func TestAuthoringAPIOnlyShowsVerifiedRevision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	root := t.TempDir()
	database := testpostgres.New(t)
	if _, err := database.CreateUserWithAuth("u-author", "author", "hash", "totp"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: "author-visible", UserID: "u-author"}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	intent := testAuthoringPlan("Intent title", "intent overview")
	first, err := database.ReplaceAuthoringPlan(ctx, "author-visible", "u-author", 0, intent, authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	firstRun, err := startTestGeneratorRun(ctx, database, "author-visible", "u-author", first.Number, "generator-one")
	if err != nil {
		t.Fatal(err)
	}
	artifactDir := authoring.ArtifactDirectory(root, "author-visible", first.Number)
	writeAuthoringArtifact(t, artifactDir, "Actual verified title", "actual verified description")
	if err := completeTestGeneratorVerification(ctx, database, "author-visible", firstRun, authoring.Artifact{
		SubmissionID: generator.SubmissionID(firstRun.ID), Directory: authoring.ArtifactRelativePath("author-visible", first.Number), GeneratorRunID: firstRun.ID,
	}, "verify-one"); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(database, nil, config.Config{DataDir: root})
	response := getAuthoringSessionResponse(t, handler, "author-visible")
	if response.VisibleRevision != first.Number || response.IntentRevision != first.Number {
		t.Fatalf("unexpected verified revisions: %#v", response)
	}
	if response.Verified == nil || response.Verified.Metadata.Title != "Actual verified title" || response.Intent.Metadata.Title != "Intent title" {
		t.Fatalf("API did not separate intent from verified artifact: %#v", response)
	}
	if len(response.Assets) == 0 || response.Artifact == nil {
		t.Fatalf("verified artifact was not exposed: %#v", response)
	}

	second, err := database.ReplaceAuthoringPlan(ctx, "author-visible", "u-author", first.Number, testAuthoringPlan("Unverified revision", "new intent"), authoring.StateRevisingAndVerifying)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := startTestGeneratorRun(ctx, database, "author-visible", "u-author", second.Number, "generator-two"); err != nil {
		t.Fatal(err)
	}
	response = getAuthoringSessionResponse(t, handler, "author-visible")
	if response.IntentRevision != second.Number || response.VisibleRevision != first.Number {
		t.Fatalf("revision boundaries leaked: %#v", response)
	}
	if response.Verified == nil || response.Verified.Metadata.Title != "Actual verified title" {
		t.Fatalf("unverified revision replaced visible artifact: %#v", response.Verified)
	}
	if response.Intent.Metadata.Title != "Intent title" {
		t.Fatalf("unverified intent leaked into visible content: %#v", response.Intent)
	}
}

func TestCurrentAuthoringSessionResumesOnlyUnpublishedWork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	root := t.TempDir()
	database := testpostgres.New(t)
	if _, err := database.CreateUserWithAuth("u-current", "current", "hash", "totp"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: "older", UserID: "u-current"}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: "newer", UserID: "u-current"}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(database, nil, config.Config{DataDir: root})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/authoring/sessions/current", nil)
	c.Set("user_id", "u-current")
	handler.GetCurrentAuthoringSession(c)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected current authoring session, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response authoringSessionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.ID != "newer" {
		t.Fatalf("expected latest session to resume, got %#v", response)
	}
}

func TestAuthoringPublishRecoversAfterFilesystemPromotion(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	database := testpostgres.New(t)
	if _, err := database.CreateUserWithAuth("u-publish", "publish", "hash", "totp"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: "author-publish", UserID: "u-publish"}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	revision, err := database.ReplaceAuthoringPlan(ctx, "author-publish", "u-publish", 0, testAuthoringPlan("Publish title", "publish overview"), authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	run, err := startTestGeneratorRun(ctx, database, "author-publish", "u-publish", revision.Number, "generator-publish")
	if err != nil {
		t.Fatal(err)
	}
	artifactDir := authoring.ArtifactDirectory(root, "author-publish", revision.Number)
	writeAuthoringArtifact(t, artifactDir, "Verified publish title", "verified publish description")
	artifact := authoring.Artifact{
		SubmissionID:   generator.SubmissionID(run.ID),
		Directory:      authoring.ArtifactRelativePath("author-publish", revision.Number),
		GeneratorRunID: run.ID,
	}
	if err := completeTestGeneratorVerification(ctx, database, "author-publish", run, artifact, "vt-publish"); err != nil {
		t.Fatal(err)
	}

	handler := NewHandler(database, nil, config.Config{DataDir: root})
	const challengeID = "chal-publish-recovery"
	if _, err := database.BeginPublish(ctx, "author-publish", "u-publish", revision.Number, challengeID); err != nil {
		t.Fatal(err)
	}
	// This is the exact crash window: the catalog rename finished before the
	// database could record the published session state.
	if _, err := challenge.PromoteDirectory(handler.challengesDir, artifactDir, challengeID, "registry.example/verify:latest"); err != nil {
		t.Fatal(err)
	}
	if err := handler.syncAuthoringSession(ctx, "author-publish"); err != nil {
		t.Fatal(err)
	}

	session, err := database.GetAuthoringSession(ctx, "author-publish", "u-publish")
	if err != nil {
		t.Fatal(err)
	}
	if session.State != authoring.StatePublished {
		t.Fatalf("filesystem-promoted revision was not recovered: %#v", session)
	}
	stored, err := database.GetAuthoringRevision(ctx, session.ID, revision.Number)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Verification == nil || stored.Verification.ChallengeID != challengeID {
		t.Fatalf("recovered publication did not persist challenge ID: %#v", stored)
	}
}

func startTestGeneratorRun(ctx context.Context, database *db.DB, sessionID, userID string, revision int64, runID string) (*agentruntime.Run, error) {
	_, run, err := database.StartGeneratorRun(ctx, sessionID, userID, revision, agentruntime.CreateRun{
		ID: runID, Purpose: generator.RuntimePurpose, OwnerKind: "authoring-session", OwnerRef: sessionID,
		Model: "test-model", PromptVersion: generator.PromptVersion, DeadlineAt: time.Now().UTC().Add(time.Hour),
	}, generator.RunInput{AuthoringSessionID: sessionID, Revision: revision})
	return run, err
}

func completeTestGeneratorVerification(ctx context.Context, database *db.DB, sessionID string, run *agentruntime.Run, artifact authoring.Artifact, taskID string) error {
	claim, err := database.ClaimNext(ctx, "test-generator-worker", time.Minute, time.Now().UTC())
	if err != nil {
		return err
	}
	if claim == nil || claim.Run.ID != run.ID {
		return fmt.Errorf("claim generator run %q", run.ID)
	}
	if err := database.FinalizeGeneratorSubmission(ctx, *claim, artifact.SubmissionID, taskID); err != nil {
		return err
	}
	return database.CompleteGeneratorVerification(ctx, sessionID, run.ID, artifact, authoring.Verification{
		TaskID: taskID, Phase: "Succeeded", Report: &authoring.VerificationReport{BuildPassed: true, AnswerPassed: true, CheckpointsPassed: true},
	})
}

func getAuthoringSessionResponse(t *testing.T, handler *Handler, sessionID string) authoringSessionResponse {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/authoring/sessions/"+sessionID, nil)
	c.Set("user_id", "u-author")
	handler.GetAuthoringSession(c, sessionID)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected authoring session, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response authoringSessionResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func testAuthoringPlan(title, overview string) authoring.Plan {
	return authoring.Plan{
		Metadata: authoring.Metadata{Title: title, Description: "intent description", Difficulty: "easy", Runtime: "container"},
		Overview: overview,
		Checkpoints: []authoring.Checkpoint{{
			ID: "service-ready", Title: "Service ready", Markdown: "The service is ready.", Position: 1,
		}},
	}
}

func writeAuthoringArtifact(t *testing.T, root, title, description string) {
	t.Helper()
	writeGatewayTestFile(t, filepath.Join(root, "challenge.yaml"), "title: "+title+"\ntype: script\nruntime: container\ndifficulty: medium\ndescription: "+description+"\ncheckpoints:\n  - id: service-ready\n    title: Service ready\n    description: The service responds successfully.\n    hint: hints/service-ready.md\n")
	writeGatewayTestFile(t, filepath.Join(root, "Dockerfile"), "FROM breakfix-base:latest\n")
	writeGatewayTestFile(t, filepath.Join(root, "generate.sh"), "#!/bin/sh\n")
	writeGatewayTestFile(t, filepath.Join(root, "problem.md"), "# Actual problem\n")
	writeGatewayTestFile(t, filepath.Join(root, "solution.md"), "# Actual solution\n")
	writeGatewayTestFile(t, filepath.Join(root, "hints", "service-ready.md"), "hint\n")
	writeGatewayTestFile(t, filepath.Join(root, "checks", "checkpoints.sh"), "#!/bin/sh\n")
	writeGatewayTestFile(t, filepath.Join(root, "answer.sh"), "#!/bin/sh\n")
}
