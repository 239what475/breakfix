package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	documentdomain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestRunnableRepositoryPersistsImmutableValuesAndReapLease(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	revision := testRunnableRevision(t)
	revisionDigest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	storedRevision := runnable.StoredRevision{
		Reference: runnable.RevisionReference{ID: "revision-01", Digest: revisionDigest}, Revision: revision, CreatedAt: now,
	}
	if err := database.Runnable.StoreRunnableRevision(ctx, storedRevision); err != nil {
		t.Fatalf("store runnable revision: %v", err)
	}
	if err := database.Runnable.StoreRunnableRevision(ctx, storedRevision); err != nil {
		t.Fatalf("repeat runnable revision: %v", err)
	}
	resolved, err := database.Runnable.ResolveRunnableRevision(ctx, storedRevision.Reference.ID, revisionDigest)
	if err != nil {
		t.Fatalf("resolve runnable revision: %v", err)
	}
	if !reflect.DeepEqual(resolved, revision) {
		t.Fatalf("resolved revision = %#v, want %#v", resolved, revision)
	}
	if err := database.Runnable.StoreRunnableSource(ctx, revision.Spec.Source, []byte("source archive"), now); err != nil {
		t.Fatalf("store runnable source: %v", err)
	}
	materializeIdentity, err := database.Runnable.ScheduleMaterialization(ctx, revision.Spec, 1, now)
	if err != nil {
		t.Fatalf("schedule materialization: %v", err)
	}
	if _, err := database.Runnable.ResolveMaterializedRunnableRevision(ctx, materializeIdentity); !errors.Is(err, runnable.ErrMaterializationNotReady) {
		t.Fatalf("resolve pending materialization = %v, want not ready", err)
	}
	materializeAction, err := database.Runnable.ClaimRunnableAction(ctx, "worker-a", time.Minute, now.Add(time.Second))
	if err != nil || materializeAction == nil || materializeAction.Credential.Identity != materializeIdentity || materializeAction.Attempt != 1 || materializeAction.Spec == nil {
		t.Fatalf("claim materialization = %#v, %v", materializeAction, err)
	}
	source, err := database.Runnable.ReadRunnableActionSource(ctx, materializeAction.Credential, now.Add(time.Second))
	if err != nil || string(source) != "source archive" {
		t.Fatalf("read runnable action source = %q, %v", source, err)
	}
	if err := database.Runnable.CompleteRunnableMaterialization(ctx, materializeAction.Credential, storedRevision, now.Add(2*time.Second)); err != nil {
		t.Fatalf("complete materialization: %v", err)
	}
	materializedReference, err := database.Runnable.ResolveMaterializedRunnableRevision(ctx, materializeIdentity)
	if err != nil || materializedReference != storedRevision.Reference {
		t.Fatalf("resolve materialized revision = %#v, %v", materializedReference, err)
	}
	if _, err := database.Runnable.ReadRunnableActionSource(ctx, materializeAction.Credential, now.Add(3*time.Second)); !errors.Is(err, runnable.ErrActionLeaseLost) {
		t.Fatalf("read completed action source = %v, want lease loss", err)
	}

	report := testVerificationReport(t, revision)
	reportDigest, err := report.Digest(revision)
	if err != nil {
		t.Fatal(err)
	}
	storedReport := runnable.StoredVerificationReport{
		Reference: runnable.VerificationReportReference{ID: "report-01", Digest: reportDigest}, Report: report, RunnableRevision: revision, CreatedAt: now,
	}
	if err := database.Runnable.StoreVerificationReport(ctx, storedReport); err != nil {
		t.Fatalf("store verification report: %v", err)
	}
	loadedReport, err := database.Runnable.ResolveVerificationReport(ctx, storedReport.Reference.ID, reportDigest, revision)
	if err != nil {
		t.Fatalf("resolve verification report: %v", err)
	}
	if !reflect.DeepEqual(loadedReport, report) {
		t.Fatalf("resolved report = %#v, want %#v", loadedReport, report)
	}
	verifyIdentity, err := database.Runnable.ScheduleVerification(ctx, storedRevision.Reference, 2, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("schedule verification: %v", err)
	}
	verifyAction, err := database.Runnable.ClaimRunnableAction(ctx, "worker-b", time.Minute, now.Add(4*time.Second))
	if err != nil || verifyAction == nil || verifyAction.Credential.Identity != verifyIdentity || verifyAction.Attempt != 1 || verifyAction.RunnableRevision == nil {
		t.Fatalf("claim verification = %#v, %v", verifyAction, err)
	}
	resolvedReference, resolvedAttempt, err := database.Runnable.ResolveRunnableVerificationLease(ctx, verifyAction.Credential, now.Add(4*time.Second))
	if err != nil || resolvedReference != storedRevision.Reference || resolvedAttempt != verifyAction.Attempt {
		t.Fatalf("resolved verification lease = %#v attempt=%d err=%v", resolvedReference, resolvedAttempt, err)
	}
	if err := database.Runnable.ValidateRunnableVerificationLease(ctx, verifyAction.Credential, storedRevision.Reference, verifyAction.Attempt, now.Add(4*time.Second)); err != nil {
		t.Fatalf("validate verification lease: %v", err)
	}
	if err := database.Runnable.ValidateRunnableVerificationLease(ctx, verifyAction.Credential, storedRevision.Reference, verifyAction.Attempt+1, now.Add(4*time.Second)); !errors.Is(err, runnable.ErrActionLeaseLost) {
		t.Fatalf("validate verification lease with another attempt = %v", err)
	}
	capture := runnable.OutputCapture{Stdout: []byte("verification stdout"), Stderr: []byte("verification stderr")}
	outputRef, err := database.Runnable.StoreRunnableExecutionOutput(ctx, verifyAction.Credential, capture, now.Add(4*time.Second))
	if err != nil {
		t.Fatalf("store runnable execution output: %v", err)
	}
	expectedOutputRef, _, err := capture.Reference()
	if err != nil || outputRef != expectedOutputRef {
		t.Fatalf("stored output reference = %#v, %v", outputRef, err)
	}
	resolvedCapture, err := database.Runnable.ResolveRunnableExecutionOutput(ctx, outputRef)
	if err != nil || !reflect.DeepEqual(resolvedCapture, capture) {
		t.Fatalf("resolved output capture = %#v, %v", resolvedCapture, err)
	}
	verificationReport := storedReport
	verificationReport.Reference.ID = "report-02"
	verificationReport.Report.Phases[0].Actions[0].Outputs = []runnable.ImmutableReference{outputRef}
	verificationReport.Report.Phases[1].Assertions[0].Outputs = []runnable.ImmutableReference{outputRef}
	verificationReport.Reference.Digest, err = verificationReport.Report.Digest(revision)
	if err != nil {
		t.Fatal(err)
	}
	wrongAttempt := verificationReport
	wrongAttempt.Report.Attempt++
	wrongAttempt.Reference.Digest, err = wrongAttempt.Report.Digest(revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Runnable.CompleteRunnableVerification(ctx, verifyAction.Credential, wrongAttempt, now.Add(5*time.Second)); err == nil || !strings.Contains(err.Error(), "attempt") {
		t.Fatalf("complete verification with wrong attempt = %v", err)
	}
	missingOutput := verificationReport
	missingOutput.Report.Phases = append([]runnable.PhaseResult(nil), verificationReport.Report.Phases...)
	missingOutput.Report.Phases[0].Actions = append([]runnable.ActionResult(nil), verificationReport.Report.Phases[0].Actions...)
	missingOutput.Report.Phases[0].Actions[0].Outputs = append([]runnable.ImmutableReference(nil), verificationReport.Report.Phases[0].Actions[0].Outputs...)
	missingOutput.Reference.ID = "report-03"
	missingOutput.Report.Phases[0].Actions[0].Outputs[0].Digest = testRunnableDigest("f")
	missingOutput.Report.Phases[0].Actions[0].Outputs[0].Reference = "runnable-output://sha256/" + strings.TrimPrefix(testRunnableDigest("f"), "sha256:")
	missingOutput.Reference.Digest, err = missingOutput.Report.Digest(revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Runnable.CompleteRunnableVerification(ctx, verifyAction.Credential, missingOutput, now.Add(5*time.Second)); err == nil || !strings.Contains(err.Error(), "was not persisted") {
		t.Fatalf("complete verification with missing output = %v", err)
	}
	if err := database.Runnable.CompleteRunnableVerification(ctx, verifyAction.Credential, verificationReport, now.Add(5*time.Second)); err != nil {
		t.Fatalf("complete verification: %v", err)
	}
	if _, err := database.Runnable.StoreRunnableExecutionOutput(ctx, verifyAction.Credential, capture, now.Add(6*time.Second)); !errors.Is(err, runnable.ErrActionLeaseLost) {
		t.Fatalf("store output after verification completion = %v, want lease loss", err)
	}
	if _, _, err := database.Runnable.ResolveRunnableVerificationLease(ctx, verifyAction.Credential, now.Add(6*time.Second)); !errors.Is(err, runnable.ErrActionLeaseLost) {
		t.Fatalf("resolve lease after verification completion = %v, want lease loss", err)
	}

	request := runnable.ReapRequest{
		Namespace: "breakfix-system", Name: "environment-01", UID: "environment-uid", Revision: revisionDigest,
		Binding: runnable.EnvironmentBinding{Namespace: "breakfix-system", Name: "environment-01", UID: "environment-uid", Purpose: runnable.PurposeVerification, RunnableRevision: revision},
	}
	if err := database.Runnable.Enqueue(ctx, request); err != nil {
		t.Fatalf("enqueue reap: %v", err)
	}
	claim, err := database.Runnable.Claim(ctx, "reaper-a", time.Minute, now.Add(time.Second))
	if err != nil || claim == nil {
		t.Fatalf("claim reap = %#v, %v", claim, err)
	}
	if claim.Record.Attempt != 1 || claim.Record.LeaseOwner != "reaper-a" {
		t.Fatalf("reap claim = %#v", claim)
	}
	if err := database.Runnable.Complete(ctx, *claim, true, "", now.Add(2*time.Second), now.Add(3*time.Second)); err != nil {
		t.Fatalf("complete reap: %v", err)
	}
	reap, err := database.Runnable.Get(ctx, request.Key())
	if err != nil || reap.State != runnable.ReapSucceeded || reap.CompletedAt == nil {
		t.Fatalf("completed reap = %#v, %v", reap, err)
	}
	if err := database.Runnable.Complete(ctx, *claim, true, "", now.Add(2*time.Second), now.Add(3*time.Second)); err != runnable.ErrReapLeaseLost {
		t.Fatalf("duplicate reap completion error = %v", err)
	}
}

func testRunnableRevision(t *testing.T) runnable.RunnableRevision {
	t.Helper()
	spec := runnable.RunnableSpec{
		FormatVersion: runnable.FormatVersion,
		Identity:      runnable.ContentIdentity{Kind: "operations", ID: "service-startup", Revision: "rev-01"},
		RuntimeProfile: runnable.RuntimeProfile{
			Runtime: runnable.RuntimeNode, ProfileRevision: "profile-01", BaseImage: "registry.example/base@sha256:" + strings.Repeat("a", 64),
			SoftwareVersions: map[string]string{"runtime": "v1"},
			Resources:        runnable.ResourceLimits{CPU: "2", MemoryBytes: 1 << 30, EphemeralBytes: 1 << 30, MaxProcesses: 64, MaxConcurrentTasks: 1},
			Network:          runnable.NetworkPrivate, Topology: "single-host",
			ExecutionBoundaries: []runnable.ExecutionBoundary{
				{ID: "host-write", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, Permission: runnable.PermissionReadWrite, Network: runnable.NetworkPrivate, MaxTimeout: 60},
				{ID: "host-read", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, Permission: runnable.PermissionReadOnly, Network: runnable.NetworkPrivate, MaxTimeout: 60},
			},
		},
		Source:          runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "archives/source.tar.gz", Digest: runnableArchiveDigest([]byte("source archive"))},
		Initialization:  []runnable.ActionSpec{{ID: "initialize", Entrypoint: "scripts/init.sh", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, BoundaryID: "host-write", TimeoutSeconds: 60, ExpectedExitCodes: []int{0}}},
		ValidationPlan:  runnable.ValidationPlan{FormatVersion: runnable.FormatVersion, Phases: []runnable.ValidationPhase{{ID: "observe", TimeoutSeconds: 60, Execution: runnable.PhaseSequential, Assertions: []runnable.AssertionSpec{{ID: "ready", Entrypoint: "scripts/assert.sh", Target: runnable.TargetLocation{Kind: "node", ID: "host"}, BoundaryID: "host-read", TimeoutSeconds: 60}}}}},
		LifecyclePolicy: runnable.LifecyclePolicy{CreateTimeoutSeconds: 60, ResetTimeoutSeconds: 60, StopTimeoutSeconds: 60, ReapTimeoutSeconds: 60, IdleTTLSeconds: 600, MaxLifetimeSeconds: 1800},
	}
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	artifactDigest := testRunnableDigest("c")
	return runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: spec, Artifact: runnable.ArtifactReference{FormatVersion: runnable.FormatVersion, Runtime: runnable.RuntimeNode, ProviderReference: "incus://breakfix/image@" + artifactDigest, ArtifactDigest: artifactDigest, BuiltFromSpecDigest: specDigest, BuilderVersion: "builder-01"}}
}

