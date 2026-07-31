package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/generator"
	"github.com/breakfix/breakfix/internal/worklist"
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
		Input: json.RawMessage(`{"base_revision":0}`), Model: "deepseek-v4-pro", PromptVersion: "authoring-v1", ExecutionTimeout: time.Hour,
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

func TestCandidateVerificationOnlyPublishesCurrentAuthoringRevision(t *testing.T) {
	ctx := context.Background()
	database := newTestDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	const sessionID = "author-generator"
	const userID = "generator-user"
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: sessionID, UserID: userID}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	first, err := database.ReplaceAuthoringPlan(ctx, sessionID, userID, 0, validAuthoringPlan("first overview"), authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	firstCandidate, _ := finalizeTestCandidate(t, database, sessionID, userID, first.Number, "generator-first", generator.RunInput{AuthoringSessionID: sessionID, Revision: first.Number}, now)
	completeTestCandidatePipeline(t, database, firstCandidate.ID, now.Add(3*time.Second))

	second, err := database.ReplaceAuthoringPlan(ctx, sessionID, userID, first.Number, validAuthoringPlan("revised overview"), authoring.StateRevisingAndVerifying)
	if err != nil {
		t.Fatal(err)
	}
	session, err := database.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if session.State != authoring.StateRevisingAndVerifying || session.GeneratorRunID != "" || session.CandidateRevisionID != "" || session.VisibleRevision != first.Number {
		t.Fatalf("revised session did not detach old verification: %#v", session)
	}
	storedFirst, err := database.GetCandidateRevision(ctx, firstCandidate.ID)
	if err != nil || storedFirst.State != candidate.StateVerified {
		t.Fatalf("previous verified candidate changed while revising: %#v, %v", storedFirst, err)
	}
	input := generator.RunInput{AuthoringSessionID: sessionID, Revision: second.Number, SeedCandidateRevisionID: firstCandidate.ID}
	updated, secondRun, err := database.StartGeneratorRun(ctx, sessionID, userID, second.Number, generatorCreateRun("generator-second", sessionID), input)
	if err != nil {
		t.Fatal(err)
	}
	if updated.GeneratorSessionID == session.GeneratorSessionID {
		t.Fatal("author revision reused the previous generator session")
	}
	if secondRun.SessionID != updated.GeneratorSessionID {
		t.Fatalf("second run session = %q, want %q", secondRun.SessionID, updated.GeneratorSessionID)
	}
	replayed, replayedRun, err := database.StartGeneratorRun(ctx, sessionID, userID, second.Number, generatorCreateRun("generator-second-replayed", sessionID), input)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.GeneratorRunID != secondRun.ID || replayedRun.ID != secondRun.ID {
		t.Fatalf("replayed revision created a duplicate run: session=%#v run=%#v", replayed, replayedRun)
	}
}

