package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	generationapp "github.com/breakfix/breakfix/internal/application/generation"
	taxonomyapp "github.com/breakfix/breakfix/internal/application/taxonomy"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/taxonomy"
)

const workflowTestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestGenerationWorkflowPersistsRepairAndPublicationLifecycle(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 1, 9, 0, 0, 0, time.UTC)
	workflow, sessionID, userID := createGenerationWorkflowFixture(t, database, now)

	claim := claimGenerationWorkflow(t, database, workflow.ID, "generator-a", now)
	candidateOne := finalizeGeneratedCandidate(t, database, claim, 1, now)
	claim = refreshGenerationClaim(t, database, claim, now)
	if claim.Workflow.State != generation.StateJudging {
		t.Fatalf("state after generation = %s, want Judging", claim.Workflow.State)
	}
	judgeRun := startGenerationRun(t, database, claim, generationapp.JudgePurpose, now)
	if err := database.Generation.FinalizeGenerationJudgement(ctx, claim, judgeRun.ID, false, "检查点描述与实际题目不一致", now); err != nil {
		t.Fatalf("reject generated candidate: %v", err)
	}
	claim = refreshGenerationClaim(t, database, claim, now)
	if claim.Workflow.State != generation.StateGenerating || claim.Workflow.CandidateRevisionID != candidateOne.ID {
		t.Fatalf("workflow after judge repair = %#v", claim.Workflow)
	}

	candidateTwo := finalizeGeneratedCandidate(t, database, claim, 2, now)
	claim = refreshGenerationClaim(t, database, claim, now)
	approveGenerationJudgement(t, database, claim, now)
	claim = refreshGenerationClaim(t, database, claim, now)
	advanceGenerationBuildAndArtifact(t, database, claim, now)
	claim = refreshGenerationClaim(t, database, claim, now)
	if err := database.Generation.RecordGenerationVerificationEnvironment(ctx, claim, verificationEnvironment(claim), now); err != nil {
		t.Fatalf("record failed candidate verification environment: %v", err)
	}
	if err := database.Generation.CompleteGenerationVerification(ctx, claim, verificationReport(false), now); err != nil {
		t.Fatalf("record failed candidate verification: %v", err)
	}
	claim = refreshGenerationClaim(t, database, claim, now)
	if claim.Workflow.State != generation.StateGenerating || claim.Workflow.CandidateRevisionID != candidateTwo.ID {
		t.Fatalf("workflow after verification repair = %#v", claim.Workflow)
	}

	firstVerifiedCandidate := finalizeGeneratedCandidate(t, database, claim, 3, now)
	claim = refreshGenerationClaim(t, database, claim, now)
	approveGenerationJudgement(t, database, claim, now)
	claim = refreshGenerationClaim(t, database, claim, now)
	advanceGenerationBuildAndArtifact(t, database, claim, now)
	claim = refreshGenerationClaim(t, database, claim, now)
	if err := database.Generation.RecordGenerationVerificationEnvironment(ctx, claim, verificationEnvironment(claim), now); err != nil {
		t.Fatalf("record passing candidate verification environment: %v", err)
	}
	if err := database.Generation.CompleteGenerationVerification(ctx, claim, verificationReport(true), now); err != nil {
		t.Fatalf("record passing candidate verification: %v", err)
	}
	paused, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load workflow waiting for author: %v", err)
	}
	if paused.State != generation.StateNeedsAuthorReview || paused.LeaseOwner != "" || paused.DeadlinePausedAt == nil {
		t.Fatalf("workflow after passing verification = %#v", paused)
	}
	resumedAt := now.Add(10 * time.Minute)
	resumed, err := database.Generation.ResumeGenerationForRevision(ctx, sessionID, userID, 1, resumedAt)
	if err != nil {
		t.Fatalf("resume generation for author feedback: %v", err)
	}
	if resumed.State != generation.StateGenerating || resumed.DeadlinePausedAt != nil || resumed.DeadlineAt == nil || !resumed.DeadlineAt.After(*paused.DeadlineAt) {
		t.Fatalf("resumed generation workflow = %#v", resumed)
	}
	claim = claimGenerationWorkflow(t, database, workflow.ID, "generator-c", resumedAt)
	finalCandidate := finalizeGeneratedCandidate(t, database, claim, 4, resumedAt)
	claim = refreshGenerationClaim(t, database, claim, resumedAt)
	approveGenerationJudgement(t, database, claim, resumedAt)
	claim = refreshGenerationClaim(t, database, claim, resumedAt)
	advanceGenerationBuildAndArtifact(t, database, claim, resumedAt)
	claim = refreshGenerationClaim(t, database, claim, resumedAt)
	if err := database.Generation.RecordGenerationVerificationEnvironment(ctx, claim, verificationEnvironment(claim), resumedAt); err != nil {
		t.Fatalf("record reverified candidate environment: %v", err)
	}
	if err := database.Generation.CompleteGenerationVerification(ctx, claim, verificationReport(true), resumedAt); err != nil {
		t.Fatalf("record reverified candidate: %v", err)
	}
	paused, err = database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load reverified workflow: %v", err)
	}
	if paused.State != generation.StateNeedsAuthorReview || paused.CandidateRevisionID != finalCandidate.ID {
		t.Fatalf("workflow after author-requested regeneration = %#v", paused)
	}
	if firstVerifiedCandidate.ID == finalCandidate.ID {
		t.Fatal("author-requested regeneration reused its previous candidate revision")
	}

	challengeID := challenge.NewID()
	publicationTime := resumedAt.Add(10 * time.Minute)
	if _, err := database.Generation.BeginGenerationPublication(ctx, sessionID, userID, generation.Publication{
		ChallengeID: challengeID,
		SourceSlug:  challenge.SourceSlugFor("Workflow lifecycle", challengeID),
		TargetPath:  challenge.SourceSlugFor("Workflow lifecycle", challengeID),
		RequestedAt: publicationTime,
	}, publicationTime); err != nil {
		t.Fatalf("begin generation publication: %v", err)
	}
	claim = claimGenerationWorkflow(t, database, workflow.ID, "generator-b", publicationTime)
	if claim.Workflow.State != generation.StateChallengePublishing {
		t.Fatalf("state after author publication = %s, want ChallengePublishing", claim.Workflow.State)
	}
	if err := database.Generation.CompleteGenerationChallengePublish(ctx, claim, artifactReference(), challengeID, workflowTestDigest, "", publicationTime); err != nil {
		t.Fatalf("complete challenge publication: %v", err)
	}
	claim = refreshGenerationClaim(t, database, claim, publicationTime)
	if claim.Workflow.State != generation.StateCleaningUp || claim.Workflow.CleanupIntent != generation.CleanupCompleted {
		t.Fatalf("workflow after challenge publication = %#v", claim.Workflow)
	}
	if err := database.Generation.CompleteGenerationCleanup(ctx, claim, publicationTime); err != nil {
		t.Fatalf("complete generation cleanup: %v", err)
	}
	completed, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load completed workflow: %v", err)
	}
	if completed.State != generation.StateCompleted {
		t.Fatalf("completed workflow state = %s, want Completed", completed.State)
	}
	if completed.CandidateRevisionID != finalCandidate.ID {
		t.Fatalf("published candidate = %q, want %q", completed.CandidateRevisionID, finalCandidate.ID)
	}
	if _, err := database.Taxonomy.GetTaxonomyWorkflowByChallenge(ctx, challengeID, workflowTestDigest); err != nil {
		t.Fatalf("taxonomy workflow was not created for published challenge: %v", err)
	}
}

