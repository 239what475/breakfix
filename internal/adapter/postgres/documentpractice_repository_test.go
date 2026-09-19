package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/audit"
	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func testWorkflowIdentity() domain.WorkflowPageIdentity {
	return domain.WorkflowPageIdentity{SourceID: "kubernetes", Commit: strings.Repeat("a", 40), Language: "en", PagePath: "docs/concepts/workloads/pods/pod-lifecycle", Anchor: "pod-lifetime"}
}

func TestDocumentPracticeRepositoryAppendsAndAdvancesAnImmutableLedger(t *testing.T) {
	database := newTestDB(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	workflow, err := domain.NewWorkflow("document-workflow-01", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DocumentPractice.CreateWorkflow(context.Background(), workflow, testWorkflowIdentity(), nil); err != nil {
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
	if err := database.DocumentPractice.CreateWorkflow(ctx, workflow, testWorkflowIdentity(), nil); err != nil {
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
	planArtifact := domain.ArtifactRecord{ID: "plan-plan-01-r1-a1", ParentID: contextArtifact.ID, Kind: "learning-unit-plan", ContentRevision: "1", Digest: planDigest, SchemaVersion: domain.FormatVersion, OwnerRole: "planner", CreatedAt: now, Payload: planPayload}
	planGate := domain.GateResult{ArtifactID: planArtifact.ID, ArtifactDigest: planArtifact.Digest, Decision: domain.ReviewApprove, PolicyVersion: "gate-v1", CreatedAt: now}
	candidate := domain.PracticeCandidate{FormatVersion: domain.FormatVersion, ID: "candidate-document-01", Revision: 1, PlanID: plan.ID, PlanRevision: plan.Revision, Context: documentContext, Source: runnableRevision.Spec.Source, Spec: runnableRevision.Spec, Observations: plan.Observations, CreatedAt: now}
	candidatePayload, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidateArtifact := domain.ArtifactRecord{ID: "candidate-candidate-document-01-r1-a1", ParentID: planArtifact.ID, Kind: "practice-candidate", ContentRevision: "1", Digest: candidate.Source.Digest, SchemaVersion: domain.FormatVersion, OwnerRole: "generator", CreatedAt: now, Payload: candidatePayload}
	artifactGate := domain.GateResult{ArtifactID: candidate.ID, ArtifactDigest: candidate.Source.Digest, Decision: domain.ReviewApprove, PolicyVersion: "gate-v1", CreatedAt: now}
	manifest := domain.PublicationManifest{FormatVersion: domain.FormatVersion, ID: "manifest-document-01", Context: documentContext, PracticeCandidateID: candidate.ID, RunnableRevisionDigest: runnableDigest, EnvironmentProfileDigest: report.Environment.ProfileDigest, VerificationReportDigest: reportDigest, PlanGate: planGate, ArtifactGate: artifactGate, VerificationReview: review, CreatedAt: now}
	revision := domain.PracticeRevision{FormatVersion: domain.FormatVersion, ID: "practice-revision-01", WorkflowID: workflow.ID, Context: documentContext, PlanID: plan.ID, PlanRevision: plan.Revision, WorkflowRevision: 1, CandidateID: candidate.ID, RunnableRevisionRef: storedRevision.Reference, VerificationReportRef: storedReport.Reference, PublicationManifestID: manifest.ID, ReaderProjection: domain.ReaderProjectionFromPlan(plan), PublishedAt: now}
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
	if _, err := database.conn.ExecContext(ctx, `CREATE FUNCTION fail_document_practice_revision_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced document publication failure'; END; $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(ctx, `CREATE TRIGGER fail_document_practice_revision_insert BEFORE INSERT ON document_practice_revisions FOR EACH ROW EXECUTE FUNCTION fail_document_practice_revision_insert()`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.DocumentPractice.PublishPracticeRevision(ctx, workflow.ID, workflow.StateVersion, revision, manifest, now.Add(time.Second)); err == nil {
		t.Fatal("forced publication failure succeeded")
	}
	if _, err := database.conn.ExecContext(ctx, `DROP TRIGGER fail_document_practice_revision_insert ON document_practice_revisions`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(ctx, `DROP FUNCTION fail_document_practice_revision_insert()`); err != nil {
		t.Fatal(err)
	}
	var manifests, revisions, indexEntries int
	if err := database.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_publication_manifests`).Scan(&manifests); err != nil || manifests != 0 {
		t.Fatalf("failed publication committed a manifest: count=%d err=%v", manifests, err)
	}
	if err := database.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_practice_revisions`).Scan(&revisions); err != nil || revisions != 0 {
		t.Fatalf("failed publication committed a revision: count=%d err=%v", revisions, err)
	}
	if err := database.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_practice_index`).Scan(&indexEntries); err != nil || indexEntries != 0 {
		t.Fatalf("failed publication committed an index entry: count=%d err=%v", indexEntries, err)
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
	var storedRevisionJSON []byte
	if err := database.conn.QueryRowContext(ctx, `SELECT revision FROM document_practice_revisions WHERE id = ?`, revision.ID).Scan(&storedRevisionJSON); err != nil {
		t.Fatal(err)
	}
	var persisted domain.PracticeRevision
	if err := json.Unmarshal(storedRevisionJSON, &persisted); err != nil {
		t.Fatal(err)
	}
	projection := persisted.ReaderProjection
	if projection == nil || projection.Title != "Pod lifecycle" || projection.Objective != "Observe Pod state" || projection.Boundary != "One Pod" || len(projection.Steps) != 0 || len(projection.Observations) != 1 || projection.Observations[0] != "The Pod becomes ready" {
		t.Fatalf("reader projection was not persisted intact: %+v", projection)
	}
	if persisted.ReaderProjection != nil && strings.Contains(string(storedRevisionJSON), "evidence_ids") {
		t.Fatal("reader projection leaked evidence bindings into the persisted revision")
	}
	if _, err := database.DocumentPractice.PublishPracticeRevision(ctx, workflow.ID, workflow.StateVersion, revision, manifest, now.Add(2*time.Second)); err != nil {
		t.Fatalf("repeat publication: %v", err)
	}
	manifest.VerificationReportDigest = testRunnableDigest("f")
	if _, err := database.DocumentPractice.PublishPracticeRevision(ctx, workflow.ID, workflow.StateVersion, revision, manifest, now.Add(3*time.Second)); err == nil {
		t.Fatal("mismatched verification digest was published")
	}

	// The reader index returns exactly the anchor the page registered.
	summaries, err := database.DocumentPractice.ListPublishedPracticesForPage(ctx, documentContext.SourceID, documentContext.Commit, documentContext.Language, documentContext.PagePath)
	if err != nil || len(summaries) != 1 || summaries[0].Anchor != documentContext.Anchor || summaries[0].PracticeID != revision.ID || summaries[0].Title != "Pod lifecycle" {
		t.Fatalf("published practice summaries = %#v, %v", summaries, err)
	}
	empty, err := database.DocumentPractice.ListPublishedPracticesForPage(ctx, documentContext.SourceID, documentContext.Commit, documentContext.Language, "docs/other.md")
	if err != nil || len(empty) != 0 {
		t.Fatalf("other pages must have no practices: %#v, %v", empty, err)
	}
	found, err := database.DocumentPractice.GetPublishedPractice(ctx, revision.ID)
	if err != nil || found.ReaderProjection == nil || found.ReaderProjection.Title != "Pod lifecycle" || found.RunnableRevisionRef != storedRevision.Reference {
		t.Fatalf("published practice read = %#v, %v", found, err)
	}

	// The single legacy record published before projections existed is not
	// backfilled: stripping its projection makes it invisible to readers,
	// exactly like an unknown ID.
	if _, err := database.conn.ExecContext(ctx, `UPDATE document_practice_revisions SET revision = revision - 'reader_projection' WHERE id = ?`, revision.ID); err != nil {
		t.Fatal(err)
	}
	legacy, err := database.DocumentPractice.ListPublishedPracticesForPage(ctx, documentContext.SourceID, documentContext.Commit, documentContext.Language, documentContext.PagePath)
	if err != nil || len(legacy) != 0 {
		t.Fatalf("projection-less record must be invisible: %#v, %v", legacy, err)
	}
	if _, err := database.DocumentPractice.GetPublishedPractice(ctx, revision.ID); !errors.Is(err, ErrPublishedPracticeNotFound) {
		t.Fatalf("projection-less record read err = %v", err)
	}
	if _, err := database.DocumentPractice.GetPublishedPractice(ctx, "practice-absent"); !errors.Is(err, ErrPublishedPracticeNotFound) {
		t.Fatalf("unknown practice read err = %v", err)
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

func TestCreateWorkflowRecordsTheIgnitionAuditInTheSameTransaction(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	workflow, err := domain.NewWorkflow("document-workflow-audit", now)
	if err != nil {
		t.Fatal(err)
	}
	action := audit.HumanAction{
		ID:         audit.NewID(now),
		UserID:     "u-admin",
		Action:     audit.ActionDocumentationPracticeStart,
		TargetType: audit.TargetDocumentWorkflow,
		TargetID:   workflow.ID,
		Detail:     json.RawMessage(`{"workflow_id":"document-workflow-audit"}`),
		CreatedAt:  now,
	}
	if err := database.DocumentPractice.CreateWorkflow(ctx, workflow, testWorkflowIdentity(), &action); err != nil {
		t.Fatalf("create workflow with audit: %v", err)
	}
	rows, err := database.Audit.ListHumanActions(ctx, HumanActionFilter{Action: audit.ActionDocumentationPracticeStart, Limit: 10})
	if err != nil {
		t.Fatalf("list ignition audits: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != action.ID {
		t.Fatalf("ignition audit rows = %#v, want exactly the committed action", rows)
	}

	// A rolled-back creation never leaves its audit row behind.
	duplicate := action
	duplicate.ID = audit.NewID(now.Add(time.Millisecond))
	if err := database.DocumentPractice.CreateWorkflow(ctx, workflow, testWorkflowIdentity(), &duplicate); err == nil {
		t.Fatalf("duplicate workflow creation = nil error, want failure")
	}
	rows, err = database.Audit.ListHumanActions(ctx, HumanActionFilter{Action: audit.ActionDocumentationPracticeStart, Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ignition audit rows after rollback = %#v, %v", rows, err)
	}
}

func seedWorkflowInState(t *testing.T, database *Store, id string, state domain.WorkflowState, updatedAt time.Time) domain.Workflow {
	t.Helper()
	ctx := context.Background()
	workflow, err := domain.NewWorkflow(id, updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DocumentPractice.CreateWorkflow(ctx, workflow, testWorkflowIdentity(), nil); err != nil {
		t.Fatal(err)
	}
	for workflow.State != state {
		var next domain.WorkflowState
		switch workflow.State {
		case domain.Planning:
			next = domain.PlanReviewing
		case domain.PlanReviewing:
			if state == domain.NoPractice {
				next = domain.NoPractice
			} else {
				next = domain.Generating
			}
		case domain.Generating:
			next = domain.ArtifactReviewing
		case domain.ArtifactReviewing:
			next = domain.MaterializingArtifact
		default:
			t.Fatalf("cannot advance seed workflow from %s to %s", workflow.State, state)
		}
		updated, err := database.DocumentPractice.AdvanceWorkflow(ctx, id, workflow.StateVersion, next, updatedAt, nil...)
		if err != nil {
			t.Fatalf("advance seed workflow to %s: %v", next, err)
		}
		workflow = updated
	}
	return workflow
}

func adminActionFor(t *testing.T, actionName, actor, target string, now time.Time) audit.HumanAction {
	t.Helper()
	return audit.HumanAction{
		ID:         audit.NewID(now),
		UserID:     actor,
		Action:     actionName,
		TargetType: audit.TargetDocumentWorkflow,
		TargetID:   target,
		Detail:     json.RawMessage(`{}`),
		CreatedAt:  now,
	}
}

func TestForceFailWorkflowRecordsTheTwinAuditTrail(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	stuckAt := time.Now().UTC().Add(-time.Hour)
	workflow := seedWorkflowInState(t, database, "document-workflow-stuck", domain.PlanReviewing, stuckAt)
	now := time.Now().UTC()
	action := adminActionFor(t, audit.ActionDocumentationWorkflowForceFail, "u-admin", workflow.ID, now)

	forced, err := database.DocumentPractice.ForceFailWorkflow(ctx, workflow.ID, "agent stage crashed", &action, now)
	if err != nil {
		t.Fatalf("force fail: %v", err)
	}
	if forced.State != domain.Failed || forced.StateVersion != workflow.StateVersion+1 {
		t.Fatalf("forced workflow = %s v%d, want Failed v%d", forced.State, forced.StateVersion, workflow.StateVersion+1)
	}
	// The ledger carries the administrative decision with its from-state.
	artifacts, err := database.DocumentPractice.ListArtifacts(ctx, workflow.ID)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("ledger after force fail = %#v, %v", artifacts, err)
	}
	if artifacts[0].Kind != "admin.force_fail" || artifacts[0].OwnerRole != "admin" {
		t.Fatalf("force fail ledger entry = %#v", artifacts[0])
	}
	var payload struct {
		Actor     string    `json:"actor"`
		FromState string    `json:"from_state"`
		Reason    string    `json:"reason"`
		At        time.Time `json:"at"`
	}
	if err := json.Unmarshal(artifacts[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Actor != "u-admin" || payload.FromState != string(domain.PlanReviewing) || payload.Reason != "agent stage crashed" {
		t.Fatalf("force fail payload = %#v", payload)
	}
	// The human audit row commits with the state change and carries the
	// transition summary in its detail.
	rows, err := database.Audit.ListHumanActions(ctx, HumanActionFilter{Action: audit.ActionDocumentationWorkflowForceFail, Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("force fail audit rows = %#v, %v", rows, err)
	}
	var detail map[string]string
	if err := json.Unmarshal(rows[0].Detail, &detail); err != nil {
		t.Fatal(err)
	}
	if detail["reason"] != "agent stage crashed" || detail["from_state"] != "PlanReviewing" || detail["to_state"] != "Failed" {
		t.Fatalf("force fail audit detail = %#v", detail)
	}

	// Repeating the verb on the terminal workflow conflicts and leaves no trace.
	repeat := adminActionFor(t, audit.ActionDocumentationWorkflowForceFail, "u-admin", workflow.ID, now.Add(time.Second))
	if _, err := database.DocumentPractice.ForceFailWorkflow(ctx, workflow.ID, "again", &repeat, now.Add(time.Second)); !errors.Is(err, domain.ErrWorkflowConflict) {
		t.Fatalf("repeat force fail = %v, want conflict", err)
	}
	rows, err = database.Audit.ListHumanActions(ctx, HumanActionFilter{Action: audit.ActionDocumentationWorkflowForceFail, Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("audit rows after rejected repeat = %#v, %v", rows, err)
	}

	// Unknown workflows are not found.
	missing := adminActionFor(t, audit.ActionDocumentationWorkflowForceFail, "u-admin", "document-workflow-missing", now)
	if _, err := database.DocumentPractice.ForceFailWorkflow(ctx, "document-workflow-missing", "x", &missing, now); !errors.Is(err, domain.ErrWorkflowNotFound) {
		t.Fatalf("force fail missing workflow = %v, want not found", err)
	}
}

func TestRestartWorkflowResetsFailedAndRejectedPastTheRevisionCap(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	stuckAt := time.Now().UTC().Add(-time.Hour)
	workflow := seedWorkflowInState(t, database, "document-workflow-restart", domain.PlanReviewing, stuckAt)
	now := time.Now().UTC()
	forceAction := adminActionFor(t, audit.ActionDocumentationWorkflowForceFail, "u-admin", workflow.ID, now)
	forced, err := database.DocumentPractice.ForceFailWorkflow(ctx, workflow.ID, "stuck", &forceAction, now)
	if err != nil {
		t.Fatalf("force fail before restart: %v", err)
	}

	restartAction := adminActionFor(t, audit.ActionDocumentationWorkflowRestart, "u-admin", workflow.ID, now.Add(time.Second))
	restarted, err := database.DocumentPractice.RestartWorkflow(ctx, workflow.ID, "retry", &restartAction, now.Add(time.Second))
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if restarted.State != domain.Planning || restarted.Revision != forced.Revision+1 || restarted.StateVersion != forced.StateVersion+1 {
		t.Fatalf("restarted workflow = %#v", restarted)
	}
	artifacts, err := database.DocumentPractice.ListArtifacts(ctx, workflow.ID)
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("ledger after restart = %#v, %v", artifacts, err)
	}
	if artifacts[1].Kind != "admin.restart" || artifacts[1].OwnerRole != "admin" {
		t.Fatalf("restart ledger entry = %#v", artifacts[1])
	}
	rows, err := database.Audit.ListHumanActions(ctx, HumanActionFilter{Action: audit.ActionDocumentationWorkflowRestart, Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].TargetID != workflow.ID {
		t.Fatalf("restart audit rows = %#v, %v", rows, err)
	}

	// Restart only resets state; a non-terminal workflow conflicts.
	second := adminActionFor(t, audit.ActionDocumentationWorkflowRestart, "u-admin", workflow.ID, now.Add(2*time.Second))
	if _, err := database.DocumentPractice.RestartWorkflow(ctx, workflow.ID, "again", &second, now.Add(2*time.Second)); !errors.Is(err, domain.ErrWorkflowConflict) {
		t.Fatalf("restart from Planning = %v, want conflict", err)
	}

	// A published workflow conflicts, even with revisions still available.
	// Force-fail is the only path out of a published state (nothing at all).
	terminal := seedWorkflowInState(t, database, "document-workflow-nopractice", domain.NoPractice, stuckAt)
	third := adminActionFor(t, audit.ActionDocumentationWorkflowRestart, "u-admin", terminal.ID, now)
	if _, err := database.DocumentPractice.RestartWorkflow(ctx, terminal.ID, "nope", &third, now); !errors.Is(err, domain.ErrWorkflowConflict) {
		t.Fatalf("restart from NoPractice = %v, want conflict", err)
	}

}

func TestForceFailAndRestartIgnoreTheMaxRevisionsCap(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	workflow := seedWorkflowInState(t, database, "document-workflow-cap", domain.PlanReviewing, now.Add(-time.Minute))
	// Cap the automatic revision loop at its current revision.
	if _, err := database.conn.ExecContext(ctx, `UPDATE document_workflows SET max_revisions = revision WHERE id = ?`, workflow.ID); err != nil {
		t.Fatal(err)
	}
	forceAction := adminActionFor(t, audit.ActionDocumentationWorkflowForceFail, "u-admin", workflow.ID, now)
	forced, err := database.DocumentPractice.ForceFailWorkflow(ctx, workflow.ID, "stuck", &forceAction, now)
	if err != nil {
		t.Fatalf("force fail: %v", err)
	}
	// The revision cap does not bind a restart: the revision advances past it.
	restartAction := adminActionFor(t, audit.ActionDocumentationWorkflowRestart, "u-admin", workflow.ID, now.Add(time.Second))
	restarted, err := database.DocumentPractice.RestartWorkflow(ctx, workflow.ID, "retry", &restartAction, now.Add(time.Second))
	if err != nil {
		t.Fatalf("restart with exhausted revisions: %v", err)
	}
	if restarted.State != domain.Planning || restarted.Revision != forced.Revision+1 {
		t.Fatalf("restarted workflow = %#v", restarted)
	}
}

func TestWorkflowObservationJoinsTheBoundActionStatus(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	workflow := seedWorkflowInState(t, database, "document-workflow-observe", domain.Planning, now)
	observation, err := database.DocumentPractice.GetWorkflowObservation(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	if observation.Workflow.ID != workflow.ID || observation.Action != nil {
		t.Fatalf("agent-phase observation = %#v, want no bound action", observation)
	}
	list, _, err := database.DocumentPractice.ListWorkflowObservations(ctx, DocumentWorkflowListFilter{Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("observation list = %#v, %v", list, err)
	}
	if list[0].Identity.PagePath != "docs/concepts/workloads/pods/pod-lifecycle" || list[0].Identity.Anchor != "pod-lifetime" {
		t.Fatalf("observation identity = %#v", list[0].Identity)
	}
	if _, err := database.DocumentPractice.GetWorkflowObservation(ctx, "document-workflow-missing"); !errors.Is(err, domain.ErrWorkflowNotFound) {
		t.Fatalf("missing observation = %v, want not found", err)
	}
	agentAudits, err := database.DocumentPractice.ListAgentAudits(ctx, workflow.ID)
	if err != nil || len(agentAudits) != 0 {
		t.Fatalf("agent audits = %#v, %v", agentAudits, err)
	}
	publication, err := database.DocumentPractice.GetWorkflowPublication(ctx, workflow.ID)
	if err != nil || publication != nil {
		t.Fatalf("publication = %#v, %v", publication, err)
	}
}

func TestWorkflowObservationJoinsFailedAndExhaustedRunnableActions(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	workflow := seedWorkflowInState(t, database, "document-workflow-actions", domain.MaterializingArtifact, now)
	identity := runnable.ActionIdentity{
		Content:      runnable.ContentIdentity{Kind: "practice", ID: "page-1", Revision: "1"},
		SpecDigest:   testRunnableDigest("a"),
		Phase:        runnable.ActionMaterializeArtifact,
		StateVersion: workflow.StateVersion,
	}
	if err := database.DocumentPractice.BindRunnableAction(ctx, workflow.ID, identity, now); err != nil {
		t.Fatalf("bind runnable action: %v", err)
	}
	if _, err := database.conn.ExecContext(ctx, `INSERT INTO runnable_sources (source_digest, archive, created_at) VALUES (?, ?, ?)`, testRunnableDigest("source"), []byte("archive"), now.UTC()); err != nil {
		t.Fatalf("insert runnable source: %v", err)
	}
	if _, err := database.conn.ExecContext(ctx, `INSERT INTO runnable_specs (spec_digest, content_kind, content_id, content_revision, source_digest, spec, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		testRunnableDigest("a"), identity.Content.Kind, identity.Content.ID, identity.Content.Revision, testRunnableDigest("source"), []byte(`{}`), now.UTC()); err != nil {
		t.Fatalf("insert runnable spec: %v", err)
	}
	if _, err := database.conn.ExecContext(ctx, `INSERT INTO runnable_actions (action_key, content_kind, content_id, content_revision, spec_digest, phase, state_version, state, attempt, next_run_at, failure_class, failure_code, failure_summary, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'failed', 5, ?, 'infrastructure', 'env-lost', 'environment vanished', ?, ?)`,
		identity.Key(), identity.Content.Kind, identity.Content.ID, identity.Content.Revision, identity.SpecDigest, identity.Phase, identity.StateVersion, now.UTC(), now.UTC(), now.UTC()); err != nil {
		t.Fatalf("insert failed runnable action: %v", err)
	}

	observation, err := database.DocumentPractice.GetWorkflowObservation(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	if observation.Action == nil || observation.Action.State != "failed" || observation.Action.FailureCode != "env-lost" || observation.Action.FailureSummary != "environment vanished" || observation.Action.Attempt != 5 {
		t.Fatalf("failed action observation = %#v", observation.Action)
	}

	// The attempts-exhausted signature: still queued after five attempts.
	if _, err := database.conn.ExecContext(ctx, `UPDATE runnable_actions SET state = 'queued', failure_class = '', failure_code = '', failure_summary = '' WHERE action_key = ?`, identity.Key()); err != nil {
		t.Fatal(err)
	}
	observation, err = database.DocumentPractice.GetWorkflowObservation(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("get observation: %v", err)
	}
	if observation.Action == nil || observation.Action.State != "queued" || observation.Action.Attempt != 5 {
		t.Fatalf("exhausted action observation = %#v", observation.Action)
	}
}

func TestValidatePublicationLedgerBindsTheEvidenceTriple(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	context := domain.DocumentContext{FormatVersion: domain.FormatVersion, SourceID: "kubernetes", Repository: "https://github.com/kubernetes/website", Commit: strings.Repeat("a", 40), Version: "v1.34", Language: "en", License: "CC BY 4.0", PagePath: "docs/pods.md", Anchor: "pod-lifecycle", ParserVersion: "docs-project-v10", PageDigest: testRunnableDigest("e")}
	plan := domain.LearningUnitPlan{FormatVersion: domain.FormatVersion, ID: "plan-01", Revision: 1, Context: context, Title: "Pod lifecycle", Objective: "Observe Pod state", Boundary: "One Pod", Runtime: domain.RuntimeConstraint{Runtime: "k8s"}, Evidence: []domain.EvidenceReference{{ID: "page", Kind: domain.EvidencePage, Path: context.PagePath, Digest: testRunnableDigest("d")}}, CreatedAt: now}
	planPayload, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	contextPayload, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	contextArtifact := domain.ArtifactRecord{ID: "document-context-" + domain.ContentID(context), Kind: "document-context", ContentRevision: context.Commit, Digest: testRunnableDigest("c"), SchemaVersion: domain.FormatVersion, OwnerRole: "server", CreatedAt: now, Payload: contextPayload}
	planArtifact := domain.ArtifactRecord{ID: "plan-plan-01-r1-a1", ParentID: contextArtifact.ID, Kind: "learning-unit-plan", ContentRevision: "1", Digest: testRunnableDigest("plan"), SchemaVersion: domain.FormatVersion, OwnerRole: "planner", CreatedAt: now, Payload: planPayload}
	revision := domain.PracticeRevision{ID: "practice-01", WorkflowID: "workflow-01", Context: context, PlanID: plan.ID, PlanRevision: 1, WorkflowRevision: 1, CandidateID: "candidate-01", PublishedAt: now}
	artifacts := []domain.ArtifactRecord{contextArtifact, planArtifact}

	consistent := domain.PublicationManifest{Context: context}
	if err := validatePublicationLedger(artifacts, revision, consistent); err == nil || err.Error() == "practice publication manifest context does not match the practice revision" {
		t.Fatalf("ledger beyond the context binding should fail on later bindings: %v", err)
	}
	otherParser := context
	otherParser.ParserVersion = "docs-project-v11"
	forked := domain.PublicationManifest{Context: otherParser}
	err = validatePublicationLedger(artifacts, revision, forked)
	if err == nil || err.Error() != "practice publication manifest context does not match the practice revision" {
		t.Fatalf("manifest with a different parser identity must be rejected: %v", err)
	}
	otherPage := context
	otherPage.PageDigest = testRunnableDigest("other-page")
	err = validatePublicationLedger(artifacts, revision, domain.PublicationManifest{Context: otherPage})
	if err == nil || err.Error() != "practice publication manifest context does not match the practice revision" {
		t.Fatalf("manifest with a different page digest must be rejected: %v", err)
	}
}

func TestValidatePublicationLedgerRejectsTitlelessPlanAndDetachedReaderProjection(t *testing.T) {
	now := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	context := domain.DocumentContext{FormatVersion: domain.FormatVersion, SourceID: "kubernetes", Repository: "https://github.com/kubernetes/website", Commit: strings.Repeat("a", 40), Version: "v1.34", Language: "en", License: "CC BY 4.0", PagePath: "docs/pods.md", Anchor: "pod-lifecycle", ParserVersion: "docs-project-v10", PageDigest: testRunnableDigest("e")}
	buildLedger := func(t *testing.T, plan domain.LearningUnitPlan) []domain.ArtifactRecord {
		t.Helper()
		planPayload, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		contextPayload, err := json.Marshal(context)
		if err != nil {
			t.Fatal(err)
		}
		contextArtifact := domain.ArtifactRecord{ID: "document-context-" + domain.ContentID(context), Kind: "document-context", ContentRevision: context.Commit, Digest: testRunnableDigest("c"), SchemaVersion: domain.FormatVersion, OwnerRole: "server", CreatedAt: now, Payload: contextPayload}
		planArtifact := domain.ArtifactRecord{ID: fmt.Sprintf("plan-%s-r%d-a1", plan.ID, plan.Revision), ParentID: contextArtifact.ID, Kind: "learning-unit-plan", ContentRevision: "1", Digest: testRunnableDigest("plan"), SchemaVersion: domain.FormatVersion, OwnerRole: "planner", CreatedAt: now, Payload: planPayload}
		return []domain.ArtifactRecord{contextArtifact, planArtifact}
	}
	buildRevision := func(plan domain.LearningUnitPlan, projection *domain.ReaderProjection) domain.PracticeRevision {
		return domain.PracticeRevision{ID: "practice-01", WorkflowID: "workflow-01", Context: context, PlanID: plan.ID, PlanRevision: plan.Revision, WorkflowRevision: 1, CandidateID: "candidate-01", ReaderProjection: projection, PublishedAt: now}
	}
	manifest := domain.PublicationManifest{Context: context}

	// A plan artifact without a title never publishes, no matter what the
	// revision's projection claims to carry.
	titleless := domain.LearningUnitPlan{FormatVersion: domain.FormatVersion, ID: "plan-01", Revision: 1, Context: context, Objective: "Observe Pod state", Boundary: "One Pod", Runtime: domain.RuntimeConstraint{Runtime: "k8s", BaseImage: "kindest/node", Resources: runnable.ResourceLimits{CPU: "1", MemoryBytes: 256 << 20, EphemeralBytes: 512 << 20, MaxProcesses: 32, MaxConcurrentTasks: 1}, Network: "isolated", Topology: "single-cluster"}, Evidence: []domain.EvidenceReference{{ID: "page", Kind: domain.EvidencePage, Path: context.PagePath, Digest: testRunnableDigest("d")}}, Observations: []domain.ObservationPoint{{ID: "ready", Description: "The Pod becomes ready", EvidenceIDs: []string{"page"}}}, CreatedAt: now}
	err := validatePublicationLedger(buildLedger(t, titleless), buildRevision(titleless, domain.ReaderProjectionFromPlan(titleless)), manifest)
	if err == nil || err.Error() != "practice publication learning plan binding is invalid" {
		t.Fatalf("titleless plan must be rejected at publish: %v", err)
	}

	plan := titleless
	plan.Title = "Pod lifecycle"
	ledger := buildLedger(t, plan)
	err = validatePublicationLedger(ledger, buildRevision(plan, nil), manifest)
	if err == nil || err.Error() != "practice publication reader projection does not bind the learning plan" {
		t.Fatalf("missing reader projection must be rejected at publish: %v", err)
	}
	detached := domain.ReaderProjectionFromPlan(plan)
	detached.Title = "Another title"
	err = validatePublicationLedger(ledger, buildRevision(plan, detached), manifest)
	if err == nil || err.Error() != "practice publication reader projection does not bind the learning plan" {
		t.Fatalf("divergent reader projection must be rejected at publish: %v", err)
	}
}

func TestWatchdogRepositoryListsCandidatesAndMapsStrandedWorkflows(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	workflow := seedWorkflowInState(t, database, "document-workflow-watchdog", domain.MaterializingArtifact, now)
	identity := runnable.ActionIdentity{
		Content:      runnable.ContentIdentity{Kind: "practice", ID: "page-1", Revision: "1"},
		SpecDigest:   testRunnableDigest("a"),
		Phase:        runnable.ActionMaterializeArtifact,
		StateVersion: workflow.StateVersion,
	}
	if err := database.DocumentPractice.BindRunnableAction(ctx, workflow.ID, identity, now); err != nil {
		t.Fatalf("bind runnable action: %v", err)
	}
	insertWatchdogRunnableAction(t, database, identity, "queued", 5, now.Add(-runnableWatchdogTestBudget))

	// The exhausted action is inside its grace period: no candidate is mapped yet.
	candidates, err := database.DocumentPractice.ListWorkflowWatchdogCandidates(ctx, now)
	if err != nil {
		t.Fatalf("list watchdog candidates: %v", err)
	}
	if len(candidates) != 1 || candidates[0].WorkflowID != workflow.ID || candidates[0].Action.Attempt != 5 || candidates[0].Action.State != "queued" {
		t.Fatalf("grace candidates = %#v", candidates)
	}
	// The grace policy lives in the application layer; the store only fences.
	// A mapping whose expected state version is stale must be rejected.
	if _, err := database.DocumentPractice.WatchdogFailWorkflow(ctx, workflow.ID, "attempts_exhausted", workflow.StateVersion+3, now); err == nil {
		t.Fatal("watchdog accepted a stale state-version fence")
	}
	workflowAfterRejection, err := database.DocumentPractice.GetWorkflow(ctx, workflow.ID)
	if err != nil || workflowAfterRejection.State != domain.MaterializingArtifact {
		t.Fatalf("rejected mapping changed state: %s, %v", workflowAfterRejection.State, err)
	}

	if _, err := database.conn.ExecContext(ctx, `UPDATE runnable_actions SET updated_at = ? WHERE action_key = ?`, now.Add(-runnableWatchdogTestBudget-time.Second), identity.Key()); err != nil {
		t.Fatal(err)
	}
	mapped, err := database.DocumentPractice.WatchdogFailWorkflow(ctx, workflow.ID, "attempts_exhausted", workflow.StateVersion, now)
	if err != nil || mapped.State != domain.Failed || mapped.StateVersion != workflow.StateVersion+1 {
		t.Fatalf("watchdog mapping = %#v, %v", mapped, err)
	}
	var ownerRole, payload string
	if err := database.conn.QueryRowContext(ctx, `SELECT owner_role, payload FROM document_artifact_ledger WHERE kind = 'watchdog.force_fail' AND workflow_id = ?`, workflow.ID).Scan(&ownerRole, &payload); err != nil {
		t.Fatalf("watchdog ledger entry: %v", err)
	}
	if ownerRole != "system" || !strings.Contains(payload, "attempts_exhausted") {
		t.Fatalf("watchdog ledger = %s %s", ownerRole, payload)
	}
	humanRows, err := database.Audit.ListHumanActions(ctx, HumanActionFilter{Limit: 50})
	if err != nil {
		t.Fatalf("list human actions: %v", err)
	}
	for _, row := range humanRows {
		if row.TargetID == workflow.ID {
			t.Fatalf("watchdog wrote a human action row: %#v", row)
		}
	}

	// A terminal workflow is not a candidate even with a failed bound action.
	if _, err := database.conn.ExecContext(ctx, `UPDATE runnable_actions SET state = 'failed', failure_class = 'artifact', failure_code = 'x', failure_summary = 'y' WHERE action_key = ?`, identity.Key()); err != nil {
		t.Fatal(err)
	}
	candidates, err = database.DocumentPractice.ListWorkflowWatchdogCandidates(ctx, now)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("terminal workflow candidates = %#v, %v", candidates, err)
	}
}

// runnableWatchdogTestBudget mirrors the application watchdog grace so the SQL
// test exercises the same magnitude without importing the application package.
const runnableWatchdogTestBudget = 2100 * time.Second

func insertWatchdogRunnableAction(t *testing.T, database *Store, identity runnable.ActionIdentity, state string, attempt int, updatedAt time.Time) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := database.conn.ExecContext(context.Background(), `INSERT INTO runnable_sources (source_digest, archive, created_at) VALUES (?, ?, ?)`, testRunnableDigest("source"), []byte("archive"), now.UTC()); err != nil {
		t.Fatalf("insert runnable source: %v", err)
	}
	if _, err := database.conn.ExecContext(context.Background(), `INSERT INTO runnable_specs (spec_digest, content_kind, content_id, content_revision, source_digest, spec, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		identity.SpecDigest, identity.Content.Kind, identity.Content.ID, identity.Content.Revision, testRunnableDigest("source"), []byte(`{}`), now.UTC()); err != nil {
		t.Fatalf("insert runnable spec: %v", err)
	}
	if _, err := database.conn.ExecContext(context.Background(), `INSERT INTO runnable_actions (action_key, content_kind, content_id, content_revision, spec_digest, phase, state_version, state, attempt, next_run_at, lease_owner, lease_expires_at, failure_class, failure_code, failure_summary, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, 'infrastructure', 'env-lost', 'environment vanished', ?, ?)`,
		identity.Key(), identity.Content.Kind, identity.Content.ID, identity.Content.Revision, identity.SpecDigest, identity.Phase, identity.StateVersion, state, attempt, now.UTC(), now.UTC(), updatedAt.UTC()); err != nil {
		t.Fatalf("insert runnable action: %v", err)
	}
}

func TestWorkflowListPaginationAndFilters(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	paths := []string{"docs/a", "docs/b", "docs/c"}
	for index, path := range paths {
		workflow, err := domain.NewWorkflow(fmt.Sprintf("document-workflow-page-%d", index), now.Add(time.Duration(index)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		identity := domain.WorkflowPageIdentity{SourceID: "kubernetes", Commit: strings.Repeat("a", 40), Language: "en", PagePath: path, Anchor: "intro"}
		if err := database.DocumentPractice.CreateWorkflow(ctx, workflow, identity, nil); err != nil {
			t.Fatal(err)
		}
		updated, err := database.DocumentPractice.AdvanceWorkflow(ctx, workflow.ID, workflow.StateVersion, domain.PlanReviewing, now.Add(time.Duration(index)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		_ = updated
	}

	list, next, err := database.DocumentPractice.ListWorkflowObservations(ctx, DocumentWorkflowListFilter{Limit: 2})
	if err != nil || len(list) != 2 || next == nil {
		t.Fatalf("first page = %#v next=%#v err=%v", list, next, err)
	}
	if list[0].Workflow.ID != "document-workflow-page-2" || list[1].Workflow.ID != "document-workflow-page-1" {
		t.Fatalf("first page order = %s, %s", list[0].Workflow.ID, list[1].Workflow.ID)
	}
	second, secondNext, err := database.DocumentPractice.ListWorkflowObservations(ctx, DocumentWorkflowListFilter{Limit: 2, Cursor: next})
	if err != nil || len(second) != 1 || secondNext != nil {
		t.Fatalf("second page = %#v next=%#v err=%v", second, secondNext, err)
	}
	if second[0].Workflow.ID != "document-workflow-page-0" {
		t.Fatalf("second page = %s", second[0].Workflow.ID)
	}
	if second[0].Identity.PagePath != "docs/a" || second[0].Identity.Anchor != "intro" {
		t.Fatalf("page identity = %#v", second[0].Identity)
	}

	filtered, _, err := database.DocumentPractice.ListWorkflowObservations(ctx, DocumentWorkflowListFilter{Limit: 10, PagePath: "docs/b"})
	if err != nil || len(filtered) != 1 || filtered[0].Workflow.ID != "document-workflow-page-1" {
		t.Fatalf("page_path filter = %#v, %v", filtered, err)
	}
	filtered, _, err = database.DocumentPractice.ListWorkflowObservations(ctx, DocumentWorkflowListFilter{Limit: 10, State: string(domain.PlanReviewing)})
	if err != nil || len(filtered) != 3 {
		t.Fatalf("state filter = %#v, %v", filtered, err)
	}
	filtered, _, err = database.DocumentPractice.ListWorkflowObservations(ctx, DocumentWorkflowListFilter{Limit: 10, State: string(domain.Published)})
	if err != nil || len(filtered) != 0 {
		t.Fatalf("published filter = %#v, %v", filtered, err)
	}
}

func TestBackfillWorkflowPageIdentityDerivesFromLedgerContext(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 19, 11, 0, 0, 0, time.UTC)
	workflow, err := domain.NewWorkflow("document-workflow-legacy", now)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-identity row: insert directly without the identity columns.
	if _, err := database.conn.ExecContext(ctx, `INSERT INTO document_workflows (id, state, state_version, revision, max_revisions, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, workflow.ID, workflow.State, workflow.StateVersion, workflow.Revision, workflow.MaxRevisions, workflow.UpdatedAt.UTC()); err != nil {
		t.Fatal(err)
	}
	context := domain.DocumentContext{FormatVersion: domain.FormatVersion, SourceID: "kubernetes", Repository: "https://github.com/kubernetes/website", Commit: strings.Repeat("b", 40), Version: "v1.34", Language: "en", License: "CC BY 4.0", PagePath: "docs/legacy", Anchor: "setup"}
	encoded, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	artifact := domain.ArtifactRecord{ID: "document-context-" + domain.ContentID(context), Kind: "document-context", ContentRevision: context.Commit, Digest: testRunnableDigest("c"), SchemaVersion: domain.FormatVersion, OwnerRole: "server", CreatedAt: now, Payload: encoded}
	if err := database.DocumentPractice.AppendArtifact(ctx, workflow.ID, artifact); err != nil {
		t.Fatal(err)
	}

	backfilled, err := database.DocumentPractice.BackfillWorkflowPageIdentity(ctx)
	if err != nil || backfilled != 1 {
		t.Fatalf("backfill = %d, %v", backfilled, err)
	}
	observation, err := database.DocumentPractice.GetWorkflowObservation(ctx, workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Identity != (domain.WorkflowPageIdentity{SourceID: context.SourceID, Commit: context.Commit, Language: context.Language, PagePath: context.PagePath, Anchor: context.Anchor}) {
		t.Fatalf("backfilled identity = %#v", observation.Identity)
	}

	// The backfill is idempotent: a second pass changes nothing.
	backfilled, err = database.DocumentPractice.BackfillWorkflowPageIdentity(ctx)
	if err != nil || backfilled != 0 {
		t.Fatalf("second backfill = %d, %v", backfilled, err)
	}
}

func TestBatchRepositoryPersistsBatchWithItemsAndAudit(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	batch := domain.DocumentBatch{
		ID:          domain.NewBatchID(now),
		State:       domain.BatchPending,
		Scope:       domain.BatchScope{Kind: domain.BatchScopePages, Pages: []string{"docs/a", "docs/b"}},
		Concurrency: 2,
		Resolution:  domain.BatchResolution{Resolved: 2, Excluded: []domain.BatchExcludedPage{{PagePath: "docs/c", Reason: domain.ExcludedNoAnchor}}},
		TotalItems:  2,
		CreatedBy:   "u-admin",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	items := []domain.BatchItem{
		{ID: domain.NewBatchItemID(batch.ID, 0), BatchID: batch.ID, Ordinal: 0, PagePath: "docs/a", Anchor: "a-h2", Title: "A", WorkflowID: "document-workflow-a", State: domain.ItemSkipped, Detail: "already-published", CreatedAt: now, UpdatedAt: now},
		{ID: domain.NewBatchItemID(batch.ID, 1), BatchID: batch.ID, Ordinal: 1, PagePath: "docs/b", Anchor: "b-h2", Title: "B", WorkflowID: "document-workflow-b", State: domain.ItemPending, CreatedAt: now, UpdatedAt: now},
	}
	action := audit.HumanAction{ID: "audit-batch-create", UserID: "u-admin", Action: audit.ActionDocumentationBatchCreate, TargetType: audit.TargetDocumentBatch, TargetID: batch.ID, Detail: []byte(`{}`), CreatedAt: now}
	if err := database.DocumentPractice.CreateBatch(ctx, batch, items, &action); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	got, counts, err := database.DocumentPractice.GetBatch(ctx, batch.ID)
	if err != nil {
		t.Fatalf("get batch: %v", err)
	}
	if got.Scope.Kind != domain.BatchScopePages || len(got.Scope.Pages) != 2 || got.TotalItems != 2 || len(got.Resolution.Excluded) != 1 {
		t.Fatalf("stored batch = %#v", got)
	}
	if counts[domain.ItemSkipped] != 1 || counts[domain.ItemPending] != 1 {
		t.Fatalf("counts = %#v", counts)
	}

	// Duplicate item anchors are refused by the unique constraint.
	duplicate := items[0]
	duplicate.ID = domain.NewBatchItemID(batch.ID, 9)
	if err := database.DocumentPractice.CreateBatch(ctx, batch, []domain.BatchItem{duplicate}, &action); err == nil {
		t.Fatal("duplicate batch anchor was accepted")
	}

	// Item pages keyset through corpus order, with a state filter.
	page1, next, err := database.DocumentPractice.ListBatchItems(ctx, domain.BatchItemFilter{BatchID: batch.ID, Limit: 1})
	if err != nil || len(page1) != 1 || next == nil || page1[0].Ordinal != 0 {
		t.Fatalf("item page 1 = %#v next=%#v err=%v", page1, next, err)
	}
	page2, next2, err := database.DocumentPractice.ListBatchItems(ctx, domain.BatchItemFilter{BatchID: batch.ID, Limit: 5, Cursor: next})
	if err != nil || len(page2) != 1 || next2 != nil || page2[0].Ordinal != 1 {
		t.Fatalf("item page 2 = %#v next=%#v err=%v", page2, next2, err)
	}
	skipped, _, err := database.DocumentPractice.ListBatchItems(ctx, domain.BatchItemFilter{BatchID: batch.ID, Limit: 5, State: domain.ItemSkipped})
	if err != nil || len(skipped) != 1 || skipped[0].Detail != "already-published" {
		t.Fatalf("skipped filter = %#v, %v", skipped, err)
	}

	list, listCounts, err := database.DocumentPractice.ListBatches(ctx, 10)
	if err != nil || len(list) != 1 || listCounts[0][domain.ItemPending] != 1 {
		t.Fatalf("batch list = %#v counts=%#v err=%v", list, listCounts, err)
	}

	// The creation audit was written inside the same transaction.
	rows, err := database.Audit.ListHumanActions(ctx, HumanActionFilter{Action: audit.ActionDocumentationBatchCreate, Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].TargetID != batch.ID {
		t.Fatalf("batch audit rows = %#v, %v", rows, err)
	}
}

func TestListPublishedWorkflowAnchorsScopesToThePinnedIdentity(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	identity := domain.DocumentContext{FormatVersion: domain.FormatVersion, SourceID: "kubernetes", Commit: strings.Repeat("a", 40), Language: "en"}
	workflow := seedWorkflowInState(t, database, "document-workflow-published-anchor", domain.Planning, now)
	if _, err := database.conn.ExecContext(ctx, `UPDATE document_workflows SET state = 'Published' WHERE id = ?`, workflow.ID); err != nil {
		t.Fatal(err)
	}
	published, err := database.DocumentPractice.ListPublishedWorkflowAnchors(ctx, identity)
	if err != nil {
		t.Fatal(err)
	}
	if !published["docs/concepts/workloads/pods/pod-lifecycle\x00pod-lifetime"] {
		t.Fatalf("published anchors = %#v", published)
	}
	otherIdentity := identity
	otherIdentity.SourceID = "other"
	published, err = database.DocumentPractice.ListPublishedWorkflowAnchors(ctx, otherIdentity)
	if err != nil || len(published) != 0 {
		t.Fatalf("foreign identity anchors = %#v, %v", published, err)
	}
}

func TestBatchSchedulerRepositoryFencesTransitionsAndCancels(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	batch := domain.DocumentBatch{
		ID: domain.NewBatchID(now), State: domain.BatchPending,
		Scope:       domain.BatchScope{Kind: domain.BatchScopePages, Pages: []string{"docs/a"}},
		Concurrency: 2, Resolution: domain.BatchResolution{Resolved: 1}, TotalItems: 1,
		CreatedBy: "u-admin", CreatedAt: now, UpdatedAt: now,
	}
	items := []domain.BatchItem{{ID: domain.NewBatchItemID(batch.ID, 0), BatchID: batch.ID, Ordinal: 0, PagePath: "docs/a", Anchor: "a-h2", Title: "A", WorkflowID: "document-workflow-a", State: domain.ItemPending, CreatedAt: now, UpdatedAt: now}}
	action := audit.HumanAction{ID: "audit-batch-create", UserID: "u-admin", Action: audit.ActionDocumentationBatchCreate, TargetType: audit.TargetDocumentBatch, TargetID: batch.ID, Detail: []byte(`{}`), CreatedAt: now}
	if err := database.DocumentPractice.CreateBatch(ctx, batch, items, &action); err != nil {
		t.Fatal(err)
	}

	// Fenced transitions: wrong source state loses the fence without an audit.
	won, err := database.DocumentPractice.TransitionBatchState(ctx, batch.ID, domain.BatchRunning, domain.BatchPaused, nil, now)
	if err != nil || won {
		t.Fatalf("stale transition won = %t, %v", won, err)
	}
	won, err = database.DocumentPractice.TransitionBatchState(ctx, batch.ID, domain.BatchPending, domain.BatchRunning, nil, now)
	if err != nil || !won {
		t.Fatalf("start transition = %t, %v", won, err)
	}
	action.ID = "audit-batch-pause"
	action.Action = audit.ActionDocumentationBatchPause
	won, err = database.DocumentPractice.TransitionBatchState(ctx, batch.ID, domain.BatchRunning, domain.BatchPaused, &action, now)
	if err != nil || !won {
		t.Fatalf("pause transition = %t, %v", won, err)
	}
	rows, err := database.Audit.ListHumanActions(ctx, HumanActionFilter{Action: audit.ActionDocumentationBatchPause, Limit: 5})
	if err != nil || len(rows) != 1 {
		t.Fatalf("pause audit rows = %#v, %v", rows, err)
	}

	// Cancel fences the batch and cancels not-yet-started items atomically.
	cancel := audit.HumanAction{ID: "audit-batch-cancel", UserID: "u-admin", Action: audit.ActionDocumentationBatchCancel, TargetType: audit.TargetDocumentBatch, TargetID: batch.ID, Detail: []byte(`{}`), CreatedAt: now}
	cancelled, err := database.DocumentPractice.CancelBatch(ctx, batch.ID, domain.BatchRunning, &cancel, now)
	if err != nil || cancelled != 0 {
		t.Fatalf("cancel from the wrong source state = %d, %v", cancelled, err)
	}
	cancelled, err = database.DocumentPractice.CancelBatch(ctx, batch.ID, domain.BatchPaused, &cancel, now)
	if err != nil || cancelled != 1 {
		t.Fatalf("cancelled = %d, %v", cancelled, err)
	}
	got, counts, err := database.DocumentPractice.GetBatch(ctx, batch.ID)
	if err != nil || got.State != domain.BatchCancelled || counts[domain.ItemCancelled] != 1 {
		t.Fatalf("cancelled batch = %#v counts=%#v err=%v", got, counts, err)
	}

	// Item fences still apply to a cancelled batch's items: only the current
	// state owner advances.
	if won, err := database.DocumentPractice.TransitionBatchItem(ctx, items[0].ID, domain.ItemRunning, domain.ItemScheduled, "", now); err != nil || won {
		t.Fatalf("stale item transition = %t, %v", won, err)
	}
	if won, err := database.DocumentPractice.TransitionBatchItem(ctx, items[0].ID, domain.ItemRunning, domain.ItemScheduled, "", now); err != nil || won {
		t.Fatalf("stale claimed-item transition = %t, %v", won, err)
	}

	// The scheduler listing filters by state.
	running, err := database.DocumentPractice.ListSchedulerBatches(ctx, []domain.BatchState{domain.BatchRunning})
	if err != nil || len(running) != 0 {
		t.Fatalf("running batches = %#v, %v", running, err)
	}
	active, err := database.DocumentPractice.ListSchedulerBatches(ctx, []domain.BatchState{domain.BatchPending, domain.BatchRunning, domain.BatchPaused, domain.BatchCancelled})
	if err != nil || len(active) != 1 {
		t.Fatalf("active batches = %#v, %v", active, err)
	}
}

func TestCorpusRollupNeverMarksAgedTerminalWorkflowsStuck(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	aged := base.Add(-2 * time.Hour)
	identity := testWorkflowIdentity()

	// Aged terminal workflow: the rollup must not count it as stuck.
	terminal, err := domain.NewWorkflow("document-workflow-terminal", aged)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DocumentPractice.CreateWorkflow(ctx, terminal, identity, nil); err != nil {
		t.Fatal(err)
	}
	artifact := domain.ArtifactRecord{ID: "context-terminal", Kind: "document-context", ContentRevision: "1", Digest: testRunnableDigest("t"), SchemaVersion: domain.FormatVersion, OwnerRole: "planner", CreatedAt: aged, Payload: []byte(`{"id":"context-terminal"}`)}
	appendAndAdvanceDocumentWorkflow(t, database.DocumentPractice, ctx, terminal.ID, artifact, domain.PlanReviewing, aged.Add(time.Second))
	gate := domain.ArtifactRecord{ID: "plan-gate-terminal", Kind: "plan-gate", ContentRevision: "1", Digest: testRunnableDigest("g"), SchemaVersion: domain.FormatVersion, OwnerRole: "planner", CreatedAt: aged.Add(time.Second), Payload: []byte(`{"id":"plan-gate-terminal"}`)}
	appendAndAdvanceDocumentWorkflow(t, database.DocumentPractice, ctx, terminal.ID, gate, domain.NoPractice, aged.Add(2*time.Second))

	// Aged non-terminal workflow: stuck through the agent dwell cutoff.
	stalled, err := domain.NewWorkflow("document-workflow-stalled", aged)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DocumentPractice.CreateWorkflow(ctx, stalled, identity, nil); err != nil {
		t.Fatal(err)
	}

	// Fresh non-terminal workflow: within every dwell budget.
	fresh, err := domain.NewWorkflow("document-workflow-fresh", base)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DocumentPractice.CreateWorkflow(ctx, fresh, identity, nil); err != nil {
		t.Fatal(err)
	}

	// Failed workflow: always the attention signal.
	failed, err := domain.NewWorkflow("document-workflow-failed", aged)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DocumentPractice.CreateWorkflow(ctx, failed, identity, nil); err != nil {
		t.Fatal(err)
	}
	failedArtifact := domain.ArtifactRecord{ID: "context-failed", Kind: "document-context", ContentRevision: "1", Digest: testRunnableDigest("f"), SchemaVersion: domain.FormatVersion, OwnerRole: "planner", CreatedAt: aged, Payload: []byte(`{"id":"context-failed"}`)}
	appendAndAdvanceDocumentWorkflow(t, database.DocumentPractice, ctx, failed.ID, failedArtifact, domain.Failed, aged.Add(time.Second))

	counts, err := database.DocumentPractice.SummarizeWorkflowStatesByPages(ctx,
		domain.DocumentContext{SourceID: identity.SourceID, Commit: identity.Commit, Language: identity.Language},
		[]string{identity.PagePath},
		base.Add(-30*time.Minute), base.Add(-35*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	got := counts[identity.PagePath]
	if got.Total != 4 || got.NoPractice != 1 || got.Failed != 1 || got.InProgress != 2 {
		t.Fatalf("rollup counts = %#v", got)
	}
	if got.Stuck != 2 {
		t.Fatalf("stuck = %d, want 2 (aged stalled + failed; the aged terminal row must not count)", got.Stuck)
	}
}
