package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	generationapp "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
	roadmaptest "github.com/breakfix/breakfix/internal/testkit/roadmap"
)

const workflowTestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestGenerationWorkflowPersistsClassificationAndPublicationLifecycle(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 9, 0, 0, 0, time.UTC)
	initialRoadmap := publishWorkflowRoadmap(t, database, now)
	workflow, sessionID, userID := createGenerationWorkflowFixture(t, database, now)
	candidate := advanceToVerifiedCandidate(t, database, workflow.ID, now)

	classificationStart := now.Add(10 * time.Minute)
	classified, err := database.Generation.ConfirmGenerationContent(ctx, sessionID, userID, generation.ContentConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, IdempotencyKey: "confirm-content-1",
	}, classificationStart)
	if err != nil {
		t.Fatalf("confirm verified content: %v", err)
	}
	if classified.State != generation.StateClassifying || classified.CandidateRevisionID != candidate.ID ||
		classified.ClassificationRoadmapRevision != initialRoadmap.Revision || classified.DeadlinePausedAt != nil || classified.DeadlineAt == nil {
		t.Fatalf("classification start = %#v", classified)
	}
	repeatedContent, err := database.Generation.ConfirmGenerationContent(ctx, sessionID, userID, generation.ContentConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, IdempotencyKey: "confirm-content-1",
	}, classificationStart.Add(time.Second))
	if err != nil {
		t.Fatalf("repeat content confirmation: %v", err)
	}
	if repeatedContent.ID != classified.ID || repeatedContent.State != generation.StateClassifying {
		t.Fatalf("idempotent content confirmation = %#v", repeatedContent)
	}

	claim := claimGenerationWorkflow(t, database, workflow.ID, "classifier-a", classificationStart)
	classifierRun := startGenerationRun(t, database, claim, generationapp.ClassifierPurpose, classificationStart)
	if err := database.Generation.FinalizeGenerationClassification(ctx, claim, classifierRun.ID, existingClassification(initialRoadmap, candidate.ID), classificationStart); err != nil {
		t.Fatalf("persist classification proposal: %v", err)
	}
	awaitingReview, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load classification review workflow: %v", err)
	}
	if awaitingReview.State != generation.StateNeedsClassificationReview || awaitingReview.DeadlinePausedAt == nil || awaitingReview.LeaseOwner != "" {
		t.Fatalf("classification review workflow = %#v", awaitingReview)
	}
	persisted, err := database.Generation.GetCandidateRevision(ctx, candidate.ID)
	if err != nil {
		t.Fatalf("load classified candidate: %v", err)
	}
	if persisted.Classification == nil || persisted.Classification.Revision != 1 || persisted.Classification.RoadmapRevision != initialRoadmap.Revision ||
		persisted.Build == nil || persisted.Artifact == nil || persisted.Verification == nil || !persisted.Verification.Passed {
		t.Fatalf("classified candidate = %#v", persisted)
	}

	publishAt := classificationStart.Add(10 * time.Minute)
	publishing, err := database.Generation.BeginClassificationPublication(ctx, sessionID, userID, generationTestPlan().Metadata.Title, generation.PublicationConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, ProposalRevision: persisted.Classification.Revision, IdempotencyKey: "confirm-publication-1",
	}, publishAt)
	if err != nil {
		t.Fatalf("begin classification publication: %v", err)
	}
	if publishing.State != generation.StateChallengePublishing || publishing.DeadlinePausedAt != nil || publishing.CandidateRevisionID != candidate.ID {
		t.Fatalf("publication workflow = %#v", publishing)
	}
	repeatedPublication, err := database.Generation.BeginClassificationPublication(ctx, sessionID, userID, generationTestPlan().Metadata.Title, generation.PublicationConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, ProposalRevision: persisted.Classification.Revision, IdempotencyKey: "confirm-publication-1",
	}, publishAt.Add(time.Second))
	if err != nil {
		t.Fatalf("repeat publication confirmation: %v", err)
	}
	if repeatedPublication.ID != publishing.ID || repeatedPublication.State != generation.StateChallengePublishing {
		t.Fatalf("idempotent publication confirmation = %#v", repeatedPublication)
	}

	claim = claimGenerationWorkflow(t, database, workflow.ID, "publisher-a", publishAt)
	finalArtifact := generation.ArtifactReference{Runtime: challenge.RuntimeK8s, OCIReference: "registry.example/challenge@" + workflowTestDigest}
	if err := database.Generation.CompleteGenerationChallengePublish(ctx, claim, finalArtifact, "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", publishAt); err != nil {
		t.Fatalf("complete classification publication: %v", err)
	}
	published, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load published workflow: %v", err)
	}
	if published.State != generation.StatePublished || !published.State.Terminal() || published.CandidateRevisionID != candidate.ID {
		t.Fatalf("published workflow = %#v", published)
	}
	current, err := database.Roadmap.CurrentRoadmap(ctx)
	if err != nil {
		t.Fatalf("load published roadmap: %v", err)
	}
	if current.Revision == initialRoadmap.Revision || len(current.ChallengeBindings) != len(initialRoadmap.ChallengeBindings)+1 {
		t.Fatalf("published roadmap = %#v", current)
	}
	if !hasChallengeBinding(current, "platform-runtime/systemd-service-recovery/workflow-lifecycle") {
		t.Fatalf("published roadmap omitted generated challenge binding: %#v", current.ChallengeBindings)
	}
}