func TestGenerationWorkflowReclaimsInfrastructureRetryAndCleansTerminalStates(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 1, 10, 0, 0, 0, time.UTC)
	workflow, _, _ := createGenerationWorkflowFixture(t, database, now)
	claim := claimGenerationWorkflow(t, database, workflow.ID, "generator-a", now)
	failed, err := database.Generation.ReportGenerationInfrastructureFailure(ctx, claim, generation.StateGenerating, generation.Failure{
		Class: generation.FailureInfrastructure, Code: "REGISTRY_UNAVAILABLE", Summary: "registry temporarily unavailable",
	}, now)
	if err != nil {
		t.Fatalf("record infrastructure retry: %v", err)
	}
	if failed.State != generation.StateGenerating || failed.StateAttempt != 1 || failed.LeaseOwner != "" {
		t.Fatalf("workflow after infrastructure retry = %#v", failed)
	}
	takeover := claimGenerationWorkflow(t, database, workflow.ID, "generator-b", failed.NextRunAt)
	if takeover.Workflow.State != generation.StateGenerating || takeover.Workflow.StateAttempt != 1 || takeover.LeaseOwner == claim.LeaseOwner {
		t.Fatalf("workflow was not reclaimed with a fresh lease = %#v", takeover)
	}

	if err := database.Generation.RecoverExpiredGenerationWorkflows(ctx, *takeover.Workflow.DeadlineAt); err != nil {
		t.Fatalf("expire generation workflow: %v", err)
	}
	expired, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load expired workflow: %v", err)
	}
	if expired.State != generation.StateCleaningUp || expired.CleanupIntent != generation.CleanupFailed {
		t.Fatalf("expired workflow = %#v", expired)
	}
	cleanup := claimGenerationWorkflow(t, database, workflow.ID, "generator-c", *takeover.Workflow.DeadlineAt)
	if err := database.Generation.CompleteGenerationCleanup(ctx, cleanup, *takeover.Workflow.DeadlineAt); err != nil {
		t.Fatalf("complete failed cleanup: %v", err)
	}
	terminal, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load failed workflow: %v", err)
	}
	if terminal.State != generation.StateFailed {
		t.Fatalf("expired workflow terminal state = %s, want Failed", terminal.State)
	}

	cancelledWorkflow, _, _ := createGenerationWorkflowFixture(t, database, now.Add(time.Hour))
	if err := database.Generation.CancelGenerationWorkflow(ctx, cancelledWorkflow.ID, "author discarded this draft", now.Add(time.Hour)); err != nil {
		t.Fatalf("cancel generation workflow: %v", err)
	}
	cancelCleanup := claimGenerationWorkflow(t, database, cancelledWorkflow.ID, "generator-d", now.Add(time.Hour))
	if cancelCleanup.Workflow.State != generation.StateCleaningUp || cancelCleanup.Workflow.CleanupIntent != generation.CleanupCancelled {
		t.Fatalf("cancelled workflow cleanup state = %#v", cancelCleanup.Workflow)
	}
	if err := database.Generation.CompleteGenerationCleanup(ctx, cancelCleanup, now.Add(time.Hour)); err != nil {
		t.Fatalf("complete cancelled cleanup: %v", err)
	}
	cancelled, err := database.Generation.GetGenerationWorkflow(ctx, cancelledWorkflow.ID)
	if err != nil {
		t.Fatalf("load cancelled workflow: %v", err)
	}
	if cancelled.State != generation.StateCancelled {
		t.Fatalf("cancelled workflow terminal state = %s, want Cancelled", cancelled.State)
	}
}

