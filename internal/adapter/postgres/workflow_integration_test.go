package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	generationapp "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/environment"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/publication"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
)

const workflowTestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestGenerationWorkflowPublishesVerifiedContentAfterSingleConfirmation(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 9, 0, 0, 0, time.UTC)
	workflow, sessionID, userID := createGenerationWorkflowFixture(t, database, now)
	candidate := advanceToVerifiedCandidate(t, database, workflow.ID, now)

	publishAt := now.Add(10 * time.Minute)
	publishing, err := database.Generation.ConfirmGenerationContent(ctx, sessionID, userID, generation.ContentConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, IdempotencyKey: "confirm-content-1",
	}, generation.PublicationMetadata{Title: generationTestPlan().Metadata.Title, Runtime: scenario.RuntimeK8s}, publishAt)
	if err != nil {
		t.Fatalf("confirm verified content: %v", err)
	}
	if publishing.State != generation.StateScenarioPublishing || publishing.CandidateRevisionID != candidate.ID || publishing.RuntimeAttempt != 1 {
		t.Fatalf("publication start = %#v", publishing)
	}
	repeatedContent, err := database.Generation.ConfirmGenerationContent(ctx, sessionID, userID, generation.ContentConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, IdempotencyKey: "confirm-content-1",
	}, generation.PublicationMetadata{Title: generationTestPlan().Metadata.Title, Runtime: scenario.RuntimeK8s}, publishAt.Add(time.Second))
	if err != nil {
		t.Fatalf("repeat content confirmation: %v", err)
	}
	if repeatedContent.ID != publishing.ID || repeatedContent.State != generation.StateScenarioPublishing {
		t.Fatalf("idempotent content confirmation = %#v", repeatedContent)
	}

	claim := claimGenerationWorkflow(t, database, workflow.ID, "publisher-a", publishAt)
	finalArtifact := generation.ArtifactReference{Runtime: scenario.RuntimeK8s, OCIReference: "registry.example/scenario@" + workflowTestDigest}
	if err := database.Generation.RecordGenerationScenarioPublicationResult(ctx, claim, finalArtifact, publishAt); err != nil {
		t.Fatalf("record publication promotion: %v", err)
	}
	pendingFinalizations, err := database.Generation.PendingGenerationPublicationFinalizations(ctx, publishAt)
	if err != nil {
		t.Fatalf("list pending publication finalizations: %v", err)
	}
	if len(pendingFinalizations) != 1 || pendingFinalizations[0].Workflow.ID != workflow.ID {
		t.Fatalf("pending publication finalizations = %#v", pendingFinalizations)
	}
	diagnostic, err := publication.NewDiagnostic(publication.Transient(errors.New("temporary PVC write failure")), publishAt)
	if err != nil {
		t.Fatalf("create publication diagnostic: %v", err)
	}
	transient, err := database.Generation.RecordGenerationPublicationFinalizerFailure(ctx, workflow.ID, candidate.ID, diagnostic)
	if err != nil {
		t.Fatalf("record transient publication diagnostic: %v", err)
	}
	if transient.State != generation.StateScenarioPublishing || transient.FinalizerErrorCategory != publication.CategoryTransient || transient.FinalizerNextRetryAt == nil {
		t.Fatalf("transient publication workflow = %#v", transient)
	}
	restarted, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("reload transient publication workflow: %v", err)
	}
	if restarted.FinalizerLastError != "temporary PVC write failure" || restarted.FinalizerNextRetryAt == nil || !restarted.FinalizerNextRetryAt.Equal(*transient.FinalizerNextRetryAt) {
		t.Fatalf("reloaded publication diagnostic = %#v", restarted)
	}
	pendingFinalizations, err = database.Generation.PendingGenerationPublicationFinalizations(ctx, publishAt.Add(time.Second))
	if err != nil {
		t.Fatalf("list early publication retry: %v", err)
	}
	if len(pendingFinalizations) != 0 {
		t.Fatalf("publication retried before durable retry time: %#v", pendingFinalizations)
	}
	pendingFinalizations, err = database.Generation.PendingGenerationPublicationFinalizations(ctx, *transient.FinalizerNextRetryAt)
	if err != nil {
		t.Fatalf("list due publication retry: %v", err)
	}
	if len(pendingFinalizations) != 1 {
		t.Fatalf("due publication retry count = %d", len(pendingFinalizations))
	}
	persisted, err := database.Generation.GetCandidateRevision(ctx, candidate.ID)
	if err != nil || persisted.Publication == nil {
		t.Fatalf("load publication intent: %#v, %v", persisted, err)
	}
	if err := database.Generation.FinalizeGenerationScenarioPublication(ctx, workflow.ID, candidate.ID, "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", scenario.ScenarioOperationsScenario, nil, publishAt); err != nil {
		t.Fatalf("finalize publication: %v", err)
	}
	pendingFinalizations, err = database.Generation.PendingGenerationPublicationFinalizations(ctx, publishAt)
	if err != nil {
		t.Fatalf("list finalized publication finalizations: %v", err)
	}
	if len(pendingFinalizations) != 0 {
		t.Fatalf("finalized publication remains pending: %#v", pendingFinalizations)
	}
	published, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load published workflow: %v", err)
	}
	if published.State != generation.StatePublished || !published.State.Terminal() || published.CandidateRevisionID != candidate.ID {
		t.Fatalf("published workflow = %#v", published)
	}
	if published.FinalizerErrorCategory != publication.CategoryUnknown || published.FinalizerLastError != "" || published.FinalizerLastAttemptedAt != nil || published.FinalizerNextRetryAt != nil {
		t.Fatalf("published workflow retained finalizer diagnostic = %#v", published)
	}
	stable, err := database.Scenario.GetScenario(ctx, persisted.Publication.ScenarioID)
	if err != nil {
		t.Fatalf("load published scenario: %v", err)
	}
	if stable == nil || stable.ID == "" || stable.ActiveRevisionID == "" {
		t.Fatalf("published scenario = %#v", stable)
	}
}

