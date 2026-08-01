package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/taxonomy"
)

var ErrTaxonomyWorkflowNotFound = errors.New("taxonomy workflow not found")

const taxonomyWorkflowColumns = `id, challenge_id, challenge_revision, state, base_revision, round, state_attempt,
	candidate_changeset, curriculum_review_json, sre_review_json, expected_snapshot_revision, published_revision,
	lease_owner, lease_expires_at, next_run_at, last_error, created_at, updated_at`
const taxonomyWorkflowSelect = `SELECT ` + taxonomyWorkflowColumns + ` FROM taxonomy_workflows`

func (d *DB) CreateOrGetTaxonomyWorkflow(ctx context.Context, challengeID, challengeRevision, baseRevision string, now time.Time) (*taxonomy.Workflow, bool, error) {
	if strings.TrimSpace(challengeID) == "" || strings.TrimSpace(challengeRevision) == "" || now.IsZero() {
		return nil, false, errors.New("taxonomy workflow requires challenge identity and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin taxonomy workflow creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, created, err := createOrGetTaxonomyWorkflowTx(ctx, tx, challengeID, challengeRevision, baseRevision, now)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit taxonomy workflow creation: %w", err)
	}
	return workflow, created, nil
}

// createOrGetTaxonomyWorkflowTx is shared by catalog recovery and final
// challenge publication, so a published authoring session and its taxonomy
// work item become visible in one database transaction.
func createOrGetTaxonomyWorkflowTx(ctx context.Context, tx *Tx, challengeID, challengeRevision, baseRevision string, now time.Time) (*taxonomy.Workflow, bool, error) {
	if tx == nil || strings.TrimSpace(challengeID) == "" || strings.TrimSpace(challengeRevision) == "" || now.IsZero() {
		return nil, false, errors.New("taxonomy workflow requires transaction, challenge identity, and current time")
	}
	workflow := taxonomy.Workflow{
		ID:                   taxonomy.NewWorkflowID(),
		ChallengeID:          challengeID,
		ChallengeRevision:    challengeRevision,
		State:                taxonomy.WorkflowQueued,
		BaseTaxonomyRevision: baseRevision,
		NextRunAt:            now.UTC(),
		CreatedAt:            now.UTC(),
		UpdatedAt:            now.UTC(),
	}
	row := tx.QueryRowContext(ctx, `INSERT INTO taxonomy_workflows
		(id, challenge_id, challenge_revision, base_revision, state, round, state_attempt, lease_owner, next_run_at, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, 0, '', ?, '', ?, ?)
		ON CONFLICT (challenge_id, challenge_revision) DO UPDATE SET updated_at = taxonomy_workflows.updated_at
		RETURNING `+taxonomyWorkflowColumns, workflow.ID, workflow.ChallengeID, workflow.ChallengeRevision, workflow.BaseTaxonomyRevision,
		workflow.State, workflow.NextRunAt, workflow.CreatedAt, workflow.UpdatedAt)
	created, err := scanTaxonomyWorkflow(row)
	if err != nil {
		return nil, false, fmt.Errorf("create taxonomy workflow: %w", err)
	}
	return created, created.ID == workflow.ID, nil
}