func TestGenerationLeaseRenewsAfterSuccessfulPhaseResetsAttempt(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	workflow, _, _ := createGenerationWorkflowFixture(t, database, now)

	initial := claimGenerationWorkflow(t, database, workflow.ID, "generate-a", now)
	retried, err := database.Generation.ReportGenerationInfrastructureFailure(ctx, initial, generation.StateGenerating, generation.Failure{
		Class: generation.FailureInfrastructure, Code: "MODEL_UNAVAILABLE", Summary: "model temporarily unavailable",
	}, now)
	if err != nil {
		t.Fatalf("record generation retry: %v", err)
	}
	claim := claimGenerationWorkflow(t, database, workflow.ID, "generate-a", retried.NextRunAt)
	if claim.Workflow.StateAttempt != 1 {
		t.Fatalf("retry state attempt = %d, want 1", claim.Workflow.StateAttempt)
	}

	finalizeGeneratedCandidate(t, database, claim, 1, retried.NextRunAt)
	advanced, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load advanced generation workflow: %v", err)
	}
	if advanced.State != generation.StateJudging || advanced.StateAttempt != 0 || advanced.LeaseOwner != claim.LeaseOwner {
		t.Fatalf("advanced generation workflow = %#v", advanced)
	}
	oldExpiry := *advanced.LeaseExpiresAt
	if err := database.Generation.RenewGenerationLease(ctx, claim, time.Minute, retried.NextRunAt.Add(time.Second)); err != nil {
		t.Fatalf("renew generation lease after phase transition: %v", err)
	}
	renewed, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load renewed generation workflow: %v", err)
	}
	if renewed.LeaseExpiresAt == nil || !renewed.LeaseExpiresAt.After(oldExpiry) {
		t.Fatalf("generation lease expiry was not extended: %#v", renewed.LeaseExpiresAt)
	}
}

