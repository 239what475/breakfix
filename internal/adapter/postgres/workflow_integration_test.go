package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	generationapp "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/environment"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/publication"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
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
		classified.ClassificationRoadmapRevision != initialRoadmap.Revision || classified.RuntimeAttempt != 0 {
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
	if awaitingReview.State != generation.StateNeedsClassificationReview || awaitingReview.RuntimeAttempt != 0 || awaitingReview.LeaseOwner != "" {
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
	if publishing.State != generation.StateChallengePublishing || publishing.RuntimeAttempt != 1 || publishing.CandidateRevisionID != candidate.ID {
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
	if err := database.Generation.RecordGenerationChallengePublicationResult(ctx, claim, finalArtifact, publishAt); err != nil {
		t.Fatalf("record classification promotion: %v", err)
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
	if transient.State != generation.StateChallengePublishing || transient.FinalizerErrorCategory != publication.CategoryTransient || transient.FinalizerNextRetryAt == nil {
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
	if err := database.Generation.FinalizeGenerationChallengePublication(ctx, workflow.ID, candidate.ID, "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", publishAt); err != nil {
		t.Fatalf("finalize classification publication: %v", err)
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
	for _, binding := range current.ChallengeBindings {
		if binding.Challenge.SourceRef != "platform-runtime/systemd-service-recovery/workflow-lifecycle" {
			continue
		}
		if binding.Challenge.SourceSlug == "" || binding.Challenge.MaterializedRevision != "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" {
			t.Fatalf("generated binding omitted materialized identity: %#v", binding.Challenge)
		}
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
	if _, err := database.Generation.ResumeGenerationClassification(ctx, sessionID, userID, generation.ClassificationAdjustmentConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, ProposalRevision: 1,
		Feedback: "请重新检查当前分类提案。", IdempotencyKey: "retry-classification",
	}, start.Add(time.Minute)); err != nil {
		t.Fatalf("resume classification: %v", err)
	}

	attemptAt := start.Add(time.Minute)
	claim = claimGenerationWorkflow(t, database, workflow.ID, "classifier-retry", attemptAt)
	run = startGenerationRun(t, database, claim, generationapp.ClassifierPurpose, attemptAt)
	for attempt := 1; attempt <= agent.MaxAttempts; attempt++ {
		next, err := database.Generation.RetryGenerationAgentRun(ctx, claim, run.ID, "classification service is unavailable", attemptAt.Add(time.Duration(attempt)*time.Second))
		if err != nil {
			t.Fatalf("record classification retry %d: %v", attempt, err)
		}
		if attempt < agent.MaxAttempts {
			if next == nil || next.ID != run.ID || next.Attempt != attempt+1 {
				t.Fatalf("classification retry %d = %#v", attempt, next)
			}
			run = next
		}
	}
	paused, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load exhausted classification workflow: %v", err)
	}
	if paused.State != generation.StateNeedsClassificationReview || paused.RuntimeAttempt != 0 || !strings.HasPrefix(paused.LastError, "classification_unavailable:") {
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

func TestGenerationClassificationAdjustmentRetainsItsPinnedRoadmapRevision(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 10, 15, 0, 0, time.UTC)
	initialRoadmap := publishWorkflowRoadmap(t, database, now)
	workflow, sessionID, userID := createGenerationWorkflowFixture(t, database, now)
	candidate := advanceToVerifiedCandidate(t, database, workflow.ID, now)

	classificationAt := now.Add(time.Minute)
	if _, err := database.Generation.ConfirmGenerationContent(ctx, sessionID, userID, generation.ContentConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, IdempotencyKey: "confirm-content-adjustment",
	}, classificationAt); err != nil {
		t.Fatalf("start classification: %v", err)
	}
	claim := claimGenerationWorkflow(t, database, workflow.ID, "classifier-initial", classificationAt)
	run := startGenerationRun(t, database, claim, generationapp.ClassifierPurpose, classificationAt)
	if err := database.Generation.FinalizeGenerationClassification(ctx, claim, run.ID, existingClassification(initialRoadmap, candidate.ID), classificationAt); err != nil {
		t.Fatalf("persist initial proposal: %v", err)
	}

	newerRoadmap := initialRoadmap.Clone()
	newerRoadmap.Tags = append(newerRoadmap.Tags, roadmap.Tag{
		ID: roadmap.RuntimeID(roadmap.KindTag, "later-added"), SourceRef: "later-added", Title: "Later added", Description: "A later unrelated Roadmap revision.",
	})
	currentRoadmap, err := database.Roadmap.PublishRoadmap(ctx, newerRoadmap, classificationAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("publish unrelated roadmap revision: %v", err)
	}
	if currentRoadmap.Revision == initialRoadmap.Revision {
		t.Fatalf("unrelated roadmap publication did not create a new revision: %#v", currentRoadmap)
	}

	resumedAt := classificationAt.Add(2 * time.Minute)
	adjustment := generation.ClassificationAdjustmentConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, ProposalRevision: 1,
		Feedback: "移除与题目无关的标签。", IdempotencyKey: "adjust-classification",
	}
	resumed, err := database.Generation.ResumeGenerationClassification(ctx, sessionID, userID, adjustment, resumedAt)
	if err != nil {
		t.Fatalf("resume classification adjustment: %v", err)
	}
	if resumed.ClassificationRoadmapRevision != initialRoadmap.Revision || resumed.ClassificationFeedback != "移除与题目无关的标签。" {
		t.Fatalf("resumed classification = %#v", resumed)
	}
	repeated, err := database.Generation.ResumeGenerationClassification(ctx, sessionID, userID, adjustment, resumedAt.Add(time.Second))
	if err != nil {
		t.Fatalf("repeat classification adjustment: %v", err)
	}
	if repeated.ID != resumed.ID || repeated.State != generation.StateClassifying || repeated.ClassificationFeedback != adjustment.Feedback {
		t.Fatalf("idempotent classification adjustment = %#v", repeated)
	}

	claim = claimGenerationWorkflow(t, database, workflow.ID, "classifier-adjustment", resumedAt)
	run = startGenerationRun(t, database, claim, generationapp.ClassifierPurpose, resumedAt)
	output := existingClassification(initialRoadmap, candidate.ID)
	output.Tags = nil
	if err := database.Generation.FinalizeGenerationClassificationAdjustment(ctx, claim, generation.ClassificationAdjustment{
		RunID: run.ID, ChangeScope: generation.ClassificationChangeClassification, Output: &output,
	}, resumedAt); err != nil {
		t.Fatalf("persist adjusted proposal: %v", err)
	}

	persisted, err := database.Generation.GetCandidateRevision(ctx, candidate.ID)
	if err != nil {
		t.Fatalf("load adjusted candidate: %v", err)
	}
	if persisted.Classification == nil || persisted.Classification.Revision != 2 || persisted.Classification.RoadmapRevision != initialRoadmap.Revision || len(persisted.Classification.Tags) != 0 {
		t.Fatalf("adjusted classification = %#v", persisted.Classification)
	}
	current, err := database.Roadmap.CurrentRoadmap(ctx)
	if err != nil {
		t.Fatalf("load current roadmap: %v", err)
	}
	if current.Revision != currentRoadmap.Revision {
		t.Fatalf("private classification adjustment changed the global roadmap: got %q, want %q", current.Revision, currentRoadmap.Revision)
	}
}

func TestGenerationPublicationRejectsStaleClassificationRoadmapRevision(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 10, 20, 0, 0, time.UTC)
	initialRoadmap := publishWorkflowRoadmap(t, database, now)
	workflow, sessionID, userID := createGenerationWorkflowFixture(t, database, now)
	candidate := advanceToVerifiedCandidate(t, database, workflow.ID, now)

	classificationAt := now.Add(time.Minute)
	if _, err := database.Generation.ConfirmGenerationContent(ctx, sessionID, userID, generation.ContentConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, IdempotencyKey: "confirm-content-stale-roadmap",
	}, classificationAt); err != nil {
		t.Fatalf("start classification: %v", err)
	}
	claim := claimGenerationWorkflow(t, database, workflow.ID, "classifier-stale-roadmap", classificationAt)
	run := startGenerationRun(t, database, claim, generationapp.ClassifierPurpose, classificationAt)
	if err := database.Generation.FinalizeGenerationClassification(ctx, claim, run.ID, existingClassification(initialRoadmap, candidate.ID), classificationAt); err != nil {
		t.Fatalf("persist classification proposal: %v", err)
	}
	before, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load classification review workflow: %v", err)
	}

	newerRoadmap := initialRoadmap.Clone()
	newerRoadmap.Tags = append(newerRoadmap.Tags, roadmap.Tag{
		ID: roadmap.RuntimeID(roadmap.KindTag, "stale-roadmap-tag"), SourceRef: "stale-roadmap-tag", Title: "Stale roadmap tag", Description: "Creates a distinct current Roadmap revision.",
	})
	if _, err := database.Roadmap.PublishRoadmap(ctx, newerRoadmap, classificationAt.Add(time.Minute)); err != nil {
		t.Fatalf("publish newer roadmap: %v", err)
	}

	_, err = database.Generation.BeginClassificationPublication(ctx, sessionID, userID, generationTestPlan().Metadata.Title, generation.PublicationConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, ProposalRevision: 1, IdempotencyKey: "confirm-stale-roadmap-publication",
	}, classificationAt.Add(2*time.Minute))
	if !errors.Is(err, generation.ErrClassificationConflict) {
		t.Fatalf("publish stale classification = %v, want roadmap conflict", err)
	}
	after, err := database.Generation.GetGenerationWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load stale classification workflow: %v", err)
	}
	if after.State != generation.StateNeedsClassificationReview || after.StateVersion != before.StateVersion || after.LastError != generation.ErrClassificationConflict.Error() {
		t.Fatalf("stale classification workflow = %#v", after)
	}
	persisted, err := database.Generation.GetCandidateRevision(ctx, candidate.ID)
	if err != nil {
		t.Fatalf("load stale classification candidate: %v", err)
	}
	if persisted.Classification == nil || persisted.Classification.RoadmapRevision != initialRoadmap.Revision || persisted.Publication != nil {
		t.Fatalf("stale classification proposal was changed or published: %#v", persisted)
	}
}