func TestGenerationClassificationTechnicalRetryPreservesVerifiedCandidate(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 10, 0, 0, 0, time.UTC)
	initialRoadmap := publishWorkflowRoadmap(t, database, now)
	workflow, sessionID, userID := createGenerationWorkflowFixture(t, database, now)
	candidate := advanceToVerifiedCandidate(t, database, workflow.ID, now)

	start := now.Add(time.Minute)
	if _, err := database.Generation.ConfirmGenerationContent(ctx, sessionID, userID, generation.ContentConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, IdempotencyKey: "confirm-content-retry",
	}, start); err != nil {
		t.Fatalf("start classification: %v", err)
	}
	claim := claimGenerationWorkflow(t, database, workflow.ID, "classifier-a", start)
	run := startGenerationRun(t, database, claim, generationapp.ClassifierPurpose, start)
	if err := database.Generation.FinalizeGenerationClassification(ctx, claim, run.ID, existingClassification(initialRoadmap, candidate.ID), start); err != nil {
		t.Fatalf("persist initial proposal: %v", err)
	}
	if _, err := database.Generation.ResumeGenerationClassification(ctx, sessionID, userID, start.Add(time.Minute)); err != nil {
		t.Fatalf("resume classification: %v", err)
	}

	attemptAt := start.Add(time.Minute)
	for attempt := 0; attempt < generation.MaxStateAttempts; attempt++ {
		claim = claimGenerationWorkflow(t, database, workflow.ID, "classifier-retry", attemptAt)
		updated, err := database.Generation.ReportGenerationInfrastructureFailure(ctx, claim, generation.StateClassifying, generation.Failure{
			Class: generation.FailureInfrastructure, Code: "MODEL_UNAVAILABLE", Summary: "classification service is unavailable",
		}, attemptAt)
		if err != nil {
			t.Fatalf("record classification retry %d: %v", attempt+1, err)
		}
		if attempt < generation.MaxStateAttempts-1 {
			if updated.State != generation.StateClassifying || updated.StateAttempt != attempt+1 {
				t.Fatalf("classification retry %d = %#v", attempt+1, updated)
			}
			attemptAt = updated.NextRunAt
		}
	}
	paused, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load exhausted classification workflow: %v", err)
	}
	if paused.State != generation.StateNeedsClassificationReview || paused.DeadlinePausedAt == nil || !strings.HasPrefix(paused.LastError, "classification_unavailable:") {
		t.Fatalf("exhausted classification workflow = %#v", paused)
	}
	persisted, err := database.Generation.GetCandidateRevision(ctx, candidate.ID)
	if err != nil {
		t.Fatalf("load candidate after retries: %v", err)
	}
	if persisted.Classification == nil || persisted.Classification.Revision != 1 || persisted.Build == nil || persisted.Artifact == nil || persisted.Verification == nil || !persisted.Verification.Passed {
		t.Fatalf("candidate changed during classification retries = %#v", persisted)
	}
}