func TestGenerationPublicationDeterministicFinalizerFailureEndsWorkflow(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 16, 0, 0, 0, time.UTC)
	workflow, candidate, publishAt := prepareGenerationPublication(t, database, now)
	diagnostic, err := publication.NewDiagnostic(publication.Deterministic(errors.New("materialized revision conflicts with publication intent")), publishAt)
	if err != nil {
		t.Fatalf("create deterministic publication diagnostic: %v", err)
	}
	failed, err := database.Generation.RecordGenerationPublicationFinalizerFailure(ctx, workflow.ID, candidate.ID, diagnostic)
	if err != nil {
		t.Fatalf("record deterministic publication diagnostic: %v", err)
	}
	if failed.State != generation.StateFailed || failed.FinalizerErrorCategory != publication.CategoryDeterministic || failed.FinalizerNextRetryAt != nil {
		t.Fatalf("failed publication workflow = %#v", failed)
	}
	persisted, err := database.Generation.GetCandidateRevision(ctx, candidate.ID)
	if err != nil {
		t.Fatalf("load failed publication candidate: %v", err)
	}
	if persisted.Failure == nil || persisted.Failure.Code != "PUBLICATION_FINALIZER" || persisted.PublishedAt != nil {
		t.Fatalf("failed publication candidate = %#v", persisted)
	}
	pending, err := database.Generation.PendingGenerationPublicationFinalizations(ctx, publishAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("list failed publication finalizers: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("failed publication remained claimable: %#v", pending)
	}
}

func TestGenerationPublicationFailsAfterFiveRuntimeAttempts(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 10, 30, 0, 0, time.UTC)
	workflow, sessionID, userID := createGenerationWorkflowFixture(t, database, now)
	candidate := advanceToVerifiedCandidate(t, database, workflow.ID, now)

	publishingAt := now.Add(time.Minute)
	if _, err := database.Generation.ConfirmGenerationContent(ctx, sessionID, userID, generation.ContentConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, IdempotencyKey: "confirm-content-publication-retry",
	}, generation.PublicationMetadata{Title: generationTestPlan().Metadata.Title, Runtime: scenario.RuntimeK8s}, publishingAt); err != nil {
		t.Fatalf("start publication: %v", err)
	}

	attemptAt := publishingAt
	for attempt := 1; attempt <= generation.MaxRuntimeAttempts; attempt++ {
		claim := claimGenerationWorkflow(t, database, workflow.ID, "publisher-retry", attemptAt)
		updated, err := database.Generation.ReportGenerationInfrastructureFailure(ctx, claim, generation.StateScenarioPublishing, generation.Failure{
			Class: generation.FailureInfrastructure, Code: "REGISTRY_UNAVAILABLE", Summary: "registry is unavailable",
		}, attemptAt)
		if err != nil {
			t.Fatalf("record publication retry %d: %v", attempt, err)
		}
		if attempt < generation.MaxRuntimeAttempts {
			if updated.State != generation.StateScenarioPublishing || updated.RuntimeAttempt != attempt+1 {
				t.Fatalf("publication retry %d = %#v", attempt, updated)
			}
			attemptAt = updated.NextRunAt
		}
	}
	paused, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load exhausted publication workflow: %v", err)
	}
	if paused.State != generation.StateFailed || paused.RuntimeAttempt != 0 || paused.LastError != "registry is unavailable" {
		t.Fatalf("exhausted publication workflow = %#v", paused)
	}
	persisted, err := database.Generation.GetCandidateRevision(ctx, candidate.ID)
	if err != nil {
		t.Fatalf("load candidate after publication retry: %v", err)
	}
	if persisted.Publication == nil || persisted.Publication.ScenarioID == "" || persisted.Artifact == nil || persisted.Verification == nil || !persisted.Verification.Passed {
		t.Fatalf("publication retry changed candidate = %#v", persisted)
	}
}

func TestGenerationRuntimeLeaseTakeoverRetainsActionVersion(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 10, 45, 0, 0, time.UTC)
	workflow, _, _ := createGenerationWorkflowFixture(t, database, now)
	finalizeSubmittedCandidate(t, database, workflow.ID, 1, now)
	claim := claimGenerationWorkflow(t, database, workflow.ID, "judge-takeover", now)
	approveGenerationJudgement(t, database, claim, now)

	first, err := database.Generation.ClaimGenerationWorkflow(ctx, "runtime-before-takeover", time.Second, now)
	if err != nil || first == nil {
		t.Fatalf("claim initial runtime action = %#v, %v", first, err)
	}
	if first.Workflow.State != generation.StateBuilding || first.Workflow.RuntimeAttempt != 1 {
		t.Fatalf("initial runtime claim = %#v", first)
	}
	firstAction, err := database.Generation.LoadGenerationRuntimeAction(ctx, *first, now)
	if err != nil {
		t.Fatalf("load initial runtime action: %v", err)
	}
	takenOver, err := database.Generation.ClaimGenerationWorkflow(ctx, "runtime-after-takeover", time.Minute, now.Add(2*time.Second))
	if err != nil || takenOver == nil {
		t.Fatalf("take over expired runtime action = %#v, %v", takenOver, err)
	}
	if takenOver.Workflow.State != generation.StateBuilding || takenOver.StateVersion != first.StateVersion || takenOver.Workflow.RuntimeAttempt != 2 ||
		takenOver.LeaseOwner == first.LeaseOwner {
		t.Fatalf("runtime takeover changed action identity: first=%#v taken_over=%#v", first, takenOver)
	}
	takenOverAction, err := database.Generation.LoadGenerationRuntimeAction(ctx, *takenOver, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("load taken-over runtime action: %v", err)
	}
	if takenOverAction.Identity != firstAction.Identity {
		t.Fatalf("runtime takeover changed external action identity: first=%#v taken_over=%#v", firstAction.Identity, takenOverAction.Identity)
	}
	if _, err := database.Generation.ReportGenerationInfrastructureFailure(ctx, *first, generation.StateBuilding, generation.Failure{
		Class: generation.FailureInfrastructure, Code: "LATE_REPORT", Summary: "late runtime report",
	}, now.Add(2*time.Second)); !errors.Is(err, generation.ErrLeaseLost) {
		t.Fatalf("late report after takeover = %v, want lease lost", err)
	}
}

