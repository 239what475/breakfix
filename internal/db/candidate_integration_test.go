package db

import (
	"context"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/generator"
	"github.com/breakfix/breakfix/internal/worklist"
)

func TestCandidateStagesAdvanceAtomically(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	revision := seedCandidateForStages(t, database, candidate.StateBuilding, now)
	enqueueCandidateStage(t, database, revision.ID, worklist.KindBuild, now)

	buildClaim := claimCandidateStage(t, database, worklist.KindBuild, now)
	build := candidate.BuildOutput{
		Runtime: challenge.RuntimeNode,
		Incus: &candidate.IncusBuildReference{
			Project: "breakfix-build", WorkItemID: buildClaim.Work.Item.ID,
			Attempt: int64(buildClaim.Work.Item.Attempt), InstanceName: "build-instance",
			Alias: "build-alias", Fingerprint: fullHex('b'),
		},
	}
	if err := database.CompleteCandidateBuild(ctx, buildClaim.Work, build, now.Add(time.Second)); err != nil {
		t.Fatalf("complete build: %v", err)
	}
	assertCandidateState(t, database, revision.ID, candidate.StatePublishingArtifact)
	assertWorkState(t, database, buildClaim.Work.Item.ID, worklist.StateSucceeded)

	publishClaim := claimCandidateStage(t, database, worklist.KindArtifactPublish, now.Add(2*time.Second))
	artifact := candidate.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: "candidate-artifact", IncusFingerprint: fullHex('c')}
	if err := database.CompleteCandidateArtifactPublish(ctx, publishClaim.Work, artifact, now.Add(3*time.Second)); err != nil {
		t.Fatalf("complete artifact publication: %v", err)
	}
	assertCandidateState(t, database, revision.ID, candidate.StateVerifying)

	verifyClaim := claimCandidateStage(t, database, worklist.KindVerify, now.Add(4*time.Second))
	report := passedNodeReport()
	if err := database.CompleteCandidateVerification(ctx, verifyClaim.Work, report, now.Add(5*time.Second)); err != nil {
		t.Fatalf("complete verification: %v", err)
	}
	stored := assertCandidateState(t, database, revision.ID, candidate.StateVerified)
	if stored.Verification == nil || !stored.Verification.Passed || stored.VerifiedAt == nil {
		t.Fatalf("verified candidate = %#v", stored)
	}
}

func TestInvalidBuildResultDoesNotPartiallyAdvanceCandidate(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	revision := seedCandidateForStages(t, database, candidate.StateBuilding, now)
	enqueueCandidateStage(t, database, revision.ID, worklist.KindBuild, now)
	claim := claimCandidateStage(t, database, worklist.KindBuild, now)

	err := database.CompleteCandidateBuild(ctx, claim.Work, candidate.BuildOutput{Runtime: challenge.RuntimeNode}, now.Add(time.Second))
	if err == nil {
		t.Fatal("invalid build output unexpectedly advanced candidate")
	}
	assertCandidateState(t, database, revision.ID, candidate.StateBuilding)
	assertWorkState(t, database, claim.Work.Item.ID, worklist.StateRunning)
	if _, err := database.GetWorkItemForSubject(ctx, worklist.KindArtifactPublish, worklist.SubjectCandidateRevision, revision.ID); err != worklist.ErrNotFound {
		t.Fatalf("artifact publication item after failed transaction = %v", err)
	}
}

