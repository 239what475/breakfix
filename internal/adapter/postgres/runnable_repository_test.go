package postgres

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

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

// A reap that keeps failing stores the caller's backoff schedule verbatim and
// never enters a terminal give-up state: every failed attempt returns to
// queued under the same lease fence, keeps its diagnostics observable, and
// stays claimable no matter how many attempts have burned.
func TestRunnableRepositoryRetriesFailedReapsWithoutGivingUp(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	revision := testRunnableRevision(t)
	revisionDigest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	request := runnable.ReapRequest{
		Namespace: "breakfix-system", Name: "environment-retry", UID: "environment-retry-uid", Revision: revisionDigest,
		Binding: runnable.EnvironmentBinding{Namespace: "breakfix-system", Name: "environment-retry", UID: "environment-retry-uid", Purpose: runnable.PurposeVerification, RunnableRevision: revision},
	}
	if err := database.Runnable.Enqueue(ctx, request); err != nil {
		t.Fatalf("enqueue reap: %v", err)
	}

	// The repository stores the retry schedule it is handed — the reaper owns
	// the exponential sequence — and a failed attempt stays queued. The clock
	// starts after the enqueue so the initial next_attempt_at is due.
	clock := time.Now().UTC().Truncate(time.Microsecond)
	for attempt := 1; attempt <= 8; attempt++ {
		claim, err := database.Runnable.Claim(ctx, "reaper-a", time.Minute, clock)
		if err != nil || claim == nil {
			t.Fatalf("claim attempt %d = %#v, %v", attempt, claim, err)
		}
		if claim.Record.Attempt != int64(attempt) {
			t.Fatalf("claim attempt = %d, want %d", claim.Record.Attempt, attempt)
		}
		next := clock.Add(time.Duration(attempt) * 5 * time.Second)
		if err := database.Runnable.Complete(ctx, *claim, false, "provider unavailable", clock, next); err != nil {
			t.Fatalf("complete failed attempt %d: %v", attempt, err)
		}
		clock = next
	}
	record, err := database.Runnable.Get(ctx, request.Key())
	if err != nil || record.State != runnable.ReapQueued || record.Attempt != 8 || record.LastError != "provider unavailable" || !record.NextAttemptAt.Equal(clock) {
		t.Fatalf("backoff record = %#v, %v", record, err)
	}

	// The failure fence still guards the transition: a replayed completion of
	// an already-returned claim loses its lease.
	stale, err := database.Runnable.Claim(ctx, "reaper-b", time.Minute, clock)
	if err != nil || stale == nil {
		t.Fatalf("claim stuck reap = %#v, %v", stale, err)
	}
	if err := database.Runnable.Complete(ctx, *stale, false, "provider unavailable", clock.Add(time.Second), clock.Add(2*time.Second)); err != nil {
		t.Fatalf("complete stuck reap: %v", err)
	}
	if err := database.Runnable.Complete(ctx, *stale, false, "replayed", clock.Add(2*time.Second), clock.Add(3*time.Second)); err != runnable.ErrReapLeaseLost {
		t.Fatalf("replayed completion error = %v, want lease lost", err)
	}

	// Well past any historical attempt bound the record is still queued and
	// still claimable: the queue never abandons the teardown goal.
	clock = clock.Add(2 * time.Second)
	late, err := database.Runnable.Claim(ctx, "reaper-c", time.Minute, clock)
	if err != nil || late == nil || late.Record.Attempt != 10 {
		t.Fatalf("late claim = %#v, %v, want the stuck record claimable", late, err)
	}
	observations, err := database.Runnable.ListRunnableReapObservations(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, observation := range observations {
		if observation.ReapKey == request.Key() && observation.State == runnable.ReapClaimed && observation.Attempt == 10 && observation.LastError == "provider unavailable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("stuck reap missing from observations: %#v", observations)
	}
}

// Infrastructure failures requeue on the platform backoff curve, and the
// attempt that reaches the ceiling reports the explicit exhaustion failure —
// the failure report, never the claim scan, ends the patience. The failed
// row is visible to the resolver as a terminal action failure.
func TestRunnableActionInfraFailureBacksOffThenFailsExplicitly(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Microsecond)
	revision := testRunnableRevision(t)
	if err := database.Runnable.StoreRunnableSource(ctx, revision.Spec.Source, []byte("source archive"), start); err != nil {
		t.Fatalf("store runnable source: %v", err)
	}
	identity, err := database.Runnable.ScheduleMaterialization(ctx, revision.Spec, 1, start)
	if err != nil {
		t.Fatalf("schedule materialization: %v", err)
	}

	// Attempts below the ceiling requeue with the shared delay curve and the
	// caller's diagnostics intact.
	clock := start
	for attempt := int64(1); attempt < runnableActionMaxAttempts; attempt++ {
		action, err := database.Runnable.ClaimRunnableAction(ctx, "worker-a", time.Minute, clock)
		if err != nil || action == nil || action.Credential.Identity != identity || action.Attempt != attempt {
			t.Fatalf("claim attempt %d = %#v, %v", attempt, action, err)
		}
		if err := database.Runnable.ReportRunnableActionFailure(ctx, action.Credential, runnable.FailureInfrastructure, "runnable-action-failed", "provider unavailable", clock); err != nil {
			t.Fatalf("report infra failure %d: %v", attempt, err)
		}
		want := clock.Add(runnable.RetryBackoff(runnableActionRetryBase, attempt))
		row := runnableActionRowState(t, database, identity.Key())
		if row.state != "queued" || row.attempt != attempt || !row.nextRunAt.Equal(want) ||
			row.failureClass != "infrastructure" || row.failureCode != "runnable-action-failed" || row.failureSummary != "provider unavailable" {
			t.Fatalf("requeued attempt %d = %#v, want queued at %s with diagnostics", attempt, row, want)
		}
		clock = want
	}

	// The ceiling-th attempt moves the row to failed with the explicit
	// exhaustion code; the caller's summary survives as the last error.
	action, err := database.Runnable.ClaimRunnableAction(ctx, "worker-a", time.Minute, clock)
	if err != nil || action == nil || action.Attempt != runnableActionMaxAttempts {
		t.Fatalf("exhausted claim = %#v, %v", action, err)
	}
	if err := database.Runnable.ReportRunnableActionFailure(ctx, action.Credential, runnable.FailureInfrastructure, "runnable-action-failed", "provider unavailable", clock); err != nil {
		t.Fatalf("report exhausted failure: %v", err)
	}
	row := runnableActionRowState(t, database, identity.Key())
	if row.state != "failed" || row.attempt != runnableActionMaxAttempts || row.failureClass != "infrastructure" ||
		row.failureCode != "attempts-exhausted" || row.failureSummary != "provider unavailable" {
		t.Fatalf("exhausted row = %#v, want explicit infrastructure failure", row)
	}

	// A failed row is never claimable again, and the resolver hands the
	// stored failure to the waiting coordinator instead of "not ready".
	if late, err := database.Runnable.ClaimRunnableAction(ctx, "worker-b", time.Minute, clock.Add(time.Hour)); err != nil || late != nil {
		t.Fatalf("failed claim = %#v, %v, want nothing claimable", late, err)
	}
	_, err = database.Runnable.ResolveMaterializedRunnableRevision(ctx, identity)
	var failure *runnable.ActionFailure
	if !errors.As(err, &failure) || !errors.Is(err, runnable.ErrRunnableActionFailed) ||
		failure.Class != runnable.FailureInfrastructure || failure.Code != "attempts-exhausted" || failure.Summary != "provider unavailable" {
		t.Fatalf("resolve failed materialization = %v, want the explicit action failure", err)
	}
}