func TestConfirmedPlanRevisionsOwnIndependentWorkflows(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 11, 0, 0, 0, time.UTC)
	workflow, sessionID, userID := createGenerationWorkflowFixture(t, database, now)
	advanceToVerifiedCandidate(t, database, workflow.ID, now)
	repeated, err := database.Generation.CreateGenerationWorkflow(ctx, sessionID, userID, generation.StartConfirmation{
		PlanRevision: 1, IdempotencyKey: "start-workflow",
	}, now.Add(30*time.Second))
	if err != nil {
		t.Fatalf("repeat confirmed workflow: %v", err)
	}
	if repeated.ID != workflow.ID {
		t.Fatalf("repeat confirmation created workflow %q, want %q", repeated.ID, workflow.ID)
	}
	repeatedWithNewKey, err := database.Generation.CreateGenerationWorkflow(ctx, sessionID, userID, generation.StartConfirmation{
		PlanRevision: 1, IdempotencyKey: "start-workflow-retry",
	}, now.Add(45*time.Second))
	if err != nil {
		t.Fatalf("repeat confirmed workflow with a new idempotency key: %v", err)
	}
	if repeatedWithNewKey.ID != workflow.ID || repeatedWithNewKey.SourceRevision != workflow.SourceRevision {
		t.Fatalf("repeat confirmation with a new key created workflow %#v, want %#v", repeatedWithNewKey, workflow)
	}
	plan := generationTestPlan()
	plan.Overview = "A replacement authoring plan creates a separate workflow."
	if _, err := database.Authoring.ReplaceAuthoringPlan(ctx, sessionID, userID, 1, plan, authoring.StateIntentReview); err != nil {
		t.Fatalf("confirm replacement authoring plan: %v", err)
	}
	replacement, err := database.Generation.CreateGenerationWorkflow(ctx, sessionID, userID, generation.StartConfirmation{
		PlanRevision: 2, IdempotencyKey: "start-replacement",
	}, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("start replacement workflow: %v", err)
	}
	if replacement.ID == workflow.ID || replacement.SourceRevision != "2" || replacement.State != generation.StateGenerating {
		t.Fatalf("replacement workflow = %#v", replacement)
	}
}

func TestGenerationConfirmationCanRunInsideAuthoringTurn(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 5, 13, 0, 0, 0, time.UTC)
	userID := authoring.NewID("active-authoring-user")
	if _, err := database.Identity.CreateUserWithAuth(userID, userID, "", ""); err != nil {
		t.Fatalf("create workflow author: %v", err)
	}
	plan := generationTestPlan()
	session, err := database.Authoring.CreateAuthoringSession(ctx, authoring.Session{ID: authoring.NewID("active-authoring"), UserID: userID}, plan)
	if err != nil {
		t.Fatalf("create authoring session: %v", err)
	}
	if _, err := database.Authoring.ReplaceAuthoringPlan(ctx, session.ID, session.UserID, 0, plan, authoring.StateIntentReview); err != nil {
		t.Fatalf("confirm authoring plan: %v", err)
	}
	if _, _, _, err := database.Authoring.StartAuthoringRun(ctx, session.ID, session.UserID, "authoring-confirmation-run", agent.Message{Role: "user", Content: "继续完善现场说明。"}, agent.CreateRun{
		ID: agent.NewID("authoring-run"), SessionID: session.RuntimeSessionID, Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: session.ID,
		Model: "test-model", PromptVersion: "authoring-v2",
	}); err != nil {
		t.Fatalf("start authoring run: %v", err)
	}
	workflow, err := database.Generation.CreateGenerationWorkflow(ctx, session.ID, session.UserID, generation.StartConfirmation{
		PlanRevision: 1, IdempotencyKey: "confirm-inside-authoring-run",
	}, now)
	if err != nil {
		t.Fatalf("confirm generation during authoring run: %v", err)
	}
	if workflow.State != generation.StateGenerating || workflow.Source.Ref != session.ID || workflow.SourceRevision != "1" {
		t.Fatalf("generation workflow created inside authoring run = %#v", workflow)
	}
}