func TestTaxonomyWorkflowPersistsCommitteeRoundsAndReclaimsTechnicalFailures(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 1, 11, 0, 0, 0, time.UTC)
	challengeID := "chal-taxonomy-round"
	challengeRevision := "sha256:" + strings.Repeat("b", 64)
	workflow, _, err := database.Taxonomy.CreateOrGetTaxonomyWorkflow(ctx, challengeID, challengeRevision, "base-0", now)
	if err != nil {
		t.Fatalf("create taxonomy workflow: %v", err)
	}
	claim := claimTaxonomyWorkflow(t, database, workflow.ID, "taxonomy-a", now)
	changes := taxonomyTestChangeSet(challengeID, challengeRevision)
	mapper := startTaxonomyRun(t, database, claim, taxonomyapp.AgentRoleMapper, now)
	if err := database.Taxonomy.FinalizeTaxonomyMapper(ctx, claim, mapper.ID, changes, now); err != nil {
		t.Fatalf("finalize first mapper: %v", err)
	}
	claim = refreshTaxonomyClaim(t, database, claim, now)
	curriculum := startTaxonomyRun(t, database, claim, taxonomyapp.AgentRoleCurriculumReviewer, now)
	sre := startTaxonomyRun(t, database, claim, taxonomyapp.AgentRoleSREReviewer, now)
	if err := database.Taxonomy.FinalizeTaxonomyReviewPair(ctx, claim, curriculum.ID, sre.ID,
		taxonomy.Review{Decision: taxonomy.ReviewReject, Feedback: "请明确入口能力与学习结果的边界"},
		taxonomy.Review{Decision: taxonomy.ReviewApprove}, "base-1", now); err != nil {
		t.Fatalf("finalize rejected reviewer pair: %v", err)
	}
	rejected, err := database.Taxonomy.GetTaxonomyWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load rejected taxonomy workflow: %v", err)
	}
	if rejected.State != taxonomy.WorkflowMapping || rejected.Round != 1 || rejected.LeaseOwner != "" || rejected.BaseTaxonomyRevision != "base-1" {
		t.Fatalf("workflow after semantic reject = %#v", rejected)
	}
	claim = claimTaxonomyWorkflow(t, database, workflow.ID, "taxonomy-b", now)
	mapper = startTaxonomyRun(t, database, claim, taxonomyapp.AgentRoleMapper, now)
	if err := database.Taxonomy.FinalizeTaxonomyMapper(ctx, claim, mapper.ID, changes, now); err != nil {
		t.Fatalf("finalize second mapper: %v", err)
	}
	claim = refreshTaxonomyClaim(t, database, claim, now)
	curriculum = startTaxonomyRun(t, database, claim, taxonomyapp.AgentRoleCurriculumReviewer, now)
	sre = startTaxonomyRun(t, database, claim, taxonomyapp.AgentRoleSREReviewer, now)
	if err := database.Taxonomy.FinalizeTaxonomyReviewPair(ctx, claim, curriculum.ID, sre.ID,
		taxonomy.Review{Decision: taxonomy.ReviewApprove}, taxonomy.Review{Decision: taxonomy.ReviewApprove}, "base-1", now); err != nil {
		t.Fatalf("finalize approved reviewer pair: %v", err)
	}
	claim = refreshTaxonomyClaim(t, database, claim, now)
	if claim.Workflow.State != taxonomy.WorkflowPublishing || claim.Workflow.Round != 1 {
		t.Fatalf("workflow after approved committee = %#v", claim.Workflow)
	}
	if err := database.Taxonomy.SetTaxonomyExpectedSnapshot(ctx, claim, "sha256:"+strings.Repeat("c", 64), now); err != nil {
		t.Fatalf("record expected taxonomy snapshot: %v", err)
	}
	if err := database.Taxonomy.CompleteTaxonomyPublication(ctx, claim, "sha256:"+strings.Repeat("c", 64), now); err != nil {
		t.Fatalf("complete taxonomy publication: %v", err)
	}

	retryID := "chal-taxonomy-retry"
	retryRevision := "sha256:" + strings.Repeat("d", 64)
	retryWorkflow, _, err := database.Taxonomy.CreateOrGetTaxonomyWorkflow(ctx, retryID, retryRevision, "base-0", now)
	if err != nil {
		t.Fatalf("create retry taxonomy workflow: %v", err)
	}
	retryClaim := claimTaxonomyWorkflow(t, database, retryWorkflow.ID, "taxonomy-c", now)
	mapper = startTaxonomyRun(t, database, retryClaim, taxonomyapp.AgentRoleMapper, now)
	if err := database.Taxonomy.FinalizeTaxonomyMapper(ctx, retryClaim, mapper.ID, taxonomyTestChangeSet(retryID, retryRevision), now); err != nil {
		t.Fatalf("finalize retry mapper: %v", err)
	}
	retryClaim = refreshTaxonomyClaim(t, database, retryClaim, now)
	for attempt := 1; attempt <= 10; attempt++ {
		curriculum = startTaxonomyRun(t, database, retryClaim, taxonomyapp.AgentRoleCurriculumReviewer, now)
		sre = startTaxonomyRun(t, database, retryClaim, taxonomyapp.AgentRoleSREReviewer, now)
		updated, released, err := database.Taxonomy.ReportTaxonomyTechnicalFailure(ctx, retryClaim, taxonomy.WorkflowReviewing, "reviewer transport failed", []string{curriculum.ID, sre.ID}, now)
		if err != nil {
			t.Fatalf("report reviewer technical failure %d: %v", attempt, err)
		}
		if updated.StateAttempt != attempt {
			t.Fatalf("state attempt = %d, want %d", updated.StateAttempt, attempt)
		}
		if attempt < 10 {
			if released || updated.LeaseOwner == "" {
				t.Fatalf("technical attempt %d released the workflow too early", attempt)
			}
			retryClaim = refreshTaxonomyClaim(t, database, retryClaim, now)
			continue
		}
		if !released || updated.LeaseOwner != "" {
			t.Fatalf("tenth technical attempt did not release workflow = %#v", updated)
		}
		retryClaim = claimTaxonomyWorkflow(t, database, retryWorkflow.ID, "taxonomy-d", updated.NextRunAt)
		if retryClaim.Workflow.StateAttempt != 0 || retryClaim.Workflow.State != taxonomy.WorkflowReviewing {
			t.Fatalf("reclaimed retry workflow = %#v", retryClaim.Workflow)
		}
	}
}