// The claim scan has no attempt truncation: a queued row past any policy
// ceiling is still claimable, and only a failure report transfers it to
// failed. Rows can therefore never wedge as "queued but never claimed".
func TestRunnableActionClaimHasNoAttemptTruncation(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	revision := testRunnableRevision(t)
	specDigest, err := revision.Spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	identity := runnable.ActionIdentity{Content: revision.Spec.Identity, SpecDigest: specDigest, Phase: runnable.ActionMaterializeArtifact, StateVersion: 1}
	if _, err := database.Runnable.StoreRunnableSpec(ctx, revision.Spec, now); err != nil {
		t.Fatalf("store runnable spec: %v", err)
	}
	if _, err := database.conn.ExecContext(ctx, `INSERT INTO runnable_actions
		(action_key, content_kind, content_id, content_revision, spec_digest, phase, state_version, state, attempt, next_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?)`,
		identity.Key(), identity.Content.Kind, identity.Content.ID, identity.Content.Revision, identity.SpecDigest, identity.Phase, identity.StateVersion, runnableActionMaxAttempts+1, now.UTC(), now.UTC(), now.UTC()); err != nil {
		t.Fatalf("insert over-ceiling action: %v", err)
	}
	action, err := database.Runnable.ClaimRunnableAction(ctx, "worker-a", time.Minute, now)
	if err != nil || action == nil || action.Attempt != runnableActionMaxAttempts+2 {
		t.Fatalf("over-ceiling claim = %#v, %v, want the row still claimable", action, err)
	}
	// The over-ceiling report fails explicitly instead of requeueing.
	if err := database.Runnable.ReportRunnableActionFailure(ctx, action.Credential, runnable.FailureInfrastructure, "runnable-action-failed", "provider unavailable", now); err != nil {
		t.Fatalf("report over-ceiling failure: %v", err)
	}
	row := runnableActionRowState(t, database, identity.Key())
	if row.state != "failed" || row.failureCode != "attempts-exhausted" {
		t.Fatalf("over-ceiling row = %#v, want explicit exhaustion", row)
	}
}