func TestAuthoringMessageReceiptReturnsTheOriginalRun(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	userID := authoring.NewID("authoring-message-user")
	if _, err := database.Identity.CreateUserWithAuth(userID, userID, "", ""); err != nil {
		t.Fatalf("create author: %v", err)
	}
	session, err := database.Authoring.CreateAuthoringSession(ctx, authoring.Session{ID: authoring.NewID("authoring-message"), UserID: userID}, generationTestPlan())
	if err != nil {
		t.Fatalf("create authoring session: %v", err)
	}
	createRun := func(id string) agent.CreateRun {
		return agent.CreateRun{
			ID: id, SessionID: session.RuntimeSessionID, Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: session.ID,
			Model: "test-model", PromptVersion: "authoring-v4",
		}
	}
	stage, first, created, err := database.Authoring.StartAuthoringRun(ctx, session.ID, userID, "message-request-one", agent.Message{
		Role: "user", Content: "继续完善现场说明。",
	}, createRun(agent.NewID("authoring-run")))
	if err != nil {
		t.Fatalf("start authoring run: %v", err)
	}
	if !created || stage == nil {
		t.Fatalf("first authoring receipt = created=%t stage=%#v", created, stage)
	}
	replayedStage, replayed, created, err := database.Authoring.StartAuthoringRun(ctx, session.ID, userID, "message-request-one", agent.Message{
		Role: "user", Content: "继续完善现场说明。",
	}, createRun(agent.NewID("authoring-run")))
	if err != nil {
		t.Fatalf("replay authoring run: %v", err)
	}
	if created || replayedStage != nil || replayed.ID != first.ID {
		t.Fatalf("replayed authoring receipt = created=%t stage=%#v run=%#v", created, replayedStage, replayed)
	}
	messages, err := database.Authoring.ListMessages(ctx, session.RuntimeSessionID)
	if err != nil {
		t.Fatalf("list authoring messages: %v", err)
	}
	runs, err := database.Agent.ListRunsForOwner(ctx, "authoring-session", session.ID)
	if err != nil {
		t.Fatalf("list authoring runs: %v", err)
	}
	if len(messages) != 1 || len(runs) != 1 {
		t.Fatalf("idempotent authoring records: messages=%#v runs=%#v", messages, runs)
	}
	if _, _, _, err := database.Authoring.StartAuthoringRun(ctx, session.ID, userID, "message-request-one", agent.Message{
		Role: "user", Content: "改成另一条消息。",
	}, createRun(agent.NewID("authoring-run"))); !errors.Is(err, authoring.ErrVersionConflict) {
		t.Fatalf("reuse message key with different content: %v", err)
	}
}

func TestAuthoringFinalizationCanReplayAfterItsResultIsLost(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	userID := authoring.NewID("authoring-finalization-user")
	if _, err := database.Identity.CreateUserWithAuth(userID, userID, "", ""); err != nil {
		t.Fatalf("create author: %v", err)
	}
	session, err := database.Authoring.CreateAuthoringSession(ctx, authoring.Session{ID: authoring.NewID("authoring-finalization"), UserID: userID}, generationTestPlan())
	if err != nil {
		t.Fatalf("create authoring session: %v", err)
	}
	_, run, _, err := database.Authoring.StartAuthoringRun(ctx, session.ID, userID, "finalization-request", agent.Message{
		Role: "user", Content: "继续完善现场说明。",
	}, agent.CreateRun{
		ID: agent.NewID("authoring-run"), SessionID: session.RuntimeSessionID, Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: session.ID,
		InputRevision: "0", Model: "test-model", PromptVersion: "authoring-v4",
	})
	if err != nil {
		t.Fatalf("start authoring run: %v", err)
	}
	first, err := database.Authoring.FinalizeAuthoringRun(ctx, run.ID, run.Attempt, "本轮已经完成。", time.Now().UTC())
	if err != nil {
		t.Fatalf("finalize authoring run: %v", err)
	}
	replayed, err := database.Authoring.FinalizeAuthoringRun(ctx, run.ID, run.Attempt, "本轮已经完成。", time.Now().UTC())
	if err != nil {
		t.Fatalf("replay authoring finalization: %v", err)
	}
	if first.Number != replayed.Number || !reflect.DeepEqual(first.Plan, replayed.Plan) {
		t.Fatalf("replayed authoring revision = %#v, want %#v", replayed, first)
	}
	messages, err := database.Authoring.ListMessages(ctx, session.RuntimeSessionID)
	if err != nil {
		t.Fatalf("list authoring messages: %v", err)
	}
	if len(messages) != 2 || messages[1].ID != authoring.RunCompletionMessageID(run.ID) || messages[1].Role != "assistant" {
		t.Fatalf("idempotent finalization messages = %#v", messages)
	}
	persisted, err := database.Agent.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("read finalized authoring run: %v", err)
	}
	if persisted.Status != agent.RunSucceeded {
		t.Fatalf("finalized authoring run = %#v", persisted)
	}
}