func testVerificationReport(t *testing.T, revision runnable.RunnableRevision) runnable.VerificationReport {
	t.Helper()
	revisionDigest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	profileDigest, err := revision.Spec.RuntimeProfile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return runnable.VerificationReport{
		FormatVersion: runnable.FormatVersion, RunnableRevisionDigest: revisionDigest,
		Environment: runnable.EnvironmentIdentity{ID: "environment-01", Provider: "incus", ProfileDigest: profileDigest}, Attempt: 1, Passed: true,
		CreatedAt: time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		Phases: []runnable.PhaseResult{
			{ID: "initialization", Actions: []runnable.ActionResult{{ID: "initialize", ExitCode: 0, Summary: "initialized"}}},
			{ID: "observe", Assertions: []runnable.AssertionResult{{ID: "ready", Satisfied: true, Summary: "ready"}}},
		},
	}
}

func testRunnableDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func TestRunnableQueueObservationCountsFlagsAndFilters(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	revision := testRunnableRevision(t)
	revisionDigest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Runnable.StoreRunnableRevision(ctx, runnable.StoredRevision{Reference: runnable.RevisionReference{ID: "revision-queue", Digest: revisionDigest}, Revision: revision, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := database.Runnable.StoreRunnableSource(ctx, revision.Spec.Source, []byte("source archive"), now); err != nil {
		t.Fatal(err)
	}

	// queued attempt 0, running attempt 4 (attempt-high), completed attempt 0,
	// and a failed action bound to an unreconciled document workflow
	// (failed-unreconciled).
	queued, err := database.Runnable.ScheduleMaterialization(ctx, revision.Spec, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	running := queued
	running.Phase = runnable.ActionVerify
	running.StateVersion = 2
	if _, err := database.conn.ExecContext(ctx, `INSERT INTO runnable_actions (action_key, content_kind, content_id, content_revision, spec_digest, phase, state_version, runnable_revision_digest, state, attempt, next_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'running', 4, ?, ?, ?)`,
		running.Key(), running.Content.Kind, running.Content.ID, running.Content.Revision, running.SpecDigest, running.Phase, running.StateVersion, revisionDigest, now.UTC(), now.UTC(), now.UTC()); err != nil {
		t.Fatalf("insert running action: %v", err)
	}
	completed, err := database.Runnable.ScheduleVerification(ctx, runnable.RevisionReference{ID: "revision-queue", Digest: revisionDigest}, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(ctx, `UPDATE runnable_actions SET state = 'completed', completed_at = ? WHERE action_key = ?`, now.UTC(), completed.Key()); err != nil {
		t.Fatal(err)
	}

	// A documentation workflow with a failed, unreconciled verification action.
	docWorkflow, err := documentdomain.NewWorkflow("document-workflow-queue", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.DocumentPractice.CreateWorkflow(ctx, docWorkflow, nil); err != nil {
		t.Fatal(err)
	}
	failed := runnable.ActionIdentity{
		Content:      revision.Spec.Identity,
		SpecDigest:   queued.SpecDigest,
		Phase:        runnable.ActionVerify,
		StateVersion: 7,
	}
	if err := database.DocumentPractice.BindRunnableAction(ctx, docWorkflow.ID, failed, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(ctx, `INSERT INTO runnable_actions (action_key, content_kind, content_id, content_revision, spec_digest, phase, state_version, runnable_revision_digest, state, attempt, next_run_at, failure_class, failure_code, failure_summary, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'failed', 2, ?, 'infrastructure', 'env-lost', 'environment vanished', ?, ?)`,
		failed.Key(), failed.Content.Kind, failed.Content.ID, failed.Content.Revision, failed.SpecDigest, failed.Phase, failed.StateVersion, revisionDigest, now.UTC(), now.UTC(), now.UTC()); err != nil {
		t.Fatalf("insert failed action: %v", err)
	}

	byState, err := database.Runnable.CountRunnableActionsByState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if byState["queued"] != 1 || byState["running"] != 1 || byState["completed"] != 1 || byState["failed"] != 1 {
		t.Fatalf("by state = %#v", byState)
	}
	byAttempt, err := database.Runnable.CountRunnableActionsByAttempt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if byAttempt["0"] != 2 || byAttempt["2"] != 1 || byAttempt["4"] != 1 {
		t.Fatalf("by attempt = %#v", byAttempt)
	}

	observations, err := database.Runnable.ListRunnableActionObservations(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 4 {
		t.Fatalf("observations = %d, want 4", len(observations))
	}
	flags := map[string]string{}
	for _, observation := range observations {
		switch observation.ActionKey {
		case running.Key():
			flags["attempt-high"] = "attempt-high"
		case failed.Key():
			if observation.DocumentWorkflowID == nil || *observation.DocumentWorkflowID != docWorkflow.ID || observation.ReconciledAt != nil {
				t.Fatalf("failed binding observation = %#v", observation)
			}
			flags["failed-unreconciled"] = "failed-unreconciled"
		}
	}
	if len(flags) != 2 {
		t.Fatalf("expected both flag conditions in the queue, got %v", flags)
	}
	filtered, err := database.Runnable.ListRunnableActionObservations(ctx, "failed", "verify")
	if err != nil || len(filtered) != 1 || filtered[0].ActionKey != failed.Key() {
		t.Fatalf("filtered observations = %#v, %v", filtered, err)
	}
}