// Rescheduling a deterministic action key resets only infrastructure
// failures: identical content with an artifact failure fails identically, so
// the cached failure must survive reinstall scheduling.
func TestRunnableActionRescheduleResetsInfraFailuresOnly(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	revision := testRunnableRevision(t)
	revisionDigest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Runnable.StoreRunnableRevision(ctx, runnable.StoredRevision{Reference: runnable.RevisionReference{ID: "revision-reschedule", Digest: revisionDigest}, Revision: revision, CreatedAt: now}); err != nil {
		t.Fatalf("store runnable revision: %v", err)
	}
	if err := database.Runnable.StoreRunnableSource(ctx, revision.Spec.Source, []byte("source archive"), now); err != nil {
		t.Fatalf("store runnable source: %v", err)
	}
	materialize, err := database.Runnable.ScheduleMaterialization(ctx, revision.Spec, 4, now)
	if err != nil {
		t.Fatalf("schedule materialization: %v", err)
	}
	verify, err := database.Runnable.ScheduleVerification(ctx, runnable.RevisionReference{ID: "revision-reschedule", Digest: revisionDigest}, 5, now)
	if err != nil {
		t.Fatalf("schedule verification: %v", err)
	}

	// Claim order follows the queue (materialization first). The verification
	// action fails with the content's fault through the real reporting path;
	// the materialization row is retired as an exhausted infrastructure
	// failure (the exhaustion path itself is covered above).
	claimed, err := database.Runnable.ClaimRunnableAction(ctx, "worker-a", time.Minute, now)
	if err != nil || claimed == nil || claimed.Credential.Identity != materialize {
		t.Fatalf("claim materialization = %#v, %v", claimed, err)
	}
	verifyAction, err := database.Runnable.ClaimRunnableAction(ctx, "worker-a", time.Minute, now)
	if err != nil || verifyAction == nil || verifyAction.Credential.Identity != verify {
		t.Fatalf("claim verification = %#v, %v", verifyAction, err)
	}
	if err := database.Runnable.ReportRunnableActionFailure(ctx, verifyAction.Credential, runnable.FailureArtifact, "assertion-unsatisfied", "terminal failure", now); err != nil {
		t.Fatalf("report verification failure: %v", err)
	}
	if _, err := database.conn.ExecContext(ctx, `UPDATE runnable_actions SET state = 'failed', attempt = ?, failure_class = 'infrastructure', failure_code = 'attempts-exhausted', failure_summary = 'provider unavailable' WHERE action_key = ?`, runnableActionMaxAttempts, materialize.Key()); err != nil {
		t.Fatalf("retire materialization as exhausted: %v", err)
	}

	// A verification action failed with the content's fault keeps its cached
	// failure: the reschedule is a no-op, the row neither resets nor requeues.
	if _, err := database.Runnable.ScheduleVerification(ctx, runnable.RevisionReference{ID: "revision-reschedule", Digest: revisionDigest}, 5, now.Add(time.Minute)); err != nil {
		t.Fatalf("reschedule artifact-failed verification: %v", err)
	}
	row := runnableActionRowState(t, database, verify.Key())
	if row.state != "failed" || row.attempt != 1 || row.failureClass != "artifact" || row.failureCode != "assertion-unsatisfied" {
		t.Fatalf("artifact-failed reschedule = %#v, want the cached failure untouched", row)
	}

	// An infrastructure failure is the world's fault: rescheduling starts a
	// fresh attempt cycle so a fixed provider can heal the entry.
	if _, err := database.Runnable.ScheduleMaterialization(ctx, revision.Spec, 4, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("reschedule infra-failed materialization: %v", err)
	}
	row = runnableActionRowState(t, database, materialize.Key())
	if row.state != "queued" || row.attempt != 0 || row.failureClass != "" || row.failureCode != "" || row.failureSummary != "" {
		t.Fatalf("infra-failed reschedule = %#v, want a fresh queued cycle", row)
	}
	if !row.nextRunAt.Equal(now.Add(2 * time.Minute)) {
		t.Fatalf("infra-failed reschedule next run = %s, want the new schedule time", row.nextRunAt)
	}
	healed, err := database.Runnable.ClaimRunnableAction(ctx, "worker-b", time.Minute, now.Add(2*time.Minute))
	if err != nil || healed == nil || healed.Credential.Identity != materialize || healed.Attempt != 1 {
		t.Fatalf("healed claim = %#v, %v, want a fresh first attempt", healed, err)
	}
}