func TestAuthoringStageOperationReplayReturnsThePersistedStage(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	userID := authoring.NewID("authoring-recovery-user")
	if _, err := database.Identity.CreateUserWithAuth(userID, userID, "", ""); err != nil {
		t.Fatalf("create author: %v", err)
	}
	plan := generationTestPlan()
	session, err := database.Authoring.CreateAuthoringSession(ctx, authoring.Session{ID: authoring.NewID("authoring-recovery"), UserID: userID}, plan)
	if err != nil {
		t.Fatalf("create authoring session: %v", err)
	}
	stage, run, _, err := database.Authoring.StartAuthoringRun(ctx, session.ID, session.UserID, "authoring-recovery-message", agent.Message{Role: "user", Content: "继续完善现场说明。"}, agent.CreateRun{
		ID: agent.NewID("authoring-run"), SessionID: session.RuntimeSessionID, Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: session.ID,
		Model: "test-model", PromptVersion: "authoring-v2",
	})
	if err != nil {
		t.Fatalf("start authoring run: %v", err)
	}
	operation, err := authoring.NewStageOperation(run.ID, stage.StageRevision, "overview", struct {
		Markdown string `json:"markdown"`
	}{Markdown: "新的概览"})
	if err != nil {
		t.Fatalf("create stage operation: %v", err)
	}
	changedPlan := stage.Plan.Clone()
	changedPlan.Overview = "尚未提交的私有概览"
	updated, err := database.Authoring.UpdateAuthoringStage(ctx, run.ID, 1, stage.StageRevision, operation, changedPlan, authoring.Change{
		Kind: "overview", Summary: "stale attempt",
	})
	if err != nil {
		t.Fatalf("apply stage operation: %v", err)
	}
	replayed, err := database.Authoring.UpdateAuthoringStage(ctx, run.ID, 1, stage.StageRevision, operation, changedPlan, authoring.Change{
		Kind: "overview", Summary: "stale attempt",
	})
	if err != nil {
		t.Fatalf("replay stage operation: %v", err)
	}
	if updated.StageRevision != replayed.StageRevision || updated.StageRevision != stage.StageRevision+1 || !reflect.DeepEqual(updated.Plan, replayed.Plan) || !reflect.DeepEqual(updated.Changes, replayed.Changes) {
		t.Fatalf("replayed stage = %#v, original = %#v", replayed, updated)
	}
	if err := database.Authoring.TerminateAuthoringRun(ctx, run.ID, authoring.RunTerminationServerRestarted, "server restarted before agent completion", time.Now().UTC()); err != nil {
		t.Fatalf("terminate authoring run: %v", err)
	}
	if err := database.Authoring.TerminateAuthoringRun(ctx, run.ID, authoring.RunTerminationServerRestarted, "server restarted before agent completion", time.Now().UTC()); err != nil {
		t.Fatalf("replay authoring run termination: %v", err)
	}
	interrupted, err := database.Agent.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("read interrupted authoring run: %v", err)
	}
	if interrupted.Status != agent.RunInterrupted {
		t.Fatalf("interrupted authoring run = %#v", interrupted)
	}
	if _, err := database.Authoring.GetAuthoringStage(ctx, run.ID); !errors.Is(err, authoring.ErrNotFound) {
		t.Fatalf("old private stage still exists: %v", err)
	}
	persistedPlan, err := database.Authoring.GetAuthoringRevision(ctx, session.ID, session.CurrentRevision)
	if err != nil {
		t.Fatalf("read persisted authoring plan: %v", err)
	}
	if persistedPlan.Plan.Overview == changedPlan.Overview {
		t.Fatalf("private Plan change escaped terminated stage: %#v", persistedPlan.Plan)
	}
	messages, err := database.Authoring.ListMessages(ctx, session.RuntimeSessionID)
	if err != nil {
		t.Fatalf("read authoring messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != "user" || messages[1].Role != "event" || messages[1].ID != authoring.RunEventMessageID(run.ID, authoring.RunTerminationServerRestarted) {
		t.Fatalf("termination conversation = %#v", messages)
	}
	var event authoring.RunEvent
	if err := json.Unmarshal([]byte(messages[1].Content), &event); err != nil {
		t.Fatalf("decode termination event: %v", err)
	}
	if event.Kind != "authoring_run_interrupted" || event.Reason != authoring.RunTerminationServerRestarted || !event.Resumable || event.Recovery != "empty" {
		t.Fatalf("termination event = %#v", event)
	}
	runs, err := database.Agent.ListRunsForOwner(ctx, "authoring-session", session.ID)
	if err != nil {
		t.Fatalf("list authoring runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("startup termination created replacement runs: %#v", runs)
	}
}

func TestAuthoringRestartEventReportsSnapshotRecovery(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	workflow, sessionID, userID := createGenerationWorkflowFixture(t, database, now)
	session, err := database.Authoring.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		t.Fatalf("read authoring session: %v", err)
	}
	_, run, _, err := database.Authoring.StartAuthoringRun(ctx, session.ID, session.UserID, "snapshot-recovery-message", agent.Message{
		Role: "user", Content: "继续生成任务。",
	}, agent.CreateRun{
		ID: agent.NewID("authoring-run"), SessionID: session.RuntimeSessionID, Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: session.ID,
		InputRevision: "1", Model: "test-model", PromptVersion: "authoring-v4",
	})
	if err != nil {
		t.Fatalf("start authoring run: %v", err)
	}
	if _, err := database.conn.ExecContext(ctx, `UPDATE generation_workflows SET workspace_snapshot_digest = ? WHERE id = ?`, workflowTestDigest, workflow.ID); err != nil {
		t.Fatalf("record workspace snapshot: %v", err)
	}
	if err := database.Authoring.TerminateAuthoringRun(ctx, run.ID, authoring.RunTerminationServerRestarted, "server restarted before agent completion", now.Add(time.Second)); err != nil {
		t.Fatalf("terminate authoring run: %v", err)
	}
	messages, err := database.Authoring.ListMessages(ctx, session.RuntimeSessionID)
	if err != nil {
		t.Fatalf("list authoring messages: %v", err)
	}
	if len(messages) != 2 || messages[1].Role != "event" {
		t.Fatalf("restart messages = %#v", messages)
	}
	var event authoring.RunEvent
	if err := json.Unmarshal([]byte(messages[1].Content), &event); err != nil {
		t.Fatalf("decode restart event: %v", err)
	}
	if event.Reason != authoring.RunTerminationServerRestarted || event.Recovery != "snapshot" {
		t.Fatalf("restart event = %#v", event)
	}
}

func TestInteractiveAssistantRunRecoveryCreatesFreshBudget(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	session, err := database.Agent.CreateSession(ctx, agent.Session{
		ID: agent.NewID("assistant-session"), Purpose: "assistant", OwnerKind: "environment", OwnerRef: "environment-one", UserRef: "user-one",
	})
	if err != nil {
		t.Fatalf("create assistant session: %v", err)
	}
	run, err := database.Agent.CreateMessageAndRun(ctx, agent.Message{
		ID: agent.NewID("assistant-message"), SessionID: session.ID, Role: "user", Content: "请分析当前故障。",
	}, agent.CreateRun{
		ID: agent.NewID("assistant-run"), SessionID: session.ID, Purpose: "assistant", OwnerKind: "environment", OwnerRef: "environment-one",
		InputRevision: "environment-one", Input: []byte(`{"current_window":"shell-1"}`), Model: "test-model", PromptVersion: "assistant-v1",
	})
	if err != nil {
		t.Fatalf("start assistant run: %v", err)
	}
	replacement, err := database.Agent.RestartRun(ctx, run.ID, "server restarted", time.Now().UTC())
	if err != nil {
		t.Fatalf("restart assistant run: %v", err)
	}
	if replacement.ID == run.ID || replacement.Attempt != 1 || replacement.DeadlineAt.IsZero() || string(replacement.Input) != string(run.Input) {
		t.Fatalf("replacement assistant run = %#v", replacement)
	}
	interrupted, err := database.Agent.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("read interrupted assistant run: %v", err)
	}
	if interrupted.Status != agent.RunInterrupted {
		t.Fatalf("interrupted assistant run = %#v", interrupted)
	}
	messages, err := database.Agent.ListMessages(ctx, session.ID)
	if err != nil {
		t.Fatalf("read assistant messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Content != "请分析当前故障。" {
		t.Fatalf("restart rewrote assistant conversation: %#v", messages)
	}
}

