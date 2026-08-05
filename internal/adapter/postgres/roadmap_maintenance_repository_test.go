package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
	roadmaptest "github.com/breakfix/breakfix/internal/testkit/roadmap"
)

const roadmapMaintenanceTestDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func TestRoadmapMaintenanceAutomaticallySnapshotsAllPendingEntries(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 12, 0, 0, 0, time.UTC)
	revision := publishMaintenanceRoadmap(t, database, now, 22)

	recordMaintenanceEntries(t, database, revision, 0, 19, true, now)
	workflow, err := database.Roadmap.TryStartRoadmapWorkflow(ctx, now)
	if err != nil {
		t.Fatalf("try start before automatic threshold: %v", err)
	}
	if workflow != nil {
		t.Fatalf("workflow before 20 published challenges = %#v", workflow)
	}

	recordMaintenanceEntries(t, database, revision, 19, 3, true, now.Add(time.Second))
	workflow, err = database.Roadmap.TryStartRoadmapWorkflow(ctx, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("start automatic roadmap workflow: %v", err)
	}
	if workflow == nil || workflow.State != roadmap.WorkflowQueued || workflow.BaseRevision != revision.Revision {
		t.Fatalf("automatic workflow = %#v", workflow)
	}
	entries, err := database.Roadmap.RoadmapWorkflowEntries(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("list workflow snapshot: %v", err)
	}
	if len(entries) != 22 {
		t.Fatalf("snapshot entry count = %d, want all 22 pending entries", len(entries))
	}
	tasks, err := database.Roadmap.RoadmapTasks(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("list workflow tasks: %v", err)
	}
	if len(tasks) != 22 {
		t.Fatalf("task count = %d, want one Challenge task for every pending entry", len(tasks))
	}
	for _, task := range tasks {
		if task.Kind != roadmap.TaskChallenge || task.State != roadmap.TaskPending {
			t.Fatalf("automatic task = %#v", task)
		}
	}
}

func TestRoadmapMaintenanceWaitsForGenerationIdleWindowAndLeasesTasks(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 13, 0, 0, 0, time.UTC)
	revision := publishMaintenanceRoadmap(t, database, now, 1)
	recordMaintenanceEntries(t, database, revision, 0, 1, true, now)
	if requested, err := database.Roadmap.RequestRoadmapMaintenance(ctx, now); err != nil || !requested {
		t.Fatalf("request maintenance = %v, %v", requested, err)
	}
	if _, err := database.conn.ExecContext(ctx, `INSERT INTO generation_workflows
		(id, source_kind, source_ref, source_revision, state, classification_roadmap_revision, classification_feedback,
		candidate_revision_id, active_agent_run_id, state_version, runtime_attempt, lease_owner, next_run_at, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '', '', NULL, NULL, 1, 0, '', ?, '', ?, ?)`,
		"roadmap-blocking-generation", generation.SourceAuthoring, "roadmap-blocking-session", "1", generation.StateGenerating,
		now, now, now); err != nil {
		t.Fatalf("insert active generation workflow: %v", err)
	}
	workflow, err := database.Roadmap.TryStartRoadmapWorkflow(ctx, now)
	if err != nil {
		t.Fatalf("try start while generation active: %v", err)
	}
	if workflow != nil {
		t.Fatalf("workflow started while Generation was executing: %#v", workflow)
	}
	if _, err := database.conn.ExecContext(ctx, `UPDATE generation_workflows
		SET state = ?, state_version = state_version + 1, runtime_attempt = 0, updated_at = ? WHERE id = ?`,
		generation.StateNeedsAuthorReview, now.Add(time.Second), "roadmap-blocking-generation"); err != nil {
		t.Fatalf("pause generation workflow: %v", err)
	}
	workflow, err = database.Roadmap.TryStartRoadmapWorkflow(ctx, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("start after idle window: %v", err)
	}
	if workflow == nil {
		t.Fatal("idle window did not create a roadmap workflow")
	}
	claims, err := database.Roadmap.ClaimRoadmapTasks(ctx, "server-a", time.Minute, now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("claim roadmap task: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("task claims = %#v", claims)
	}
	if repeated, err := database.Roadmap.ClaimRoadmapTasks(ctx, "server-b", time.Minute, now.Add(4*time.Second)); err != nil || len(repeated) != 0 {
		t.Fatalf("second server claimed active task = %#v, %v", repeated, err)
	}
	takenOver, err := database.Roadmap.ClaimRoadmapTasks(ctx, "server-b", time.Minute, now.Add(63*time.Second))
	if err != nil {
		t.Fatalf("take over expired task lease: %v", err)
	}
	if len(takenOver) != 1 || takenOver[0].Task.ID != claims[0].Task.ID || takenOver[0].LeaseVersion <= claims[0].LeaseVersion || takenOver[0].LeaseOwner == claims[0].LeaseOwner {
		t.Fatalf("task lease takeover = %#v, first = %#v", takenOver, claims)
	}
}

