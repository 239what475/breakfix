package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/generator"
)

func TestAuthoringRunStagesThenAtomicallyFinalizesOneRevision(t *testing.T) {
	ctx := context.Background()
	database := newTestDB(t)
	session, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: "author-stage", UserID: "user-stage"}, authoring.Plan{})
	if err != nil {
		t.Fatal(err)
	}
	if session.RuntimeSessionID == "" {
		t.Fatal("authoring runtime session was not created")
	}
	stage, run, err := database.StartAuthoringRun(ctx, session.ID, session.UserID, agentruntime.Message{
		ID: "author-user", Role: "user", Content: "设计一个明确的服务修复题",
	}, agentruntime.CreateRun{
		ID: "author-run", SessionID: session.RuntimeSessionID, Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: session.ID,
		Input: json.RawMessage(`{"base_revision":0}`), Model: "deepseek-v4-pro", PromptVersion: "authoring-v1", DeadlineAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := database.ClaimNext(ctx, "author-worker", time.Minute, time.Now().UTC())
	if err != nil || claim == nil || claim.Run.ID != run.ID {
		t.Fatalf("claim authoring run = %#v, %v", claim, err)
	}
	plan := validAuthoringPlan("修复损坏的服务配置并验证健康检查。")
	stage, err = database.UpdateAuthoringStage(ctx, *claim, stage.StageRevision, plan, authoring.Change{Kind: "full-plan", Summary: "补全题目约定和检查点", DifficultyImpact: "难度不变"})
	if err != nil {
		t.Fatal(err)
	}
	if stage.StageRevision != 1 {
		t.Fatalf("stage revision = %d, want 1", stage.StageRevision)
	}
	revision, err := database.FinalizeAuthoringRun(ctx, *claim, "题意约定已更新，请审核左侧方案。")
	if err != nil {
		t.Fatal(err)
	}
	if revision.Number != 1 || revision.Plan.Overview != plan.Overview {
		t.Fatalf("finalized revision = %#v", revision)
	}
	if _, err := database.GetAuthoringStage(ctx, run.ID); !errors.Is(err, authoring.ErrNotFound) {
		t.Fatalf("private stage remained after finalization: %v", err)
	}
	messages, err := database.ListMessages(ctx, session.RuntimeSessionID)
	if err != nil || len(messages) != 2 || messages[1].Role != "assistant" {
		t.Fatalf("runtime messages = %#v, %v", messages, err)
	}
}

func TestGeneratorVerificationOnlyPublishesCurrentGeneratorRun(t *testing.T) {
	ctx := context.Background()
	database := newTestDB(t)
	const sessionID = "author-generator"
	const userID = "generator-user"
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: sessionID, UserID: userID}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	first, err := database.ReplaceAuthoringPlan(ctx, sessionID, userID, 0, validAuthoringPlan("first overview"), authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	_, firstRun, err := database.StartGeneratorRun(ctx, sessionID, userID, first.Number, generatorCreateRun("generator-first", sessionID), generator.RunInput{AuthoringSessionID: sessionID, Revision: first.Number})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := database.ClaimNext(ctx, "generator-worker", time.Minute, time.Now().UTC())
	if err != nil || claim == nil {
		t.Fatalf("claim first generator run: %#v, %v", claim, err)
	}
	if err := database.FinalizeGeneratorSubmission(ctx, *claim, generator.SubmissionID(firstRun.ID), "verify-first"); err != nil {
		t.Fatal(err)
	}
	verification := authoring.Verification{TaskID: "verify-first", Phase: "Succeeded", Report: &authoring.VerificationReport{BuildPassed: true, AnswerPassed: true, CheckpointsPassed: true}}
	artifact := authoring.Artifact{SubmissionID: generator.SubmissionID(firstRun.ID), Directory: "authoring/author-generator/revisions/1/artifact", GeneratorRunID: firstRun.ID}
	if err := database.CompleteGeneratorVerification(ctx, sessionID, firstRun.ID, artifact, verification); err != nil {
		t.Fatal(err)
	}

	second, err := database.ReplaceAuthoringPlan(ctx, sessionID, userID, first.Number, validAuthoringPlan("revised overview"), authoring.StateRevisingAndVerifying)
	if err != nil {
		t.Fatal(err)
	}
	session, err := database.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if session.State != authoring.StateRevisingAndVerifying || session.GeneratorRunID != "" || session.VerifyTaskID != "" || session.VisibleRevision != first.Number {
		t.Fatalf("revised session did not detach old verification: %#v", session)
	}
	if err := database.CompleteGeneratorVerification(ctx, sessionID, firstRun.ID, artifact, verification); !errors.Is(err, authoring.ErrInvalidState) {
		t.Fatalf("old verify task accepted a new revision: %v", err)
	}
	updated, secondRun, err := database.StartGeneratorRun(ctx, sessionID, userID, second.Number, generatorCreateRun("generator-second", sessionID), generator.RunInput{AuthoringSessionID: sessionID, Revision: second.Number, SeedSubmissionID: artifact.SubmissionID})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GeneratorSessionID == session.GeneratorSessionID {
		t.Fatal("author revision reused the previous generator session")
	}
	if secondRun.SessionID != updated.GeneratorSessionID {
		t.Fatalf("second run session = %q, want %q", secondRun.SessionID, updated.GeneratorSessionID)
	}
}

func TestGeneratorInfrastructureFailureDoesNotStartRepairRun(t *testing.T) {
	ctx := context.Background()
	database := newTestDB(t)
	const sessionID = "author-infrastructure"
	const userID = "infrastructure-user"
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: sessionID, UserID: userID}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	revision, err := database.ReplaceAuthoringPlan(ctx, sessionID, userID, 0, validAuthoringPlan("overview"), authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := database.StartGeneratorRun(ctx, sessionID, userID, revision.Number, generatorCreateRun("generator-infrastructure", sessionID), generator.RunInput{AuthoringSessionID: sessionID, Revision: revision.Number})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := database.ClaimNext(ctx, "generator-worker", time.Minute, time.Now().UTC())
	if err != nil || claim == nil {
		t.Fatalf("claim generator run: %#v, %v", claim, err)
	}
	if err := database.FinalizeGeneratorSubmission(ctx, *claim, generator.SubmissionID(run.ID), "verify-infrastructure"); err != nil {
		t.Fatal(err)
	}
	verification := authoring.Verification{TaskID: "verify-infrastructure", Phase: "Failed", Report: &authoring.VerificationReport{Class: authoring.VerificationFailureInfrastructure, Summary: "registry unavailable"}}
	if err := database.RecordGeneratorVerificationInfrastructureFailure(ctx, sessionID, run.ID, verification); err != nil {
		t.Fatal(err)
	}
	session, err := database.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if session.State != authoring.StateVerificationInfrastructureFailed || session.GeneratorRunID != run.ID || session.VerifyTaskID != verification.TaskID {
		t.Fatalf("infrastructure failure state = %#v", session)
	}
}

func validAuthoringPlan(overview string) authoring.Plan {
	return authoring.Plan{
		Metadata:    authoring.Metadata{Title: "Fix service", Description: "Repair a broken service", Difficulty: "medium", Runtime: "container"},
		Overview:    overview,
		Checkpoints: []authoring.Checkpoint{{ID: "service-ready", Title: "Service ready", Markdown: "The service responds successfully.", Position: 1}},
	}
}
