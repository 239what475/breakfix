package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/agent"
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
	run, err := database.Roadmap.StartRoadmapTaskAgentRun(ctx, claims[0], roadmap.AgentPlanner, "test-model", now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("start planner before lease takeover: %v", err)
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
	runs, err := database.Agent.ListRunsForOwner(ctx, "roadmap-task", claims[0].Task.ID)
	if err != nil {
		t.Fatalf("list runs after lease takeover: %v", err)
	}
	if len(runs) != 2 || runs[0].ID != run.ID || runs[0].Status != agent.RunInterrupted || runs[1].Status != agent.RunRunning || runs[1].Attempt != 1 {
		t.Fatalf("lease takeover runs = %#v", runs)
	}
	resumed, err := database.Roadmap.StartRoadmapTaskAgentRun(ctx, takenOver[0], roadmap.AgentPlanner, "other-model", now.Add(64*time.Second))
	if err != nil {
		t.Fatalf("resume replacement planner run: %v", err)
	}
	if resumed.ID != runs[1].ID || resumed.Model != run.Model {
		t.Fatalf("resumed replacement = %#v, prior = %#v", resumed, run)
	}
	tasks, err := database.Roadmap.RoadmapTasks(ctx, workflow.ID)
	if err != nil || len(tasks) != 1 || tasks[0].PlannerCalls != 1 {
		t.Fatalf("task semantic planner calls after takeover = %#v, %v", tasks, err)
	}
}

func TestRoadmapTaskAgentRunRetriesWithinOneSemanticCall(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 5, 9, 0, 0, 0, time.UTC)
	workflow, claim := startSingleMaintenanceTask(t, database, now)
	run, err := database.Roadmap.StartRoadmapTaskAgentRun(ctx, claim, roadmap.AgentPlanner, "test-model", now.Add(time.Second))
	if err != nil {
		t.Fatalf("start planner run: %v", err)
	}
	for attempt := 1; attempt < agent.MaxAttempts; attempt++ {
		next, err := database.Roadmap.RetryRoadmapTaskAgentRun(ctx, claim, run.ID, roadmap.AgentPlanner, run.Attempt, "model transport failed", now.Add(time.Duration(attempt+1)*time.Second))
		if err != nil {
			t.Fatalf("retry planner attempt %d: %v", attempt, err)
		}
		if next == nil || next.ID != run.ID || next.Attempt != attempt+1 {
			t.Fatalf("planner retry %d = %#v", attempt, next)
		}
		run = next
	}
	if exhausted, err := database.Roadmap.RetryRoadmapTaskAgentRun(ctx, claim, run.ID, roadmap.AgentPlanner, run.Attempt, "model transport failed", now.Add(6*time.Second)); err != nil || exhausted != nil {
		t.Fatalf("exhaust planner run = %#v, %v", exhausted, err)
	}
	tasks, err := database.Roadmap.RoadmapTasks(ctx, workflow.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("load exhausted roadmap task = %#v, %v", tasks, err)
	}
	if tasks[0].State != roadmap.TaskFailed || tasks[0].PlannerCalls != 1 || tasks[0].CurriculumCalls != 0 || tasks[0].SRECalls != 0 {
		t.Fatalf("exhausted task = %#v", tasks[0])
	}
	runs, err := database.Agent.ListRunsForOwner(ctx, "roadmap-task", claim.Task.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("load exhausted planner run = %#v, %v", runs, err)
	}
	if runs[0].Status != agent.RunFailed || runs[0].Attempt != agent.MaxAttempts {
		t.Fatalf("exhausted planner run = %#v", runs[0])
	}
}

