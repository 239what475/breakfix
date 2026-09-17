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

func TestDocumentPracticeRepositoryAppendsAndAdvancesAnImmutableLedger(t *testing.T) {
	database := newTestDB(t)
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	workflow, err := domain.NewWorkflow("document-workflow-01", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DocumentPractice.CreateWorkflow(context.Background(), workflow, nil); err != nil {
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
	if err := database.DocumentPractice.CreateWorkflow(ctx, workflow, nil); err != nil {
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
	revision := domain.PracticeRevision{FormatVersion: domain.FormatVersion, ID: "practice-revision-01", WorkflowID: workflow.ID, Context: documentContext, PlanID: plan.ID, PlanRevision: plan.Revision, WorkflowRevision: 1, CandidateID: candidate.ID, RunnableRevisionRef: storedRevision.Reference, VerificationReportRef: storedReport.Reference, PublicationManifestID: manifest.ID, PublishedAt: now}
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
	if err := database.DocumentPractice.CreateWorkflow(ctx, workflow, &action); err != nil {
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
	if err := database.DocumentPractice.CreateWorkflow(ctx, workflow, &duplicate); err == nil {
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
	if err := database.DocumentPractice.CreateWorkflow(ctx, workflow, nil); err != nil {
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
	list, err := database.DocumentPractice.ListWorkflowObservations(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("observation list = %#v, %v", list, err)
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
