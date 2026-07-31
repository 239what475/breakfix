package db

import (
	"context"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/generator"
	"github.com/breakfix/breakfix/internal/worklist"
)

func TestCandidateArtifactFailureAtomicallyStartsRepairAndCleanup(t *testing.T) {
	ctx := context.Background()
	database := newTestDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	const sessionID = "author-generator"
	const userID = "generator-user"
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: sessionID, UserID: userID}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	revision, err := database.ReplaceAuthoringPlan(ctx, sessionID, userID, 0, validAuthoringPlan("generator repair"), authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}

	firstCandidate, firstRun := finalizeTestCandidate(t, database, sessionID, userID, revision.Number, "generator-run-one", generator.RunInput{
		AuthoringSessionID: sessionID, Revision: revision.Number,
	}, now)
	firstRecord, err := database.GetGeneratorRun(ctx, firstRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstRecord.CandidateRevisionID != firstCandidate.ID {
		t.Fatalf("first generator candidate = %q, want %q", firstRecord.CandidateRevisionID, firstCandidate.ID)
	}

	buildClaim := claimCandidateStage(t, database, worklist.KindBuild, now.Add(3*time.Second))
	build := candidate.BuildOutput{Runtime: challenge.RuntimeNode, Incus: &candidate.IncusBuildReference{
		Project: "breakfix-build", WorkItemID: buildClaim.Work.Item.ID, Attempt: int64(buildClaim.Work.Item.Attempt),
		InstanceName: "repair-build", Alias: "repair-build", Fingerprint: fullHex('b'),
	}}
	if err := database.CompleteCandidateBuild(ctx, buildClaim.Work, build, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	publishClaim := claimCandidateStage(t, database, worklist.KindArtifactPublish, now.Add(5*time.Second))
	artifact := candidate.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: "repair-artifact", IncusFingerprint: fullHex('c')}
	if err := database.CompleteCandidateArtifactPublish(ctx, publishClaim.Work, artifact, now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	verifyClaim := claimCandidateStage(t, database, worklist.KindVerify, now.Add(7*time.Second))
	report := candidate.VerificationReport{
		Passed: false, Summary: "answer failed",
		Answers:     []candidate.ExecutionResult{{Location: "proxy", ExitCode: 1, Stderr: "invalid nginx configuration"}},
		Checkpoints: []candidate.CheckpointResult{{ID: "proxy-ready", Passed: false, Summary: "proxy unavailable"}},
	}
	failure := candidate.Failure{Class: candidate.FailureArtifact, Code: "ANSWER_FAILED", Summary: "answer failed"}
	if err := database.FailCandidateArtifact(ctx, verifyClaim.Work, failure, &report, now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	failed, err := database.GetCandidateRevision(ctx, firstCandidate.ID)
	if err != nil || failed.State != candidate.StateArtifactFailed || failed.Failure == nil || failed.Failure.Code != failure.Code || failed.Verification == nil {
		t.Fatalf("failed candidate = %#v, %v", failed, err)
	}
	cleanup, err := database.GetWorkItemForSubject(ctx, worklist.KindArtifactCleanup, worklist.SubjectCandidateRevision, firstCandidate.ID)
	if err != nil || cleanup.State != worklist.StatePending || cleanup.DeadlineAt != nil {
		t.Fatalf("cleanup item = %#v, %v", cleanup, err)
	}
	assertWorkState(t, database, verifyClaim.Work.Item.ID, worklist.StateFailed)

	secondRun, err := database.GetActiveRunForSession(ctx, firstRun.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if secondRun.ID == firstRun.ID || secondRun.SessionID != firstRun.SessionID {
		t.Fatalf("repair run did not reuse the generator session: first=%#v repair=%#v", firstRun, secondRun)
	}
	input, err := generator.DecodeRunInput(secondRun.Input)
	if err != nil {
		t.Fatal(err)
	}
	if input.SeedCandidateRevisionID != firstCandidate.ID || input.Feedback.Summary != failure.Summary || len(input.Feedback.Issues) != 3 {
		t.Fatalf("repair run immutable input = %#v", input)
	}
}

func TestFailedGeneratorRunReleasesAuthoringSessionForRevision(t *testing.T) {
	ctx := context.Background()
	database := newTestDB(t)
	const sessionID = "author-generator-failure"
	const userID = "generator-failure-user"
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: sessionID, UserID: userID}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	revision, err := database.ReplaceAuthoringPlan(ctx, sessionID, userID, 0, validAuthoringPlan("generator failure"), authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := database.StartGeneratorRun(ctx, sessionID, userID, revision.Number, generatorCreateRun("generator-run-failure", sessionID), generator.RunInput{
		AuthoringSessionID: sessionID,
		Revision:           revision.Number,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimAt := time.Now().UTC().Add(time.Second).Truncate(time.Microsecond)
	claim, err := database.ClaimNext(ctx, "generator-worker", time.Minute, claimAt)
	if err != nil || claim == nil || claim.Run.ID != run.ID {
		t.Fatalf("claim generator run = %#v, %v", claim, err)
	}
	if err := database.Fail(ctx, *claim, "model transport failed", claimAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	changed, err := database.ReconcileFailedGeneratorRuns(ctx, claimAt.Add(2*time.Second))
	if err != nil || changed != 1 {
		t.Fatalf("reconcile failed generator = %d, %v", changed, err)
	}
	session, err := database.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if session.State != authoring.StateInfrastructureFailed || session.GeneratorRunID != "" || session.LastError != "model transport failed" {
		t.Fatalf("failed generator authoring session = %#v", session)
	}
	if changed, err := database.ReconcileFailedGeneratorRuns(ctx, claimAt.Add(3*time.Second)); err != nil || changed != 0 {
		t.Fatalf("repeat generator reconciliation = %d, %v", changed, err)
	}
}

func generatorCreateRun(id, sessionID string) agentruntime.CreateRun {
	return agentruntime.CreateRun{
		ID:               id,
		Purpose:          generator.RuntimePurpose,
		OwnerKind:        "authoring-session",
		OwnerRef:         sessionID,
		Model:            "deepseek-v4-pro",
		PromptVersion:    generator.PromptVersion,
		ExecutionTimeout: time.Hour,
	}
}
