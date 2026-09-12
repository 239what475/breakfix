package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
)

var (
	// ErrRoadmapMaintenanceActive is returned to a transition that would enter
	// a Generation execution state or make a Catalog commit visible while a
	// fixed Roadmap snapshot owns the shared publication barrier.
	ErrRoadmapMaintenanceActive = errors.New("roadmap maintenance is active")
)

//nolint:gosec // This is a PostgreSQL column list, not a credential.
const roadmapWorkflowColumns = `id, base_revision, state, publish_attempt, lease_owner, lease_version, lease_expires_at,
	next_run_at, last_error, created_at, updated_at`
const roadmapWorkflowSelect = `SELECT ` + roadmapWorkflowColumns + ` FROM roadmap_workflows`

const roadmapTaskColumns = `id, workflow_id, entry_challenge_id, snapshot_order, kind, subject_id, subject_source_ref,
	subject_title, subject_content_revision, state, round, planner_calls, curriculum_calls, sre_calls, changeset_json,
	curriculum_review_json, sre_review_json, lease_owner, lease_version, lease_expires_at, next_run_at, last_error, created_at, updated_at`
const roadmapTaskSelect = `SELECT ` + roadmapTaskColumns + ` FROM roadmap_tasks`

type roadmapMaintenanceControl struct {
	Requested                 bool
	UnrequestedChallengeCount int
	RequestedAt               *time.Time
}