func TestGenerationAgentRunInterruptionCreatesReplacementWithNewBudget(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 5, 13, 15, 0, 0, time.UTC)
	workflow, _, _ := createGenerationWorkflowFixture(t, database, now)
	finalizeSubmittedCandidate(t, database, workflow.ID, 1, now)
	claim := claimGenerationWorkflow(t, database, workflow.ID, "server-before-restart", now)
	run := startGenerationRun(t, database, claim, generationapp.JudgePurpose, now)

	if err := database.Generation.InterruptActiveGenerationAgentRuns(ctx, "server restarted", now.Add(time.Minute)); err != nil {
		t.Fatalf("interrupt active generation runs: %v", err)
	}
	stored, err := database.Agent.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("read interrupted run: %v", err)
	}
	if stored.Status != agent.RunInterrupted {
		t.Fatalf("interrupted run status = %s, want %s", stored.Status, agent.RunInterrupted)
	}
	replacementClaim := claimGenerationWorkflow(t, database, workflow.ID, "server-after-restart", now.Add(time.Minute))
	replacement := startGenerationRun(t, database, replacementClaim, generationapp.JudgePurpose, now.Add(time.Minute))
	if replacement.ID == run.ID || replacement.Attempt != 1 {
		t.Fatalf("replacement run = %#v, interrupted = %#v", replacement, run)
	}
}

func TestGenerationAgentRunUsesFiveTechnicalAttempts(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 5, 13, 30, 0, 0, time.UTC)
	workflow, _, _ := createGenerationWorkflowFixture(t, database, now)
	finalizeSubmittedCandidate(t, database, workflow.ID, 1, now)
	claim := claimGenerationWorkflow(t, database, workflow.ID, "server-retry", now)
	run := startGenerationRun(t, database, claim, generationapp.JudgePurpose, now)
	for attempt := 1; attempt <= agent.MaxAttempts; attempt++ {
		next, err := database.Generation.RetryGenerationAgentRun(ctx, claim, run.ID, "model transport unavailable", now.Add(time.Duration(attempt)*time.Second))
		if err != nil {
			t.Fatalf("record technical failure %d: %v", attempt, err)
		}
		if attempt < agent.MaxAttempts {
			if next == nil || next.ID != run.ID || next.Attempt != attempt+1 {
				t.Fatalf("retry %d = %#v", attempt, next)
			}
			run = next
			continue
		}
		if next != nil {
			t.Fatalf("exhausted retry returned another attempt: %#v", next)
		}
	}
	stored, err := database.Agent.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("read exhausted run: %v", err)
	}
	if stored.Status != agent.RunFailed || stored.Attempt != agent.MaxAttempts {
		t.Fatalf("exhausted agent run = %#v", stored)
	}
	exhausted, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("read exhausted workflow: %v", err)
	}
	if exhausted.State != generation.StateFailed {
		t.Fatalf("exhausted workflow state = %s, want %s", exhausted.State, generation.StateFailed)
	}
}

func TestGenerationResourceReaperKeepsActiveCandidateAndReapsTerminalResources(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 12, 0, 0, 0, time.UTC)
	workflow, sessionID, userID := createGenerationWorkflowFixture(t, database, now)
	candidate := advanceToVerifiedCandidate(t, database, workflow.ID, now)

	reapClaim, err := database.Generation.ClaimGenerationResourceReap(ctx, "reaper", time.Minute, now)
	if err != nil {
		t.Fatalf("claim active candidate verification-environment reap: %v", err)
	}
	if reapClaim == nil || reapClaim.Scope != runtime.ScopeGenerationWorkflow || reapClaim.ResourceID != candidate.ID || reapClaim.Kind != runtime.ReapVerificationEnvironment {
		t.Fatalf("active verified candidate verification-environment reap = %#v", reapClaim)
	}
	if err := database.Generation.CompleteGenerationResourceReap(ctx, *reapClaim, "", now); err != nil {
		t.Fatalf("complete active candidate verification-environment reap: %v", err)
	}
	publishAt := now.Add(30 * time.Second)
	if _, err := database.Generation.ConfirmGenerationContent(ctx, sessionID, userID, generation.ContentConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, IdempotencyKey: "confirm-content-reaper",
	}, generation.PublicationMetadata{Title: generationTestPlan().Metadata.Title, Runtime: scenario.RuntimeK8s}, publishAt); err != nil {
		t.Fatalf("start publication: %v", err)
	}
	reapClaim, err = database.Generation.ClaimGenerationResourceReap(ctx, "reaper", time.Minute, publishAt.Add(time.Second))
	if err != nil {
		t.Fatalf("claim publishing artifact reap: %v", err)
	}
	if reapClaim != nil {
		t.Fatalf("publishing candidate was eligible for artifact reap: %#v", reapClaim)
	}

	if _, err := database.Generation.CancelGenerationWorkflow(ctx, sessionID, userID, generation.Cancellation{
		WorkflowID: workflow.ID, IdempotencyKey: "cancel-resource-reaper",
	}, now.Add(time.Minute)); err != nil {
		t.Fatalf("cancel generation workflow: %v", err)
	}
	for _, kind := range []runtime.ReapKind{runtime.ReapCandidateArtifact} {
		reapClaim, err := database.Generation.ClaimGenerationResourceReap(ctx, "reaper", time.Minute, now.Add(2*time.Minute))
		if err != nil {
			t.Fatalf("claim %s reap: %v", kind, err)
		}
		if reapClaim == nil || reapClaim.Scope != runtime.ScopeGenerationWorkflow || reapClaim.ResourceID != candidate.ID || reapClaim.Kind != kind {
			t.Fatalf("claimed %s reap = %#v", kind, reapClaim)
		}
		if err := database.Generation.CompleteGenerationResourceReap(ctx, *reapClaim, "", now.Add(2*time.Minute)); err != nil {
			t.Fatalf("complete %s reap: %v", kind, err)
		}
	}
	repeated, err := database.Generation.ClaimGenerationResourceReap(ctx, "reaper", time.Minute, now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("claim completed reaps: %v", err)
	}
	if repeated != nil {
		t.Fatalf("completed reaps were claimed again: %#v", repeated)
	}
}