func (d *DB) GetTaxonomyWorkflow(ctx context.Context, id string) (*taxonomy.Workflow, error) {
	workflow, err := scanTaxonomyWorkflow(d.conn.QueryRowContext(ctx, taxonomyWorkflowSelect+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaxonomyWorkflowNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get taxonomy workflow: %w", err)
	}
	return workflow, nil
}

func (d *DB) GetTaxonomyWorkflowByChallenge(ctx context.Context, challengeID, challengeRevision string) (*taxonomy.Workflow, error) {
	workflow, err := scanTaxonomyWorkflow(d.conn.QueryRowContext(ctx, taxonomyWorkflowSelect+` WHERE challenge_id = ? AND challenge_revision = ?`,
		strings.TrimSpace(challengeID), strings.TrimSpace(challengeRevision)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaxonomyWorkflowNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get taxonomy workflow by challenge: %w", err)
	}
	return workflow, nil
}

func (d *DB) ClaimTaxonomyWorkflow(ctx context.Context, workerID string, leaseTTL time.Duration, now time.Time) (*taxonomy.Claim, error) {
	if strings.TrimSpace(workerID) == "" || leaseTTL <= 0 || now.IsZero() {
		return nil, errors.New("taxonomy workflow claim requires worker, lease ttl, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin taxonomy claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now = now.UTC()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM taxonomy_workflows
		WHERE state NOT IN (?, ?, ?) AND next_run_at <= ? AND (lease_expires_at IS NULL OR lease_expires_at <= ?)
		ORDER BY next_run_at, created_at, id FOR UPDATE SKIP LOCKED LIMIT 1`,
		taxonomy.WorkflowCompleted, taxonomy.WorkflowFailed, taxonomy.WorkflowCancelled, now, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty taxonomy claim: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select taxonomy workflow: %w", err)
	}
	workflow, err := scanTaxonomyWorkflow(tx.QueryRowContext(ctx, taxonomyWorkflowSelect+` WHERE id = ? FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	// A prior worker can die after creating a model-call record. Those calls
	// cannot be resumed under a new lease, so preserve their history as failed
	// before another worker starts the same workflow state.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
		WHERE owner_kind = ? AND owner_ref = ? AND status = ?`, agentruntime.RunFailed,
		"taxonomy worker lease replaced before agent call completed", now, now, "taxonomy-workflow", workflow.ID, agentruntime.RunRunning); err != nil {
		return nil, fmt.Errorf("abandon prior taxonomy agent runs: %w", err)
	}
	state := workflow.State
	if state == taxonomy.WorkflowQueued {
		state = taxonomy.WorkflowMapping
	}
	stateAttempt := workflow.StateAttempt
	if stateAttempt >= 10 && workflow.LeaseOwner == "" {
		stateAttempt = 0
	}
	owner := strings.TrimSpace(workerID) + "-" + taxonomy.NewWorkflowID()
	expires := now.Add(leaseTTL)
	workflow, err = scanTaxonomyWorkflow(tx.QueryRowContext(ctx, `UPDATE taxonomy_workflows SET state = ?, state_attempt = ?, lease_owner = ?, lease_expires_at = ?, updated_at = ?
		WHERE id = ? RETURNING `+taxonomyWorkflowColumns, state, stateAttempt, owner, expires, now, workflow.ID))
	if err != nil {
		return nil, fmt.Errorf("claim taxonomy workflow: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit taxonomy claim: %w", err)
	}
	return &taxonomy.Claim{Workflow: *workflow, LeaseCredential: taxonomy.LeaseCredential{StateAttempt: workflow.StateAttempt, LeaseOwner: owner}}, nil
}

// RefreshTaxonomyClaim returns the current lease-fenced workflow after a
// phase report. Technical retries increment state_attempt, so callers must
// always use this fresh credential for the next report.
func (d *DB) RefreshTaxonomyClaim(ctx context.Context, workflowID, leaseOwner string, now time.Time) (*taxonomy.Claim, error) {
	if strings.TrimSpace(workflowID) == "" || strings.TrimSpace(leaseOwner) == "" || now.IsZero() {
		return nil, errors.New("taxonomy workflow identity and lease owner are required")
	}
	workflow, err := scanTaxonomyWorkflow(d.conn.QueryRowContext(ctx, taxonomyWorkflowSelect+` WHERE id = ? AND lease_owner = ? AND lease_expires_at > ?`,
		workflowID, leaseOwner, now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, taxonomyLeaseLost()
	}
	if err != nil {
		return nil, fmt.Errorf("refresh taxonomy workflow claim: %w", err)
	}
	return &taxonomy.Claim{Workflow: *workflow, LeaseCredential: taxonomy.LeaseCredential{
		StateAttempt: workflow.StateAttempt,
		LeaseOwner:   workflow.LeaseOwner,
	}}, nil
}

func (d *DB) GetTaxonomyClaim(ctx context.Context, id string, credential taxonomy.LeaseCredential, now time.Time) (*taxonomy.Claim, error) {
	if strings.TrimSpace(id) == "" || !credential.Valid() || now.IsZero() {
		return nil, errors.New("taxonomy workflow lease credentials are required")
	}
	workflow, err := scanTaxonomyWorkflow(d.conn.QueryRowContext(ctx, taxonomyWorkflowSelect+` WHERE id = ? AND state_attempt = ?
		AND lease_owner = ? AND lease_expires_at > ?`, id, credential.StateAttempt, credential.LeaseOwner, now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, taxonomyLeaseLost()
	}
	if err != nil {
		return nil, fmt.Errorf("get taxonomy workflow lease: %w", err)
	}
	return &taxonomy.Claim{Workflow: *workflow, LeaseCredential: credential}, nil
}

func (d *DB) RenewTaxonomyLease(ctx context.Context, claim taxonomy.Claim, leaseTTL time.Duration, now time.Time) error {
	if !claim.Valid() || leaseTTL <= 0 || now.IsZero() {
		return errors.New("taxonomy workflow lease renewal is invalid")
	}
	// A successful phase keeps the lease owner but resets state_attempt. Renewal
	// therefore fences only on the current lease identity; phase reports retain
	// the attempt fence that rejects stale results.
	result, err := d.conn.ExecContext(ctx, `UPDATE taxonomy_workflows SET lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND lease_owner = ? AND lease_expires_at > ?`,
		now.UTC().Add(leaseTTL), now.UTC(), claim.Workflow.ID, claim.LeaseOwner, now.UTC())
	if err != nil {
		return fmt.Errorf("renew taxonomy workflow lease: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return taxonomyLeaseLost()
	}
	return nil
}

func (d *DB) StartTaxonomyAgentRun(ctx context.Context, claim taxonomy.Claim, role taxonomy.AgentRole, model string, now time.Time) (*agentruntime.Run, error) {
	if !claim.Valid() || !role.Valid() || strings.TrimSpace(model) == "" || now.IsZero() {
		return nil, errors.New("taxonomy agent run is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin taxonomy agent run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockTaxonomyClaimTx(ctx, tx, claim, role.State(), now.UTC())
	if err != nil {
		return nil, err
	}
	input, err := marshalJSON(struct {
		Role         taxonomy.AgentRole `json:"role"`
		Round        int                `json:"round"`
		StateAttempt int                `json:"state_attempt"`
	}{Role: role, Round: workflow.Round, StateAttempt: workflow.StateAttempt})
	if err != nil {
		return nil, err
	}
	created, err := createRunTx(ctx, tx, agentruntime.CreateRun{
		ID:            agentruntime.NewID("taxonomy-agent-run"),
		Purpose:       role.Purpose(),
		OwnerKind:     "taxonomy-workflow",
		OwnerRef:      workflow.ID,
		InputRevision: workflow.BaseTaxonomyRevision,
		Input:         []byte(input),
		Model:         model,
		PromptVersion: role.PromptVersion(),
	}, now.UTC())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit taxonomy agent run: %w", err)
	}
	return created, nil
}

func (d *DB) FinalizeTaxonomyMapper(ctx context.Context, claim taxonomy.Claim, runID string, changes taxonomy.ChangeSet, now time.Time) error {
	if !claim.Valid() || strings.TrimSpace(runID) == "" || changes.Empty() || now.IsZero() {
		return errors.New("taxonomy mapper finalization is invalid")
	}
	encoded, err := marshalJSON(changes)
	if err != nil {
		return err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin taxonomy mapper finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockTaxonomyClaimTx(ctx, tx, claim, taxonomy.WorkflowMapping, now.UTC())
	if err != nil {
		return err
	}
	if err := completeTaxonomyRunTx(ctx, tx, runID, workflow.ID, taxonomy.AgentRoleMapper, now.UTC()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE taxonomy_workflows SET state = ?, candidate_changeset = ?::jsonb,
		curriculum_review_json = NULL, sre_review_json = NULL, state_attempt = 0, last_error = '', next_run_at = ?, updated_at = ? WHERE id = ?`,
		taxonomy.WorkflowReviewing, encoded, now.UTC(), now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("record taxonomy mapper result: %w", err)
	}
	return tx.Commit()
}

func (d *DB) FinalizeTaxonomyReviewPair(ctx context.Context, claim taxonomy.Claim, curriculumRunID, sreRunID string, curriculum, sre taxonomy.Review, latestBaseRevision string, now time.Time) error {
	if !claim.Valid() || strings.TrimSpace(curriculumRunID) == "" || strings.TrimSpace(sreRunID) == "" || curriculumRunID == sreRunID || taxonomy.ValidateReview(curriculum) != nil || taxonomy.ValidateReview(sre) != nil || now.IsZero() {
		return errors.New("taxonomy reviewer finalization is invalid")
	}
	curriculumJSON, err := marshalJSON(curriculum)
	if err != nil {
		return err
	}
	sreJSON, err := marshalJSON(sre)
	if err != nil {
		return err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin taxonomy reviewer finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockTaxonomyClaimTx(ctx, tx, claim, taxonomy.WorkflowReviewing, now.UTC())
	if err != nil {
		return err
	}
	if workflow.CandidateChangeSet == nil {
		return errors.New("taxonomy review has no mapper changeset")
	}
	if err := completeTaxonomyRunTx(ctx, tx, curriculumRunID, workflow.ID, taxonomy.AgentRoleCurriculumReviewer, now.UTC()); err != nil {
		return err
	}
	if err := completeTaxonomyRunTx(ctx, tx, sreRunID, workflow.ID, taxonomy.AgentRoleSREReviewer, now.UTC()); err != nil {
		return err
	}
	state := taxonomy.WorkflowPublishing
	round := workflow.Round
	leaseOwner := workflow.LeaseOwner
	leaseExpiresAt := workflow.LeaseExpiresAt
	baseRevision := workflow.BaseTaxonomyRevision
	if curriculum.Decision == taxonomy.ReviewReject || sre.Decision == taxonomy.ReviewReject {
		state = taxonomy.WorkflowMapping
		round++
		leaseOwner = ""
		leaseExpiresAt = nil
		baseRevision = latestBaseRevision
	}
	if _, err := tx.ExecContext(ctx, `UPDATE taxonomy_workflows SET state = ?, base_revision = ?, round = ?, state_attempt = 0,
		curriculum_review_json = ?::jsonb, sre_review_json = ?::jsonb, lease_owner = ?, lease_expires_at = ?, next_run_at = ?, last_error = '', updated_at = ? WHERE id = ?`,
		state, baseRevision, round, curriculumJSON, sreJSON, leaseOwner, leaseExpiresAt, now.UTC(), now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("record taxonomy review pair: %w", err)
	}
	return tx.Commit()
}

func (d *DB) ResetTaxonomyForLatest(ctx context.Context, claim taxonomy.Claim, baseRevision, reason string, now time.Time) error {
	if !claim.Valid() || strings.TrimSpace(reason) == "" || now.IsZero() {
		return errors.New("taxonomy reset requires claim, reason, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin taxonomy reset: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockTaxonomyClaimTx(ctx, tx, claim, taxonomy.WorkflowPublishing, now.UTC())
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE taxonomy_workflows SET state = ?, base_revision = ?, round = round + 1, state_attempt = 0,
		candidate_changeset = NULL, curriculum_review_json = NULL, sre_review_json = NULL, expected_snapshot_revision = '',
		lease_owner = '', lease_expires_at = NULL, next_run_at = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		taxonomy.WorkflowMapping, baseRevision, now.UTC(), reason, now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("reset taxonomy workflow: %w", err)
	}
	return tx.Commit()
}

func (d *DB) SetTaxonomyExpectedSnapshot(ctx context.Context, claim taxonomy.Claim, expected string, now time.Time) error {
	if !claim.Valid() || strings.TrimSpace(expected) == "" || now.IsZero() {
		return errors.New("taxonomy expected snapshot is invalid")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE taxonomy_workflows SET expected_snapshot_revision = ?, updated_at = ?
		WHERE id = ? AND state = ? AND state_attempt = ? AND lease_owner = ? AND lease_expires_at > ?`, expected, now.UTC(),
		claim.Workflow.ID, taxonomy.WorkflowPublishing, claim.StateAttempt, claim.LeaseOwner, now.UTC())
	if err != nil {
		return fmt.Errorf("record expected taxonomy snapshot: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return taxonomyLeaseLost()
	}
	return nil
}

func (d *DB) CompleteTaxonomyPublication(ctx context.Context, claim taxonomy.Claim, revision string, now time.Time) error {
	if !claim.Valid() || strings.TrimSpace(revision) == "" || now.IsZero() {
		return errors.New("taxonomy publication finalization is invalid")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE taxonomy_workflows SET state = ?, published_revision = ?, lease_owner = '', lease_expires_at = NULL,
		last_error = '', updated_at = ? WHERE id = ? AND state = ? AND state_attempt = ? AND lease_owner = ? AND lease_expires_at > ?`,
		taxonomy.WorkflowCompleted, revision, now.UTC(), claim.Workflow.ID, taxonomy.WorkflowPublishing, claim.StateAttempt, claim.LeaseOwner, now.UTC())
	if err != nil {
		return fmt.Errorf("complete taxonomy publication: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return taxonomyLeaseLost()
	}
	return nil
}

// RecordTaxonomyTechnicalFailure leaves the same workflow in its current
// state. The tenth continuous failure releases the lease for a later worker;
// it never turns a recoverable outage into a semantic failure.
func (d *DB) ReportTaxonomyTechnicalFailure(ctx context.Context, claim taxonomy.Claim, expected taxonomy.WorkflowState, message string, runIDs []string, now time.Time) (*taxonomy.Workflow, bool, error) {
	if !claim.Valid() || strings.TrimSpace(message) == "" || now.IsZero() {
		return nil, false, errors.New("taxonomy technical failure is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin taxonomy technical failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockTaxonomyClaimTx(ctx, tx, claim, expected, now.UTC())
	if err != nil {
		return nil, false, err
	}
	seenRuns := make(map[string]struct{}, len(runIDs))
	for _, runID := range runIDs {
		runID = strings.TrimSpace(runID)
		if runID == "" {
			return nil, false, errors.New("taxonomy technical failure contains an empty agent run id")
		}
		if _, duplicate := seenRuns[runID]; duplicate {
			return nil, false, errors.New("taxonomy technical failure repeats an agent run id")
		}
		seenRuns[runID] = struct{}{}
		if err := failTaxonomyRunTx(ctx, tx, runID, workflow.ID, strings.TrimSpace(message), now.UTC()); err != nil {
			return nil, false, err
		}
	}
	nextAttempt := workflow.StateAttempt + 1
	release := nextAttempt >= 10
	leaseOwner := workflow.LeaseOwner
	leaseExpiresAt := workflow.LeaseExpiresAt
	nextRun := now.UTC()
	if release {
		leaseOwner = ""
		leaseExpiresAt = nil
		nextRun = taxonomy.RetryAt(nextAttempt, now)
	}
	updated, err := scanTaxonomyWorkflow(tx.QueryRowContext(ctx, `UPDATE taxonomy_workflows SET state_attempt = ?, lease_owner = ?, lease_expires_at = ?,
		next_run_at = ?, last_error = ?, updated_at = ? WHERE id = ? RETURNING `+taxonomyWorkflowColumns,
		nextAttempt, leaseOwner, leaseExpiresAt, nextRun, message, now.UTC(), workflow.ID))
	if err != nil {
		return nil, false, fmt.Errorf("record taxonomy technical failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return updated, release, nil
}

func completeTaxonomyRunTx(ctx context.Context, tx *Tx, runID, workflowID string, role taxonomy.AgentRole, now time.Time) error {
	var purpose, ownerKind, ownerRef string
	var status agentruntime.RunStatus
	err := tx.QueryRowContext(ctx, `SELECT purpose, owner_kind, owner_ref, status FROM agent_runs WHERE id = ? FOR UPDATE`, runID).Scan(&purpose, &ownerKind, &ownerRef, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return agentruntime.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load taxonomy agent run: %w", err)
	}
	if purpose != role.Purpose() || ownerKind != "taxonomy-workflow" || ownerRef != workflowID || status != agentruntime.RunRunning {
		return taxonomyLeaseLost()
	}
	return completeRunTx(ctx, tx, runID, now)
}

func failTaxonomyRunTx(ctx context.Context, tx *Tx, runID, workflowID, message string, now time.Time) error {
	var ownerKind, ownerRef string
	var status agentruntime.RunStatus
	err := tx.QueryRowContext(ctx, `SELECT owner_kind, owner_ref, status FROM agent_runs WHERE id = ? FOR UPDATE`, runID).Scan(&ownerKind, &ownerRef, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return agentruntime.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load taxonomy agent run: %w", err)
	}
	if ownerKind != "taxonomy-workflow" || ownerRef != workflowID || status != agentruntime.RunRunning {
		return taxonomyLeaseLost()
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ? WHERE id = ? AND status = ?`,
		agentruntime.RunFailed, message, now, now, runID, agentruntime.RunRunning)
	if err != nil {
		return fmt.Errorf("fail taxonomy agent run: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return taxonomyLeaseLost()
	}
	return nil
}

func (d *DB) CancelTaxonomyWorkflow(ctx context.Context, id, reason string, now time.Time) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(reason) == "" || now.IsZero() {
		return errors.New("taxonomy cancellation is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin taxonomy cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE taxonomy_workflows SET state = ?, lease_owner = '', lease_expires_at = NULL,
		last_error = ?, updated_at = ? WHERE id = ? AND state NOT IN (?, ?, ?)`, taxonomy.WorkflowCancelled, reason, now.UTC(), id,
		taxonomy.WorkflowCompleted, taxonomy.WorkflowFailed, taxonomy.WorkflowCancelled)
	if err != nil {
		return fmt.Errorf("cancel taxonomy workflow: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrTaxonomyWorkflowNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
		WHERE owner_kind = ? AND owner_ref = ? AND status = ?`, agentruntime.RunCancelled, reason, now.UTC(), now.UTC(),
		"taxonomy-workflow", id, agentruntime.RunRunning); err != nil {
		return fmt.Errorf("cancel taxonomy agent runs: %w", err)
	}
	return tx.Commit()
}

func (d *DB) ListActiveTaxonomyWorkflows(ctx context.Context) ([]taxonomy.Workflow, error) {
	rows, err := d.conn.QueryContext(ctx, taxonomyWorkflowSelect+` WHERE state NOT IN (?, ?, ?) ORDER BY created_at, id`, taxonomy.WorkflowCompleted, taxonomy.WorkflowFailed, taxonomy.WorkflowCancelled)
	if err != nil {
		return nil, fmt.Errorf("list active taxonomy workflows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]taxonomy.Workflow, 0)
	for rows.Next() {
		workflow, err := scanTaxonomyWorkflow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *workflow)
	}
	return result, rows.Err()
}

func (d *DB) ListPublishingTaxonomyWorkflows(ctx context.Context) ([]taxonomy.Workflow, error) {
	rows, err := d.conn.QueryContext(ctx, taxonomyWorkflowSelect+` WHERE state = ? ORDER BY created_at, id`, taxonomy.WorkflowPublishing)
	if err != nil {
		return nil, fmt.Errorf("list publishing taxonomy workflows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]taxonomy.Workflow, 0)
	for rows.Next() {
		workflow, err := scanTaxonomyWorkflow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *workflow)
	}
	return result, rows.Err()
}

// RecoverTaxonomyPublication completes a publication whose immutable snapshot
// is already current after Server crashed between filesystem publication and
// the database completion record.
func (d *DB) RecoverTaxonomyPublication(ctx context.Context, id, expectedRevision string, now time.Time) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(expectedRevision) == "" || now.IsZero() {
		return errors.New("taxonomy publication recovery is invalid")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE taxonomy_workflows SET state = ?, published_revision = ?, lease_owner = '', lease_expires_at = NULL,
		last_error = '', updated_at = ? WHERE id = ? AND state = ? AND expected_snapshot_revision = ?`, taxonomy.WorkflowCompleted,
		expectedRevision, now.UTC(), id, taxonomy.WorkflowPublishing, expectedRevision)
	if err != nil {
		return fmt.Errorf("recover taxonomy publication: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return taxonomyLeaseLost()
	}
	return nil
}

func lockTaxonomyClaimTx(ctx context.Context, tx *Tx, claim taxonomy.Claim, expected taxonomy.WorkflowState, now time.Time) (*taxonomy.Workflow, error) {
	workflow, err := scanTaxonomyWorkflow(tx.QueryRowContext(ctx, taxonomyWorkflowSelect+` WHERE id = ? AND state = ? AND state_attempt = ?
		AND lease_owner = ? AND lease_expires_at > ? FOR UPDATE`, claim.Workflow.ID, expected, claim.StateAttempt, claim.LeaseOwner, now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, taxonomyLeaseLost()
	}
	if err != nil {
		return nil, fmt.Errorf("lock taxonomy workflow lease: %w", err)
	}
	return workflow, nil
}

func taxonomyLeaseLost() error { return taxonomy.ErrLeaseLost }

func scanTaxonomyWorkflow(row agentRow) (*taxonomy.Workflow, error) {
	var workflow taxonomy.Workflow
	var candidateJSON, curriculumJSON, sreJSON []byte
	var leaseExpiresAt sql.NullTime
	err := row.Scan(&workflow.ID, &workflow.ChallengeID, &workflow.ChallengeRevision, &workflow.State, &workflow.BaseTaxonomyRevision,
		&workflow.Round, &workflow.StateAttempt, &candidateJSON, &curriculumJSON, &sreJSON, &workflow.ExpectedSnapshotRevision,
		&workflow.PublishedRevision, &workflow.LeaseOwner, &leaseExpiresAt, &workflow.NextRunAt, &workflow.LastError, &workflow.CreatedAt, &workflow.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if len(candidateJSON) > 0 && string(candidateJSON) != "null" {
		var value taxonomy.ChangeSet
		if err := json.Unmarshal(candidateJSON, &value); err != nil {
			return nil, fmt.Errorf("decode taxonomy changeset: %w", err)
		}
		workflow.CandidateChangeSet = &value
	}
	for _, value := range []struct {
		data   []byte
		target **taxonomy.Review
		name   string
	}{
		{curriculumJSON, &workflow.CurriculumReview, "curriculum review"},
		{sreJSON, &workflow.SREReview, "SRE review"},
	} {
		if len(value.data) == 0 || string(value.data) == "null" {
			continue
		}
		var review taxonomy.Review
		if err := json.Unmarshal(value.data, &review); err != nil {
			return nil, fmt.Errorf("decode taxonomy %s: %w", value.name, err)
		}
		*value.target = &review
	}
	if leaseExpiresAt.Valid {
		value := leaseExpiresAt.Time.UTC()
		workflow.LeaseExpiresAt = &value
	}
	workflow.NextRunAt = workflow.NextRunAt.UTC()
	workflow.CreatedAt = workflow.CreatedAt.UTC()
	workflow.UpdatedAt = workflow.UpdatedAt.UTC()
	return &workflow, nil
}