func TestGenerationPublicationTechnicalRetryResumesTheSameIntent(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 10, 30, 0, 0, time.UTC)
	initialRoadmap := publishWorkflowRoadmap(t, database, now)
	workflow, sessionID, userID := createGenerationWorkflowFixture(t, database, now)
	candidate := advanceToVerifiedCandidate(t, database, workflow.ID, now)

	classificationAt := now.Add(time.Minute)
	if _, err := database.Generation.ConfirmGenerationContent(ctx, sessionID, userID, generation.ContentConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, IdempotencyKey: "confirm-content-publication-retry",
	}, classificationAt); err != nil {
		t.Fatalf("start classification: %v", err)
	}
	claim := claimGenerationWorkflow(t, database, workflow.ID, "classifier-a", classificationAt)
	run := startGenerationRun(t, database, claim, generationapp.ClassifierPurpose, classificationAt)
	if err := database.Generation.FinalizeGenerationClassification(ctx, claim, run.ID, existingClassification(initialRoadmap, candidate.ID), classificationAt); err != nil {
		t.Fatalf("persist classification proposal: %v", err)
	}
	classified, err := database.Generation.GetCandidateRevision(ctx, candidate.ID)
	if err != nil {
		t.Fatalf("load classified candidate: %v", err)
	}
	publishingAt := classificationAt.Add(time.Minute)
	if _, err := database.Generation.BeginClassificationPublication(ctx, sessionID, userID, generationTestPlan().Metadata.Title, generation.PublicationConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, ProposalRevision: classified.Classification.Revision, IdempotencyKey: "confirm-publication-retry",
	}, publishingAt); err != nil {
		t.Fatalf("begin publication: %v", err)
	}

	attemptAt := publishingAt
	for attempt := 0; attempt < generation.MaxStateAttempts; attempt++ {
		claim = claimGenerationWorkflow(t, database, workflow.ID, "publisher-retry", attemptAt)
		updated, err := database.Generation.ReportGenerationInfrastructureFailure(ctx, claim, generation.StateChallengePublishing, generation.Failure{
			Class: generation.FailureInfrastructure, Code: "REGISTRY_UNAVAILABLE", Summary: "registry is unavailable",
		}, attemptAt)
		if err != nil {
			t.Fatalf("record publication retry %d: %v", attempt+1, err)
		}
		if attempt < generation.MaxStateAttempts-1 {
			if updated.State != generation.StateChallengePublishing || updated.StateAttempt != attempt+1 {
				t.Fatalf("publication retry %d = %#v", attempt+1, updated)
			}
			attemptAt = updated.NextRunAt
		}
	}
	paused, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load exhausted publication workflow: %v", err)
	}
	if paused.State != generation.StateNeedsClassificationReview || paused.DeadlinePausedAt == nil || !strings.HasPrefix(paused.LastError, "publication_unavailable:") {
		t.Fatalf("exhausted publication workflow = %#v", paused)
	}

	resumed, err := database.Generation.BeginClassificationPublication(ctx, sessionID, userID, generationTestPlan().Metadata.Title, generation.PublicationConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, ProposalRevision: classified.Classification.Revision, IdempotencyKey: "resume-publication",
	}, attemptAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("resume publication: %v", err)
	}
	if resumed.State != generation.StateChallengePublishing || resumed.CandidateRevisionID != candidate.ID || resumed.DeadlinePausedAt != nil || resumed.StateAttempt != 0 {
		t.Fatalf("resumed publication workflow = %#v", resumed)
	}
	persisted, err := database.Generation.GetCandidateRevision(ctx, candidate.ID)
	if err != nil {
		t.Fatalf("load candidate after publication retry: %v", err)
	}
	if persisted.Publication == nil || persisted.Publication.ChallengeID == "" || persisted.Artifact == nil || persisted.Verification == nil || !persisted.Verification.Passed {
		t.Fatalf("publication retry changed candidate = %#v", persisted)
	}
}