func TestGenerationPublicationFailsAfterFiveRuntimeAttempts(t *testing.T) {
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
	for attempt := 1; attempt <= generation.MaxRuntimeAttempts; attempt++ {
		claim = claimGenerationWorkflow(t, database, workflow.ID, "publisher-retry", attemptAt)
		updated, err := database.Generation.ReportGenerationInfrastructureFailure(ctx, claim, generation.StateChallengePublishing, generation.Failure{
			Class: generation.FailureInfrastructure, Code: "REGISTRY_UNAVAILABLE", Summary: "registry is unavailable",
		}, attemptAt)
		if err != nil {
			t.Fatalf("record publication retry %d: %v", attempt, err)
		}
		if attempt < generation.MaxRuntimeAttempts {
			if updated.State != generation.StateChallengePublishing || updated.RuntimeAttempt != attempt+1 {
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
	if persisted.Publication == nil || persisted.Publication.ChallengeID == "" || persisted.Artifact == nil || persisted.Verification == nil || !persisted.Verification.Passed {
		t.Fatalf("publication retry changed candidate = %#v", persisted)
	}
}

func TestGenerationRuntimeLeaseTakeoverRetainsActionVersion(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 10, 45, 0, 0, time.UTC)
	publishWorkflowRoadmap(t, database, now)
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
	publishWorkflowRoadmap(t, database, now)
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
	if _, _, err := database.Authoring.StartAuthoringRun(ctx, session.ID, session.UserID, agent.Message{Role: "user", Content: "继续完善题意。"}, agent.CreateRun{
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

func TestAuthoringRunRecoveryReplacesPrivateStageAndFencesOldAttempt(t *testing.T) {
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
	stage, run, err := database.Authoring.StartAuthoringRun(ctx, session.ID, session.UserID, agent.Message{Role: "user", Content: "继续完善题意。"}, agent.CreateRun{
		ID: agent.NewID("authoring-run"), SessionID: session.RuntimeSessionID, Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: session.ID,
		Model: "test-model", PromptVersion: "authoring-v2",
	})
	if err != nil {
		t.Fatalf("start authoring run: %v", err)
	}
	next, err := database.Authoring.RetryAuthoringRun(ctx, run.ID, run.Attempt, "model transport unavailable", time.Now().UTC())
	if err != nil || next == nil || next.Attempt != 2 {
		t.Fatalf("retry authoring run = %#v, err=%v", next, err)
	}
	if _, err := database.Authoring.UpdateAuthoringStage(ctx, run.ID, 1, stage.StageRevision, stage.Plan, authoring.Change{
		Kind: "overview", Summary: "stale attempt", DifficultyImpact: "unchanged",
	}); !errors.Is(err, agent.ErrRunActive) {
		t.Fatalf("old attempt stage write = %v, want active-run fence", err)
	}
	replacement, err := database.Authoring.RestartInterruptedAuthoringRun(ctx, run.ID, "server restarted", time.Now().UTC())
	if err != nil {
		t.Fatalf("restart authoring run: %v", err)
	}
	if replacement == nil || replacement.ID == run.ID || replacement.Attempt != 1 || replacement.DeadlineAt.IsZero() {
		t.Fatalf("replacement authoring run = %#v", replacement)
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
	replacementStage, err := database.Authoring.GetAuthoringStage(ctx, replacement.ID)
	if err != nil {
		t.Fatalf("read replacement stage: %v", err)
	}
	if replacementStage.RunAttempt != 1 || replacementStage.BaseRevision != session.CurrentRevision || replacementStage.Plan.Metadata.Title != plan.Metadata.Title {
		t.Fatalf("replacement stage = %#v", replacementStage)
	}
	messages, err := database.Authoring.ListMessages(ctx, session.RuntimeSessionID)
	if err != nil {
		t.Fatalf("read authoring messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("restart rewrote authoring conversation: %#v", messages)
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
	publishWorkflowRoadmap(t, database, now)
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
	publishWorkflowRoadmap(t, database, now)
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
	publishWorkflowRoadmap(t, database, now)
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
	reapClaim, err = database.Generation.ClaimGenerationResourceReap(ctx, "reaper", time.Minute, classificationAt.Add(time.Second))
	if err != nil {
		t.Fatalf("claim classification-review artifact reap: %v", err)
	}
	if reapClaim != nil {
		t.Fatalf("classification-review candidate was eligible for artifact reap: %#v", reapClaim)
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

func publishWorkflowRoadmap(t *testing.T, database *Store, now time.Time) *roadmap.Revision {
	t.Helper()
	value, err := database.Roadmap.PublishRoadmap(context.Background(), roadmaptest.RuntimeRevision(), now)
	if err != nil {
		t.Fatalf("publish workflow roadmap fixture: %v", err)
	}
	return value
}

func prepareGenerationPublication(t *testing.T, database *Store, now time.Time) (*generation.Workflow, generation.Revision, time.Time) {
	t.Helper()
	initialRoadmap := publishWorkflowRoadmap(t, database, now)
	workflow, sessionID, userID := createGenerationWorkflowFixture(t, database, now)
	candidate := advanceToVerifiedCandidate(t, database, workflow.ID, now)
	classificationAt := now.Add(10 * time.Minute)
	if _, err := database.Generation.ConfirmGenerationContent(context.Background(), sessionID, userID, generation.ContentConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, IdempotencyKey: "prepare-content",
	}, classificationAt); err != nil {
		t.Fatalf("start classification: %v", err)
	}
	claim := claimGenerationWorkflow(t, database, workflow.ID, "classifier-prepare", classificationAt)
	run := startGenerationRun(t, database, claim, generationapp.ClassifierPurpose, classificationAt)
	if err := database.Generation.FinalizeGenerationClassification(context.Background(), claim, run.ID, existingClassification(initialRoadmap, candidate.ID), classificationAt); err != nil {
		t.Fatalf("persist classification: %v", err)
	}
	persisted, err := database.Generation.GetCandidateRevision(context.Background(), candidate.ID)
	if err != nil {
		t.Fatalf("load classified candidate: %v", err)
	}
	publishAt := classificationAt.Add(10 * time.Minute)
	if _, err := database.Generation.BeginClassificationPublication(context.Background(), sessionID, userID, generationTestPlan().Metadata.Title, generation.PublicationConfirmation{
		WorkflowID: workflow.ID, CandidateRevisionID: candidate.ID, ProposalRevision: persisted.Classification.Revision, IdempotencyKey: "prepare-publication",
	}, publishAt); err != nil {
		t.Fatalf("begin publication: %v", err)
	}
	claim = claimGenerationWorkflow(t, database, workflow.ID, "publisher-prepare", publishAt)
	if err := database.Generation.RecordGenerationChallengePublicationResult(context.Background(), claim, generation.ArtifactReference{
		Runtime: challenge.RuntimeK8s, OCIReference: "registry.example/challenge@" + workflowTestDigest,
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
	workflow, err := database.Generation.GetGenerationWorkflow(context.Background(), workflowID)
	if err != nil {
		t.Fatalf("read generation workflow before claim: %v", err)
	}
	var claim *generation.Claim
	if workflow.State == generation.StateJudging || workflow.State == generation.StateClassifying {
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
		Runtime: challenge.RuntimeK8s, OCIReference: "registry.example/build@" + workflowTestDigest,
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
			Network: environment.VK8sNetwork{
				PublicEgressCIDR: "0.0.0.0/0",
				ProtectedCIDRs:   []string{"10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16"},
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