func TestCandidateDeadlineRecoveryEnqueuesCleanupWithoutAnotherWorkerClaim(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	revision := seedCandidateForStages(t, database, candidate.StateBuilding, now)
	if _, err := database.EnqueueWorkItem(ctx, worklist.CreateItem{
		ID: "work-expiring-build", Kind: worklist.KindBuild, SubjectType: worklist.SubjectCandidateRevision,
		SubjectID: revision.ID, NextRunAt: now, ExecutionTimeout: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	claimAt := now.Add(2 * time.Hour)
	claim, err := database.ClaimCandidateWork(ctx, worklist.KindBuild, "builder", time.Minute, claimAt)
	if err != nil || claim == nil {
		t.Fatalf("claim build = %#v, %v", claim, err)
	}
	if claim.Work.Item.DeadlineAt == nil || !claim.Work.Item.DeadlineAt.Equal(claimAt.Add(time.Second)) {
		t.Fatalf("claimed build deadline = %#v", claim.Work.Item.DeadlineAt)
	}
	if err := database.ExpireCandidateWork(ctx, worklist.KindBuild, claimAt.Add(2*time.Second)); err != nil {
		t.Fatalf("recover expired build = %v", err)
	}
	stored := assertCandidateState(t, database, revision.ID, candidate.StateInfrastructureFailed)
	if stored.Failure == nil || stored.Failure.Code != "DEADLINE_EXCEEDED" {
		t.Fatalf("expired candidate failure = %#v", stored.Failure)
	}
	assertWorkState(t, database, "work-expiring-build", worklist.StateFailed)
	cleanup, err := database.GetWorkItemForSubject(ctx, worklist.KindArtifactCleanup, worklist.SubjectCandidateRevision, revision.ID)
	if err != nil || cleanup.State != worklist.StatePending {
		t.Fatalf("expired cleanup = %#v, %v", cleanup, err)
	}
}

func TestFinalizeGeneratorCandidateCommitsRunCandidateAndBuildTogether(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	claim, revision := seedClaimedGenerator(t, database, now)

	if err := database.FinalizeGeneratorCandidate(ctx, claim, revision, now.Add(time.Second)); err != nil {
		t.Fatalf("finalize generator candidate: %v", err)
	}
	assertCandidateState(t, database, revision.ID, candidate.StateBuilding)
	run, err := database.GetRun(ctx, claim.Run.ID)
	if err != nil || run.Status != agentruntime.RunSucceeded {
		t.Fatalf("generator run = %#v, %v", run, err)
	}
	assertWorkState(t, database, claim.WorkItemID, worklist.StateSucceeded)
	build, err := database.GetWorkItemForSubject(ctx, worklist.KindBuild, worklist.SubjectCandidateRevision, revision.ID)
	if err != nil || build.State != worklist.StatePending {
		t.Fatalf("build item = %#v, %v", build, err)
	}
	record, err := database.GetGeneratorRun(ctx, claim.Run.ID)
	if err != nil || record.CandidateRevisionID != revision.ID {
		t.Fatalf("generator record = %#v, %v", record, err)
	}
}

func seedCandidateForStages(t *testing.T, database *DB, state candidate.State, now time.Time) candidate.Revision {
	t.Helper()
	seedCandidateReferences(t, database, now)
	revision := candidate.Revision{
		ID: "candidate-stage", AuthoringSessionID: "authoring-candidate", AuthoringRevision: 0,
		GeneratorSessionID: "generator-session-candidate", GeneratorRunID: "generator-run-candidate",
		JudgeRunID: "generator-run-candidate", ArchivePath: "/server/candidates/candidate-stage/candidate.tar.gz",
		ArchiveSHA256: "sha256:" + fullHex('a'), Snapshot: nodeSnapshot(), State: state,
		CreatedAt: now, UpdatedAt: now,
	}
	snapshot, err := marshalJSON(revision.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(context.Background(), `INSERT INTO candidate_revisions
		(id, authoring_session_id, authoring_revision, generator_session_id, generator_run_id, judge_run_id,
		archive_path, archive_sha256, execution_snapshot, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?)`, revision.ID, revision.AuthoringSessionID,
		revision.AuthoringRevision, revision.GeneratorSessionID, revision.GeneratorRunID, revision.JudgeRunID,
		revision.ArchivePath, revision.ArchiveSHA256, snapshot, revision.State, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(context.Background(), `UPDATE authoring_sessions SET
		generator_session_id = ?, generator_run_id = ?, candidate_revision_id = ? WHERE id = ?`,
		revision.GeneratorSessionID, revision.GeneratorRunID, revision.ID, revision.AuthoringSessionID); err != nil {
		t.Fatal(err)
	}
	return revision
}

func seedCandidateReferences(t *testing.T, database *DB, now time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := database.conn.ExecContext(ctx, `INSERT INTO users (id, subject, name) VALUES ('user-candidate', 'subject-candidate', 'Candidate Author')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(ctx, `INSERT INTO authoring_sessions
		(id, user_id, state, current_revision, visible_revision, created_at, updated_at)
		VALUES ('authoring-candidate', 'user-candidate', ?, 0, 0, ?, ?)`, authoring.StateGeneratingAndVerifying, nowText(now), nowText(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(ctx, `INSERT INTO authoring_revisions
		(session_id, revision, plan_json, created_at) VALUES ('authoring-candidate', 0, '{}'::jsonb, ?)`, nowText(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(ctx, `INSERT INTO agent_sessions
		(id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at)
		VALUES ('generator-session-candidate', ?, 'authoring-generator', 'authoring-candidate:0', 'user-candidate', ?, ?, ?)`,
		generator.RuntimePurpose, agentruntime.SessionActive, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(ctx, `INSERT INTO agent_runs
		(id, session_id, purpose, owner_kind, owner_ref, status, model, prompt_version, deadline_at, created_at, updated_at)
		VALUES ('generator-run-candidate', 'generator-session-candidate', ?, 'authoring-session', 'authoring-candidate', ?, 'model', 'prompt', NULL, ?, ?)`,
		generator.RuntimePurpose, agentruntime.RunPending, now, now); err != nil {
		t.Fatal(err)
	}
}

func seedClaimedGenerator(t *testing.T, database *DB, now time.Time) (agentruntime.Claim, candidate.Revision) {
	t.Helper()
	seedCandidateReferences(t, database, now)
	ctx := context.Background()
	if _, err := database.conn.ExecContext(ctx, `UPDATE authoring_sessions SET generator_session_id = 'generator-session-candidate',
		generator_run_id = 'generator-run-candidate' WHERE id = 'authoring-candidate'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(ctx, `INSERT INTO generator_runs
		(run_id, generator_session_id, authoring_session_id, authoring_revision, created_at, updated_at)
		VALUES ('generator-run-candidate', 'generator-session-candidate', 'authoring-candidate', 0, ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.EnqueueWorkItem(ctx, worklist.CreateItem{
		ID: "work-generator-candidate", Kind: worklist.KindAgent, SubjectType: worklist.SubjectAgentRun,
		SubjectID: "generator-run-candidate", NextRunAt: now, ExecutionTimeout: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := database.ClaimNext(ctx, "generator-worker", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if claim == nil {
		t.Fatal("claim generator run")
	}
	revision := candidate.Revision{
		ID: candidate.IDForGeneratorRun(claim.Run.ID), AuthoringSessionID: "authoring-candidate", AuthoringRevision: 0,
		GeneratorSessionID: claim.Run.SessionID, GeneratorRunID: claim.Run.ID, JudgeRunID: claim.Run.ID,
		ArchivePath: "/server/candidates/immutable.tar.gz", ArchiveSHA256: "sha256:" + fullHex('a'),
		Snapshot: nodeSnapshot(), State: candidate.StateBuilding,
	}
	return *claim, revision
}

func enqueueCandidateStage(t *testing.T, database *DB, candidateID string, kind worklist.Kind, now time.Time) {
	t.Helper()
	if _, err := database.EnqueueWorkItem(context.Background(), worklist.CreateItem{
		ID: "work-" + string(kind), Kind: kind, SubjectType: worklist.SubjectCandidateRevision,
		SubjectID: candidateID, NextRunAt: now, ExecutionTimeout: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
}

func claimCandidateStage(t *testing.T, database *DB, kind worklist.Kind, now time.Time) *CandidateWorkClaim {
	t.Helper()
	claim, err := database.ClaimCandidateWork(context.Background(), kind, "worker-"+string(kind), time.Minute, now)
	if err != nil || claim == nil {
		t.Fatalf("claim %s = %#v, %v", kind, claim, err)
	}
	return claim
}

func assertCandidateState(t *testing.T, database *DB, id string, state candidate.State) *candidate.Revision {
	t.Helper()
	revision, err := database.GetCandidateRevision(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if revision.State != state {
		t.Fatalf("candidate state = %s, want %s", revision.State, state)
	}
	return revision
}

func assertWorkState(t *testing.T, database *DB, id string, state worklist.State) {
	t.Helper()
	item, err := database.GetWorkItem(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if item.State != state {
		t.Fatalf("work item state = %s, want %s", item.State, state)
	}
}

func nodeSnapshot() candidate.ExecutionSnapshot {
	return candidate.ExecutionSnapshot{
		Runtime:     challenge.RuntimeNode,
		Checkpoints: []candidate.CheckpointSnapshot{{ID: "proxy-ready", Node: "proxy"}},
		Node: &candidate.NodeRuntimeSnapshot{
			BaseImageFingerprint: fullHex('a'), ProfileRevision: "node-profile-v1", NetworkPolicyRevision: "node-network-v1",
			Nodes:     []candidate.NodeSnapshot{{Name: "proxy", Title: "Reverse proxy"}},
			Resources: candidate.NodeResources{CPU: "1", Memory: "512MiB", Processes: 512, RootDisk: "5GiB"},
		},
	}
}

func passedNodeReport() candidate.VerificationReport {
	return candidate.VerificationReport{
		Passed: true, Summary: "all checks passed",
		Answers:     []candidate.ExecutionResult{{Location: "proxy", ExitCode: 0}},
		Checkpoints: []candidate.CheckpointResult{{ID: "proxy-ready", Passed: true, Summary: "proxy is ready"}},
	}
}

func fullHex(character byte) string {
	value := make([]byte, 64)
	for index := range value {
		value[index] = character
	}
	return string(value)
}