func TestRoadmapMaintenanceCreatesTopicAndChallengeTasksForNewTopic(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 13, 30, 0, 0, time.UTC)
	revision := publishMaintenanceRoadmap(t, database, now, 1)
	recordMaintenanceEntries(t, database, revision, 0, 1, false, now)
	if requested, err := database.Roadmap.RequestRoadmapMaintenance(ctx, now); err != nil || !requested {
		t.Fatalf("request maintenance = %v, %v", requested, err)
	}
	workflow, err := database.Roadmap.TryStartRoadmapWorkflow(ctx, now)
	if err != nil {
		t.Fatalf("start roadmap workflow: %v", err)
	}
	if workflow == nil {
		t.Fatal("workflow was not created for the new topic")
	}
	tasks, err := database.Roadmap.RoadmapTasks(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("list new-topic roadmap tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("new-topic task count = %d, want topic and challenge tasks", len(tasks))
	}
	seen := map[roadmap.TaskKind]roadmap.Task{}
	for _, task := range tasks {
		seen[task.Kind] = task
	}
	topicTask, topicOK := seen[roadmap.TaskTopic]
	challengeTask, challengeOK := seen[roadmap.TaskChallenge]
	if !topicOK || !challengeOK || topicTask.Subject.Ref.ID != revision.ChallengeBindings[0].Topic.ID ||
		challengeTask.Subject.Ref.ID != revision.ChallengeBindings[0].Challenge.ID ||
		challengeTask.Subject.ContentRevision != revision.ChallengeBindings[0].Challenge.ContentRevision {
		t.Fatalf("new-topic task subjects = %#v", tasks)
	}
}

func TestRoadmapMaintenancePublishesAcceptedTasksAndRetainsFailedEntries(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 14, 0, 0, 0, time.UTC)
	revision := roadmaptest.RuntimeRevision()
	revision.TopicEdges = nil
	revision.ChallengeEdges = nil
	published, err := database.Roadmap.PublishRoadmap(ctx, revision, now)
	if err != nil {
		t.Fatalf("publish roadmap: %v", err)
	}
	recordMaintenanceEntries(t, database, published, 0, 2, true, now)
	if requested, err := database.Roadmap.RequestRoadmapMaintenance(ctx, now); err != nil || !requested {
		t.Fatalf("request maintenance = %v, %v", requested, err)
	}
	workflow, err := database.Roadmap.TryStartRoadmapWorkflow(ctx, now)
	if err != nil {
		t.Fatalf("start roadmap workflow: %v", err)
	}
	if workflow == nil {
		t.Fatal("workflow was not created")
	}
	claims, err := database.Roadmap.ClaimRoadmapTasks(ctx, "server-a", time.Minute, now.Add(time.Second))
	if err != nil {
		t.Fatalf("claim roadmap tasks: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("task claims = %#v", claims)
	}
	accepted, failed := claims[0], claims[1]
	if accepted.Task.Subject.Ref.SourceRef > failed.Task.Subject.Ref.SourceRef {
		accepted, failed = failed, accepted
	}
	acceptRoadmapTask(t, database, accepted, now.Add(2*time.Second), roadmap.ChangeSet{Edges: []roadmap.Edge{{
		Source: accepted.Task.Subject.Ref, Target: failed.Task.Subject.Ref, Relation: roadmap.RelationRelated, Reason: "两题共享同一运行时诊断基础。",
	}}})
	if _, err := database.Roadmap.FailRoadmapTask(ctx, failed, "模拟一个耗尽调用预算的 task", now.Add(3*time.Second)); err != nil {
		t.Fatalf("fail second task: %v", err)
	}
	publishClaim, err := database.Roadmap.ClaimRoadmapWorkflowPublication(ctx, "server-a", time.Minute, now.Add(4*time.Second))
	if err != nil {
		t.Fatalf("claim workflow publication: %v", err)
	}
	if publishClaim == nil {
		t.Fatal("workflow publication was not claimable after terminal task results")
	}
	if repeated, err := database.Roadmap.ClaimRoadmapWorkflowPublication(ctx, "server-b", time.Minute, now.Add(5*time.Second)); err != nil || repeated != nil {
		t.Fatalf("second server claimed active publication = %#v, %v", repeated, err)
	}
	takenOver, err := database.Roadmap.ClaimRoadmapWorkflowPublication(ctx, "server-b", time.Minute, now.Add(65*time.Second))
	if err != nil {
		t.Fatalf("take over expired publication lease: %v", err)
	}
	if takenOver == nil || takenOver.Workflow.ID != publishClaim.Workflow.ID || takenOver.LeaseVersion <= publishClaim.LeaseVersion || takenOver.LeaseOwner == publishClaim.LeaseOwner {
		t.Fatalf("publication lease takeover = %#v, first = %#v", takenOver, publishClaim)
	}
	updated, err := database.Roadmap.CompleteRoadmapWorkflow(ctx, *takenOver, now.Add(66*time.Second))
	if err != nil {
		t.Fatalf("complete roadmap workflow: %v", err)
	}
	if updated.Revision == published.Revision || len(updated.ChallengeEdges) != 1 || updated.ChallengeEdges[0].Relation != roadmap.RelationRelated {
		t.Fatalf("published roadmap = %#v", updated)
	}
	completed, err := database.Roadmap.GetRoadmapWorkflow(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("load completed workflow: %v", err)
	}
	if completed.State != roadmap.WorkflowCompleted {
		t.Fatalf("workflow state = %#v", completed)
	}
	var acceptedProcessed, failedProcessed bool
	if err := database.conn.QueryRowContext(ctx, `SELECT challenge_processed FROM roadmap_entries WHERE challenge_id = ?`, accepted.Task.EntryChallengeID).Scan(&acceptedProcessed); err != nil {
		t.Fatalf("read accepted entry: %v", err)
	}
	if err := database.conn.QueryRowContext(ctx, `SELECT challenge_processed FROM roadmap_entries WHERE challenge_id = ?`, failed.Task.EntryChallengeID).Scan(&failedProcessed); err != nil {
		t.Fatalf("read failed entry: %v", err)
	}
	if !acceptedProcessed || failedProcessed {
		t.Fatalf("entry processing = accepted:%t failed:%t", acceptedProcessed, failedProcessed)
	}
	audits, err := database.Roadmap.RoadmapMergeAudits(ctx, workflow.ID)
	if err != nil {
		t.Fatalf("read roadmap merge audits: %v", err)
	}
	if !containsRoadmapAudit(audits, "accepted_related") {
		t.Fatalf("merge audits = %#v", audits)
	}
}