func TestConfirmedPlanSupersedesOnlyAReviewWorkflow(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 11, 0, 0, 0, time.UTC)
	publishWorkflowRoadmap(t, database, now)
	workflow, sessionID, userID := createGenerationWorkflowFixture(t, database, now)
	advanceToVerifiedCandidate(t, database, workflow.ID, now)

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
	old, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load superseded workflow: %v", err)
	}
	if old.State != generation.StateSuperseded || old.SupersededByWorkflowID != replacement.ID {
		t.Fatalf("superseded workflow = %#v", old)
	}
	repeated, err := database.Generation.CreateGenerationWorkflow(ctx, sessionID, userID, generation.StartConfirmation{
		PlanRevision: 2, IdempotencyKey: "start-replacement",
	}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("repeat replacement confirmation: %v", err)
	}
	if repeated.ID != replacement.ID {
		t.Fatalf("idempotent replacement confirmation created %q, want %q", repeated.ID, replacement.ID)
	}
}

func TestGenerationResourceReaperKeepsActiveCandidateAndReapsTerminalResources(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 12, 0, 0, 0, time.UTC)
	publishWorkflowRoadmap(t, database, now)
	workflow, sessionID, userID := createGenerationWorkflowFixture(t, database, now)
	candidate := advanceToVerifiedCandidate(t, database, workflow.ID, now)

	reapClaim, err := database.Generation.ClaimGenerationResourceReap(ctx, generation.ResourceReapCandidateArtifact, "reaper", time.Minute, now)
	if err != nil {
		t.Fatalf("claim active candidate artifact reap: %v", err)
	}
	if reapClaim != nil {
		t.Fatalf("active verified candidate was eligible for artifact reap: %#v", reapClaim)
	}
	classificationAt := now.Add(30 * time.Second)
	if _, err := database.Generation.ConfirmGenerationContent(ctx, sessionID, userID, generation.ContentConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, IdempotencyKey: "confirm-content-reaper",
	}, classificationAt); err != nil {
		t.Fatalf("start classification: %v", err)
	}
	workflowClaim := claimGenerationWorkflow(t, database, workflow.ID, "classifier-reaper", classificationAt)
	run := startGenerationRun(t, database, workflowClaim, generationapp.ClassifierPurpose, classificationAt)
	currentRoadmap, err := database.Roadmap.CurrentRoadmap(ctx)
	if err != nil {
		t.Fatalf("load current roadmap for classification: %v", err)
	}
	if err := database.Generation.FinalizeGenerationClassification(ctx, workflowClaim, run.ID, existingClassification(currentRoadmap, candidate.ID), classificationAt); err != nil {
		t.Fatalf("persist classification proposal: %v", err)
	}
	reapClaim, err = database.Generation.ClaimGenerationResourceReap(ctx, generation.ResourceReapCandidateArtifact, "reaper", time.Minute, classificationAt.Add(time.Second))
	if err != nil {
		t.Fatalf("claim classification-review artifact reap: %v", err)
	}
	if reapClaim != nil {
		t.Fatalf("classification-review candidate was eligible for artifact reap: %#v", reapClaim)
	}

	if err := database.Generation.CancelGenerationWorkflow(ctx, workflow.ID, "author cancelled generation", now.Add(time.Minute)); err != nil {
		t.Fatalf("cancel generation workflow: %v", err)
	}
	for _, kind := range []generation.ResourceReapKind{
		generation.ResourceReapVerificationEnvironment,
		generation.ResourceReapBuildArchive,
		generation.ResourceReapCandidateArtifact,
	} {
		reapClaim, err := database.Generation.ClaimGenerationResourceReap(ctx, kind, "reaper", time.Minute, now.Add(2*time.Minute))
		if err != nil {
			t.Fatalf("claim %s reap: %v", kind, err)
		}
		if reapClaim == nil || reapClaim.CandidateRevisionID != candidate.ID || reapClaim.Kind != kind || reapClaim.Candidate.ID != candidate.ID {
			t.Fatalf("claimed %s reap = %#v", kind, reapClaim)
		}
		if err := database.Generation.CompleteGenerationResourceReap(ctx, *reapClaim, "", now.Add(2*time.Minute)); err != nil {
			t.Fatalf("complete %s reap: %v", kind, err)
		}
		repeated, err := database.Generation.ClaimGenerationResourceReap(ctx, kind, "reaper", time.Minute, now.Add(3*time.Minute))
		if err != nil {
			t.Fatalf("claim completed %s reap: %v", kind, err)
		}
		if repeated != nil {
			t.Fatalf("completed %s reap was claimed again: %#v", kind, repeated)
		}
	}
}

