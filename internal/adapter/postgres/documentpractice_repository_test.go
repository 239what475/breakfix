package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestDocumentPracticeRepositoryAppendsAndAdvancesAnImmutableLedger(t *testing.T) {
	database := newTestDB(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	workflow, err := domain.NewWorkflow("document-workflow-01", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DocumentPractice.CreateWorkflow(context.Background(), workflow); err != nil {
		t.Fatal(err)
	}
	artifact := domain.ArtifactRecord{ID: "plan-01", Kind: "learning-unit-plan", ContentRevision: "1", Digest: testRunnableDigest("a"), SchemaVersion: domain.FormatVersion, OwnerRole: "planner", CreatedAt: now, Payload: []byte(`{"id":"plan-01"}`)}
	if err := database.DocumentPractice.AppendArtifact(context.Background(), workflow.ID, artifact); err != nil {
		t.Fatal(err)
	}
	if err := database.DocumentPractice.AppendArtifact(context.Background(), workflow.ID, artifact); err != nil {
		t.Fatal(err)
	}
	advanced, err := database.DocumentPractice.AdvanceWorkflow(context.Background(), workflow.ID, workflow.StateVersion, domain.PlanReviewing, now.Add(time.Second), artifact.Kind)
	if err != nil || advanced.State != domain.PlanReviewing || advanced.StateVersion != 2 {
		t.Fatalf("advance = %#v, %v", advanced, err)
	}
	if _, err := database.DocumentPractice.AdvanceWorkflow(context.Background(), workflow.ID, 1, domain.Generating, now.Add(2*time.Second)); err == nil {
		t.Fatal("stale state version accepted")
	}
	if _, err := database.DocumentPractice.AcquireWorkflowLease(context.Background(), workflow.ID, "server-a", time.Minute, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DocumentPractice.AcquireWorkflowLease(context.Background(), workflow.ID, "server-b", time.Minute, now.Add(4*time.Second)); err == nil {
		t.Fatal("active workflow lease was stolen")
	}
	action := runnable.ActionIdentity{Content: runnable.ContentIdentity{Kind: "documentation-practice", ID: "practice-document-01", Revision: "candidate-01"}, SpecDigest: testRunnableDigest("f"), Phase: runnable.ActionMaterializeArtifact, StateVersion: 2}
	if err := database.DocumentPractice.BindRunnableAction(context.Background(), workflow.ID, action, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if workflowID, found, err := database.DocumentPractice.WorkflowForRunnableAction(context.Background(), action); err != nil || !found || workflowID != workflow.ID {
		t.Fatalf("runnable action binding = %q %t %v", workflowID, found, err)
	}
	if err := database.DocumentPractice.MarkRunnableActionReconciled(context.Background(), action, now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentPracticeRepositoryPublishesOnlyVerifiedRuntimeBindings(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	workflow, err := domain.NewWorkflow("document-workflow-02", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DocumentPractice.CreateWorkflow(ctx, workflow); err != nil {
		t.Fatal(err)
	}
	documentContext := domain.DocumentContext{FormatVersion: domain.FormatVersion, SourceID: "kubernetes", Repository: "https://github.com/kubernetes/website", Commit: strings.Repeat("a", 40), Version: "v1.34", Language: "en", License: "CC BY 4.0", PagePath: "docs/pods.md", Anchor: "pod-lifecycle"}
	runnableRevision := testRunnableRevision(t)
	runnableRevision.Spec.Identity = runnable.ContentIdentity{Kind: "documentation-practice", ID: domain.ContentID(documentContext), Revision: "candidate-document-01"}
	specDigest, err := runnableRevision.Spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	runnableRevision.Artifact.BuiltFromSpecDigest = specDigest
	runnableDigest, err := runnableRevision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	storedRevision := runnable.StoredRevision{Reference: runnable.RevisionReference{ID: "runnable-document-01", Digest: runnableDigest}, Revision: runnableRevision, CreatedAt: now}
	if err := database.Runnable.StoreRunnableRevision(ctx, storedRevision); err != nil {
		t.Fatal(err)
	}
	report := testVerificationReport(t, runnableRevision)
	reportDigest, err := report.Digest(runnableRevision)
	if err != nil {
		t.Fatal(err)
	}
	storedReport := runnable.StoredVerificationReport{Reference: runnable.VerificationReportReference{ID: "report-document-01", Digest: reportDigest}, Report: report, RunnableRevision: runnableRevision, CreatedAt: now}
	if err := database.Runnable.StoreVerificationReport(ctx, storedReport); err != nil {
		t.Fatal(err)
	}
	review := domain.VerificationReviewBundle{ArtifactID: "verification-review-01", ArtifactDigest: testRunnableDigest("c"), ReportDigest: reportDigest, Opinions: []domain.ReviewOpinion{{ReviewerID: "review-run-01", Role: "verification", Decision: domain.ReviewApprove, PolicyVersion: "review-v1"}}, CreatedAt: now}
	plan := domain.LearningUnitPlan{FormatVersion: domain.FormatVersion, ID: "plan-01", Revision: 1, Context: documentContext, Title: "Pod lifecycle", Objective: "Observe Pod state", Boundary: "One Pod", Runtime: domain.RuntimeConstraint{Runtime: runnableRevision.Spec.RuntimeProfile.Runtime, BaseImage: runnableRevision.Spec.RuntimeProfile.BaseImage, Resources: runnableRevision.Spec.RuntimeProfile.Resources, Network: runnableRevision.Spec.RuntimeProfile.Network, Topology: runnableRevision.Spec.RuntimeProfile.Topology}, Evidence: []domain.EvidenceReference{{ID: "page", Kind: domain.EvidencePage, Path: documentContext.PagePath, Digest: testRunnableDigest("d")}}, Observations: []domain.ObservationPoint{{ID: "ready", Description: "The Pod becomes ready", EvidenceIDs: []string{"page"}}}, CreatedAt: now}
	planPayload, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	planDigest, err := domainDigestForTest(planPayload)
	if err != nil {
		t.Fatal(err)
	}
	contextPayload, err := json.Marshal(documentContext)
	if err != nil {
		t.Fatal(err)
	}
	contextArtifact := domain.ArtifactRecord{ID: "document-context-" + domain.ContentID(documentContext), Kind: "document-context", ContentRevision: documentContext.Commit, Digest: domainDigest(contextPayload), SchemaVersion: domain.FormatVersion, OwnerRole: "server", CreatedAt: now, Payload: contextPayload}
	planArtifact := domain.ArtifactRecord{ID: "plan-plan-01-r1", ParentID: contextArtifact.ID, Kind: "learning-unit-plan", ContentRevision: "1", Digest: planDigest, SchemaVersion: domain.FormatVersion, OwnerRole: "planner", CreatedAt: now, Payload: planPayload}
	planGate := domain.GateResult{ArtifactID: planArtifact.ID, ArtifactDigest: planArtifact.Digest, Decision: domain.ReviewApprove, PolicyVersion: "gate-v1", CreatedAt: now}
	candidate := domain.PracticeCandidate{FormatVersion: domain.FormatVersion, ID: "candidate-document-01", Revision: 1, PlanID: plan.ID, PlanRevision: plan.Revision, Context: documentContext, Source: runnableRevision.Spec.Source, Spec: runnableRevision.Spec, Observations: plan.Observations, CreatedAt: now}
	candidatePayload, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidateArtifact := domain.ArtifactRecord{ID: "candidate-candidate-document-01-r1", ParentID: planArtifact.ID, Kind: "practice-candidate", ContentRevision: "1", Digest: candidate.Source.Digest, SchemaVersion: domain.FormatVersion, OwnerRole: "generator", CreatedAt: now, Payload: candidatePayload}
	artifactGate := domain.GateResult{ArtifactID: candidate.ID, ArtifactDigest: candidate.Source.Digest, Decision: domain.ReviewApprove, PolicyVersion: "gate-v1", CreatedAt: now}
	manifest := domain.PublicationManifest{FormatVersion: domain.FormatVersion, ID: "manifest-document-01", Context: documentContext, PracticeCandidateID: candidate.ID, RunnableRevisionDigest: runnableDigest, EnvironmentProfileDigest: report.Environment.ProfileDigest, VerificationReportDigest: reportDigest, PlanGate: planGate, ArtifactGate: artifactGate, VerificationReview: review, CreatedAt: now}
	revision := domain.PracticeRevision{FormatVersion: domain.FormatVersion, ID: "practice-revision-01", WorkflowID: workflow.ID, Context: documentContext, PlanID: plan.ID, PlanRevision: plan.Revision, CandidateID: candidate.ID, RunnableRevisionRef: storedRevision.Reference, VerificationReportRef: storedReport.Reference, PublicationManifestID: manifest.ID, PublishedAt: now}
	if err := database.DocumentPractice.AppendArtifact(ctx, workflow.ID, contextArtifact); err != nil {
		t.Fatal(err)
	}
	appendAndAdvanceDocumentWorkflow(t, database.DocumentPractice, ctx, workflow.ID, planArtifact, domain.PlanReviewing, now)
	planGatePayload, _ := json.Marshal(planGate)
	appendAndAdvanceDocumentWorkflow(t, database.DocumentPractice, ctx, workflow.ID, domain.ArtifactRecord{ID: "plan-gate-" + planArtifact.ID, ParentID: planArtifact.ID, Kind: "plan-gate", ContentRevision: "1", Digest: testRunnableDigest("e"), SchemaVersion: domain.FormatVersion, OwnerRole: "server", CreatedAt: now, Payload: planGatePayload}, domain.Generating, now)
	appendAndAdvanceDocumentWorkflow(t, database.DocumentPractice, ctx, workflow.ID, candidateArtifact, domain.ArtifactReviewing, now)
	artifactGatePayload, _ := json.Marshal(artifactGate)
	appendAndAdvanceDocumentWorkflow(t, database.DocumentPractice, ctx, workflow.ID, domain.ArtifactRecord{ID: "artifact-gate-" + candidateArtifact.ID, ParentID: candidateArtifact.ID, Kind: "artifact-gate", ContentRevision: "1", Digest: testRunnableDigest("f"), SchemaVersion: domain.FormatVersion, OwnerRole: "server", CreatedAt: now, Payload: artifactGatePayload}, domain.MaterializingArtifact, now)
	revisionPayload, _ := json.Marshal(storedRevision.Reference)
	appendAndAdvanceDocumentWorkflow(t, database.DocumentPractice, ctx, workflow.ID, domain.ArtifactRecord{ID: "runnable-revision-" + storedRevision.Reference.ID, Kind: "runnable-revision", ContentRevision: "candidate-document-01", Digest: testRunnableDigest("1"), SchemaVersion: domain.FormatVersion, OwnerRole: "runtime-worker", CreatedAt: now, Payload: revisionPayload}, domain.Verifying, now)
	reportPayload, _ := json.Marshal(storedReport.Reference)
	appendAndAdvanceDocumentWorkflow(t, database.DocumentPractice, ctx, workflow.ID, domain.ArtifactRecord{ID: "verification-report-" + storedReport.Reference.ID, Kind: "verification-report", ContentRevision: "candidate-document-01", Digest: testRunnableDigest("2"), SchemaVersion: domain.FormatVersion, OwnerRole: "runtime-worker", CreatedAt: now, Payload: reportPayload}, domain.VerificationReviewing, now)
	reviewPayload, _ := json.Marshal(review)
	appendAndAdvanceDocumentWorkflow(t, database.DocumentPractice, ctx, workflow.ID, domain.ArtifactRecord{ID: "verification-review-" + storedReport.Reference.ID, Kind: "verification-review", ContentRevision: runnableDigest, Digest: testRunnableDigest("3"), SchemaVersion: domain.FormatVersion, OwnerRole: "verification-review", CreatedAt: now, Payload: reviewPayload}, domain.Publishing, now)
	workflow, err = database.DocumentPractice.GetWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.DocumentPractice.PublishPracticeRevision(ctx, workflow.ID, workflow.StateVersion, revision, manifest, now.Add(time.Second)); err != nil {
		t.Fatalf("publish practice revision: %v", err)
	}
	artifacts, err := database.DocumentPractice.ListArtifacts(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if artifact, found := documentArtifactByKind(artifacts, "publication-manifest"); !found || artifact.ParentID != "verification-review-"+storedReport.Reference.ID || !sameJSON(artifact.Payload, manifest) {
		t.Fatalf("publication manifest was not appended to the immutable ledger: %#v", artifact)
	}
	if _, err := database.DocumentPractice.PublishPracticeRevision(ctx, workflow.ID, workflow.StateVersion, revision, manifest, now.Add(2*time.Second)); err != nil {
		t.Fatalf("repeat publication: %v", err)
	}
	manifest.VerificationReportDigest = testRunnableDigest("f")
	if _, err := database.DocumentPractice.PublishPracticeRevision(ctx, workflow.ID, workflow.StateVersion, revision, manifest, now.Add(3*time.Second)); err == nil {
		t.Fatal("mismatched verification digest was published")
	}
}

func TestSameJSONIgnoresObjectKeyOrder(t *testing.T) {
	value := domain.GateResult{
		ArtifactID:     "plan-pod-lifecycle-r1",
		ArtifactDigest: testRunnableDigest("a"),
		Decision:       domain.ReviewApprove,
		PolicyVersion:  "document-gate-v1",
		CreatedAt:      time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC),
	}
	// PostgreSQL jsonb returns objects in its canonical key order, which is
	// intentionally different from the Go struct field order.
	payload := []byte(`{"decision":"approve","created_at":"2026-09-15T12:00:00Z","artifact_id":"plan-pod-lifecycle-r1","policy_version":"document-gate-v1","artifact_digest":"` + testRunnableDigest("a") + `"}`)
	if !sameJSON(payload, value) {
		t.Fatal("equivalent JSON objects with a different key order did not match")
	}
}

func documentArtifactByKind(artifacts []domain.ArtifactRecord, kind string) (domain.ArtifactRecord, bool) {
	for _, artifact := range artifacts {
		if artifact.Kind == kind {
			return artifact, true
		}
	}
	return domain.ArtifactRecord{}, false
}

func appendAndAdvanceDocumentWorkflow(t *testing.T, repository *DocumentPracticeRepository, ctx context.Context, workflowID string, artifact domain.ArtifactRecord, next domain.WorkflowState, now time.Time) {
	t.Helper()
	if err := repository.AppendArtifact(ctx, workflowID, artifact); err != nil {
		t.Fatal(err)
	}
	workflow, err := repository.GetWorkflow(ctx, workflowID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AdvanceWorkflow(ctx, workflowID, workflow.StateVersion, next, now.Add(time.Duration(workflow.StateVersion)*time.Second), artifact.Kind); err != nil {
		t.Fatal(err)
	}
}

func domainDigestForTest(value []byte) (string, error) {
	if len(value) == 0 {
		return "", fmt.Errorf("empty JSON")
	}
	return domainDigest(value), nil
}