func publishMaintenanceRoadmap(t *testing.T, database *Store, now time.Time, count int) *roadmap.Revision {
	t.Helper()
	value := roadmaptest.RuntimeRevision()
	value.TopicEdges = nil
	value.ChallengeEdges = nil
	if count < 1 {
		count = 1
	}
	value.ChallengeBindings = nil
	topic := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindTopic, "platform-runtime/node-environment-validation"), SourceRef: "platform-runtime/node-environment-validation", Title: "节点环境验证"}
	tag := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindTag, "runtime-fixture"), SourceRef: "runtime-fixture", Title: "运行时验证"}
	for index := 0; index < count; index++ {
		segment := fmt.Sprintf("roadmap-maintenance-%02d", index+1)
		value.ChallengeBindings = append(value.ChallengeBindings, roadmap.ChallengeBinding{
			Challenge: roadmap.ChallengeRef{
				ID: "challenge-" + segment, SourceRef: topic.SourceRef + "/" + segment,
				Title: fmt.Sprintf("Roadmap maintenance %02d", index+1), ContentRevision: roadmapMaintenanceTestDigest,
			},
			Topic: topic, Tags: []roadmap.Ref{tag},
		})
	}
	published, err := database.Roadmap.PublishRoadmap(context.Background(), value, now)
	if err != nil {
		t.Fatalf("publish maintenance roadmap: %v", err)
	}
	return published
}

func recordMaintenanceEntries(t *testing.T, database *Store, revision *roadmap.Revision, start, count int, topicProcessed bool, now time.Time) {
	t.Helper()
	ctx := context.Background()
	tx, err := database.conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin roadmap entry transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	for index := start; index < start+count; index++ {
		binding := revision.ChallengeBindings[index]
		if err := recordRoadmapEntryTx(ctx, tx, binding.Challenge.ID, binding.Topic.ID, topicProcessed, now); err != nil {
			t.Fatalf("record roadmap entry %d: %v", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit roadmap entries: %v", err)
	}
}

func acceptRoadmapTask(t *testing.T, database *Store, claim roadmap.TaskClaim, now time.Time, changes roadmap.ChangeSet) {
	t.Helper()
	ctx := context.Background()
	run, err := database.Roadmap.StartRoadmapTaskAgentRun(ctx, claim, roadmap.AgentPlanner, "test-model", now)
	if err != nil {
		t.Fatalf("start planner run: %v", err)
	}
	if _, err := database.Roadmap.FinalizeRoadmapTaskPlanner(ctx, claim, run.ID, changes, now); err != nil {
		t.Fatalf("finalize planner run: %v", err)
	}
	for _, role := range []roadmap.AgentRole{roadmap.AgentCurriculumReviewer, roadmap.AgentSREReviewer} {
		run, err := database.Roadmap.StartRoadmapTaskAgentRun(ctx, claim, role, "test-model", now)
		if err != nil {
			t.Fatalf("start %s run: %v", role, err)
		}
		if _, err := database.Roadmap.FinalizeRoadmapTaskReview(ctx, claim, run.ID, role, roadmap.Review{Decision: roadmap.ReviewApproved}, now); err != nil {
			t.Fatalf("finalize %s run: %v", role, err)
		}
	}
}

func containsRoadmapAudit(values []roadmap.MergeAudit, outcome string) bool {
	for _, value := range values {
		if value.Outcome == outcome {
			return true
		}
	}
	return false
}