// RequestRoadmapMaintenance creates the one durable request used by the
// internal debugging path. It does not create a workflow itself: TryStart
// owns the idle-window gate and the fixed entry snapshot.
func (d *RoadmapRepository) RequestRoadmapMaintenance(ctx context.Context, now time.Time) (bool, error) {
	if now.IsZero() {
		return false, errors.New("roadmap maintenance request requires current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin roadmap maintenance request: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	control, err := lockRoadmapMaintenanceControlTx(ctx, tx)
	if err != nil {
		return false, err
	}
	pending, err := hasPendingRoadmapEntriesTx(ctx, tx)
	if err != nil {
		return false, err
	}
	if !pending {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if !control.Requested {
		if _, err := tx.ExecContext(ctx, `UPDATE roadmap_maintenance_control SET requested = TRUE, requested_at = ? WHERE singleton = TRUE`, now.UTC()); err != nil {
			return false, fmt.Errorf("request roadmap maintenance: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// TryStartRoadmapWorkflow atomically checks the Roadmap write lock, the
// Generation idle window, Catalog commit ownership, and the durable request.
// A successful call freezes every currently pending entry in one workflow.
func (d *RoadmapRepository) TryStartRoadmapWorkflow(ctx context.Context, now time.Time) (*roadmap.Workflow, error) {
	if now.IsZero() {
		return nil, errors.New("roadmap workflow start requires current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin roadmap workflow start: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now = now.UTC()
	revision, err := currentRoadmapForUpdateTx(ctx, tx)
	if errors.Is(err, roadmap.ErrNoCurrentRevision) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	control, err := lockRoadmapMaintenanceControlTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !control.Requested {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	active, err := hasActiveRoadmapWorkflowTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if active {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	activeGeneration, err := hasGenerationExecutionTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	committingCatalog, err := hasCatalogCommitTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if activeGeneration || committingCatalog {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	entries, err := pendingRoadmapEntriesForRevisionTx(ctx, tx, *revision)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE roadmap_maintenance_control
			SET requested = FALSE, unrequested_challenge_count = 0, requested_at = NULL WHERE singleton = TRUE`); err != nil {
			return nil, fmt.Errorf("clear empty roadmap maintenance request: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	workflow := roadmap.Workflow{
		ID:           roadmap.NewWorkflowID(),
		BaseRevision: revision.Revision,
		State:        roadmap.WorkflowQueued,
		NextRunAt:    now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO roadmap_workflows
		(id, base_revision, state, publish_attempt, lease_owner, lease_version, lease_expires_at, next_run_at, last_error, created_at, updated_at)
		VALUES (?, ?, ?, 0, '', 0, NULL, ?, '', ?, ?)`,
		workflow.ID, workflow.BaseRevision, workflow.State, workflow.NextRunAt, workflow.CreatedAt, workflow.UpdatedAt); err != nil {
		return nil, fmt.Errorf("create roadmap workflow: %w", err)
	}
	for _, entry := range entries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO roadmap_workflow_entries
			(workflow_id, challenge_id, topic_id, snapshot_order, topic_required, challenge_required)
			VALUES (?, ?, ?, ?, ?, ?)`,
			workflow.ID, entry.entry.ChallengeID, entry.entry.TopicID, entry.snapshotOrder, !entry.entry.TopicProcessed, !entry.entry.ChallengeProcessed); err != nil {
			return nil, fmt.Errorf("store roadmap workflow entry: %w", err)
		}
		if !entry.entry.TopicProcessed {
			if err := insertRoadmapTaskTx(ctx, tx, workflow, entry, roadmap.TaskTopic, now); err != nil {
				return nil, err
			}
		}
		if !entry.entry.ChallengeProcessed {
			if err := insertRoadmapTaskTx(ctx, tx, workflow, entry, roadmap.TaskChallenge, now); err != nil {
				return nil, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE roadmap_maintenance_control
		SET requested = FALSE, unrequested_challenge_count = 0, requested_at = NULL WHERE singleton = TRUE`); err != nil {
		return nil, fmt.Errorf("consume roadmap maintenance request: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit roadmap workflow start: %w", err)
	}
	return &workflow, nil
}

// recordRoadmapEntryTx is shared by authoring publication and deliberately
// lives beside the request counter. Catalog installation calls neither path:
// its complete portable graph is the processed baseline by definition.
func recordRoadmapEntryTx(ctx context.Context, tx *Tx, challengeID, topicID string, topicProcessed bool, now time.Time) error {
	if strings.TrimSpace(challengeID) == "" || strings.TrimSpace(topicID) == "" || now.IsZero() {
		return errors.New("roadmap entry requires challenge, topic, and current time")
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO roadmap_entries (challenge_id, topic_id, topic_processed, challenge_processed, created_at)
		VALUES (?, ?, ?, FALSE, ?) ON CONFLICT (challenge_id) DO NOTHING`, challengeID, topicID, topicProcessed, now.UTC())
	if err != nil {
		return fmt.Errorf("record roadmap entry: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted != 1 {
		return nil
	}
	return noteRoadmapChallengePendingTx(ctx, tx, now)
}

// requeueRoadmapChallengeTx marks one already-visible Challenge for another
// challenge-edge review after an immutable content revision becomes active.
// Topic ownership is intentionally retained, so only the challenge task is
// made pending.
func requeueRoadmapChallengeTx(ctx context.Context, tx *Tx, challengeID, topicID string, now time.Time) error {
	if strings.TrimSpace(challengeID) == "" || strings.TrimSpace(topicID) == "" || now.IsZero() {
		return errors.New("roadmap challenge requeue requires challenge, topic, and current time")
	}
	var storedTopicID string
	var pending bool
	if err := tx.QueryRowContext(ctx, `SELECT topic_id, NOT challenge_processed FROM roadmap_entries WHERE challenge_id = ? FOR UPDATE`, challengeID).Scan(&storedTopicID, &pending); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return roadmap.ErrNoCurrentRevision
		}
		return fmt.Errorf("lock roadmap challenge entry: %w", err)
	}
	if storedTopicID != topicID {
		return roadmap.ErrNoCurrentRevision
	}
	if pending {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE roadmap_entries SET challenge_processed = FALSE WHERE challenge_id = ?`, challengeID); err != nil {
		return fmt.Errorf("requeue roadmap challenge entry: %w", err)
	}
	return noteRoadmapChallengePendingTx(ctx, tx, now)
}

func noteRoadmapChallengePendingTx(ctx context.Context, tx *Tx, now time.Time) error {
	control, err := lockRoadmapMaintenanceControlTx(ctx, tx)
	if err != nil {
		return err
	}
	count := control.UnrequestedChallengeCount + 1
	requested := control.Requested
	requestedAt := control.RequestedAt
	if !requested && count >= roadmap.AutomaticRequestThreshold {
		requested = true
		count = 0
		value := now.UTC()
		requestedAt = &value
	}
	if _, err := tx.ExecContext(ctx, `UPDATE roadmap_maintenance_control
		SET requested = ?, unrequested_challenge_count = ?, requested_at = ? WHERE singleton = TRUE`, requested, count, requestedAt); err != nil {
		return fmt.Errorf("update roadmap maintenance request counter: %w", err)
	}
	return nil
}

func lockRoadmapMaintenanceControlTx(ctx context.Context, tx *Tx) (roadmapMaintenanceControl, error) {
	var result roadmapMaintenanceControl
	var requestedAt sql.NullTime
	if err := tx.QueryRowContext(ctx, `SELECT requested, unrequested_challenge_count, requested_at
		FROM roadmap_maintenance_control WHERE singleton = TRUE FOR UPDATE`).Scan(&result.Requested, &result.UnrequestedChallengeCount, &requestedAt); err != nil {
		return roadmapMaintenanceControl{}, fmt.Errorf("lock roadmap maintenance control: %w", err)
	}
	if requestedAt.Valid {
		value := requestedAt.Time.UTC()
		result.RequestedAt = &value
	}
	return result, nil
}

func hasPendingRoadmapEntriesTx(ctx context.Context, tx *Tx) (bool, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM roadmap_entries WHERE NOT topic_processed OR NOT challenge_processed)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("check pending roadmap entries: %w", err)
	}
	return exists, nil
}

func hasActiveRoadmapWorkflowTx(ctx context.Context, tx *Tx) (bool, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM roadmap_workflows WHERE state IN (?, ?, ?))`,
		roadmap.WorkflowQueued, roadmap.WorkflowRunning, roadmap.WorkflowPublishing).Scan(&exists); err != nil {
		return false, fmt.Errorf("check active roadmap workflow: %w", err)
	}
	return exists, nil
}

func hasGenerationExecutionTx(ctx context.Context, tx *Tx) (bool, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM generation_workflows WHERE state IN (?, ?, ?, ?, ?, ?))`,
		generation.StateGenerating, generation.StateJudging, generation.StateBuilding, generation.StateArtifactPublishing,
		generation.StateVerifying, generation.StateChallengePublishing).Scan(&exists); err != nil {
		return false, fmt.Errorf("check active generation execution: %w", err)
	}
	return exists, nil
}

func hasCatalogCommitTx(ctx context.Context, tx *Tx) (bool, error) {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM catalog_releases WHERE state = 'Committing')`).Scan(&exists); err != nil {
		return false, fmt.Errorf("check catalog commit: %w", err)
	}
	return exists, nil
}

// ensureRoadmapExecutionAllowedTx takes the same write lock used by workflow
// creation and publication. Generation transition callers use it before they
// enter an execution state, so a snapshot can never race a new execution.
func ensureRoadmapExecutionAllowedTx(ctx context.Context, tx *Tx, now time.Time) error {
	if now.IsZero() {
		return errors.New("roadmap execution barrier requires current time")
	}
	if _, err := tx.ExecContext(ctx, `SELECT revision_id FROM roadmap_current WHERE singleton = TRUE FOR UPDATE`); err != nil {
		return fmt.Errorf("lock roadmap execution barrier: %w", err)
	}
	active, err := hasActiveRoadmapWorkflowTx(ctx, tx)
	if err != nil {
		return err
	}
	if active {
		return ErrRoadmapMaintenanceActive
	}
	return nil
}

func ensureRoadmapPublicationAllowedTx(ctx context.Context, tx *Tx, now time.Time) error {
	if now.IsZero() {
		return errors.New("roadmap publication barrier requires current time")
	}
	active, err := hasActiveRoadmapWorkflowTx(ctx, tx)
	if err != nil {
		return err
	}
	if active {
		return ErrRoadmapMaintenanceActive
	}
	return nil
}

type roadmapPendingEntry struct {
	entry         roadmap.Entry
	snapshotOrder int
	topic         roadmap.Ref
	challenge     roadmap.Subject
}

func pendingRoadmapEntriesForRevisionTx(ctx context.Context, tx *Tx, revision roadmap.Revision) ([]roadmapPendingEntry, error) {
	rows, err := tx.QueryContext(ctx, `SELECT challenge_id, topic_id, topic_processed, challenge_processed, created_at
		FROM roadmap_entries WHERE NOT topic_processed OR NOT challenge_processed`)
	if err != nil {
		return nil, fmt.Errorf("list pending roadmap entries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	entries := make([]roadmap.Entry, 0)
	for rows.Next() {
		var value roadmap.Entry
		if err := rows.Scan(&value.ChallengeID, &value.TopicID, &value.TopicProcessed, &value.ChallengeProcessed, &value.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan pending roadmap entry: %w", err)
		}
		entries = append(entries, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending roadmap entries: %w", err)
	}
	topics := make(map[string]roadmap.Ref, len(revision.Topics))
	for _, value := range revision.Topics {
		topics[value.ID] = roadmap.Ref{ID: value.ID, SourceRef: value.SourceRef, Title: value.Title}
	}
	bindings := make(map[string]roadmap.ChallengeBinding, len(revision.ChallengeBindings))
	for _, value := range revision.ChallengeBindings {
		bindings[value.Challenge.ID] = value
	}
	result := make([]roadmapPendingEntry, 0, len(entries))
	for _, entry := range entries {
		binding, exists := bindings[entry.ChallengeID]
		if !exists || binding.Topic.ID != entry.TopicID {
			return nil, fmt.Errorf("pending roadmap entry %q is absent from current revision", entry.ChallengeID)
		}
		topic, exists := topics[entry.TopicID]
		if !exists || topic != binding.Topic {
			return nil, fmt.Errorf("pending roadmap entry %q has an unknown topic", entry.ChallengeID)
		}
		result = append(result, roadmapPendingEntry{
			entry: entry, topic: topic,
			challenge: roadmap.Subject{Ref: roadmap.Ref{ID: binding.Challenge.ID, SourceRef: binding.Challenge.SourceRef, Title: binding.Challenge.Title}, ContentRevision: binding.Challenge.ContentRevision},
		})
	}
	slices.SortFunc(result, func(left, right roadmapPendingEntry) int {
		if left.challenge.Ref.SourceRef < right.challenge.Ref.SourceRef {
			return -1
		}
		if left.challenge.Ref.SourceRef > right.challenge.Ref.SourceRef {
			return 1
		}
		return strings.Compare(left.entry.ChallengeID, right.entry.ChallengeID)
	})
	for index := range result {
		result[index].snapshotOrder = index
	}
	return result, nil
}

func insertRoadmapTaskTx(ctx context.Context, tx *Tx, workflow roadmap.Workflow, entry roadmapPendingEntry, kind roadmap.TaskKind, now time.Time) error {
	var subject roadmap.Subject
	switch kind {
	case roadmap.TaskTopic:
		subject = roadmap.Subject{Ref: entry.topic}
	case roadmap.TaskChallenge:
		subject = entry.challenge
	default:
		return errors.New("invalid roadmap task kind")
	}
	task := roadmap.Task{
		ID:               roadmap.NewTaskID(),
		WorkflowID:       workflow.ID,
		EntryChallengeID: entry.entry.ChallengeID,
		SnapshotOrder:    entry.snapshotOrder,
		Kind:             kind,
		Subject:          subject,
		State:            roadmap.TaskPending,
		NextRunAt:        now.UTC(),
		CreatedAt:        now.UTC(),
		UpdatedAt:        now.UTC(),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO roadmap_tasks
		(id, workflow_id, entry_challenge_id, snapshot_order, kind, subject_id, subject_source_ref, subject_title, subject_content_revision,
		state, round, planner_calls, curriculum_calls, sre_calls, changeset_json, curriculum_review_json, sre_review_json,
		lease_owner, lease_version, lease_expires_at, next_run_at, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, 0, 0, NULL, NULL, NULL, '', 0, NULL, ?, '', ?, ?)`,
		task.ID, task.WorkflowID, task.EntryChallengeID, task.SnapshotOrder, task.Kind, task.Subject.Ref.ID, task.Subject.Ref.SourceRef,
		task.Subject.Ref.Title, task.Subject.ContentRevision, task.State, task.NextRunAt, task.CreatedAt, task.UpdatedAt); err != nil {
		return fmt.Errorf("create roadmap %s task for entry %q: %w", kind, entry.entry.ChallengeID, err)
	}
	return nil
}

func (d *RoadmapRepository) GetRoadmapWorkflow(ctx context.Context, id string) (*roadmap.Workflow, error) {
	workflow, err := scanRoadmapWorkflow(d.conn.QueryRowContext(ctx, roadmapWorkflowSelect+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, roadmap.ErrWorkflowNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get roadmap workflow: %w", err)
	}
	return workflow, nil
}

func (d *RoadmapRepository) RoadmapTasks(ctx context.Context, workflowID string) ([]roadmap.Task, error) {
	rows, err := d.conn.QueryContext(ctx, roadmapTaskSelect+` WHERE workflow_id = ? ORDER BY snapshot_order, kind, id`, strings.TrimSpace(workflowID))
	if err != nil {
		return nil, fmt.Errorf("list roadmap tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]roadmap.Task, 0)
	for rows.Next() {
		task, scanErr := scanRoadmapTask(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, *task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate roadmap tasks: %w", err)
	}
	return result, nil
}

func (d *RoadmapRepository) RoadmapWorkflowEntries(ctx context.Context, workflowID string) ([]roadmap.WorkflowEntry, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT workflow_id, challenge_id, topic_id, snapshot_order, topic_required, challenge_required
		FROM roadmap_workflow_entries WHERE workflow_id = ? ORDER BY snapshot_order, challenge_id`, strings.TrimSpace(workflowID))
	if err != nil {
		return nil, fmt.Errorf("list roadmap workflow entries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]roadmap.WorkflowEntry, 0)
	for rows.Next() {
		var entry roadmap.WorkflowEntry
		if err := rows.Scan(&entry.WorkflowID, &entry.ChallengeID, &entry.TopicID, &entry.SnapshotOrder, &entry.TopicRequired, &entry.ChallengeRequired); err != nil {
			return nil, fmt.Errorf("scan roadmap workflow entry: %w", err)
		}
		result = append(result, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate roadmap workflow entries: %w", err)
	}
	return result, nil
}

// ClaimRoadmapTasks leases every available task in the fixed workflow
// snapshots. There is deliberately no arbitrary batch size: the workflow
// snapshot, rather than a scheduler-side page, defines the complete unit of
// incremental maintenance. Individual task leases permit Server replicas to
// share that work without another worker queue or a process-local lock. An
// expired owner never turns its unfinished model call into a failure: its Run
// is interrupted and replaced from the task's committed boundary.
func (d *RoadmapRepository) ClaimRoadmapTasks(ctx context.Context, workerID string, leaseTTL time.Duration, now time.Time) ([]roadmap.TaskClaim, error) {
	if strings.TrimSpace(workerID) == "" || leaseTTL <= 0 || now.IsZero() {
		return nil, errors.New("roadmap task claim requires server identity, lease ttl, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin roadmap task claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now = now.UTC()
	rows, err := tx.QueryContext(ctx, `SELECT task.id
		FROM roadmap_tasks task
		JOIN roadmap_workflows workflow ON workflow.id = task.workflow_id
		WHERE workflow.state IN (?, ?)
			AND task.state IN (?, ?)
			AND task.next_run_at <= ?
			AND (task.lease_expires_at IS NULL OR task.lease_expires_at <= ?)
		ORDER BY task.snapshot_order, task.kind, task.id
		FOR UPDATE OF task SKIP LOCKED`,
		roadmap.WorkflowQueued, roadmap.WorkflowRunning,
		roadmap.TaskPending, roadmap.TaskRunning, now, now)
	if err != nil {
		return nil, fmt.Errorf("select roadmap task claims: %w", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan roadmap task claim: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate roadmap task claims: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close roadmap task claims: %w", err)
	}
	claims := make([]roadmap.TaskClaim, 0, len(ids))
	for _, id := range ids {
		task, err := scanRoadmapTask(tx.QueryRowContext(ctx, roadmapTaskSelect+` WHERE id = ? FOR UPDATE`, id))
		if err != nil {
			return nil, err
		}
		if task.State == roadmap.TaskRunning {
			if err := interruptRoadmapTaskAgentRunsTx(ctx, tx, *task, "roadmap task lease expired before agent completion", now); err != nil {
				return nil, err
			}
		}
		owner := roadmap.NewLeaseOwner(workerID)
		leaseVersion := task.LeaseVersion + 1
		expires := now.Add(leaseTTL)
		updated, err := scanRoadmapTask(tx.QueryRowContext(ctx, `UPDATE roadmap_tasks SET state = ?, lease_owner = ?, lease_version = ?,
			lease_expires_at = ?, next_run_at = ?, updated_at = ? WHERE id = ? RETURNING `+roadmapTaskColumns,
			roadmap.TaskRunning, owner, leaseVersion, expires, now, now, task.ID))
		if err != nil {
			return nil, fmt.Errorf("claim roadmap task: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE roadmap_workflows SET state = CASE WHEN state = ? THEN ? ELSE state END, updated_at = ?
			WHERE id = ?`, roadmap.WorkflowQueued, roadmap.WorkflowRunning, now, updated.WorkflowID); err != nil {
			return nil, fmt.Errorf("start roadmap workflow after task claim: %w", err)
		}
		claims = append(claims, roadmap.TaskClaim{Task: *updated, LeaseCredential: roadmap.LeaseCredential{LeaseOwner: owner, LeaseVersion: leaseVersion}})
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit roadmap task claims: %w", err)
	}
	return claims, nil
}

// RecoverInterruptedRoadmapAgentRuns is the Server startup recovery boundary
// for Roadmap model execution. It only changes tasks that were actively leased
// when the prior Server stopped. The committed ChangeSet and reviews decide
// which interrupted role is replaced; no model execution is resumed.
func (d *RoadmapRepository) RecoverInterruptedRoadmapAgentRuns(ctx context.Context, reason string, now time.Time) error {
	if strings.TrimSpace(reason) == "" || now.IsZero() {
		return errors.New("roadmap agent interruption requires reason and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin roadmap agent interruption: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now = now.UTC()
	rows, err := tx.QueryContext(ctx, `SELECT task.id
		FROM roadmap_tasks task
		JOIN roadmap_workflows workflow ON workflow.id = task.workflow_id
		WHERE task.state = ? AND workflow.state = ?
		FOR UPDATE OF task`, roadmap.TaskRunning, roadmap.WorkflowRunning)
	if err != nil {
		return fmt.Errorf("list active roadmap tasks for recovery: %w", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan active roadmap task for recovery: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate active roadmap tasks for recovery: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close active roadmap tasks for recovery: %w", err)
	}
	for _, id := range ids {
		task, err := scanRoadmapTask(tx.QueryRowContext(ctx, roadmapTaskSelect+` WHERE id = ? FOR UPDATE`, id))
		if err != nil {
			return err
		}
		if err := interruptRoadmapTaskAgentRunsTx(ctx, tx, *task, strings.TrimSpace(reason), now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE roadmap_tasks SET state = ?, lease_owner = '', lease_expires_at = NULL,
			next_run_at = ?, updated_at = ? WHERE id = ?`, roadmap.TaskPending, now, now, task.ID); err != nil {
			return fmt.Errorf("release recovered roadmap task claim: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (d *RoadmapRepository) RenewRoadmapTaskLease(ctx context.Context, claim roadmap.TaskClaim, leaseTTL time.Duration, now time.Time) error {
	if !claim.Valid() || leaseTTL <= 0 || now.IsZero() {
		return errors.New("roadmap task lease renewal is invalid")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE roadmap_tasks task SET lease_expires_at = ?, updated_at = ?
		FROM roadmap_workflows workflow
		WHERE task.id = ? AND task.workflow_id = workflow.id AND task.state = ? AND task.lease_owner = ?
			AND task.lease_version = ? AND task.lease_expires_at > ? AND workflow.state = ?`,
		now.UTC().Add(leaseTTL), now.UTC(), claim.Task.ID, roadmap.TaskRunning, claim.LeaseOwner, claim.LeaseVersion,
		now.UTC(), roadmap.WorkflowRunning)
	if err != nil {
		return fmt.Errorf("renew roadmap task lease: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return roadmap.ErrLeaseLost
	}
	return nil
}

func (d *RoadmapRepository) GetRoadmapTaskClaim(ctx context.Context, taskID string, credential roadmap.LeaseCredential, now time.Time) (*roadmap.TaskClaim, error) {
	if strings.TrimSpace(taskID) == "" || !credential.Valid() || now.IsZero() {
		return nil, errors.New("roadmap task lease credentials are required")
	}
	task, err := scanRoadmapTask(d.conn.QueryRowContext(ctx, roadmapTaskSelect+` WHERE id = ? AND state = ? AND lease_owner = ?
		AND lease_version = ? AND lease_expires_at > ?`, taskID, roadmap.TaskRunning, credential.LeaseOwner, credential.LeaseVersion, now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, roadmap.ErrLeaseLost
	}
	if err != nil {
		return nil, fmt.Errorf("get roadmap task claim: %w", err)
	}
	return &roadmap.TaskClaim{Task: *task, LeaseCredential: credential}, nil
}

// StartRoadmapTaskAgentRun records one durable semantic call before model work
// starts. A recovery replacement remains Running and is returned here under a
// new task lease, so it never consumes another semantic role call.
func (d *RoadmapRepository) StartRoadmapTaskAgentRun(ctx context.Context, claim roadmap.TaskClaim, role roadmap.AgentRole, model string, now time.Time) (*agent.Run, error) {
	if !claim.Valid() || !role.Valid() || strings.TrimSpace(model) == "" || now.IsZero() {
		return nil, errors.New("roadmap task agent run is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin roadmap task agent run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	task, workflow, err := lockRoadmapTaskClaimTx(ctx, tx, claim, now.UTC())
	if err != nil {
		return nil, err
	}
	if !roadmapTaskRoleRequired(*task, role) {
		return nil, roadmap.ErrLeaseLost
	}
	active, err := activeRoadmapTaskAgentRunForRoleTx(ctx, tx, task.ID, role)
	if err != nil {
		return nil, err
	}
	if active != nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return active, nil
	}
	calls := taskCallsForRole(*task, role)
	if calls >= roadmap.MaxAgentCallsPerTask {
		return nil, roadmap.ErrAgentCallLimit
	}
	calls++
	if err := setTaskCallsTx(ctx, tx, task.ID, role, calls, now.UTC()); err != nil {
		return nil, err
	}
	input, err := marshalJSON(struct {
		Role            roadmap.AgentRole `json:"role"`
		Round           int               `json:"round"`
		Call            int               `json:"call"`
		TaskKind        roadmap.TaskKind  `json:"task_kind"`
		Subject         roadmap.Subject   `json:"subject"`
		RoadmapRevision string            `json:"roadmap_revision"`
	}{
		Role: role, Round: task.Round, Call: calls, TaskKind: task.Kind, Subject: task.Subject, RoadmapRevision: workflow.BaseRevision,
	})
	if err != nil {
		return nil, fmt.Errorf("encode roadmap task agent input: %w", err)
	}
	run, err := createRunTx(ctx, tx, agent.CreateRun{
		ID:            agent.NewID("roadmap-agent-run"),
		Purpose:       role.Purpose(),
		OwnerKind:     "roadmap-task",
		OwnerRef:      task.ID,
		InputRevision: workflow.BaseRevision,
		Input:         []byte(input),
		Model:         model,
		PromptVersion: role.PromptVersion(),
	}, now.UTC())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit roadmap task agent run: %w", err)
	}
	return run, nil
}

func (d *RoadmapRepository) FinalizeRoadmapTaskPlanner(ctx context.Context, claim roadmap.TaskClaim, runID string, changes roadmap.ChangeSet, now time.Time) (*roadmap.Task, error) {
	if !claim.Valid() || strings.TrimSpace(runID) == "" || now.IsZero() {
		return nil, errors.New("roadmap planner finalization is invalid")
	}
	encoded, err := marshalJSON(changes)
	if err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin roadmap planner finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	task, workflow, err := lockRoadmapTaskClaimTx(ctx, tx, claim, now.UTC())
	if err != nil {
		return nil, err
	}
	if task.ChangeSet != nil || task.CurriculumReview != nil || task.SREReview != nil {
		return nil, roadmap.ErrLeaseLost
	}
	revision, err := roadmapRevisionTx(ctx, tx, workflow.BaseRevision)
	if err != nil {
		return nil, err
	}
	if err := changes.ValidateFor(task.Kind, task.Subject, *revision); err != nil {
		return nil, fmt.Errorf("validate roadmap planner changeset: %w", err)
	}
	if err := completeRoadmapTaskAgentRunTx(ctx, tx, runID, task.ID, roadmap.AgentPlanner, now.UTC()); err != nil {
		return nil, err
	}
	updated, err := scanRoadmapTask(tx.QueryRowContext(ctx, `UPDATE roadmap_tasks SET changeset_json = ?::jsonb,
		curriculum_review_json = NULL, sre_review_json = NULL, last_error = '', next_run_at = ?, updated_at = ?
		WHERE id = ? RETURNING `+roadmapTaskColumns, encoded, now.UTC(), now.UTC(), task.ID))
	if err != nil {
		return nil, fmt.Errorf("record roadmap planner result: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

// FinalizeRoadmapTaskReview persists one independent reviewer result. The
// second result either accepts the task or clears the entire candidate for a
// fresh Planner round; an approval is never reused for a revised ChangeSet.
func (d *RoadmapRepository) FinalizeRoadmapTaskReview(ctx context.Context, claim roadmap.TaskClaim, runID string, role roadmap.AgentRole, review roadmap.Review, now time.Time) (*roadmap.Task, error) {
	if !claim.Valid() || strings.TrimSpace(runID) == "" || (role != roadmap.AgentCurriculumReviewer && role != roadmap.AgentSREReviewer) || review.Validate() != nil || now.IsZero() {
		return nil, errors.New("roadmap review finalization is invalid")
	}
	encoded, err := marshalJSON(review)
	if err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin roadmap review finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	task, _, err := lockRoadmapTaskClaimTx(ctx, tx, claim, now.UTC())
	if err != nil {
		return nil, err
	}
	if task.ChangeSet == nil {
		return nil, roadmap.ErrLeaseLost
	}
	if role == roadmap.AgentCurriculumReviewer && task.CurriculumReview != nil || role == roadmap.AgentSREReviewer && task.SREReview != nil {
		return nil, roadmap.ErrLeaseLost
	}
	if err := completeRoadmapTaskAgentRunTx(ctx, tx, runID, task.ID, role, now.UTC()); err != nil {
		return nil, err
	}
	assignment := "curriculum_review_json = ?::jsonb"
	if role == roadmap.AgentSREReviewer {
		assignment = "sre_review_json = ?::jsonb"
	}
	updated, err := scanRoadmapTask(tx.QueryRowContext(ctx, `UPDATE roadmap_tasks SET `+assignment+`, last_error = '', next_run_at = ?, updated_at = ?
		WHERE id = ? RETURNING `+roadmapTaskColumns, encoded, now.UTC(), now.UTC(), task.ID))
	if err != nil {
		return nil, fmt.Errorf("record roadmap review result: %w", err)
	}
	if updated.CurriculumReview != nil && updated.SREReview != nil {
		if updated.CurriculumReview.Decision == roadmap.ReviewRejected || updated.SREReview.Decision == roadmap.ReviewRejected {
			updated, err = scanRoadmapTask(tx.QueryRowContext(ctx, `UPDATE roadmap_tasks SET round = round + 1, changeset_json = NULL,
				curriculum_review_json = NULL, sre_review_json = NULL, last_error = '', next_run_at = ?, updated_at = ?
				WHERE id = ? RETURNING `+roadmapTaskColumns, now.UTC(), now.UTC(), task.ID))
			if err != nil {
				return nil, fmt.Errorf("start revised roadmap planner round: %w", err)
			}
		} else {
			updated, err = scanRoadmapTask(tx.QueryRowContext(ctx, `UPDATE roadmap_tasks SET state = ?, lease_owner = '', lease_expires_at = NULL,
				last_error = '', updated_at = ? WHERE id = ? RETURNING `+roadmapTaskColumns, roadmap.TaskAccepted, now.UTC(), task.ID))
			if err != nil {
				return nil, fmt.Errorf("accept roadmap task: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

// RetryRoadmapTaskAgentRun records a technical error within one logical
// semantic call. A nil Run means the fifth attempt or the deadline has been
// exhausted; that Run and its task are then terminally Failed together.
func (d *RoadmapRepository) RetryRoadmapTaskAgentRun(ctx context.Context, claim roadmap.TaskClaim, runID string, role roadmap.AgentRole, expectedAttempt int, message string, now time.Time) (*agent.Run, error) {
	if !claim.Valid() || strings.TrimSpace(runID) == "" || !role.Valid() || expectedAttempt < 1 || strings.TrimSpace(message) == "" || now.IsZero() {
		return nil, errors.New("roadmap task agent retry is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin roadmap task agent retry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	task, _, err := lockRoadmapTaskClaimTx(ctx, tx, claim, now.UTC())
	if err != nil {
		return nil, err
	}
	run, err := lockRoadmapTaskAgentRunTx(ctx, tx, runID, task.ID, role)
	if err != nil {
		return nil, err
	}
	if run.Attempt != expectedAttempt {
		return nil, roadmap.ErrLeaseLost
	}
	if run.Attempt < agent.MaxAttempts && run.DeadlineAt.After(now.UTC()) {
		next, err := scanAgentRun(tx.QueryRowContext(ctx, `UPDATE agent_runs SET attempt = attempt + 1, last_error = ?, updated_at = ?
			WHERE id = ? AND status = ? AND attempt = ? RETURNING `+agentRunColumns,
			strings.TrimSpace(message), now.UTC(), run.ID, agent.RunRunning, expectedAttempt))
		if err != nil {
			return nil, fmt.Errorf("advance roadmap task agent attempt: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE roadmap_tasks SET last_error = ?, updated_at = ? WHERE id = ?`, strings.TrimSpace(message), now.UTC(), task.ID); err != nil {
			return nil, fmt.Errorf("record roadmap task technical error: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return next, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
		WHERE id = ? AND status = ? AND attempt = ?`, agent.RunFailed, strings.TrimSpace(message), now.UTC(), now.UTC(), run.ID, agent.RunRunning, expectedAttempt); err != nil {
		return nil, fmt.Errorf("fail exhausted roadmap task agent run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
		WHERE owner_kind = ? AND owner_ref = ? AND status = ?`, agent.RunFailed, strings.TrimSpace(message), now.UTC(), now.UTC(), "roadmap-task", task.ID, agent.RunRunning); err != nil {
		return nil, fmt.Errorf("fail peer roadmap task agent runs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE roadmap_tasks SET state = ?, lease_owner = '', lease_expires_at = NULL,
		next_run_at = ?, last_error = ?, updated_at = ? WHERE id = ?`, roadmap.TaskFailed, now.UTC(), strings.TrimSpace(message), now.UTC(), task.ID); err != nil {
		return nil, fmt.Errorf("fail exhausted roadmap task: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return nil, nil
}

func (d *RoadmapRepository) FailRoadmapTask(ctx context.Context, claim roadmap.TaskClaim, message string, now time.Time) (*roadmap.Task, error) {
	if !claim.Valid() || strings.TrimSpace(message) == "" || now.IsZero() {
		return nil, errors.New("roadmap task failure is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin roadmap task failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	task, _, err := lockRoadmapTaskClaimTx(ctx, tx, claim, now.UTC())
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
		WHERE owner_kind = ? AND owner_ref = ? AND status = ?`, agent.RunFailed, strings.TrimSpace(message), now.UTC(), now.UTC(),
		"roadmap-task", task.ID, agent.RunRunning); err != nil {
		return nil, fmt.Errorf("fail active roadmap task agent runs: %w", err)
	}
	updated, err := scanRoadmapTask(tx.QueryRowContext(ctx, `UPDATE roadmap_tasks SET state = ?, lease_owner = '', lease_expires_at = NULL,
		last_error = ?, updated_at = ? WHERE id = ? RETURNING `+roadmapTaskColumns, roadmap.TaskFailed, strings.TrimSpace(message), now.UTC(), task.ID))
	if err != nil {
		return nil, fmt.Errorf("fail roadmap task: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func roadmapTaskRoleRequired(task roadmap.Task, role roadmap.AgentRole) bool {
	if task.ChangeSet == nil {
		return role == roadmap.AgentPlanner
	}
	switch role {
	case roadmap.AgentCurriculumReviewer:
		return task.CurriculumReview == nil
	case roadmap.AgentSREReviewer:
		return task.SREReview == nil
	default:
		return false
	}
}

func activeRoadmapTaskAgentRunForRoleTx(ctx context.Context, tx *Tx, taskID string, role roadmap.AgentRole) (*agent.Run, error) {
	run, err := scanAgentRun(tx.QueryRowContext(ctx, agentRunSelect+` WHERE purpose = ? AND owner_kind = ? AND owner_ref = ?
		AND status = ? ORDER BY created_at DESC, id DESC LIMIT 1 FOR UPDATE`, role.Purpose(), "roadmap-task", taskID, agent.RunRunning))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load active roadmap %s run: %w", role, err)
	}
	return run, nil
}

func activeRoadmapTaskAgentRunsForUpdateTx(ctx context.Context, tx *Tx, taskID string) ([]agent.Run, error) {
	rows, err := tx.QueryContext(ctx, agentRunSelect+` WHERE owner_kind = ? AND owner_ref = ? AND status = ?
		ORDER BY created_at, id FOR UPDATE`, "roadmap-task", taskID, agent.RunRunning)
	if err != nil {
		return nil, fmt.Errorf("list active roadmap task agent runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	runs := make([]agent.Run, 0)
	for rows.Next() {
		run, err := scanAgentRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active roadmap task agent runs: %w", err)
	}
	return runs, nil
}

func lockRoadmapTaskAgentRunTx(ctx context.Context, tx *Tx, runID, taskID string, role roadmap.AgentRole) (*agent.Run, error) {
	run, err := scanAgentRun(tx.QueryRowContext(ctx, agentRunSelect+` WHERE id = ? FOR UPDATE`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agent.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load roadmap task agent run: %w", err)
	}
	if run.Purpose != role.Purpose() || run.OwnerKind != "roadmap-task" || run.OwnerRef != taskID || run.Status != agent.RunRunning {
		return nil, roadmap.ErrLeaseLost
	}
	return run, nil
}

func interruptRoadmapTaskAgentRunsTx(ctx context.Context, tx *Tx, task roadmap.Task, reason string, now time.Time) error {
	runs, err := activeRoadmapTaskAgentRunsForUpdateTx(ctx, tx, task.ID)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return nil
	}
	priorByRole := make(map[roadmap.AgentRole]agent.Run, len(runs))
	for _, run := range runs {
		for _, role := range []roadmap.AgentRole{roadmap.AgentPlanner, roadmap.AgentCurriculumReviewer, roadmap.AgentSREReviewer} {
			if run.Purpose == role.Purpose() && roadmapTaskRoleRequired(task, role) {
				priorByRole[role] = run
				break
			}
		}
		result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
			WHERE id = ? AND status = ?`, agent.RunInterrupted, reason, now.UTC(), now.UTC(), run.ID, agent.RunRunning)
		if err != nil {
			return fmt.Errorf("interrupt roadmap task agent run: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return roadmap.ErrLeaseLost
		}
	}
	for _, role := range []roadmap.AgentRole{roadmap.AgentPlanner, roadmap.AgentCurriculumReviewer, roadmap.AgentSREReviewer} {
		prior, exists := priorByRole[role]
		if !exists {
			continue
		}
		if _, err := createRunTx(ctx, tx, agent.CreateRun{
			ID:            agent.NewID("roadmap-agent-run"),
			Purpose:       prior.Purpose,
			OwnerKind:     prior.OwnerKind,
			OwnerRef:      prior.OwnerRef,
			InputRevision: prior.InputRevision,
			Input:         prior.Input,
			Model:         prior.Model,
			PromptVersion: prior.PromptVersion,
		}, now.UTC()); err != nil {
			return fmt.Errorf("create replacement roadmap %s run: %w", role, err)
		}
	}
	return nil
}

func lockRoadmapTaskClaimTx(ctx context.Context, tx *Tx, claim roadmap.TaskClaim, now time.Time) (*roadmap.Task, *roadmap.Workflow, error) {
	task, err := scanRoadmapTask(tx.QueryRowContext(ctx, roadmapTaskSelect+` WHERE id = ? AND state = ? AND lease_owner = ?
		AND lease_version = ? AND lease_expires_at > ? FOR UPDATE`, claim.Task.ID, roadmap.TaskRunning,
		claim.LeaseOwner, claim.LeaseVersion, now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, roadmap.ErrLeaseLost
	}
	if err != nil {
		return nil, nil, fmt.Errorf("lock roadmap task claim: %w", err)
	}
	workflow, err := scanRoadmapWorkflow(tx.QueryRowContext(ctx, roadmapWorkflowSelect+` WHERE id = ? FOR UPDATE`, task.WorkflowID))
	if err != nil {
		return nil, nil, fmt.Errorf("lock roadmap task workflow: %w", err)
	}
	if workflow.State != roadmap.WorkflowRunning {
		return nil, nil, roadmap.ErrLeaseLost
	}
	return task, workflow, nil
}

func taskCallsForRole(task roadmap.Task, role roadmap.AgentRole) int {
	switch role {
	case roadmap.AgentPlanner:
		return task.PlannerCalls
	case roadmap.AgentCurriculumReviewer:
		return task.CurriculumCalls
	case roadmap.AgentSREReviewer:
		return task.SRECalls
	default:
		return 0
	}
}

func setTaskCallsTx(ctx context.Context, tx *Tx, taskID string, role roadmap.AgentRole, calls int, now time.Time) error {
	column := ""
	switch role {
	case roadmap.AgentPlanner:
		column = "planner_calls"
	case roadmap.AgentCurriculumReviewer:
		column = "curriculum_calls"
	case roadmap.AgentSREReviewer:
		column = "sre_calls"
	default:
		return errors.New("invalid roadmap task agent role")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE roadmap_tasks SET `+column+` = ?, updated_at = ? WHERE id = ?`, calls, now.UTC(), taskID); err != nil {
		return fmt.Errorf("increment roadmap %s calls: %w", role, err)
	}
	return nil
}

func completeRoadmapTaskAgentRunTx(ctx context.Context, tx *Tx, runID, taskID string, role roadmap.AgentRole, now time.Time) error {
	if _, err := lockRoadmapTaskAgentRunTx(ctx, tx, runID, taskID, role); err != nil {
		return err
	}
	return completeRunTx(ctx, tx, runID, now.UTC())
}

// ClaimRoadmapWorkflowPublication acquires the one workflow-level lease only
// after every fixed task has reached Accepted or Failed. Task failures are
// carried into the publish transaction as pending source entries.
func (d *RoadmapRepository) ClaimRoadmapWorkflowPublication(ctx context.Context, workerID string, leaseTTL time.Duration, now time.Time) (*roadmap.WorkflowClaim, error) {
	if strings.TrimSpace(workerID) == "" || leaseTTL <= 0 || now.IsZero() {
		return nil, errors.New("roadmap publication claim requires server identity, lease ttl, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin roadmap publication claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now = now.UTC()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT workflow.id FROM roadmap_workflows workflow
		WHERE workflow.state IN (?, ?) AND workflow.next_run_at <= ?
			AND (workflow.lease_expires_at IS NULL OR workflow.lease_expires_at <= ?)
			AND NOT EXISTS (SELECT 1 FROM roadmap_tasks task WHERE task.workflow_id = workflow.id AND task.state NOT IN (?, ?))
		ORDER BY workflow.created_at, workflow.id FOR UPDATE SKIP LOCKED LIMIT 1`,
		roadmap.WorkflowRunning, roadmap.WorkflowPublishing, now, now, roadmap.TaskAccepted, roadmap.TaskFailed).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select roadmap publication claim: %w", err)
	}
	workflow, err := scanRoadmapWorkflow(tx.QueryRowContext(ctx, roadmapWorkflowSelect+` WHERE id = ? FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	owner := roadmap.NewLeaseOwner(workerID)
	leaseVersion := workflow.LeaseVersion + 1
	expires := now.Add(leaseTTL)
	updated, err := scanRoadmapWorkflow(tx.QueryRowContext(ctx, `UPDATE roadmap_workflows SET state = ?, lease_owner = ?, lease_version = ?,
		lease_expires_at = ?, updated_at = ? WHERE id = ? RETURNING `+roadmapWorkflowColumns,
		roadmap.WorkflowPublishing, owner, leaseVersion, expires, now, workflow.ID))
	if err != nil {
		return nil, fmt.Errorf("claim roadmap publication: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit roadmap publication claim: %w", err)
	}
	return &roadmap.WorkflowClaim{Workflow: *updated, LeaseCredential: roadmap.LeaseCredential{LeaseOwner: owner, LeaseVersion: leaseVersion}}, nil
}

func (d *RoadmapRepository) RenewRoadmapWorkflowLease(ctx context.Context, claim roadmap.WorkflowClaim, leaseTTL time.Duration, now time.Time) error {
	if !claim.Valid() || leaseTTL <= 0 || now.IsZero() {
		return errors.New("roadmap workflow lease renewal is invalid")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE roadmap_workflows SET lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND state = ? AND lease_owner = ? AND lease_version = ? AND lease_expires_at > ?`,
		now.UTC().Add(leaseTTL), now.UTC(), claim.Workflow.ID, roadmap.WorkflowPublishing, claim.LeaseOwner, claim.LeaseVersion, now.UTC())
	if err != nil {
		return fmt.Errorf("renew roadmap workflow lease: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return roadmap.ErrLeaseLost
	}
	return nil
}

// CompleteRoadmapWorkflow computes and publishes both graph deltas beneath
// the Roadmap write lock. It never reuses the task completion order: merge
// ordering is persisted in each task's snapshot_order.
func (d *RoadmapRepository) CompleteRoadmapWorkflow(ctx context.Context, claim roadmap.WorkflowClaim, now time.Time) (*roadmap.Revision, error) {
	if !claim.Valid() || now.IsZero() {
		return nil, errors.New("roadmap workflow completion is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin roadmap workflow completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockRoadmapWorkflowClaimTx(ctx, tx, claim, now.UTC())
	if err != nil {
		return nil, err
	}
	current, err := currentRoadmapForUpdateTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if current.Revision != workflow.BaseRevision {
		return nil, fmt.Errorf("%w: current revision changed while roadmap workflow was active", roadmap.ErrLeaseLost)
	}
	tasks, err := listRoadmapTasksForUpdateTx(ctx, tx, workflow.ID)
	if err != nil {
		return nil, err
	}
	topicChanges := make([]roadmap.TaskChangeSet, 0)
	challengeChanges := make([]roadmap.TaskChangeSet, 0)
	accepted := make(map[string]roadmap.TaskState, len(tasks))
	for _, task := range tasks {
		if !task.State.Terminal() {
			return nil, roadmap.ErrLeaseLost
		}
		accepted[task.EntryChallengeID+"\x00"+string(task.Kind)] = task.State
		if task.State != roadmap.TaskAccepted || task.ChangeSet == nil {
			continue
		}
		if err := task.ChangeSet.ValidateFor(task.Kind, task.Subject, *current); err != nil {
			return nil, fmt.Errorf("accepted roadmap task %q no longer validates: %w", task.ID, err)
		}
		value := roadmap.TaskChangeSet{TaskID: task.ID, SnapshotOrder: task.SnapshotOrder, ChangeSet: task.ChangeSet.Clone()}
		if task.Kind == roadmap.TaskTopic {
			topicChanges = append(topicChanges, value)
		} else {
			challengeChanges = append(challengeChanges, value)
		}
	}
	next := current.Clone()
	next.Revision = ""
	topicEdges, topicAudits, err := roadmap.MergeEdges(current.TopicEdges, topicChanges)
	if err != nil {
		return nil, fmt.Errorf("merge roadmap topic edges: %w", err)
	}
	challengeEdges, challengeAudits, err := roadmap.MergeEdges(current.ChallengeEdges, challengeChanges)
	if err != nil {
		return nil, fmt.Errorf("merge roadmap challenge edges: %w", err)
	}
	next.TopicEdges = topicEdges
	next.ChallengeEdges = challengeEdges
	canonical, encoded, revisionID, err := canonicalRoadmap(next)
	if err != nil {
		return nil, fmt.Errorf("validate merged roadmap revision: %w", err)
	}
	if revisionID != current.Revision {
		if _, err := tx.ExecContext(ctx, `INSERT INTO roadmap_revisions (id, content_json, created_at) VALUES (?, ?::jsonb, ?)
			ON CONFLICT (id) DO NOTHING`, revisionID, encoded, now.UTC()); err != nil {
			return nil, fmt.Errorf("store merged roadmap revision: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE roadmap_current SET revision_id = ?, updated_at = ? WHERE singleton = TRUE`, revisionID, now.UTC()); err != nil {
			return nil, fmt.Errorf("publish merged roadmap revision: %w", err)
		}
	}
	if err := persistRoadmapMergeAuditsTx(ctx, tx, workflow.ID, roadmap.TaskTopic, topicAudits, now.UTC()); err != nil {
		return nil, err
	}
	if err := persistRoadmapMergeAuditsTx(ctx, tx, workflow.ID, roadmap.TaskChallenge, challengeAudits, now.UTC()); err != nil {
		return nil, err
	}
	if err := markRoadmapWorkflowEntriesProcessedTx(ctx, tx, workflow.ID, accepted); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE roadmap_workflows SET state = ?, lease_owner = '', lease_expires_at = NULL,
		last_error = '', updated_at = ? WHERE id = ?`, roadmap.WorkflowCompleted, now.UTC(), workflow.ID); err != nil {
		return nil, fmt.Errorf("complete roadmap workflow: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit roadmap workflow completion: %w", err)
	}
	canonical.Revision = revisionID
	return &canonical, nil
}

// ReportRoadmapWorkflowInfrastructureFailure releases only the publication
// lease. Task results stay intact and Server retries the same fixed snapshot
// with bounded backoff; the Roadmap barrier remains held until publication
// eventually succeeds or Server stops.
func (d *RoadmapRepository) ReportRoadmapWorkflowInfrastructureFailure(ctx context.Context, claim roadmap.WorkflowClaim, message string, now time.Time) (*roadmap.Workflow, error) {
	if !claim.Valid() || strings.TrimSpace(message) == "" || now.IsZero() {
		return nil, errors.New("roadmap workflow infrastructure failure is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin roadmap workflow infrastructure failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockRoadmapWorkflowClaimTx(ctx, tx, claim, now.UTC())
	if err != nil {
		return nil, err
	}
	attempt := workflow.PublishAttempts + 1
	state := roadmap.WorkflowRunning
	nextRun := roadmap.RetryAt(attempt, now.UTC())
	updated, err := scanRoadmapWorkflow(tx.QueryRowContext(ctx, `UPDATE roadmap_workflows SET state = ?, publish_attempt = ?, lease_owner = '',
		lease_expires_at = NULL, next_run_at = ?, last_error = ?, updated_at = ? WHERE id = ? RETURNING `+roadmapWorkflowColumns,
		state, attempt, nextRun, strings.TrimSpace(message), now.UTC(), workflow.ID))
	if err != nil {
		return nil, fmt.Errorf("record roadmap workflow infrastructure failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func (d *RoadmapRepository) RoadmapMergeAudits(ctx context.Context, workflowID string) ([]roadmap.MergeAudit, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT task_id, edge_json, outcome, reason FROM roadmap_edge_audits
		WHERE workflow_id = ? ORDER BY kind, id`, strings.TrimSpace(workflowID))
	if err != nil {
		return nil, fmt.Errorf("list roadmap merge audits: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]roadmap.MergeAudit, 0)
	for rows.Next() {
		var taskID sql.NullString
		var encoded []byte
		var audit roadmap.MergeAudit
		if err := rows.Scan(&taskID, &encoded, &audit.Outcome, &audit.Reason); err != nil {
			return nil, fmt.Errorf("scan roadmap merge audit: %w", err)
		}
		if taskID.Valid {
			audit.TaskID = taskID.String
		}
		if err := json.Unmarshal(encoded, &audit.Edge); err != nil {
			return nil, fmt.Errorf("decode roadmap merge audit edge: %w", err)
		}
		result = append(result, audit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate roadmap merge audits: %w", err)
	}
	return result, nil
}

func lockRoadmapWorkflowClaimTx(ctx context.Context, tx *Tx, claim roadmap.WorkflowClaim, now time.Time) (*roadmap.Workflow, error) {
	workflow, err := scanRoadmapWorkflow(tx.QueryRowContext(ctx, roadmapWorkflowSelect+` WHERE id = ? AND state = ? AND lease_owner = ?
		AND lease_version = ? AND lease_expires_at > ? FOR UPDATE`, claim.Workflow.ID, roadmap.WorkflowPublishing,
		claim.LeaseOwner, claim.LeaseVersion, now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, roadmap.ErrLeaseLost
	}
	if err != nil {
		return nil, fmt.Errorf("lock roadmap workflow claim: %w", err)
	}
	return workflow, nil
}

func listRoadmapTasksForUpdateTx(ctx context.Context, tx *Tx, workflowID string) ([]roadmap.Task, error) {
	rows, err := tx.QueryContext(ctx, roadmapTaskSelect+` WHERE workflow_id = ? ORDER BY snapshot_order, kind, id FOR UPDATE`, workflowID)
	if err != nil {
		return nil, fmt.Errorf("list roadmap workflow tasks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]roadmap.Task, 0)
	for rows.Next() {
		task, scanErr := scanRoadmapTask(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, *task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate roadmap workflow tasks: %w", err)
	}
	return result, nil
}

func persistRoadmapMergeAuditsTx(ctx context.Context, tx *Tx, workflowID string, kind roadmap.TaskKind, values []roadmap.MergeAudit, now time.Time) error {
	for _, audit := range values {
		encoded, err := marshalJSON(audit.Edge)
		if err != nil {
			return err
		}
		var taskID any
		if strings.TrimSpace(audit.TaskID) != "" {
			taskID = audit.TaskID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO roadmap_edge_audits (workflow_id, task_id, kind, edge_json, outcome, reason, created_at)
			VALUES (?, ?, ?, ?::jsonb, ?, ?, ?)`, workflowID, taskID, kind, encoded, audit.Outcome, audit.Reason, now.UTC()); err != nil {
			return fmt.Errorf("store roadmap merge audit: %w", err)
		}
	}
	return nil
}

func markRoadmapWorkflowEntriesProcessedTx(ctx context.Context, tx *Tx, workflowID string, states map[string]roadmap.TaskState) error {
	rows, err := tx.QueryContext(ctx, `SELECT challenge_id, topic_required, challenge_required FROM roadmap_workflow_entries
		WHERE workflow_id = ? ORDER BY snapshot_order, challenge_id FOR UPDATE`, workflowID)
	if err != nil {
		return fmt.Errorf("list workflow entries for completion: %w", err)
	}
	type workflowEntryResult struct {
		challengeID       string
		topicRequired     bool
		challengeRequired bool
	}
	entries := make([]workflowEntryResult, 0)
	for rows.Next() {
		var value workflowEntryResult
		if err := rows.Scan(&value.challengeID, &value.topicRequired, &value.challengeRequired); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan workflow entry for completion: %w", err)
		}
		entries = append(entries, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate workflow entries for completion: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close workflow entries for completion: %w", err)
	}
	for _, entry := range entries {
		if entry.topicRequired && states[entry.challengeID+"\x00"+string(roadmap.TaskTopic)] == roadmap.TaskAccepted {
			if _, err := tx.ExecContext(ctx, `UPDATE roadmap_entries SET topic_processed = TRUE WHERE challenge_id = ?`, entry.challengeID); err != nil {
				return fmt.Errorf("mark roadmap topic entry processed: %w", err)
			}
		}
		if entry.challengeRequired && states[entry.challengeID+"\x00"+string(roadmap.TaskChallenge)] == roadmap.TaskAccepted {
			if _, err := tx.ExecContext(ctx, `UPDATE roadmap_entries SET challenge_processed = TRUE WHERE challenge_id = ?`, entry.challengeID); err != nil {
				return fmt.Errorf("mark roadmap challenge entry processed: %w", err)
			}
		}
	}
	return nil
}

func scanRoadmapWorkflow(row scanner) (*roadmap.Workflow, error) {
	var value roadmap.Workflow
	var leaseExpiresAt sql.NullTime
	if err := row.Scan(
		&value.ID,
		&value.BaseRevision,
		&value.State,
		&value.PublishAttempts,
		&value.LeaseOwner,
		&value.LeaseVersion,
		&leaseExpiresAt,
		&value.NextRunAt,
		&value.LastError,
		&value.CreatedAt,
		&value.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if leaseExpiresAt.Valid {
		at := leaseExpiresAt.Time.UTC()
		value.LeaseExpiresAt = &at
	}
	value.NextRunAt = value.NextRunAt.UTC()
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	if !value.Valid() {
		return nil, errors.New("stored roadmap workflow is invalid")
	}
	return &value, nil
}

func scanRoadmapTask(row scanner) (*roadmap.Task, error) {
	var value roadmap.Task
	var changeset, curriculumReview, sreReview []byte
	var leaseExpiresAt sql.NullTime
	if err := row.Scan(
		&value.ID,
		&value.WorkflowID,
		&value.EntryChallengeID,
		&value.SnapshotOrder,
		&value.Kind,
		&value.Subject.Ref.ID,
		&value.Subject.Ref.SourceRef,
		&value.Subject.Ref.Title,
		&value.Subject.ContentRevision,
		&value.State,
		&value.Round,
		&value.PlannerCalls,
		&value.CurriculumCalls,
		&value.SRECalls,
		&changeset,
		&curriculumReview,
		&sreReview,
		&value.LeaseOwner,
		&value.LeaseVersion,
		&leaseExpiresAt,
		&value.NextRunAt,
		&value.LastError,
		&value.CreatedAt,
		&value.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(changeset) > 0 && string(changeset) != "null" {
		var candidate roadmap.ChangeSet
		if err := json.Unmarshal(changeset, &candidate); err != nil {
			return nil, fmt.Errorf("decode roadmap task changeset: %w", err)
		}
		value.ChangeSet = &candidate
	}
	if len(curriculumReview) > 0 && string(curriculumReview) != "null" {
		var review roadmap.Review
		if err := json.Unmarshal(curriculumReview, &review); err != nil {
			return nil, fmt.Errorf("decode roadmap task curriculum review: %w", err)
		}
		if err := review.Validate(); err != nil {
			return nil, fmt.Errorf("validate roadmap task curriculum review: %w", err)
		}
		value.CurriculumReview = &review
	}
	if len(sreReview) > 0 && string(sreReview) != "null" {
		var review roadmap.Review
		if err := json.Unmarshal(sreReview, &review); err != nil {
			return nil, fmt.Errorf("decode roadmap task SRE review: %w", err)
		}
		if err := review.Validate(); err != nil {
			return nil, fmt.Errorf("validate roadmap task SRE review: %w", err)
		}
		value.SREReview = &review
	}
	if leaseExpiresAt.Valid {
		at := leaseExpiresAt.Time.UTC()
		value.LeaseExpiresAt = &at
	}
	value.NextRunAt = value.NextRunAt.UTC()
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	if !value.Valid() {
		return nil, errors.New("stored roadmap task is invalid")
	}
	return &value, nil
}
