package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/authoring"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
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
	input := json.RawMessage(`{"base_revision":0}`)
	stage, run, err := database.StartAuthoringRun(ctx, session.ID, session.UserID, agentruntime.Message{
		ID: "author-user", Role: "user", Content: "设计一个明确的服务修复题",
	}, agentruntime.CreateRun{
		ID: "author-run", SessionID: session.RuntimeSessionID, Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: session.ID,
		Input: input, Model: "deepseek-v4-pro", PromptVersion: "authoring-v1", DeadlineAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if stage.BaseRevision != 0 || stage.StageRevision != 0 || run.Status != agentruntime.RunPending {
		t.Fatalf("initial authoring stage = %#v, run=%#v", stage, run)
	}
	claim, err := database.ClaimNext(ctx, "author-worker", time.Minute, time.Now().UTC())
	if err != nil || claim == nil || claim.Run.ID != run.ID {
		t.Fatalf("claim authoring run = %#v, %v", claim, err)
	}
	plan := validAuthoringPlan("修复损坏的服务配置并验证健康检查。")
	change := authoring.Change{Kind: "full-plan", Summary: "补全题目约定和检查点", DifficultyImpact: "难度不变"}
	stage, err = database.UpdateAuthoringStage(ctx, *claim, stage.StageRevision, plan, change)
	if err != nil {
		t.Fatal(err)
	}
	if stage.StageRevision != 1 || len(stage.Changes) != 1 || stage.Changes[0].Revision != 1 {
		t.Fatalf("updated private stage = %#v", stage)
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
	stored, err := database.GetAuthoringSession(ctx, session.ID, session.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CurrentRevision != 1 || stored.State != authoring.StateIntentReview {
		t.Fatalf("finalized authoring session = %#v", stored)
	}
	messages, err := database.ListMessages(ctx, session.RuntimeSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Role != "assistant" {
		t.Fatalf("runtime messages = %#v", messages)
	}
	var metadata struct {
		Changes []authoring.Change `json:"changes"`
	}
	if err := json.Unmarshal(messages[1].Metadata, &metadata); err != nil || len(metadata.Changes) != 1 {
		t.Fatalf("final authoring metadata = %q, %v", messages[1].Metadata, err)
	}
}

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

func TestAuthoringInfrastructureVerificationFailureDoesNotRestartGeneration(t *testing.T) {
	ctx := context.Background()
	database := newTestDB(t)

	const sessionID = "author-infrastructure"
	const userID = "user-infrastructure"
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: sessionID, UserID: userID}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	revision, err := database.ReplaceAuthoringPlan(ctx, sessionID, userID, 0, validAuthoringPlan("overview"), authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.BeginGeneration(ctx, sessionID, userID, revision.Number, "gen-infrastructure"); err != nil {
		t.Fatal(err)
	}
	if err := database.AttachVerificationTask(ctx, sessionID, "gen-infrastructure", revision.Number, "vt-infrastructure"); err != nil {
		t.Fatal(err)
	}
	verification := authoring.Verification{
		TaskID: "vt-infrastructure", Phase: string(breakfixv1.VerifyTaskFailed), Message: "registry unavailable",
		Report: &authoring.VerificationReport{Class: authoring.VerificationFailureInfrastructure, Summary: "registry unavailable"},
	}
	if err := database.RecordVerificationInfrastructureFailure(ctx, sessionID, "gen-infrastructure", verification); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordVerificationInfrastructureFailure(ctx, sessionID, "gen-infrastructure", verification); err != nil {
		t.Fatalf("recording the same task must be idempotent: %v", err)
	}
	session, err := database.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if session.State != authoring.StateVerificationInfrastructureFailed || session.GenerationID != "gen-infrastructure" || session.VerifyTaskID != "vt-infrastructure" {
		t.Fatalf("infrastructure failure must preserve the workflow references without restart: %#v", session)
	}
	stored, err := database.GetAuthoringRevision(ctx, sessionID, revision.Number)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Verification == nil || stored.Verification.Report == nil || stored.Verification.Report.Class != authoring.VerificationFailureInfrastructure {
		t.Fatalf("infrastructure report was not retained: %#v", stored.Verification)
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
