package db

import (
	"context"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/generator"
)

func TestGeneratorRepairRunReusesSessionWithoutReusingVerifyTaskReference(t *testing.T) {
	ctx := context.Background()
	database := newTestDB(t)
	const sessionID = "author-generator"
	const userID = "generator-user"
	if _, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: sessionID, UserID: userID}, authoring.Plan{}); err != nil {
		t.Fatal(err)
	}
	revision, err := database.ReplaceAuthoringPlan(ctx, sessionID, userID, 0, validAuthoringPlan("generator repair"), authoring.StateIntentReview)
	if err != nil {
		t.Fatal(err)
	}

	firstSession, firstRun, err := database.StartGeneratorRun(ctx, sessionID, userID, revision.Number, generatorCreateRun("generator-run-one", sessionID), generator.RunInput{
		AuthoringSessionID: sessionID,
		Revision:           revision.Number,
	})
	if err != nil {
		t.Fatalf("start first generator run: %v", err)
	}
	if firstSession.GeneratorSessionID == "" || firstSession.GeneratorRunID != firstRun.ID {
		t.Fatalf("first generator session binding = %#v, run=%#v", firstSession, firstRun)
	}
	claim, err := database.ClaimNext(ctx, "worker-one", time.Minute, time.Now().UTC())
	if err != nil || claim == nil || claim.Run.ID != firstRun.ID {
		t.Fatalf("claim first generator run = %#v, %v", claim, err)
	}
	if err := database.FinalizeGeneratorSubmission(ctx, *claim, generator.SubmissionID(firstRun.ID), "verify-first"); err != nil {
		t.Fatalf("finalize first generator submission: %v", err)
	}
	firstRecord, err := database.GetGeneratorRun(ctx, firstRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstRecord.VerifyTaskID != "verify-first" {
		t.Fatalf("first record VerifyTaskID = %q", firstRecord.VerifyTaskID)
	}

	feedback := generator.Feedback{Summary: "answer.sh exits non-zero", Issues: []generator.Issue{{Code: "ANSWER_EXIT_NONZERO", Message: "fix the answer"}}}
	secondSession, secondRun, err := database.StartGeneratorRun(ctx, sessionID, userID, revision.Number, generatorCreateRun("generator-run-two", sessionID), generator.RunInput{
		AuthoringSessionID: sessionID,
		Revision:           revision.Number,
		SeedSubmissionID:   generator.SubmissionID(firstRun.ID),
		VerifyTaskID:       "verify-first",
		Feedback:           feedback,
	})
	if err != nil {
		t.Fatalf("start repair generator run: %v", err)
	}
	if secondSession.GeneratorSessionID != firstSession.GeneratorSessionID || secondSession.GeneratorRunID != secondRun.ID {
		t.Fatalf("repair generator session binding = %#v", secondSession)
	}
	secondRecord, err := database.GetGeneratorRun(ctx, secondRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secondRecord.VerifyTaskID != "" {
		t.Fatalf("repair run reserved previous VerifyTaskID %q", secondRecord.VerifyTaskID)
	}
	input, err := generator.DecodeRunInput(secondRun.Input)
	if err != nil {
		t.Fatal(err)
	}
	if input.VerifyTaskID != "verify-first" || input.SeedSubmissionID != generator.SubmissionID(firstRun.ID) || !sameGeneratorFeedback(input.Feedback, feedback) {
		t.Fatalf("repair run immutable input = %#v", input)
	}
}

func generatorCreateRun(id, sessionID string) agentruntime.CreateRun {
	return agentruntime.CreateRun{
		ID:            id,
		Purpose:       generator.RuntimePurpose,
		OwnerKind:     "authoring-session",
		OwnerRef:      sessionID,
		Model:         "deepseek-v4-pro",
		PromptVersion: generator.PromptVersion,
		DeadlineAt:    time.Now().UTC().Add(time.Hour),
	}
}

func sameGeneratorFeedback(left, right generator.Feedback) bool {
	if left.BuildPassed != right.BuildPassed || left.AnswerPassed != right.AnswerPassed || left.CheckpointsPassed != right.CheckpointsPassed || left.Summary != right.Summary || len(left.Issues) != len(right.Issues) {
		return false
	}
	for index := range left.Issues {
		if left.Issues[index] != right.Issues[index] {
			return false
		}
	}
	return true
}