func TestCandidateInfrastructureFailureDoesNotStartRepairRun(t *testing.T) {
	ctx := context.Background()
	database := newTestDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	const sessionID = "author-infrastructure"
	const userID = "infrastructure-user"
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: sessionID, UserID: userID}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	revision, err := database.ReplaceAuthoringPlan(ctx, sessionID, userID, 0, validAuthoringPlan("overview"), authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	revisionCandidate, run := finalizeTestCandidate(t, database, sessionID, userID, revision.Number, "generator-infrastructure", generator.RunInput{AuthoringSessionID: sessionID, Revision: revision.Number}, now)
	claim := claimCandidateStage(t, database, worklist.KindBuild, now.Add(3*time.Second))
	failure := candidate.Failure{Class: candidate.FailureInfrastructure, Code: "INCUS_UNAVAILABLE", Summary: "Incus provider unavailable"}
	if err := database.FailCandidateInfrastructure(ctx, claim.Work, failure, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	session, err := database.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if session.State != authoring.StateInfrastructureFailed || session.GeneratorRunID != run.ID || session.CandidateRevisionID != revisionCandidate.ID || session.LastError != failure.Summary {
		t.Fatalf("infrastructure failure state = %#v", session)
	}
	if active, err := database.GetActiveRunForSession(ctx, run.SessionID); !errors.Is(err, agentruntime.ErrNotFound) || active != nil {
		t.Fatalf("infrastructure failure unexpectedly created a repair run: %#v, %v", active, err)
	}
	cleanup, err := database.GetWorkItemForSubject(ctx, worklist.KindArtifactCleanup, worklist.SubjectCandidateRevision, revisionCandidate.ID)
	if err != nil || cleanup.State != worklist.StatePending {
		t.Fatalf("infrastructure cleanup = %#v, %v", cleanup, err)
	}
}

func finalizeTestCandidate(t *testing.T, database *DB, sessionID, userID string, revision int64, runID string, input generator.RunInput, now time.Time) (*candidate.Revision, *agentruntime.Run) {
	t.Helper()
	_, run, err := database.StartGeneratorRun(context.Background(), sessionID, userID, revision, generatorCreateRun(runID, sessionID), input)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := database.ClaimNext(context.Background(), "generator-worker", time.Minute, now.Add(time.Second))
	if err != nil || claim == nil || claim.Run.ID != run.ID {
		t.Fatalf("claim generator run = %#v, %v", claim, err)
	}
	revisionCandidate := &candidate.Revision{
		ID: candidate.IDForGeneratorRun(run.ID), AuthoringSessionID: sessionID, AuthoringRevision: revision,
		GeneratorSessionID: run.SessionID, GeneratorRunID: run.ID, JudgeRunID: run.ID,
		ArchivePath:   "/server/candidates/" + candidate.IDForGeneratorRun(run.ID) + "/candidate.tar.gz",
		ArchiveSHA256: "sha256:" + fullHex('a'), Snapshot: nodeSnapshot(), State: candidate.StateBuilding,
	}
	if err := database.FinalizeGeneratorCandidate(context.Background(), *claim, *revisionCandidate, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	return revisionCandidate, run
}

func completeTestCandidatePipeline(t *testing.T, database *DB, candidateID string, now time.Time) {
	t.Helper()
	buildClaim := claimCandidateStage(t, database, worklist.KindBuild, now)
	build := candidate.BuildOutput{Runtime: challenge.RuntimeNode, Incus: &candidate.IncusBuildReference{
		Project: "breakfix-build", WorkItemID: buildClaim.Work.Item.ID, Attempt: int64(buildClaim.Work.Item.Attempt),
		InstanceName: "build-instance", Alias: "candidate-build", Fingerprint: fullHex('b'),
	}}
	if err := database.CompleteCandidateBuild(context.Background(), buildClaim.Work, build, now.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	publishClaim := claimCandidateStage(t, database, worklist.KindArtifactPublish, now.Add(2*time.Millisecond))
	artifact := candidate.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: "candidate-published", IncusFingerprint: fullHex('c')}
	if err := database.CompleteCandidateArtifactPublish(context.Background(), publishClaim.Work, artifact, now.Add(3*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	verifyClaim := claimCandidateStage(t, database, worklist.KindVerify, now.Add(4*time.Millisecond))
	if verifyClaim.Candidate.ID != candidateID {
		t.Fatalf("claimed candidate = %q, want %q", verifyClaim.Candidate.ID, candidateID)
	}
	if err := database.CompleteCandidateVerification(context.Background(), verifyClaim.Work, passedNodeReport(), now.Add(5*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
}

func validAuthoringPlan(overview string) authoring.Plan {
	return authoring.Plan{
		Metadata:    authoring.Metadata{Title: "Fix service", Description: "Repair a broken service", Difficulty: "medium", Runtime: "node"},
		Overview:    overview,
		Checkpoints: []authoring.Checkpoint{{ID: "service-ready", Title: "Service ready", Markdown: "The service responds successfully.", Position: 1}},
	}
}