func prepareGenerationPublication(t *testing.T, database *Store, now time.Time) (*generation.Workflow, generation.Revision, time.Time) {
	t.Helper()
	workflow, sessionID, userID := createGenerationWorkflowFixture(t, database, now)
	candidate := advanceToVerifiedCandidate(t, database, workflow.ID, now)
	publishAt := now.Add(10 * time.Minute)
	if _, err := database.Generation.ConfirmGenerationContent(context.Background(), sessionID, userID, generation.ContentConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, IdempotencyKey: "prepare-content",
	}, generation.PublicationMetadata{Title: generationTestPlan().Metadata.Title, Runtime: scenario.RuntimeK8s}, publishAt); err != nil {
		t.Fatalf("start publication: %v", err)
	}
	claim := claimGenerationWorkflow(t, database, workflow.ID, "publisher-prepare", publishAt)
	if err := database.Generation.RecordGenerationScenarioPublicationResult(context.Background(), claim, generation.ArtifactReference{
		Runtime: scenario.RuntimeK8s, OCIReference: "registry.example/scenario@" + workflowTestDigest,
	}, publishAt); err != nil {
		t.Fatalf("record promotion: %v", err)
	}
	return workflow, candidate, publishAt
}

func advanceToVerifiedCandidate(t *testing.T, database *Store, workflowID string, now time.Time) generation.Revision {
	t.Helper()
	candidate := finalizeSubmittedCandidate(t, database, workflowID, 1, now)
	claim := claimGenerationWorkflow(t, database, workflowID, "judge-a", now)
	approveGenerationJudgement(t, database, claim, now)
	claim = claimGenerationWorkflow(t, database, workflowID, "builder-a", now)
	advanceGenerationBuildAndArtifact(t, database, claim, now)
	claim = claimGenerationWorkflow(t, database, workflowID, "verifier-a", now)
	if err := database.Generation.RecordGenerationVerificationEnvironment(context.Background(), claim, verificationEnvironment(claim), now); err != nil {
		t.Fatalf("record verification environment: %v", err)
	}
	if err := database.Generation.CompleteGenerationVerification(context.Background(), claim, verificationReport(true), now); err != nil {
		t.Fatalf("complete verification: %v", err)
	}
	return candidate
}

func createGenerationWorkflowFixture(t *testing.T, database *Store, now time.Time) (*generation.Workflow, string, string) {
	t.Helper()
	ctx := context.Background()
	userID := authoring.NewID("workflow-user")
	if _, err := database.Identity.CreateUserWithAuth(userID, userID, "", ""); err != nil {
		t.Fatalf("create workflow author: %v", err)
	}
	plan := generationTestPlan()
	session, err := database.Authoring.CreateAuthoringSession(ctx, authoring.Session{ID: authoring.NewID("authoring"), UserID: userID}, plan)
	if err != nil {
		t.Fatalf("create authoring session: %v", err)
	}
	if _, err := database.Authoring.ReplaceAuthoringPlan(ctx, session.ID, session.UserID, 0, plan, authoring.StateIntentReview); err != nil {
		t.Fatalf("confirm authoring plan: %v", err)
	}
	workflow, err := database.Generation.CreateGenerationWorkflow(ctx, session.ID, session.UserID, generation.StartConfirmation{
		PlanRevision: 1, IdempotencyKey: "start-workflow",
	}, now)
	if err != nil {
		t.Fatalf("create generation workflow: %v", err)
	}
	return workflow, session.ID, userID
}

func generationTestPlan() authoring.Plan {
	return authoring.Plan{
		Metadata:    authoring.Metadata{Title: "Workflow lifecycle", Description: "Exercise durable workflow recovery.", Runtime: "k8s"},
		Overview:    "Repair a workload and verify the observable outcome.",
		Checkpoints: []authoring.Checkpoint{{ID: "ready", Title: "Ready", Markdown: "The workload is healthy.", Position: 1}},
	}
}

func claimGenerationWorkflow(t *testing.T, database *Store, workflowID, workerID string, now time.Time) generation.Claim {
	t.Helper()
	workflow, err := database.Generation.GetGenerationWorkflow(context.Background(), workflowID)
	if err != nil {
		t.Fatalf("read generation workflow before claim: %v", err)
	}
	var claim *generation.Claim
	if workflow.State == generation.StateJudging {
		claim, err = database.Generation.ClaimGenerationAgentWorkflow(context.Background(), workerID, time.Minute, now)
	} else {
		claim, err = database.Generation.ClaimGenerationWorkflow(context.Background(), workerID, time.Minute, now)
	}
	if err != nil {
		t.Fatalf("claim generation workflow: %v", err)
	}
	if claim == nil || claim.Workflow.ID != workflowID {
		t.Fatalf("claimed workflow = %#v, want %q", claim, workflowID)
	}
	return *claim
}

func startGenerationRun(t *testing.T, database *Store, claim generation.Claim, purpose string, now time.Time) *agent.Run {
	t.Helper()
	promptVersions := map[string]string{
		generationapp.JudgePurpose: generationapp.JudgePromptVersion,
	}
	run, err := database.Generation.StartGenerationAgentRun(context.Background(), claim, agent.CreateRun{
		ID:            agent.NewID("generation-test-run"),
		Purpose:       purpose,
		OwnerKind:     "generation-workflow",
		OwnerRef:      claim.Workflow.ID,
		Model:         "test-model",
		PromptVersion: promptVersions[purpose],
	}, now)
	if err != nil {
		t.Fatalf("start %s agent run: %v", purpose, err)
	}
	return run
}