func publishWorkflowRoadmap(t *testing.T, database *Store, now time.Time) *roadmap.Revision {
	t.Helper()
	value, err := database.Roadmap.PublishRoadmap(context.Background(), roadmaptest.RuntimeRevision(), now)
	if err != nil {
		t.Fatalf("publish workflow roadmap fixture: %v", err)
	}
	return value
}

func advanceToVerifiedCandidate(t *testing.T, database *Store, workflowID string, now time.Time) generation.Revision {
	t.Helper()
	claim := claimGenerationWorkflow(t, database, workflowID, "generator-a", now)
	candidate := finalizeGeneratedCandidate(t, database, claim, 1, now)
	claim = refreshGenerationClaim(t, database, claim, now)
	approveGenerationJudgement(t, database, claim, now)
	claim = refreshGenerationClaim(t, database, claim, now)
	advanceGenerationBuildAndArtifact(t, database, claim, now)
	claim = refreshGenerationClaim(t, database, claim, now)
	if err := database.Generation.RecordGenerationVerificationEnvironment(context.Background(), claim, verificationEnvironment(claim), now); err != nil {
		t.Fatalf("record verification environment: %v", err)
	}
	if err := database.Generation.CompleteGenerationVerification(context.Background(), claim, verificationReport(true), now); err != nil {
		t.Fatalf("complete verification: %v", err)
	}
	return candidate
}

func existingClassification(value *roadmap.Revision, candidateID string) generation.ClassificationOutput {
	for _, topic := range value.Topics {
		if topic.SourceRef != "platform-runtime/systemd-service-recovery" {
			continue
		}
		for _, tag := range value.Tags {
			if tag.SourceRef != "runtime-fixture" {
				continue
			}
			return generation.ClassificationOutput{
				Result: generation.ClassificationProposed,
				Topic:  &generation.TopicProposal{Existing: &roadmap.Ref{ID: topic.ID, SourceRef: topic.SourceRef, Title: topic.Title}, Reason: "题目要求恢复由 systemd 管理的服务。"},
				Tags:   []generation.TagProposal{{Existing: &roadmap.Ref{ID: tag.ID, SourceRef: tag.SourceRef, Title: tag.Title}, Reason: "题目依赖真实运行时验证。"}},
			}
		}
	}
	panic("workflow roadmap fixture is missing the expected topic or tag")
}