func TestRoadmapRecoveryReplacementDoesNotConsumeOrBypassSemanticLimit(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 5, 9, 30, 0, 0, time.UTC)
	_, claim := startSingleMaintenanceTask(t, database, now)
	run, err := database.Roadmap.StartRoadmapTaskAgentRun(ctx, claim, roadmap.AgentPlanner, "pinned-model", now.Add(time.Second))
	if err != nil {
		t.Fatalf("start planner before recovery: %v", err)
	}
	if _, err := database.conn.ExecContext(ctx, `UPDATE roadmap_tasks SET planner_calls = ? WHERE id = ?`, roadmap.MaxAgentCallsPerTask, claim.Task.ID); err != nil {
		t.Fatalf("set planner semantic call limit: %v", err)
	}
	if err := database.Roadmap.RecoverInterruptedRoadmapAgentRuns(ctx, "server restarted", now.Add(2*time.Second)); err != nil {
		t.Fatalf("recover planner at semantic limit: %v", err)
	}
	claims, err := database.Roadmap.ClaimRoadmapTasks(ctx, "server-b", time.Minute, now.Add(3*time.Second))
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim recovered planner at semantic limit = %#v, %v", claims, err)
	}
	replacement, err := database.Roadmap.StartRoadmapTaskAgentRun(ctx, claims[0], roadmap.AgentPlanner, "new-model", now.Add(4*time.Second))
	if err != nil {
		t.Fatalf("reuse planner replacement at semantic limit: %v", err)
	}
	if replacement.ID == run.ID || replacement.Model != run.Model {
		t.Fatalf("planner replacement at semantic limit = %#v", replacement)
	}
	tasks, err := database.Roadmap.RoadmapTasks(ctx, claims[0].Task.WorkflowID)
	if err != nil || len(tasks) != 1 || tasks[0].PlannerCalls != roadmap.MaxAgentCallsPerTask {
		t.Fatalf("semantic planner call limit after replacement = %#v, %v", tasks, err)
	}
}