func finalizeSubmittedCandidate(t *testing.T, database *Store, workflowID string, sequence int, now time.Time) generation.Revision {
	t.Helper()
	workflow, err := database.Generation.GetGenerationWorkflow(context.Background(), workflowID)
	if err != nil {
		t.Fatalf("read workflow for candidate submission: %v", err)
	}
	session, err := database.Authoring.GetAuthoringSessionInternal(context.Background(), workflow.Source.Ref)
	if err != nil {
		t.Fatalf("read candidate submission session: %v", err)
	}
	workspaceID := generation.NewID("workspace")
	record, err := database.Generation.CreateGeneratorWorkspace(context.Background(), generation.Workspace{
		ID: workspaceID, WorkflowID: workflow.ID, Namespace: "generator-test", PVCName: generation.NewWorkspacePVCName(workspaceID),
		State: generation.WorkspacePending, ProvisionDeadline: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create candidate submission workspace: %v", err)
	}
	if err := database.Generation.ActivateGeneratorWorkspace(context.Background(), record.ID, "sandbox-"+record.ID, now); err != nil {
		t.Fatalf("activate candidate submission workspace: %v", err)
	}
	turn := generation.WorkspaceTurn{WorkflowID: workflow.ID, ID: generation.NewID("generator-turn")}
	if _, err := database.Generation.AcquireGeneratorWorkspaceTurn(context.Background(), turn, now); err != nil {
		t.Fatalf("bind candidate submission workspace turn: %v", err)
	}
	revision := generation.Revision{
		ID:            generation.NewID("candidate-revision"),
		ArchivePath:   "/tmp/generated-candidate-" + string(rune('0'+sequence)) + ".tar.gz",
		ArchiveSHA256: workflowTestDigest,
		Snapshot:      generationTestSnapshot(),
	}
	result, err := database.Generation.SubmitGenerationCandidate(context.Background(), session.ID, session.UserID, generation.CandidateSubmission{
		WorkflowID: workflow.ID, TurnID: turn.ID, IdempotencyKey: "submit-candidate-" + string(rune('0'+sequence)),
	}, revision, now)
	if err != nil {
		t.Fatalf("submit candidate %d: %v", sequence, err)
	}
	return *result
}

func approveGenerationJudgement(t *testing.T, database *Store, claim generation.Claim, now time.Time) {
	t.Helper()
	run := startGenerationRun(t, database, claim, generationapp.JudgePurpose, now)
	if err := database.Generation.FinalizeGenerationJudgement(context.Background(), claim, run.ID, true, "", now); err != nil {
		t.Fatalf("approve generation judgement: %v", err)
	}
}

func advanceGenerationBuildAndArtifact(t *testing.T, database *Store, claim generation.Claim, now time.Time) {
	t.Helper()
	if err := database.Generation.CompleteGenerationBuild(context.Background(), claim, generation.BuildOutput{
		Runtime: scenario.RuntimeK8s, OCIReference: "registry.example/build@" + workflowTestDigest,
	}, now); err != nil {
		t.Fatalf("complete generation build: %v", err)
	}
	claim = claimGenerationWorkflow(t, database, claim.Workflow.ID, "publisher-a", now)
	if err := database.Generation.CompleteGenerationArtifactPublish(context.Background(), claim, artifactReference(), now); err != nil {
		t.Fatalf("complete artifact publication: %v", err)
	}
}

func generationTestSnapshot() generation.ExecutionSnapshot {
	return generation.ExecutionSnapshot{
		Runtime:     scenario.RuntimeK8s,
		Checkpoints: []generation.CheckpointSnapshot{{ID: "ready"}},
		K8s: &generation.K8sRuntimeSnapshot{
			BaseImageDigest:         "registry.example/base@" + workflowTestDigest,
			ProfileRevision:         "profile-v1",
			Version:                 "v0.30.0",
			ManagementTerminalImage: "registry.example/terminal@" + workflowTestDigest,
			Resources: generation.K8sResources{
				ControlPlaneCPU: "100m", ControlPlaneMemory: "128Mi", ControlPlaneEphemeralStorage: "256Mi",
				WorkloadCPU: "100m", WorkloadMemory: "128Mi", WorkloadEphemeralStorage: "256Mi",
				QuotaCPU: "1", QuotaMemory: "1Gi", QuotaEphemeralStorage: "2Gi",
			},
			Network: environment.VK8sNetwork{
				PublicEgressCIDR: "0.0.0.0/0",
				ProtectedCIDRs:   []string{"10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16"},
			},
		},
	}
}

func artifactReference() generation.ArtifactReference {
	return generation.ArtifactReference{Runtime: scenario.RuntimeK8s, OCIReference: "registry.example/candidate@" + workflowTestDigest}
}

func verificationEnvironment(claim generation.Claim) generation.VerificationEnvironment {
	return generation.VerificationEnvironment{
		Runtime: scenario.RuntimeK8s, Name: "verify-environment", UID: "verify-uid", WorkflowID: claim.Workflow.ID,
		Attempt: claim.StateVersion,
	}
}

func verificationReport(passed bool) generation.VerificationReport {
	return generation.VerificationReport{
		Passed:      passed,
		Summary:     map[bool]string{true: "all checks passed", false: "the ready checkpoint did not pass"}[passed],
		Answers:     []generation.ExecutionResult{{Location: "management", ExitCode: 0}},
		Checkpoints: []generation.CheckpointResult{{ID: "ready", Passed: passed, Summary: map[bool]string{true: "ready", false: "not ready"}[passed]}},
	}
}