type runnableActionState struct {
	state          string
	attempt        int64
	nextRunAt      time.Time
	failureClass   string
	failureCode    string
	failureSummary string
}

func runnableActionRowState(t *testing.T, database *Store, key string) runnableActionState {
	t.Helper()
	var value runnableActionState
	if err := database.conn.QueryRowContext(context.Background(),
		`SELECT state, attempt, next_run_at, failure_class, failure_code, failure_summary FROM runnable_actions WHERE action_key = ?`, key).
		Scan(&value.state, &value.attempt, &value.nextRunAt, &value.failureClass, &value.failureCode, &value.failureSummary); err != nil {
		t.Fatalf("read runnable action row %q: %v", key, err)
	}
	return value
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

	failed := runnable.ActionIdentity{
		Content:      revision.Spec.Identity,
		SpecDigest:   queued.SpecDigest,
		Phase:        runnable.ActionVerify,
		StateVersion: 7,
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
	attemptHigh := 0
	for _, observation := range observations {
		if observation.Attempt >= 4 {
			attemptHigh++
		}
	}
	// Only the hand-inserted running action sits at the flag boundary: the
	// scheduled actions start at attempt 0 and the failed one at 2.
	if attemptHigh != 1 {
		t.Fatalf("attempt-high observations = %d, want 1", attemptHigh)
	}
	filtered, err := database.Runnable.ListRunnableActionObservations(ctx, "failed", "verify")
	if err != nil || len(filtered) != 1 || filtered[0].ActionKey != failed.Key() {
		t.Fatalf("filtered observations = %#v, %v", filtered, err)
	}
}

func TestStoreRunnableRevisionIsIdempotentByDigest(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	revision := testRunnableRevision(t)
	revisionDigest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Runnable.StoreRunnableRevision(ctx, runnable.StoredRevision{
		Reference: runnable.RevisionReference{ID: "revision-original", Digest: revisionDigest}, Revision: revision, CreatedAt: now,
	}); err != nil {
		t.Fatalf("store original revision: %v", err)
	}
	// A restarted workflow re-materializes the same bytes under a fresh
	// storage id; the digest is the durable identity and must win.
	if err := database.Runnable.StoreRunnableRevision(ctx, runnable.StoredRevision{
		Reference: runnable.RevisionReference{ID: "revision-rematerialized", Digest: revisionDigest}, Revision: revision, CreatedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("store re-materialized revision: %v", err)
	}
	resolved, err := database.Runnable.ResolveRunnableRevision(ctx, "revision-original", revisionDigest)
	if err != nil {
		t.Fatalf("resolve original revision: %v", err)
	}
	if !reflect.DeepEqual(resolved, revision) {
		t.Fatalf("resolved revision = %#v", resolved)
	}
	// The same id bound to another digest remains an integrity error.
	mutated := testRunnableRevision(t)
	mutated.Artifact.ArtifactDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	mutated.Artifact.ProviderReference = "incus://breakfix/image@" + mutated.Artifact.ArtifactDigest
	otherDigest, err := mutated.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if otherDigest == revisionDigest {
		t.Skip("fixture revisions are not unique")
	}
	if err := database.Runnable.StoreRunnableRevision(ctx, runnable.StoredRevision{
		Reference: runnable.RevisionReference{ID: "revision-original", Digest: otherDigest}, Revision: mutated, CreatedAt: now.Add(2 * time.Second),
	}); err == nil {
		t.Fatalf("rebinding an id to another digest was accepted")
	}
}