func TestTaxonomyLeaseRenewsAfterSuccessfulPhaseResetsAttempt(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 1, 13, 0, 0, 0, time.UTC)
	challengeID := "chal-taxonomy-lease"
	challengeRevision := "sha256:" + strings.Repeat("e", 64)
	workflow, _, err := database.Taxonomy.CreateOrGetTaxonomyWorkflow(ctx, challengeID, challengeRevision, "base-0", now)
	if err != nil {
		t.Fatalf("create taxonomy workflow: %v", err)
	}
	initial := claimTaxonomyWorkflow(t, database, workflow.ID, "taxonomy-a", now)
	mapperRun := startTaxonomyRun(t, database, initial, taxonomyapp.AgentRoleMapper, now)
	updated, released, err := database.Taxonomy.ReportTaxonomyTechnicalFailure(ctx, initial, taxonomy.WorkflowMapping, "mapper transport failed", []string{mapperRun.ID}, now)
	if err != nil {
		t.Fatalf("record taxonomy retry: %v", err)
	}
	if released || updated.StateAttempt != 1 {
		t.Fatalf("taxonomy retry = %#v, released=%v", updated, released)
	}
	claim := refreshTaxonomyClaim(t, database, initial, now)
	mapperRun = startTaxonomyRun(t, database, claim, taxonomyapp.AgentRoleMapper, now)
	if err := database.Taxonomy.FinalizeTaxonomyMapper(ctx, claim, mapperRun.ID, taxonomyTestChangeSet(challengeID, challengeRevision), now); err != nil {
		t.Fatalf("finalize taxonomy mapper: %v", err)
	}
	advanced, err := database.Taxonomy.GetTaxonomyWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load advanced taxonomy workflow: %v", err)
	}
	if advanced.State != taxonomy.WorkflowReviewing || advanced.StateAttempt != 0 || advanced.LeaseOwner != claim.LeaseOwner {
		t.Fatalf("advanced taxonomy workflow = %#v", advanced)
	}
	oldExpiry := *advanced.LeaseExpiresAt
	if err := database.Taxonomy.RenewTaxonomyLease(ctx, claim, time.Minute, now.Add(time.Second)); err != nil {
		t.Fatalf("renew taxonomy lease after phase transition: %v", err)
	}
	renewed, err := database.Taxonomy.GetTaxonomyWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load renewed taxonomy workflow: %v", err)
	}
	if renewed.LeaseExpiresAt == nil || !renewed.LeaseExpiresAt.After(oldExpiry) {
		t.Fatalf("taxonomy lease expiry was not extended: %#v", renewed.LeaseExpiresAt)
	}
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
	workflow, err := database.Generation.CreateGenerationWorkflow(ctx, session.ID, session.UserID, 1, now)
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
	run, err := database.Generation.StartGenerationAgentRun(context.Background(), claim, agent.CreateRun{
		ID:            agent.NewID("generation-test-run"),
		Purpose:       purpose,
		OwnerKind:     "generation-workflow",
		OwnerRef:      claim.Workflow.ID,
		Model:         "test-model",
		PromptVersion: map[string]string{generationapp.GeneratorPurpose: generationapp.GeneratorPromptVersion, generationapp.JudgePurpose: generationapp.JudgePromptVersion}[purpose],
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

func claimTaxonomyWorkflow(t *testing.T, database *Store, workflowID, workerID string, now time.Time) taxonomy.Claim {
	t.Helper()
	claim, err := database.Taxonomy.ClaimTaxonomyWorkflow(context.Background(), workerID, time.Minute, now)
	if err != nil {
		t.Fatalf("claim taxonomy workflow: %v", err)
	}
	if claim == nil || claim.Workflow.ID != workflowID {
		t.Fatalf("claimed taxonomy workflow = %#v, want %q", claim, workflowID)
	}
	return *claim
}

func refreshTaxonomyClaim(t *testing.T, database *Store, claim taxonomy.Claim, now time.Time) taxonomy.Claim {
	t.Helper()
	refreshed, err := database.Taxonomy.RefreshTaxonomyClaim(context.Background(), claim.Workflow.ID, claim.LeaseOwner, now)
	if err != nil {
		t.Fatalf("refresh taxonomy claim: %v", err)
	}
	return *refreshed
}

func startTaxonomyRun(t *testing.T, database *Store, claim taxonomy.Claim, role taxonomyapp.AgentRole, now time.Time) *agent.Run {
	t.Helper()
	run, err := database.Taxonomy.StartTaxonomyAgentRun(context.Background(), claim, role, "test-model", now)
	if err != nil {
		t.Fatalf("start %s taxonomy run: %v", role, err)
	}
	return run
}

func taxonomyTestChangeSet(challengeID, revision string) taxonomy.ChangeSet {
	return taxonomy.ChangeSet{ChallengeMappings: []taxonomy.ChallengeMappingChange{{
		Operation: taxonomy.ChangeUpsert,
		Value:     &taxonomy.ChallengeMapping{Challenge: taxonomy.ChallengeRef{ID: challengeID, Title: "Taxonomy workflow", Revision: revision}},
	}}}
}