func hasChallengeBinding(value *roadmap.Revision, sourceRef string) bool {
	for _, binding := range value.ChallengeBindings {
		if binding.Challenge.SourceRef == sourceRef {
			return true
		}
	}
	return false
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
		Metadata:    authoring.Metadata{Title: "Workflow lifecycle", Difficulty: "medium", Description: "Exercise durable workflow recovery.", Runtime: "k8s"},
		Overview:    "Repair a workload and verify the observable outcome.",
		Checkpoints: []authoring.Checkpoint{{ID: "ready", Title: "Ready", Markdown: "The workload is healthy.", Position: 1}},
	}
}

func claimGenerationWorkflow(t *testing.T, database *Store, workflowID, workerID string, now time.Time) generation.Claim {
	t.Helper()
	claim, err := database.Generation.ClaimGenerationWorkflow(context.Background(), workerID, time.Minute, now)
	if err != nil {
		t.Fatalf("claim generation workflow: %v", err)
	}
	if claim == nil || claim.Workflow.ID != workflowID {
		t.Fatalf("claimed workflow = %#v, want %q", claim, workflowID)
	}
	return *claim
}

func refreshGenerationClaim(t *testing.T, database *Store, claim generation.Claim, now time.Time) generation.Claim {
	t.Helper()
	refreshed, err := database.Generation.RefreshGenerationClaim(context.Background(), claim.Workflow.ID, claim.LeaseOwner, now)
	if err != nil {
		t.Fatalf("refresh generation claim: %v", err)
	}
	return *refreshed
}

func startGenerationRun(t *testing.T, database *Store, claim generation.Claim, purpose string, now time.Time) *agent.Run {
	t.Helper()
	promptVersions := map[string]string{
		generationapp.GeneratorPurpose:  generationapp.GeneratorPromptVersion,
		generationapp.JudgePurpose:      generationapp.JudgePromptVersion,
		generationapp.ClassifierPurpose: generationapp.ClassifierPromptVersion,
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

func finalizeGeneratedCandidate(t *testing.T, database *Store, claim generation.Claim, sequence int, now time.Time) generation.Revision {
	t.Helper()
	run := startGenerationRun(t, database, claim, generationapp.GeneratorPurpose, now)
	revision := generation.Revision{
		ID:                 generation.IDForGeneratorRun(run.ID),
		Source:             claim.Workflow.Source,
		SourceRevision:     claim.Workflow.SourceRevision,
		GeneratorSessionID: run.SessionID,
		GeneratorRunID:     run.ID,
		ArchivePath:        "/tmp/generated-candidate-" + string(rune('0'+sequence)) + ".tar.gz",
		ArchiveSHA256:      workflowTestDigest,
		Snapshot:           generationTestSnapshot(),
	}
	if err := database.Generation.FinalizeGeneratedCandidate(context.Background(), claim, run.ID, revision, now); err != nil {
		t.Fatalf("finalize generated candidate %d: %v", sequence, err)
	}
	return revision
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
		Runtime: challenge.RuntimeK8s, OCIArchivePath: "/tmp/generation.oci.tar", OCIArchiveSHA256: workflowTestDigest,
	}, now); err != nil {
		t.Fatalf("complete generation build: %v", err)
	}
	claim = refreshGenerationClaim(t, database, claim, now)
	if err := database.Generation.CompleteGenerationArtifactPublish(context.Background(), claim, artifactReference(), now); err != nil {
		t.Fatalf("complete artifact publication: %v", err)
	}
}

func generationTestSnapshot() generation.ExecutionSnapshot {
	return generation.ExecutionSnapshot{
		Runtime:     challenge.RuntimeK8s,
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
		},
	}
}

func artifactReference() generation.ArtifactReference {
	return generation.ArtifactReference{Runtime: challenge.RuntimeK8s, OCIReference: "registry.example/candidate@" + workflowTestDigest}
}

func verificationEnvironment(claim generation.Claim) generation.VerificationEnvironment {
	return generation.VerificationEnvironment{
		Runtime: challenge.RuntimeK8s, Name: "verify-environment", UID: "verify-uid", WorkflowID: claim.Workflow.ID,
		Attempt: int64(claim.StateAttempt + 1),
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