func TestRoadmapRecoveryReplacesOnlyUnfinishedRoles(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 5, 10, 0, 0, 0, time.UTC)
	workflow, claim := startSingleMaintenanceTask(t, database, now)
	planner, err := database.Roadmap.StartRoadmapTaskAgentRun(ctx, claim, roadmap.AgentPlanner, "pinned-model", now.Add(time.Second))
	if err != nil {
		t.Fatalf("start planner before recovery: %v", err)
	}
	if err := database.Roadmap.RecoverInterruptedRoadmapAgentRuns(ctx, "server restarted", now.Add(2*time.Second)); err != nil {
		t.Fatalf("recover interrupted planner: %v", err)
	}
	tasks, err := database.Roadmap.RoadmapTasks(ctx, workflow.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("load recovered task = %#v, %v", tasks, err)
	}
	if tasks[0].State != roadmap.TaskPending || tasks[0].LeaseOwner != "" || tasks[0].PlannerCalls != 1 || tasks[0].Round != 0 {
		t.Fatalf("recovered planner task = %#v", tasks[0])
	}
	runs, err := database.Agent.ListRunsForOwner(ctx, "roadmap-task", claim.Task.ID)
	if err != nil || len(runs) != 2 {
		t.Fatalf("load recovered planner runs = %#v, %v", runs, err)
	}
	if runs[0].ID != planner.ID || runs[0].Status != agent.RunInterrupted || runs[1].Status != agent.RunRunning || runs[1].Model != planner.Model || runs[1].PromptVersion != planner.PromptVersion || runs[1].Attempt != 1 {
		t.Fatalf("recovered planner runs = %#v", runs)
	}
	claims, err := database.Roadmap.ClaimRoadmapTasks(ctx, "server-b", time.Minute, now.Add(3*time.Second))
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim recovered task = %#v, %v", claims, err)
	}
	plannerReplacement, err := database.Roadmap.StartRoadmapTaskAgentRun(ctx, claims[0], roadmap.AgentPlanner, "new-model", now.Add(4*time.Second))
	if err != nil {
		t.Fatalf("reuse recovered planner replacement: %v", err)
	}
	if plannerReplacement.ID != runs[1].ID || plannerReplacement.Model != planner.Model {
		t.Fatalf("planner replacement = %#v", plannerReplacement)
	}
	if _, err := database.Roadmap.FinalizeRoadmapTaskPlanner(ctx, claims[0], plannerReplacement.ID, roadmap.ChangeSet{}, now.Add(5*time.Second)); err != nil {
		t.Fatalf("finalize recovered planner replacement: %v", err)
	}
	curriculum, err := database.Roadmap.StartRoadmapTaskAgentRun(ctx, claims[0], roadmap.AgentCurriculumReviewer, "pinned-model", now.Add(6*time.Second))
	if err != nil {
		t.Fatalf("start curriculum reviewer: %v", err)
	}
	if _, err := database.Roadmap.FinalizeRoadmapTaskReview(ctx, claims[0], curriculum.ID, roadmap.AgentCurriculumReviewer, roadmap.Review{Decision: roadmap.ReviewApproved}, now.Add(7*time.Second)); err != nil {
		t.Fatalf("finalize curriculum reviewer: %v", err)
	}
	sre, err := database.Roadmap.StartRoadmapTaskAgentRun(ctx, claims[0], roadmap.AgentSREReviewer, "pinned-model", now.Add(8*time.Second))
	if err != nil {
		t.Fatalf("start SRE reviewer before recovery: %v", err)
	}
	if err := database.Roadmap.RecoverInterruptedRoadmapAgentRuns(ctx, "server restarted", now.Add(9*time.Second)); err != nil {
		t.Fatalf("recover interrupted reviewer: %v", err)
	}
	tasks, err = database.Roadmap.RoadmapTasks(ctx, workflow.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("load reviewer recovery task = %#v, %v", tasks, err)
	}
	if tasks[0].State != roadmap.TaskPending || tasks[0].ChangeSet == nil || tasks[0].CurriculumReview == nil || tasks[0].SREReview != nil ||
		tasks[0].PlannerCalls != 1 || tasks[0].CurriculumCalls != 1 || tasks[0].SRECalls != 1 || tasks[0].Round != 0 {
		t.Fatalf("recovered reviewer task = %#v", tasks[0])
	}
	runs, err = database.Agent.ListRunsForOwner(ctx, "roadmap-task", claim.Task.ID)
	if err != nil || len(runs) != 5 {
		t.Fatalf("load reviewer recovery runs = %#v, %v", runs, err)
	}
	if runs[3].ID != sre.ID || runs[3].Status != agent.RunInterrupted || runs[4].Status != agent.RunRunning || runs[4].Purpose != roadmap.AgentSREReviewer.Purpose() {
		t.Fatalf("recovered reviewer runs = %#v", runs)
	}
	claims, err = database.Roadmap.ClaimRoadmapTasks(ctx, "server-c", time.Minute, now.Add(10*time.Second))
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim reviewer recovery task = %#v, %v", claims, err)
	}
	sreReplacement, err := database.Roadmap.StartRoadmapTaskAgentRun(ctx, claims[0], roadmap.AgentSREReviewer, "new-model", now.Add(11*time.Second))
	if err != nil {
		t.Fatalf("reuse recovered reviewer replacement: %v", err)
	}
	if sreReplacement.ID != runs[4].ID || sreReplacement.Model != sre.Model {
		t.Fatalf("SRE replacement = %#v", sreReplacement)
	}
	if _, err := database.Roadmap.StartRoadmapTaskAgentRun(ctx, claims[0], roadmap.AgentPlanner, "new-model", now.Add(11*time.Second)); err != roadmap.ErrLeaseLost {
		t.Fatalf("planner after committed result error = %v, want lease lost", err)
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
				ID: "challenge-" + segment, RevisionID: fmt.Sprintf("chrev-%016x", index+1), SourceRef: topic.SourceRef + "/" + segment,
				Title: fmt.Sprintf("Roadmap maintenance %02d", index+1), ContentRevision: roadmapMaintenanceTestDigest,
				SourceSlug: segment, MaterializedRevision: roadmapMaintenanceTestDigest,
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

func startSingleMaintenanceTask(t *testing.T, database *Store, now time.Time) (*roadmap.Workflow, roadmap.TaskClaim) {
	t.Helper()
	ctx := context.Background()
	revision := publishMaintenanceRoadmap(t, database, now, 1)
	recordMaintenanceEntries(t, database, revision, 0, 1, true, now)
	if requested, err := database.Roadmap.RequestRoadmapMaintenance(ctx, now); err != nil || !requested {
		t.Fatalf("request roadmap maintenance = %v, %v", requested, err)
	}
	workflow, err := database.Roadmap.TryStartRoadmapWorkflow(ctx, now)
	if err != nil || workflow == nil {
		t.Fatalf("start roadmap maintenance workflow = %#v, %v", workflow, err)
	}
	claims, err := database.Roadmap.ClaimRoadmapTasks(ctx, "server-a", time.Minute, now)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim roadmap maintenance task = %#v, %v", claims, err)
	}
	return workflow, claims[0]
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
