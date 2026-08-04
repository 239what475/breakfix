package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
)

var ErrGenerationWorkflowNotFound = errors.New("generation workflow not found")

const generationWorkflowColumns = `id, source_kind, source_ref, source_revision, state, classification_roadmap_revision, classification_feedback,
	COALESCE(superseded_by_workflow_id, ''), COALESCE(candidate_revision_id, ''), COALESCE(active_agent_run_id, ''), state_attempt, lease_owner, lease_expires_at, next_run_at,
	deadline_at, deadline_paused_at, last_error, created_at, updated_at`
const generationWorkflowSelect = `SELECT ` + generationWorkflowColumns + ` FROM generation_workflows`

func (d *GenerationRepository) CreateGenerationWorkflow(ctx context.Context, sessionID, userID string, confirmation generation.StartConfirmation, now time.Time) (*generation.Workflow, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || !confirmation.Valid() || now.IsZero() {
		return nil, errors.New("generation workflow requires authoring session, user, idempotent plan confirmation, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generation workflow: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	session, err := readAuthoringSessionTx(ctx, tx, sessionID, userID)
	if err != nil {
		return nil, err
	}
	if receipt, err := generationConfirmationReceiptTx(ctx, tx, sessionID, confirmationStart, confirmation.IdempotencyKey); err != nil {
		return nil, err
	} else if receipt != nil {
		if receipt.PlanRevision == nil || *receipt.PlanRevision != confirmation.PlanRevision {
			return nil, authoring.ErrVersionConflict
		}
		return generationWorkflowForReceiptTx(ctx, tx, receipt.WorkflowID)
	}
	if session.CurrentRevision != confirmation.PlanRevision || session.State != authoring.StateIntentReview {
		return nil, authoring.ErrInvalidState
	}
	plan, err := readAuthoringRevisionTx(ctx, tx, sessionID, confirmation.PlanRevision)
	if err != nil {
		return nil, err
	}
	if err := plan.Plan.ValidateForGeneration(); err != nil {
		return nil, err
	}
	workflow := generation.Workflow{
		ID:             generation.NewID("generation-workflow"),
		Source:         generation.Source{Kind: generation.SourceAuthoring, Ref: sessionID},
		SourceRevision: strconv.FormatInt(confirmation.PlanRevision, 10),
		State:          generation.StateGenerating,
		NextRunAt:      now.UTC(),
		CreatedAt:      now.UTC(),
		UpdatedAt:      now.UTC(),
	}
	active, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE source_kind = ? AND source_ref = ?
		AND state NOT IN (?, ?, ?, ?) FOR UPDATE`, generation.SourceAuthoring, sessionID,
		generation.StatePublished, generation.StateFailed, generation.StateCancelled, generation.StateSuperseded))
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return nil, err
	case active.SourceRevision == workflow.SourceRevision:
		return nil, authoring.ErrInvalidState
	case !active.State.Review():
		return nil, authoring.ErrInvalidState
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
			WHERE owner_kind = ? AND owner_ref = ? AND status = ?`, agent.RunCancelled, "superseded by a confirmed authoring revision", now.UTC(), now.UTC(),
			"generation-workflow", active.ID, agent.RunRunning); err != nil {
			return nil, fmt.Errorf("cancel superseded generation runs: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, lease_owner = '', lease_expires_at = NULL,
			active_agent_run_id = NULL, deadline_paused_at = NULL, last_error = '', updated_at = ? WHERE id = ?`,
			generation.StateSuperseded, now.UTC(), active.ID); err != nil {
			return nil, fmt.Errorf("supersede prior generation workflow: %w", err)
		}
	}
	if _, err := ensureGeneratorSessionTx(ctx, tx, session); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO generation_workflows
		(id, source_kind, source_ref, source_revision, state, classification_roadmap_revision, classification_feedback, superseded_by_workflow_id,
		candidate_revision_id, active_agent_run_id, state_attempt, lease_owner, next_run_at, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '', '', NULL, NULL, NULL, 0, '', ?, '', ?, ?)`,
		workflow.ID, workflow.Source.Kind, workflow.Source.Ref, workflow.SourceRevision, workflow.State, workflow.NextRunAt, workflow.CreatedAt, workflow.UpdatedAt); err != nil {
		return nil, fmt.Errorf("insert generation workflow: %w", err)
	}
	if active != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET superseded_by_workflow_id = ?, updated_at = ? WHERE id = ?`, workflow.ID, now.UTC(), active.ID); err != nil {
			return nil, fmt.Errorf("record superseding workflow: %w", err)
		}
	}
	if err := insertGenerationConfirmationReceiptTx(ctx, tx, generationConfirmationReceipt{
		SessionID: sessionID, Action: confirmationStart, IdempotencyKey: confirmation.IdempotencyKey,
		WorkflowID: workflow.ID, PlanRevision: &confirmation.PlanRevision, CreatedAt: now.UTC(),
	}); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET last_error = '', updated_at = ? WHERE id = ?`, nowText(now), sessionID); err != nil {
		return nil, fmt.Errorf("touch generation authoring session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generation workflow: %w", err)
	}
	return &workflow, nil
}

func (d *GenerationRepository) GetGenerationWorkflow(ctx context.Context, id string) (*generation.Workflow, error) {
	workflow, err := scanGenerationWorkflow(d.conn.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGenerationWorkflowNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get generation workflow: %w", err)
	}
	return workflow, nil
}

func (d *GenerationRepository) GetActiveGenerationWorkflow(ctx context.Context, sessionID string) (*generation.Workflow, error) {
	workflow, err := scanGenerationWorkflow(d.conn.QueryRowContext(ctx, generationWorkflowSelect+` WHERE source_kind = ? AND source_ref = ?
		AND state NOT IN (?, ?, ?, ?) ORDER BY created_at DESC LIMIT 1`, generation.SourceAuthoring, sessionID,
		generation.StatePublished, generation.StateFailed, generation.StateCancelled, generation.StateSuperseded))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGenerationWorkflowNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get active generation workflow: %w", err)
	}
	return workflow, nil
}

func (d *GenerationRepository) ClaimGenerationWorkflow(ctx context.Context, workerID string, leaseTTL time.Duration, now time.Time) (*generation.Claim, error) {
	if strings.TrimSpace(workerID) == "" || leaseTTL <= 0 || now.IsZero() {
		return nil, errors.New("generation workflow claim requires worker, lease ttl, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generation claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now = now.UTC()
	if err := expireGenerationDeadlinesTx(ctx, tx, now); err != nil {
		return nil, err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM generation_workflows
		WHERE state IN (?, ?, ?, ?, ?, ?, ?)
		AND next_run_at <= ?
		AND (lease_expires_at IS NULL OR lease_expires_at <= ?)
		ORDER BY next_run_at, created_at, id
		FOR UPDATE SKIP LOCKED LIMIT 1`, generation.StateGenerating, generation.StateJudging, generation.StateBuilding,
		generation.StateArtifactPublishing, generation.StateVerifying, generation.StateClassifying, generation.StateChallengePublishing, now, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit generation recovery: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select generation workflow: %w", err)
	}
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	if !workflow.State.Leaseable() {
		return nil, fmt.Errorf("generation workflow %s is not leaseable", workflow.ID)
	}
	stateAttempt := workflow.StateAttempt
	deadline := workflow.DeadlineAt
	if deadline == nil && workflow.State.DeadlineActive() {
		value := now.Add(generation.ExecutionDeadline)
		deadline = &value
	}
	owner := strings.TrimSpace(workerID) + "-" + generation.NewID("lease")
	expires := now.Add(leaseTTL)
	workflow, err = scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET
		state_attempt = ?, lease_owner = ?, lease_expires_at = ?, deadline_at = COALESCE(deadline_at, ?), updated_at = ?
		WHERE id = ? RETURNING `+generationWorkflowColumns, stateAttempt, owner, expires, deadline, now, workflow.ID))
	if err != nil {
		return nil, fmt.Errorf("claim generation workflow: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generation claim: %w", err)
	}
	return &generation.Claim{Workflow: *workflow, LeaseCredential: generation.LeaseCredential{StateAttempt: workflow.StateAttempt, LeaseOwner: owner}}, nil
}

func (d *GenerationRepository) GetGenerationClaim(ctx context.Context, id string, credential generation.LeaseCredential, now time.Time) (*generation.Claim, error) {
	if strings.TrimSpace(id) == "" || !credential.Valid() || now.IsZero() {
		return nil, errors.New("generation workflow lease credentials are required")
	}
	workflow, err := scanGenerationWorkflow(d.conn.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND lease_owner = ?
		AND state_attempt = ? AND lease_expires_at > ?`, id, credential.LeaseOwner, credential.StateAttempt, now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generationLeaseLost()
	}
	if err != nil {
		return nil, fmt.Errorf("get generation workflow lease: %w", err)
	}
	return &generation.Claim{Workflow: *workflow, LeaseCredential: credential}, nil
}

// LoadGenerationContext returns only the data a leased worker needs for the
// current phase. Archive bytes remain behind a separate lease-fenced endpoint
// so the worker never learns the Server's filesystem layout.
func (d *GenerationRepository) LoadGenerationContext(ctx context.Context, claim generation.Claim, now time.Time) (*generation.Context, error) {
	if !claim.Valid() || now.IsZero() {
		return nil, errors.New("generation workflow context requires a valid claim")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generation context: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationClaimTx(ctx, tx, claim, claim.Workflow.State, now.UTC())
	if err != nil {
		return nil, err
	}
	result := &generation.Context{Workflow: *workflow}
	if workflow.Source.Kind != generation.SourceAuthoring {
		return nil, errors.New("generation workflow source is invalid")
	}
	revision, err := authoringSourceRevision(*workflow)
	if err != nil {
		return nil, err
	}
	plan, err := readAuthoringRevisionTx(ctx, tx, workflow.Source.Ref, revision)
	if err != nil {
		return nil, err
	}
	session, err := readAuthoringSessionTx(ctx, tx, workflow.Source.Ref, "")
	if err != nil {
		return nil, err
	}
	generatorSession, err := ensureGeneratorSessionTx(ctx, tx, session)
	if err != nil {
		return nil, err
	}
	result.Plan = plan.Plan
	result.GeneratorSession = generatorSession
	if workflow.State == generation.StateClassifying {
		result.ClassificationFeedback = workflow.ClassificationFeedback
	}
	if workflow.CandidateRevisionID != "" {
		revision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ?`, workflow.CandidateRevisionID))
		if err != nil {
			return nil, err
		}
		view := revision.WorkerView()
		result.Candidate = &view
		if revision.Failure != nil {
			result.Feedback = generation.Feedback{
				Summary: revision.Failure.Summary,
				Issues:  []generation.Issue{{Code: revision.Failure.Code, Message: revision.Failure.Summary}},
			}
		}
	}
	if result.Feedback.Empty() && strings.TrimSpace(workflow.LastError) != "" {
		result.Feedback = generation.Feedback{
			Summary: workflow.LastError,
			Issues:  []generation.Issue{{Code: "WORKFLOW_FEEDBACK", Message: workflow.LastError}},
		}
	}
	if err := result.Feedback.Validate(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generation context: %w", err)
	}
	return result, nil
}

func (d *GenerationRepository) RenewGenerationLease(ctx context.Context, claim generation.Claim, leaseTTL time.Duration, now time.Time) error {
	if !claim.Valid() || leaseTTL <= 0 || now.IsZero() {
		return errors.New("generation workflow lease renewal is invalid")
	}
	// A successful phase keeps the lease owner but resets state_attempt. Renewal
	// therefore fences only on the current lease identity; phase reports retain
	// the attempt fence that rejects stale results.
	result, err := d.conn.ExecContext(ctx, `UPDATE generation_workflows SET lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND lease_owner = ? AND lease_expires_at > ?`,
		now.UTC().Add(leaseTTL), now.UTC(), claim.Workflow.ID, claim.LeaseOwner, now.UTC())
	if err != nil {
		return fmt.Errorf("renew generation workflow lease: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return generationLeaseLost()
	}
	return nil
}

// RefreshGenerationClaim returns the current state under an already-held
// lease. A successful phase can reset state_attempt, so callers must obtain a
// fresh credential before reporting the following phase.
func (d *GenerationRepository) RefreshGenerationClaim(ctx context.Context, workflowID, leaseOwner string, now time.Time) (*generation.Claim, error) {
	if strings.TrimSpace(workflowID) == "" || strings.TrimSpace(leaseOwner) == "" || now.IsZero() {
		return nil, errors.New("generation workflow identity and lease owner are required")
	}
	workflow, err := scanGenerationWorkflow(d.conn.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND lease_owner = ? AND lease_expires_at > ?`,
		workflowID, leaseOwner, now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generationLeaseLost()
	}
	if err != nil {
		return nil, fmt.Errorf("refresh generation workflow claim: %w", err)
	}
	return &generation.Claim{Workflow: *workflow, LeaseCredential: generation.LeaseCredential{
		StateAttempt: workflow.StateAttempt,
		LeaseOwner:   workflow.LeaseOwner,
	}}, nil
}

func (d *GenerationRepository) StartGenerationAgentRun(ctx context.Context, claim generation.Claim, input agent.CreateRun, now time.Time) (*agent.Run, error) {
	if !claim.Valid() || now.IsZero() || input.OwnerKind != "generation-workflow" || input.OwnerRef != claim.Workflow.ID {
		return nil, errors.New("generation agent run ownership is invalid")
	}
	if input.Purpose != "generator" && input.Purpose != "judge" && input.Purpose != "classifier" {
		return nil, errors.New("generation agent run purpose is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generation agent run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationClaimTx(ctx, tx, claim, claim.Workflow.State, now.UTC())
	if err != nil {
		return nil, err
	}
	if (input.Purpose == "generator" && workflow.State != generation.StateGenerating) ||
		(input.Purpose == "judge" && workflow.State != generation.StateJudging) ||
		(input.Purpose == "classifier" && workflow.State != generation.StateClassifying) {
		return nil, errors.New("generation agent run purpose does not match workflow state")
	}
	if workflow.Source.Kind != generation.SourceAuthoring {
		return nil, errors.New("catalog release workflows do not run generator agents")
	}
	if workflow.ActiveAgentRunID != "" {
		// A prior process died after starting its call. It cannot be resumed, so
		// retain the durable record and start a fresh call under the new lease.
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
			WHERE id = ? AND status = ?`, agent.RunFailed, "worker lease replaced before agent call completed", now, now,
			workflow.ActiveAgentRunID, agent.RunRunning); err != nil {
			return nil, fmt.Errorf("abandon prior generation agent run: %w", err)
		}
	}
	session, err := readAuthoringSessionTx(ctx, tx, workflow.Source.Ref, "")
	if err != nil {
		return nil, err
	}
	generatorSessionID, err := ensureGeneratorSessionTx(ctx, tx, session)
	if err != nil {
		return nil, err
	}
	if input.SessionID != "" && input.SessionID != generatorSessionID {
		return nil, errors.New("generation agent run session does not belong to workflow")
	}
	input.SessionID = generatorSessionID
	if err := lockActiveSessionTx(ctx, tx, input.SessionID); err != nil {
		return nil, err
	}
	created, err := createRunTx(ctx, tx, input, now.UTC())
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET active_agent_run_id = ?, updated_at = ? WHERE id = ?`, created.ID, now.UTC(), workflow.ID); err != nil {
		return nil, fmt.Errorf("attach generation agent run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generation agent run: %w", err)
	}
	return created, nil
}

func (d *GenerationRepository) FinalizeGeneratedCandidate(ctx context.Context, claim generation.Claim, runID string, revision generation.Revision, now time.Time) error {
	if !claim.Valid() || strings.TrimSpace(runID) == "" || now.IsZero() {
		return errors.New("generated candidate finalization is invalid")
	}
	if revision.GeneratorRunID != runID || revision.JudgeRunID != "" || revision.Source != claim.Workflow.Source || revision.SourceRevision != claim.Workflow.SourceRevision {
		return errors.New("generated candidate lineage does not match workflow")
	}
	if err := revision.ValidateForCreate(); err != nil {
		return err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin generated candidate finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationClaimTx(ctx, tx, claim, generation.StateGenerating, now.UTC())
	if err != nil {
		return err
	}
	if workflow.ActiveAgentRunID != runID {
		return generationLeaseLost()
	}
	if workflow.CandidateRevisionID != "" {
		revision.ParentCandidateID = workflow.CandidateRevisionID
		revision.RepairReason = strings.TrimSpace(workflow.LastError)
	}
	if err := completeRunTx(ctx, tx, runID, now.UTC()); err != nil {
		return err
	}
	revision.CreatedAt = now.UTC()
	revision.UpdatedAt = now.UTC()
	if err := insertCandidateRevisionTx(ctx, tx, revision); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, candidate_revision_id = ?, active_agent_run_id = NULL,
		state_attempt = 0, last_error = '', next_run_at = ?, updated_at = ? WHERE id = ?`,
		generation.StateJudging, revision.ID, now.UTC(), now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("advance generated candidate workflow: %w", err)
	}
	return tx.Commit()
}

func (d *GenerationRepository) FinalizeGenerationJudgement(ctx context.Context, claim generation.Claim, runID string, approved bool, feedback string, now time.Time) error {
	if !claim.Valid() || strings.TrimSpace(runID) == "" || now.IsZero() || (!approved && strings.TrimSpace(feedback) == "") {
		return errors.New("generation judgement finalization is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin generation judgement finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationClaimTx(ctx, tx, claim, generation.StateJudging, now.UTC())
	if err != nil {
		return err
	}
	if workflow.ActiveAgentRunID != runID || workflow.CandidateRevisionID == "" {
		return generationLeaseLost()
	}
	if err := completeRunTx(ctx, tx, runID, now.UTC()); err != nil {
		return err
	}
	if approved {
		if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET judge_run_id = ?, updated_at = ? WHERE id = ?`, runID, now.UTC(), workflow.CandidateRevisionID); err != nil {
			return fmt.Errorf("record candidate judge run: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, active_agent_run_id = NULL, state_attempt = 0,
			last_error = '', next_run_at = ?, updated_at = ? WHERE id = ?`, generation.StateBuilding, now.UTC(), now.UTC(), workflow.ID); err != nil {
			return fmt.Errorf("advance approved judgement: %w", err)
		}
	} else {
		failure, err := marshalJSON(generation.Failure{Class: generation.FailureArtifact, Code: "JUDGE_REJECT", Summary: strings.TrimSpace(feedback)})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET judge_run_id = ?, failure = ?::jsonb, updated_at = ? WHERE id = ?`, runID, failure, now.UTC(), workflow.CandidateRevisionID); err != nil {
			return fmt.Errorf("record rejected judgement: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, active_agent_run_id = NULL, state_attempt = 0,
			last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ?`, generation.StateGenerating, strings.TrimSpace(feedback), now.UTC(), now.UTC(), workflow.ID); err != nil {
			return fmt.Errorf("return rejected judgement to generator: %w", err)
		}
	}
	return tx.Commit()
}

func (d *GenerationRepository) CompleteGenerationBuild(ctx context.Context, claim generation.Claim, output generation.BuildOutput, now time.Time) error {
	return d.advanceCandidateOutput(ctx, claim, generation.StateBuilding, generation.StateArtifactPublishing, "build_output", output, now)
}

func (d *GenerationRepository) CompleteGenerationArtifactPublish(ctx context.Context, claim generation.Claim, output generation.ArtifactReference, now time.Time) error {
	return d.advanceCandidateOutput(ctx, claim, generation.StateArtifactPublishing, generation.StateVerifying, "artifact_reference", output, now)
}

func (d *GenerationRepository) advanceCandidateOutput(ctx context.Context, claim generation.Claim, expected, next generation.WorkflowState, column string, value any, now time.Time) error {
	if !claim.Valid() || now.IsZero() {
		return errors.New("generation candidate output finalization is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin generation candidate output: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationClaimTx(ctx, tx, claim, expected, now.UTC())
	if err != nil {
		return err
	}
	if workflow.CandidateRevisionID == "" {
		return errors.New("generation workflow has no candidate revision")
	}
	candidateRevision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, workflow.CandidateRevisionID))
	if err != nil {
		return err
	}
	switch output := value.(type) {
	case generation.BuildOutput:
		if err := output.Validate(candidateRevision.Snapshot.Runtime); err != nil {
			return err
		}
	case generation.ArtifactReference:
		if err := output.Validate(candidateRevision.Snapshot.Runtime); err != nil {
			return err
		}
	default:
		return errors.New("unsupported generation candidate output")
	}
	encoded, err := marshalJSON(value)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET `+column+` = ?::jsonb, updated_at = ? WHERE id = ?`, encoded, now.UTC(), candidateRevision.ID); err != nil {
		return fmt.Errorf("record generation candidate output: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_attempt = 0, last_error = '', next_run_at = ?, updated_at = ? WHERE id = ?`,
		next, now.UTC(), now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("advance generation workflow: %w", err)
	}
	return tx.Commit()
}

func (d *GenerationRepository) RecordGenerationVerificationEnvironment(ctx context.Context, claim generation.Claim, environment generation.VerificationEnvironment, now time.Time) error {
	if !claim.Valid() || now.IsZero() || environment.WorkflowID != claim.Workflow.ID || environment.Attempt != int64(claim.StateAttempt+1) {
		return errors.New("generation verification environment identity is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin verification environment record: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationClaimTx(ctx, tx, claim, generation.StateVerifying, now.UTC())
	if err != nil {
		return err
	}
	if workflow.CandidateRevisionID == "" {
		return errors.New("generation workflow has no candidate revision")
	}
	runtime, err := workflowRuntimeTx(ctx, tx, workflow.CandidateRevisionID)
	if err != nil {
		return err
	}
	if err := environment.Validate(runtime); err != nil {
		return err
	}
	encoded, err := marshalJSON(environment)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET verify_environment = ?::jsonb, updated_at = ? WHERE id = ?`, encoded, now.UTC(), workflow.CandidateRevisionID); err != nil {
		return fmt.Errorf("record verification environment: %w", err)
	}
	return tx.Commit()
}

func (d *GenerationRepository) CompleteGenerationVerification(ctx context.Context, claim generation.Claim, report generation.VerificationReport, now time.Time) error {
	if !claim.Valid() || now.IsZero() {
		return errors.New("generation verification finalization is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin verification finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationClaimTx(ctx, tx, claim, generation.StateVerifying, now.UTC())
	if err != nil {
		return err
	}
	candidateRevision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, workflow.CandidateRevisionID))
	if err != nil {
		return err
	}
	if err := report.Validate(candidateRevision.Snapshot); err != nil {
		return err
	}
	encoded, err := marshalJSON(report)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET verification_report = ?::jsonb, verified_at = ?, updated_at = ? WHERE id = ?`, encoded, now.UTC(), now.UTC(), candidateRevision.ID); err != nil {
		return fmt.Errorf("record verification report: %w", err)
	}
	if workflow.Source.Kind != generation.SourceAuthoring {
		return errors.New("generation workflow source is invalid")
	}
	authoringRevision, err := authoringSourceRevision(*workflow)
	if err != nil {
		return err
	}
	if report.Passed {
		if _, err := tx.ExecContext(ctx, `UPDATE authoring_revisions SET candidate_revision_id = ? WHERE session_id = ? AND revision = ?`, candidateRevision.ID, workflow.Source.Ref, authoringRevision); err != nil {
			return fmt.Errorf("link verified candidate to authoring revision: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET visible_revision = ?, updated_at = ? WHERE id = ?`, authoringRevision, nowText(now), workflow.Source.Ref); err != nil {
			return fmt.Errorf("update visible authoring revision: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_attempt = 0, lease_owner = '', lease_expires_at = NULL,
			deadline_paused_at = ?, last_error = '', updated_at = ? WHERE id = ?`, generation.StateNeedsAuthorReview, now.UTC(), now.UTC(), workflow.ID); err != nil {
			return fmt.Errorf("pause generation workflow for author review: %w", err)
		}
		return tx.Commit()
	}
	failure, err := marshalJSON(generation.Failure{Class: generation.FailureArtifact, Code: "VERIFY_FAILED", Summary: report.Summary})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET failure = ?::jsonb, updated_at = ? WHERE id = ?`, failure, now.UTC(), candidateRevision.ID); err != nil {
		return fmt.Errorf("record verification failure: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_attempt = 0, last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ?`, generation.StateGenerating, report.Summary, now.UTC(), now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("return failed verification to generator: %w", err)
	}
	return tx.Commit()
}

// ConfirmGenerationContent freezes the verified candidate and starts the
// independent classification phase. A content change creates a new workflow;
// this operation never sends the verified candidate back through Build/Verify.
func (d *GenerationRepository) ConfirmGenerationContent(ctx context.Context, sessionID, userID string, confirmation generation.ContentConfirmation, now time.Time) (*generation.Workflow, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || !confirmation.Valid() || now.IsZero() {
		return nil, errors.New("content confirmation requires authoring session, user, idempotent candidate confirmation, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin content confirmation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	if _, err := readAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	if receipt, err := generationConfirmationReceiptTx(ctx, tx, sessionID, confirmationContent, confirmation.IdempotencyKey); err != nil {
		return nil, err
	} else if receipt != nil {
		if receipt.WorkflowID != confirmation.WorkflowID || receipt.CandidateRevisionID == nil || *receipt.CandidateRevisionID != confirmation.CandidateRevisionID {
			return nil, authoring.ErrVersionConflict
		}
		return generationWorkflowForReceiptTx(ctx, tx, receipt.WorkflowID)
	}
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND source_kind = ? AND source_ref = ? AND state = ? FOR UPDATE`,
		confirmation.WorkflowID, generation.SourceAuthoring, sessionID, generation.StateNeedsAuthorReview))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrInvalidState
	}
	if err != nil {
		return nil, err
	}
	if workflow.CandidateRevisionID != confirmation.CandidateRevisionID || workflow.DeadlinePausedAt == nil || workflow.DeadlineAt == nil {
		return nil, authoring.ErrInvalidState
	}
	candidateRevision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, workflow.CandidateRevisionID))
	if err != nil {
		return nil, err
	}
	if candidateRevision.Verification == nil || !candidateRevision.Verification.Passed || candidateRevision.Artifact == nil {
		return nil, authoring.ErrInvalidState
	}
	roadmapRevision, err := currentRoadmapForUpdateTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	deadline := resumeGenerationDeadline(*workflow, now)
	updated, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET state = ?, classification_roadmap_revision = ?, classification_feedback = '',
		state_attempt = 0, lease_owner = '', lease_expires_at = NULL, deadline_at = ?, deadline_paused_at = NULL,
		next_run_at = ?, last_error = '', updated_at = ? WHERE id = ? RETURNING `+generationWorkflowColumns,
		generation.StateClassifying, roadmapRevision.Revision, deadline, now.UTC(), now.UTC(), workflow.ID))
	if err != nil {
		return nil, fmt.Errorf("start generation classification: %w", err)
	}
	if err := insertGenerationConfirmationReceiptTx(ctx, tx, generationConfirmationReceipt{
		SessionID: sessionID, Action: confirmationContent, IdempotencyKey: confirmation.IdempotencyKey,
		WorkflowID: workflow.ID, CandidateRevisionID: &confirmation.CandidateRevisionID, CreatedAt: now.UTC(),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

// FinalizeGenerationClassification records a private typed proposal. Global
// Roadmap content remains untouched until the author later confirms publish.
func (d *GenerationRepository) FinalizeGenerationClassification(ctx context.Context, claim generation.Claim, runID string, output generation.ClassificationOutput, now time.Time) error {
	if !claim.Valid() || strings.TrimSpace(runID) == "" || now.IsZero() || output.Validate() != nil {
		return errors.New("generation classification finalization is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin generation classification finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationClaimTx(ctx, tx, claim, generation.StateClassifying, now.UTC())
	if err != nil {
		return err
	}
	if workflow.ActiveAgentRunID != runID || workflow.CandidateRevisionID == "" || strings.TrimSpace(workflow.ClassificationRoadmapRevision) == "" {
		return generationLeaseLost()
	}
	candidateRevision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, workflow.CandidateRevisionID))
	if err != nil {
		return err
	}
	if candidateRevision.Verification == nil || !candidateRevision.Verification.Passed || candidateRevision.Artifact == nil {
		return generation.ErrCandidateInvalidState
	}
	roadmapRevision, err := roadmapRevisionTx(ctx, tx, workflow.ClassificationRoadmapRevision)
	if err != nil {
		return err
	}
	proposalRevision := 1
	if candidateRevision.Classification != nil {
		proposalRevision = candidateRevision.Classification.Revision + 1
	}
	proposal := generation.ClassificationProposal{
		Revision: proposalRevision, CandidateRevisionID: candidateRevision.ID, RoadmapRevision: roadmapRevision.Revision,
		Result: output.Result, Topic: output.Topic, Tags: append([]generation.TagProposal(nil), output.Tags...),
		UnclassifiableReason: output.UnclassifiableReason, AdjustmentSuggestion: output.AdjustmentSuggestion, UpdatedAt: now.UTC(),
	}
	if proposal.Result == generation.ClassificationProposed {
		if _, _, _, err := resolveClassificationProposal(*roadmapRevision, proposal); err != nil {
			return err
		}
	}
	if err := completeRunTx(ctx, tx, runID, now.UTC()); err != nil {
		return err
	}
	encoded, err := marshalJSON(proposal)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET classification_proposal = ?::jsonb, updated_at = ? WHERE id = ?`, encoded, now.UTC(), workflow.CandidateRevisionID); err != nil {
		return fmt.Errorf("record generation classification proposal: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, active_agent_run_id = NULL, state_attempt = 0,
		lease_owner = '', lease_expires_at = NULL, deadline_paused_at = ?, classification_feedback = '', last_error = '', updated_at = ? WHERE id = ?`,
		generation.StateNeedsClassificationReview, now.UTC(), now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("pause generation workflow for classification review: %w", err)
	}
	return tx.Commit()
}

// FinalizeGenerationClassificationAdjustment applies only a private proposal
// change. Existing Roadmap definitions are resolved against the same immutable
// revision pinned when this workflow first entered Classifying.
func (d *GenerationRepository) FinalizeGenerationClassificationAdjustment(ctx context.Context, claim generation.Claim, result generation.ClassificationAdjustment, now time.Time) (*generation.ClassificationContentFeedback, error) {
	if !claim.Valid() || result.Validate() != nil || now.IsZero() {
		return nil, errors.New("generation classification adjustment finalization is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generation classification adjustment finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationClaimTx(ctx, tx, claim, generation.StateClassifying, now.UTC())
	if err != nil {
		return nil, err
	}
	if workflow.ActiveAgentRunID != result.RunID || workflow.CandidateRevisionID == "" || strings.TrimSpace(workflow.ClassificationRoadmapRevision) == "" {
		return nil, generationLeaseLost()
	}
	candidateRevision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, workflow.CandidateRevisionID))
	if err != nil {
		return nil, err
	}
	if candidateRevision.Verification == nil || !candidateRevision.Verification.Passed || candidateRevision.Artifact == nil || candidateRevision.Classification == nil ||
		candidateRevision.Classification.Result != generation.ClassificationProposed || candidateRevision.Classification.RoadmapRevision != workflow.ClassificationRoadmapRevision {
		return nil, generation.ErrCandidateInvalidState
	}
	roadmapRevision, err := roadmapRevisionTx(ctx, tx, workflow.ClassificationRoadmapRevision)
	if err != nil {
		return nil, err
	}
	var contentFeedback *generation.ClassificationContentFeedback
	lastError := ""
	if result.ChangeScope == generation.ClassificationChangeClassification {
		proposal := generation.ClassificationProposal{
			Revision: candidateRevision.Classification.Revision + 1, CandidateRevisionID: candidateRevision.ID, RoadmapRevision: roadmapRevision.Revision,
			Result: result.Output.Result, Topic: result.Output.Topic, Tags: append([]generation.TagProposal(nil), result.Output.Tags...), UpdatedAt: now.UTC(),
		}
		if _, _, _, err := resolveClassificationProposal(*roadmapRevision, proposal); err != nil {
			return nil, err
		}
		encoded, err := marshalJSON(proposal)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET classification_proposal = ?::jsonb, updated_at = ? WHERE id = ?`, encoded, now.UTC(), candidateRevision.ID); err != nil {
			return nil, fmt.Errorf("record adjusted generation classification proposal: %w", err)
		}
	}
	if result.ChangeScope == generation.ClassificationChangeContent {
		content := strings.TrimSpace(workflow.ClassificationFeedback)
		if content == "" {
			return nil, generation.ErrCandidateInvalidState
		}
		session, err := readAuthoringSessionTx(ctx, tx, workflow.Source.Ref, "")
		if err != nil {
			return nil, err
		}
		contentFeedback = &generation.ClassificationContentFeedback{SessionID: session.ID, UserID: session.UserID, Content: content}
	}
	if result.ChangeScope == generation.ClassificationChangeClarify {
		lastError = "classification_clarification: " + strings.TrimSpace(result.Clarification)
	}
	if err := completeRunTx(ctx, tx, result.RunID, now.UTC()); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, active_agent_run_id = NULL, state_attempt = 0,
		lease_owner = '', lease_expires_at = NULL, deadline_paused_at = ?, classification_feedback = '', last_error = ?, updated_at = ? WHERE id = ?`,
		generation.StateNeedsClassificationReview, now.UTC(), lastError, now.UTC(), workflow.ID); err != nil {
		return nil, fmt.Errorf("pause generation workflow for adjusted classification review: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return contentFeedback, nil
}

// ResumeGenerationClassification restarts only the classification role for a
// verified candidate after classification feedback or a transient failure.
func (d *GenerationRepository) ResumeGenerationClassification(ctx context.Context, sessionID, userID, feedback string, now time.Time) (*generation.Workflow, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || now.IsZero() {
		return nil, errors.New("classification resume requires authoring session, user, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin classification resume: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := readAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE source_kind = ? AND source_ref = ? AND state = ? FOR UPDATE`, generation.SourceAuthoring, sessionID, generation.StateNeedsClassificationReview))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrInvalidState
	}
	if err != nil {
		return nil, err
	}
	if workflow.CandidateRevisionID == "" || workflow.DeadlinePausedAt == nil || workflow.DeadlineAt == nil || strings.TrimSpace(workflow.ClassificationRoadmapRevision) == "" {
		return nil, authoring.ErrInvalidState
	}
	candidateRevision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, workflow.CandidateRevisionID))
	if err != nil {
		return nil, err
	}
	if candidateRevision.Classification != nil && candidateRevision.Classification.Result == generation.ClassificationUnclassifiable && strings.TrimSpace(feedback) != "" {
		return nil, authoring.ErrInvalidState
	}
	deadline := resumeGenerationDeadline(*workflow, now)
	updated, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET state = ?, state_attempt = 0,
		lease_owner = '', lease_expires_at = NULL, deadline_at = ?, deadline_paused_at = NULL, next_run_at = ?, classification_feedback = ?, last_error = '', updated_at = ?
		WHERE id = ? RETURNING `+generationWorkflowColumns, generation.StateClassifying, deadline, now.UTC(), now.UTC(), strings.TrimSpace(feedback), now.UTC(), workflow.ID))
	if err != nil {
		return nil, fmt.Errorf("resume generation classification: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

// BeginClassificationPublication is the idempotent, optimistic author
// confirmation that turns one reviewed proposal into a publication intent.
func (d *GenerationRepository) BeginClassificationPublication(ctx context.Context, sessionID, userID, challengeTitle string, confirmation generation.PublicationConfirmation, now time.Time) (*generation.Workflow, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || strings.TrimSpace(challengeTitle) == "" || !confirmation.Valid() || now.IsZero() {
		return nil, errors.New("classification publication requires session, user, title, idempotent proposal confirmation, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin classification publication: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	if _, err := readAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	if receipt, err := generationConfirmationReceiptTx(ctx, tx, sessionID, confirmationPublication, confirmation.IdempotencyKey); err != nil {
		return nil, err
	} else if receipt != nil {
		if receipt.WorkflowID != confirmation.WorkflowID || receipt.CandidateRevisionID == nil || *receipt.CandidateRevisionID != confirmation.CandidateRevisionID ||
			receipt.ProposalRevision == nil || *receipt.ProposalRevision != confirmation.ProposalRevision {
			return nil, authoring.ErrVersionConflict
		}
		return generationWorkflowForReceiptTx(ctx, tx, receipt.WorkflowID)
	}
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND source_kind = ? AND source_ref = ?
		AND state IN (?, ?) FOR UPDATE`, confirmation.WorkflowID, generation.SourceAuthoring, sessionID, generation.StateNeedsClassificationReview, generation.StateChallengePublishing))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrInvalidState
	}
	if err != nil {
		return nil, err
	}
	if workflow.CandidateRevisionID != confirmation.CandidateRevisionID {
		return nil, authoring.ErrInvalidState
	}
	candidateRevision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, workflow.CandidateRevisionID))
	if err != nil {
		return nil, err
	}
	if candidateRevision.Classification == nil || candidateRevision.Classification.Revision != confirmation.ProposalRevision {
		return nil, authoring.ErrVersionConflict
	}
	if candidateRevision.Publication != nil {
		if candidateRevision.Publication.ClassificationRevision != confirmation.ProposalRevision {
			return nil, authoring.ErrVersionConflict
		}
		if workflow.State == generation.StateNeedsClassificationReview {
			if workflow.DeadlinePausedAt == nil || workflow.DeadlineAt == nil {
				return nil, authoring.ErrInvalidState
			}
			deadline := resumeGenerationDeadline(*workflow, now)
			workflow, err = scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET state = ?, state_attempt = 0,
				lease_owner = '', lease_expires_at = NULL, deadline_at = ?, deadline_paused_at = NULL, next_run_at = ?, last_error = '', updated_at = ?
				WHERE id = ? RETURNING `+generationWorkflowColumns,
				generation.StateChallengePublishing, deadline, now.UTC(), now.UTC(), workflow.ID))
			if err != nil {
				return nil, fmt.Errorf("resume challenge publication: %w", err)
			}
		}
		if err := insertGenerationConfirmationReceiptTx(ctx, tx, generationConfirmationReceipt{
			SessionID: sessionID, Action: confirmationPublication, IdempotencyKey: confirmation.IdempotencyKey,
			WorkflowID: workflow.ID, CandidateRevisionID: &confirmation.CandidateRevisionID,
			ProposalRevision: &confirmation.ProposalRevision, CreatedAt: now.UTC(),
		}); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return workflow, nil
	}
	if workflow.State != generation.StateNeedsClassificationReview || workflow.DeadlinePausedAt == nil || workflow.DeadlineAt == nil ||
		candidateRevision.Verification == nil || !candidateRevision.Verification.Passed || candidateRevision.Artifact == nil {
		return nil, authoring.ErrInvalidState
	}
	current, err := currentRoadmapForUpdateTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	proposal := *candidateRevision.Classification
	publication, topicWasNew, err := prepareClassificationPublication(*current, proposal, candidateRevision, challengeTitle, now.UTC())
	if err != nil {
		next := generation.StateNeedsClassificationReview
		if errors.Is(err, generation.ErrChallengeSourceRefConflict) {
			next = generation.StateNeedsAuthorReview
		}
		if _, updateErr := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_attempt = 0,
			lease_owner = '', lease_expires_at = NULL, deadline_paused_at = ?, last_error = ?, updated_at = ? WHERE id = ?`,
			next, now.UTC(), err.Error(), now.UTC(), workflow.ID); updateErr != nil {
			return nil, updateErr
		}
		_ = topicWasNew
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, commitErr
		}
		return nil, err
	}
	proposal.RoadmapRevision = current.Revision
	proposal.UpdatedAt = now.UTC()
	encodedProposal, err := marshalJSON(proposal)
	if err != nil {
		return nil, err
	}
	encodedPublication, err := marshalJSON(publication)
	if err != nil {
		return nil, err
	}
	deadline := resumeGenerationDeadline(*workflow, now)
	updated, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET state = ?, state_attempt = 0,
		lease_owner = '', lease_expires_at = NULL, deadline_at = ?, deadline_paused_at = NULL, next_run_at = ?, last_error = '', updated_at = ?
		WHERE id = ? RETURNING `+generationWorkflowColumns, generation.StateChallengePublishing, deadline, now.UTC(), now.UTC(), workflow.ID))
	if err != nil {
		return nil, fmt.Errorf("start challenge publication: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET classification_proposal = ?::jsonb, publication = ?::jsonb, updated_at = ? WHERE id = ?`,
		encodedProposal, encodedPublication, now.UTC(), candidateRevision.ID); err != nil {
		return nil, fmt.Errorf("record challenge publication intent: %w", err)
	}
	if err := insertGenerationConfirmationReceiptTx(ctx, tx, generationConfirmationReceipt{
		SessionID: sessionID, Action: confirmationPublication, IdempotencyKey: confirmation.IdempotencyKey,
		WorkflowID: workflow.ID, CandidateRevisionID: &confirmation.CandidateRevisionID,
		ProposalRevision: &confirmation.ProposalRevision, CreatedAt: now.UTC(),
	}); err != nil {
		return nil, err
	}
	_ = topicWasNew
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

// CompleteGenerationChallengePublish records the final runtime artifact and
// makes its materialized source and Roadmap binding visible together.
func (d *GenerationRepository) CompleteGenerationChallengePublish(ctx context.Context, claim generation.Claim, artifact generation.ArtifactReference, contentRevision string, now time.Time) error {
	if !claim.Valid() || !roadmap.ValidRevision(contentRevision) || now.IsZero() {
		return errors.New("generation challenge publication finalization is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin challenge publication finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationClaimTx(ctx, tx, claim, generation.StateChallengePublishing, now.UTC())
	if err != nil {
		return err
	}
	candidateRevision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, workflow.CandidateRevisionID))
	if err != nil {
		return err
	}
	if err := artifact.Validate(candidateRevision.Snapshot.Runtime); err != nil || candidateRevision.Publication == nil || candidateRevision.Classification == nil {
		return generation.ErrCandidateInvalidState
	}
	publication := *candidateRevision.Publication
	if publication.StagingArtifact == nil || candidateRevision.Artifact == nil || *publication.StagingArtifact != *candidateRevision.Artifact || publication.Runtime != candidateRevision.Snapshot.Runtime {
		return generation.ErrCandidateInvalidState
	}
	publication.ContentRevision = contentRevision
	publication.Artifact = &artifact
	if err := publication.ValidateFinal(); err != nil {
		return err
	}
	current, err := currentRoadmapForUpdateTx(ctx, tx)
	if err != nil {
		return err
	}
	nextRoadmap, topicWasNew, err := applyClassificationPublication(*current, *candidateRevision.Classification, publication)
	if err != nil {
		return err
	}
	canonical, encodedRoadmap, revisionID, err := canonicalRoadmap(nextRoadmap)
	if err != nil {
		return err
	}
	encodedPublication, err := marshalJSON(publication)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO roadmap_revisions (id, content_json, created_at) VALUES (?, ?::jsonb, ?) ON CONFLICT (id) DO NOTHING`, revisionID, encodedRoadmap, now.UTC()); err != nil {
		return fmt.Errorf("store published roadmap revision: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE roadmap_current SET revision_id = ?, updated_at = ? WHERE singleton = TRUE`, revisionID, now.UTC()); err != nil {
		return fmt.Errorf("publish generation roadmap revision: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO roadmap_entries (challenge_id, topic_id, topic_processed, challenge_processed, created_at)
		VALUES (?, ?, ?, FALSE, ?)`, publication.ChallengeID, roadmap.RuntimeID(roadmap.KindTopic, publication.TopicSourceRef), !topicWasNew, now.UTC()); err != nil {
		return fmt.Errorf("record published roadmap entry: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET publication = ?::jsonb, published_at = ?, updated_at = ? WHERE id = ?`, encodedPublication, now.UTC(), now.UTC(), candidateRevision.ID); err != nil {
		return fmt.Errorf("record challenge publication: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET state = ?, publish_challenge_id = ?, updated_at = ? WHERE id = ?`, authoring.StatePublished, publication.ChallengeID, nowText(now), workflow.Source.Ref); err != nil {
		return fmt.Errorf("mark authoring session published: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_attempt = 0, lease_owner = '', lease_expires_at = NULL,
		active_agent_run_id = NULL, deadline_paused_at = NULL, last_error = '', updated_at = ? WHERE id = ?`, generation.StatePublished, now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("complete generation publication: %w", err)
	}
	_ = canonical
	return tx.Commit()
}

func resumeGenerationDeadline(workflow generation.Workflow, now time.Time) time.Time {
	return workflow.DeadlineAt.Add(now.UTC().Sub(*workflow.DeadlinePausedAt))
}

func currentRoadmapForUpdateTx(ctx context.Context, tx *Tx) (*roadmap.Revision, error) {
	var revisionID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT revision_id FROM roadmap_current WHERE singleton = TRUE FOR UPDATE`).Scan(&revisionID); err != nil {
		return nil, fmt.Errorf("lock current roadmap revision: %w", err)
	}
	if !revisionID.Valid || strings.TrimSpace(revisionID.String) == "" {
		return nil, roadmap.ErrNoCurrentRevision
	}
	return roadmapRevisionTx(ctx, tx, revisionID.String)
}

func roadmapRevisionTx(ctx context.Context, tx *Tx, revisionID string) (*roadmap.Revision, error) {
	if !roadmap.ValidRevision(revisionID) {
		return nil, generation.ErrClassificationConflict
	}
	var encoded []byte
	if err := tx.QueryRowContext(ctx, `SELECT content_json FROM roadmap_revisions WHERE id = ?`, revisionID).Scan(&encoded); err != nil {
		return nil, fmt.Errorf("read current roadmap revision: %w", err)
	}
	var value roadmap.Revision
	if err := json.Unmarshal(encoded, &value); err != nil {
		return nil, fmt.Errorf("decode current roadmap revision: %w", err)
	}
	value.Revision = revisionID
	if err := value.Validate(); err != nil {
		return nil, fmt.Errorf("validate current roadmap revision: %w", err)
	}
	return &value, nil
}

func prepareClassificationPublication(current roadmap.Revision, proposal generation.ClassificationProposal, candidate *generation.Revision, challengeTitle string, now time.Time) (generation.Publication, bool, error) {
	if candidate == nil || candidate.Artifact == nil || candidate.Verification == nil || !candidate.Verification.Passed || proposal.Result != generation.ClassificationProposed ||
		proposal.CandidateRevisionID != candidate.ID {
		return generation.Publication{}, false, generation.ErrCandidateInvalidState
	}
	topic, tags, topicWasNew, err := resolveClassificationProposal(current, proposal)
	if err != nil {
		return generation.Publication{}, false, err
	}
	challengeSourceRef := roadmap.NewChallengeSourceRef(topic.SourceRef, challengeTitle)
	if challengeSourceRef == "" {
		return generation.Publication{}, false, generation.ErrChallengeSourceRefConflict
	}
	challengeID := challenge.NewID()
	for _, binding := range current.ChallengeBindings {
		if binding.Challenge.SourceRef == challengeSourceRef || binding.Challenge.ID == challengeID {
			return generation.Publication{}, false, generation.ErrChallengeSourceRefConflict
		}
	}
	tagRefs := make([]string, 0, len(tags))
	for _, tag := range tags {
		tagRefs = append(tagRefs, tag.SourceRef)
	}
	staging := *candidate.Artifact
	publication := generation.Publication{
		CandidateRevisionID:    candidate.ID,
		IntentRevision:         1,
		ChallengeID:            challengeID,
		ChallengeSourceRef:     challengeSourceRef,
		SourceSlug:             challenge.SourceSlugFor(challengeTitle, challengeID),
		TargetPath:             challenge.SourceSlugFor(challengeTitle, challengeID),
		ChallengeTitle:         challengeTitle,
		Runtime:                candidate.Snapshot.Runtime,
		TopicSourceRef:         topic.SourceRef,
		TagSourceRefs:          tagRefs,
		ClassificationRevision: proposal.Revision,
		RequestedAt:            now.UTC(),
		StagingArtifact:        &staging,
	}
	if err := publication.ValidateIntent(); err != nil {
		return generation.Publication{}, false, err
	}
	return publication, topicWasNew, nil
}

func applyClassificationPublication(current roadmap.Revision, proposal generation.ClassificationProposal, publication generation.Publication) (roadmap.Revision, bool, error) {
	if proposal.Result != generation.ClassificationProposed || publication.ClassificationRevision != proposal.Revision || publication.CandidateRevisionID != proposal.CandidateRevisionID {
		return roadmap.Revision{}, false, generation.ErrClassificationConflict
	}
	next := current.Clone()
	topic, tags, topicWasNew, err := resolveClassificationProposal(next, proposal)
	if err != nil {
		return roadmap.Revision{}, false, err
	}
	if publication.TopicSourceRef != topic.SourceRef || len(publication.TagSourceRefs) != len(tags) {
		return roadmap.Revision{}, false, generation.ErrClassificationConflict
	}
	for index, tag := range tags {
		if publication.TagSourceRefs[index] != tag.SourceRef {
			return roadmap.Revision{}, false, generation.ErrClassificationConflict
		}
	}
	if proposal.Topic.New != nil {
		value := proposal.Topic.New
		next.Topics = append(next.Topics, roadmap.Topic{
			ID: roadmap.RuntimeID(roadmap.KindTopic, topic.SourceRef), SourceRef: topic.SourceRef, Title: value.Title, Domain: value.Domain,
			Definition: value.Definition, Scope: value.Scope, NonGoals: value.NonGoals, ChallengeGuidance: value.ChallengeGuidance,
		})
	}
	for index, tagProposal := range proposal.Tags {
		if tagProposal.New == nil {
			continue
		}
		value := tagProposal.New
		next.Tags = append(next.Tags, roadmap.Tag{
			ID: roadmap.RuntimeID(roadmap.KindTag, tags[index].SourceRef), SourceRef: tags[index].SourceRef,
			Title: value.Title, Description: value.Description,
		})
	}
	for _, binding := range next.ChallengeBindings {
		if binding.Challenge.ID == publication.ChallengeID || binding.Challenge.SourceRef == publication.ChallengeSourceRef {
			return roadmap.Revision{}, false, generation.ErrChallengeSourceRefConflict
		}
	}
	next.ChallengeBindings = append(next.ChallengeBindings, roadmap.ChallengeBinding{
		Challenge: roadmap.ChallengeRef{
			ID: publication.ChallengeID, SourceRef: publication.ChallengeSourceRef, Title: publication.ChallengeTitle, ContentRevision: publication.ContentRevision,
		},
		Topic: topic,
		Tags:  tags,
	})
	if err := next.Validate(); err != nil {
		return roadmap.Revision{}, false, fmt.Errorf("%w: %v", generation.ErrClassificationConflict, err)
	}
	return next, topicWasNew, nil
}

func resolveClassificationProposal(current roadmap.Revision, proposal generation.ClassificationProposal) (roadmap.Ref, []roadmap.Ref, bool, error) {
	if proposal.Validate() != nil || proposal.Result != generation.ClassificationProposed || proposal.Topic == nil {
		return roadmap.Ref{}, nil, false, generation.ErrClassificationConflict
	}
	domains := make(map[string]roadmap.Ref, len(current.Domains))
	for _, value := range current.Domains {
		domains[value.SourceRef] = roadmap.Ref{ID: value.ID, SourceRef: value.SourceRef, Title: value.Title}
	}
	topics := make(map[string]roadmap.Ref, len(current.Topics))
	for _, value := range current.Topics {
		topics[value.SourceRef] = roadmap.Ref{ID: value.ID, SourceRef: value.SourceRef, Title: value.Title}
	}
	tagsBySource := make(map[string]roadmap.Ref, len(current.Tags))
	for _, value := range current.Tags {
		tagsBySource[value.SourceRef] = roadmap.Ref{ID: value.ID, SourceRef: value.SourceRef, Title: value.Title}
	}

	var topic roadmap.Ref
	topicWasNew := proposal.Topic.New != nil
	if existing := proposal.Topic.Existing; existing != nil {
		stored, found := topics[existing.SourceRef]
		if !found || stored != *existing {
			return roadmap.Ref{}, nil, false, generation.ErrClassificationConflict
		}
		topic = stored
	} else {
		value := proposal.Topic.New
		domain, found := domains[value.Domain.SourceRef]
		if !found || domain != value.Domain {
			return roadmap.Ref{}, nil, false, generation.ErrClassificationConflict
		}
		sourceRef := roadmap.NewTopicSourceRef(domain.SourceRef, value.Title)
		if sourceRef == "" {
			return roadmap.Ref{}, nil, false, generation.ErrClassificationConflict
		}
		if _, exists := topics[sourceRef]; exists || topicTitleExists(current.Topics, domain.SourceRef, value.Title) {
			return roadmap.Ref{}, nil, false, generation.ErrClassificationConflict
		}
		topic = roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindTopic, sourceRef), SourceRef: sourceRef, Title: value.Title}
	}

	resolvedTags := make([]roadmap.Ref, 0, len(proposal.Tags))
	for _, proposalTag := range proposal.Tags {
		if existing := proposalTag.Existing; existing != nil {
			stored, found := tagsBySource[existing.SourceRef]
			if !found || stored != *existing {
				return roadmap.Ref{}, nil, false, generation.ErrClassificationConflict
			}
			resolvedTags = append(resolvedTags, stored)
			continue
		}
		value := proposalTag.New
		sourceRef := roadmap.NewTagSourceRef(value.Title)
		if sourceRef == "" {
			return roadmap.Ref{}, nil, false, generation.ErrClassificationConflict
		}
		if _, exists := tagsBySource[sourceRef]; exists || tagTitleExists(current.Tags, value.Title) {
			return roadmap.Ref{}, nil, false, generation.ErrClassificationConflict
		}
		resolved := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindTag, sourceRef), SourceRef: sourceRef, Title: value.Title}
		tagsBySource[sourceRef] = resolved
		resolvedTags = append(resolvedTags, resolved)
	}
	return topic, resolvedTags, topicWasNew, nil
}

func topicTitleExists(values []roadmap.Topic, domainSourceRef, title string) bool {
	want := normalizedRoadmapTitle(title)
	for _, value := range values {
		if value.Domain.SourceRef == domainSourceRef && normalizedRoadmapTitle(value.Title) == want {
			return true
		}
	}
	return false
}

func tagTitleExists(values []roadmap.Tag, title string) bool {
	want := normalizedRoadmapTitle(title)
	for _, value := range values {
		if normalizedRoadmapTitle(value.Title) == want {
			return true
		}
	}
	return false
}

func normalizedRoadmapTitle(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func (d *GenerationRepository) ReportGenerationInfrastructureFailure(ctx context.Context, claim generation.Claim, expected generation.WorkflowState, failure generation.Failure, now time.Time) (*generation.Workflow, error) {
	if !claim.Valid() || failure.Class != generation.FailureInfrastructure || failure.Validate() != nil || now.IsZero() {
		return nil, errors.New("generation infrastructure failure is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generation infrastructure failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationClaimTx(ctx, tx, claim, expected, now.UTC())
	if err != nil {
		return nil, err
	}
	if workflow.ActiveAgentRunID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
			WHERE id = ? AND status = ?`, agent.RunFailed, failure.Summary, now.UTC(), now.UTC(), workflow.ActiveAgentRunID, agent.RunRunning); err != nil {
			return nil, fmt.Errorf("fail generation agent run: %w", err)
		}
	}
	var updated *generation.Workflow
	if workflow.StateAttempt+1 >= generation.MaxStateAttempts {
		next := generation.StateFailed
		pausedAt := any(nil)
		lastError := failure.Summary
		if expected == generation.StateClassifying || expected == generation.StateChallengePublishing {
			next = generation.StateNeedsClassificationReview
			pausedAt = now.UTC()
			if expected == generation.StateClassifying {
				lastError = "classification_unavailable: " + failure.Summary
			} else {
				lastError = "publication_unavailable: " + failure.Summary
			}
		}
		updated, err = scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET state = ?, active_agent_run_id = NULL,
			state_attempt = 0, lease_owner = '', lease_expires_at = NULL, deadline_paused_at = ?, last_error = ?, next_run_at = ?, updated_at = ?
			WHERE id = ? RETURNING `+generationWorkflowColumns, next, pausedAt, lastError, now.UTC(), now.UTC(), workflow.ID))
	} else {
		updated, err = scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET active_agent_run_id = NULL, state_attempt = state_attempt + 1,
			lease_owner = '', lease_expires_at = NULL, last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ? RETURNING `+generationWorkflowColumns,
			failure.Summary, generation.NextRetry(workflow.StateAttempt+1, now), now.UTC(), workflow.ID))
	}
	if err != nil {
		return nil, fmt.Errorf("record generation infrastructure failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

// ReportGenerationArtifactFailure records a deterministic candidate defect and
// returns the authoring workflow to the same generator session for repair.
func (d *GenerationRepository) ReportGenerationArtifactFailure(ctx context.Context, claim generation.Claim, expected generation.WorkflowState, failure generation.Failure, report *generation.VerificationReport, now time.Time) error {
	if !claim.Valid() || failure.Class != generation.FailureArtifact || failure.Validate() != nil || now.IsZero() {
		return errors.New("generation artifact failure is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin generation artifact failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationClaimTx(ctx, tx, claim, expected, now.UTC())
	if err != nil {
		return err
	}
	postVerification := expected == generation.StateClassifying || expected == generation.StateChallengePublishing
	if workflow.CandidateRevisionID == "" {
		if expected != generation.StateGenerating || report != nil {
			return errors.New("generation artifact failure has no candidate revision")
		}
	} else {
		candidateRevision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, workflow.CandidateRevisionID))
		if err != nil {
			return err
		}
		if report != nil {
			if err := report.Validate(candidateRevision.Snapshot); err != nil {
				return err
			}
		}
		if !postVerification {
			encoded, err := marshalJSON(generation.Failure{Class: generation.FailureArtifact, Code: failure.Code, Summary: failure.Summary})
			if err != nil {
				return err
			}
			var reportJSON any
			if report != nil {
				reportJSON, err = marshalJSON(*report)
				if err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET failure = ?::jsonb, verification_report = COALESCE(?::jsonb, verification_report), updated_at = ? WHERE id = ?`, encoded, reportJSON, now.UTC(), workflow.CandidateRevisionID); err != nil {
				return fmt.Errorf("record candidate artifact failure: %w", err)
			}
		}
	}
	if workflow.ActiveAgentRunID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
			WHERE id = ? AND status = ?`, agent.RunFailed, failure.Summary, now.UTC(), now.UTC(), workflow.ActiveAgentRunID, agent.RunRunning); err != nil {
			return fmt.Errorf("fail generation agent run: %w", err)
		}
	}
	if postVerification {
		if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, active_agent_run_id = NULL, state_attempt = 0,
			lease_owner = '', lease_expires_at = NULL, deadline_paused_at = ?, last_error = ?, updated_at = ? WHERE id = ?`,
			generation.StateNeedsClassificationReview, now.UTC(), failure.Summary, now.UTC(), workflow.ID); err != nil {
			return fmt.Errorf("return post-verification failure to classification review: %w", err)
		}
	} else {
		var candidateCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM candidate_revisions WHERE source_kind = ? AND source_ref = ? AND source_revision = ?`,
			workflow.Source.Kind, workflow.Source.Ref, workflow.SourceRevision).Scan(&candidateCount); err != nil {
			return fmt.Errorf("count generation candidate revisions: %w", err)
		}
		next := generation.StateGenerating
		if candidateCount >= generation.MaxCandidateRevisions {
			next = generation.StateFailed
		}
		if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, active_agent_run_id = NULL, state_attempt = 0,
			lease_owner = CASE WHEN ? = ? THEN '' ELSE lease_owner END, lease_expires_at = CASE WHEN ? = ? THEN NULL ELSE lease_expires_at END,
			last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ?`,
			next, next, generation.StateFailed, next, generation.StateFailed, failure.Summary, now.UTC(), now.UTC(), workflow.ID); err != nil {
			return fmt.Errorf("return artifact failure to generation: %w", err)
		}
	}
	return tx.Commit()
}

func (d *GenerationRepository) CancelGenerationWorkflow(ctx context.Context, id, reason string, now time.Time) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(reason) == "" || now.IsZero() {
		return errors.New("generation cancellation requires workflow, reason, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin generation cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var state generation.WorkflowState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM generation_workflows WHERE id = ? FOR UPDATE`, id).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return ErrGenerationWorkflowNotFound
	} else if err != nil {
		return fmt.Errorf("load generation workflow for cancellation: %w", err)
	}
	if state.Terminal() {
		return ErrGenerationWorkflowNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
		WHERE owner_kind = ? AND owner_ref = ? AND status = ?`, agent.RunCancelled, reason, now.UTC(), now.UTC(),
		"generation-workflow", id, agent.RunRunning); err != nil {
		return fmt.Errorf("cancel generation agent runs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, lease_owner = '', lease_expires_at = NULL,
		active_agent_run_id = NULL, deadline_paused_at = NULL, last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ?`,
		generation.StateCancelled, reason, now.UTC(), now.UTC(), id); err != nil {
		return fmt.Errorf("cancel generation workflow: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit generation cancellation: %w", err)
	}
	return nil
}

func (d *GenerationRepository) RecoverExpiredGenerationWorkflows(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		return errors.New("generation deadline recovery requires current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := expireGenerationDeadlinesTx(ctx, tx, now.UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func expireGenerationDeadlinesTx(ctx context.Context, tx *Tx, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
		WHERE owner_kind = ? AND status = ? AND owner_ref IN (
			SELECT id FROM generation_workflows
			WHERE state IN (?, ?, ?, ?, ?, ?, ?) AND deadline_at IS NOT NULL AND deadline_at <= ?
		)`, agent.RunFailed, "generation execution deadline exceeded", now, now,
		"generation-workflow", agent.RunRunning, generation.StateGenerating, generation.StateJudging, generation.StateBuilding,
		generation.StateArtifactPublishing, generation.StateVerifying, generation.StateClassifying, generation.StateChallengePublishing, now.UTC()); err != nil {
		return fmt.Errorf("fail expired generation agent runs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, lease_owner = '', lease_expires_at = NULL,
		active_agent_run_id = NULL, state_attempt = 0, deadline_paused_at = ?,
		last_error = CASE state WHEN ? THEN 'classification_unavailable: generation execution deadline exceeded'
			WHEN ? THEN 'publication_unavailable: generation execution deadline exceeded' ELSE 'generation execution deadline exceeded' END,
		next_run_at = ?, updated_at = ?
		WHERE state IN (?, ?) AND deadline_at IS NOT NULL AND deadline_at <= ?`,
		generation.StateNeedsClassificationReview, now.UTC(), generation.StateClassifying, generation.StateChallengePublishing,
		now.UTC(), now.UTC(), generation.StateClassifying, generation.StateChallengePublishing, now.UTC()); err != nil {
		return fmt.Errorf("pause expired post-verification workflows: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, lease_owner = '', lease_expires_at = NULL,
		active_agent_run_id = NULL, state_attempt = 0, deadline_paused_at = NULL, last_error = 'generation execution deadline exceeded', next_run_at = ?, updated_at = ?
		WHERE state IN (?, ?, ?, ?, ?) AND deadline_at IS NOT NULL AND deadline_at <= ?`,
		generation.StateFailed, now.UTC(), now.UTC(), generation.StateGenerating, generation.StateJudging, generation.StateBuilding,
		generation.StateArtifactPublishing, generation.StateVerifying, now.UTC()); err != nil {
		return fmt.Errorf("fail expired generation workflows: %w", err)
	}
	return nil
}

func lockGenerationClaimTx(ctx context.Context, tx *Tx, claim generation.Claim, expected generation.WorkflowState, now time.Time) (*generation.Workflow, error) {
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND state = ? AND state_attempt = ?
		AND lease_owner = ? AND lease_expires_at > ? FOR UPDATE`, claim.Workflow.ID, expected, claim.StateAttempt, claim.LeaseOwner, now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generationLeaseLost()
	}
	if err != nil {
		return nil, fmt.Errorf("lock generation workflow lease: %w", err)
	}
	return workflow, nil
}

func generationLeaseLost() error { return generation.ErrLeaseLost }

func (d *GenerationRepository) GetCandidateRevision(ctx context.Context, id string) (*generation.Revision, error) {
	revision, err := scanCandidateRevision(d.conn.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrCandidateNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get candidate revision: %w", err)
	}
	return revision, nil
}

// FindLatestCandidateBefore returns the newest immutable candidate from an
// earlier authoring revision. It is only a presentation helper for the
// authoring diff; workflow state never derives from this query.
func (d *GenerationRepository) FindLatestCandidateBefore(ctx context.Context, sessionID string, revision int64) (*generation.Revision, error) {
	value, err := scanCandidateRevision(d.conn.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = (
		SELECT candidate_revision_id FROM authoring_revisions
		WHERE session_id = ? AND revision < ? AND candidate_revision_id <> ''
		ORDER BY revision DESC LIMIT 1
	)`, strings.TrimSpace(sessionID), revision))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find prior candidate revision: %w", err)
	}
	return value, nil
}

func insertCandidateRevisionTx(ctx context.Context, tx *Tx, revision generation.Revision) error {
	snapshot, err := marshalJSON(revision.Snapshot)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO candidate_revisions
		(id, source_kind, source_ref, source_revision, generator_session_id, generator_run_id, judge_run_id,
		parent_candidate_revision_id, repair_reason, archive_path, archive_sha256, execution_snapshot, created_at, updated_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?, ?, ?::jsonb, ?, ?)`,
		revision.ID, revision.Source.Kind, revision.Source.Ref, revision.SourceRevision, revision.GeneratorSessionID, revision.GeneratorRunID,
		revision.JudgeRunID, revision.ParentCandidateID, revision.RepairReason, revision.ArchivePath, revision.ArchiveSHA256, snapshot, revision.CreatedAt, revision.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert candidate revision: %w", err)
	}
	return nil
}

const candidateRevisionColumns = `id, source_kind, source_ref, source_revision, COALESCE(generator_session_id, ''), COALESCE(generator_run_id, ''), judge_run_id,
	COALESCE(parent_candidate_revision_id, ''), repair_reason, archive_path, archive_sha256, execution_snapshot, build_output, artifact_reference,
	verify_environment, verification_report, failure, classification_proposal, publication, created_at, updated_at, verified_at, published_at`
const candidateRevisionSelect = `SELECT ` + candidateRevisionColumns + ` FROM candidate_revisions`

func scanCandidateRevision(row agentRow) (*generation.Revision, error) {
	var revision generation.Revision
	var snapshot []byte
	var build, artifact, verifyEnvironment, verification, failure, classification, publication []byte
	var verifiedAt, publishedAt sql.NullTime
	err := row.Scan(&revision.ID, &revision.Source.Kind, &revision.Source.Ref, &revision.SourceRevision, &revision.GeneratorSessionID,
		&revision.GeneratorRunID, &revision.JudgeRunID, &revision.ParentCandidateID, &revision.RepairReason, &revision.ArchivePath, &revision.ArchiveSHA256, &snapshot,
		&build, &artifact, &verifyEnvironment, &verification, &failure, &classification, &publication, &revision.CreatedAt, &revision.UpdatedAt, &verifiedAt, &publishedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(snapshot, &revision.Snapshot); err != nil {
		return nil, fmt.Errorf("decode candidate snapshot: %w", err)
	}
	for _, value := range []struct {
		data   []byte
		target any
		name   string
	}{
		{build, &revision.Build, "build output"}, {artifact, &revision.Artifact, "artifact reference"},
		{verifyEnvironment, &revision.VerifyEnvironment, "verification environment"}, {verification, &revision.Verification, "verification report"},
		{failure, &revision.Failure, "failure"}, {classification, &revision.Classification, "classification proposal"}, {publication, &revision.Publication, "publication"},
	} {
		if len(value.data) == 0 || string(value.data) == "null" {
			continue
		}
		if err := json.Unmarshal(value.data, value.target); err != nil {
			return nil, fmt.Errorf("decode candidate %s: %w", value.name, err)
		}
	}
	if verifiedAt.Valid {
		value := verifiedAt.Time.UTC()
		revision.VerifiedAt = &value
	}
	if publishedAt.Valid {
		value := publishedAt.Time.UTC()
		revision.PublishedAt = &value
	}
	revision.CreatedAt = revision.CreatedAt.UTC()
	revision.UpdatedAt = revision.UpdatedAt.UTC()
	return &revision, nil
}

func workflowRuntimeTx(ctx context.Context, tx *Tx, candidateID string) (string, error) {
	var snapshot []byte
	if err := tx.QueryRowContext(ctx, `SELECT execution_snapshot FROM candidate_revisions WHERE id = ?`, candidateID).Scan(&snapshot); err != nil {
		return "", fmt.Errorf("load candidate execution snapshot: %w", err)
	}
	var value generation.ExecutionSnapshot
	if err := json.Unmarshal(snapshot, &value); err != nil {
		return "", fmt.Errorf("decode candidate execution snapshot: %w", err)
	}
	if err := value.Validate(); err != nil {
		return "", fmt.Errorf("validate candidate execution snapshot: %w", err)
	}
	return value.Runtime, nil
}

// ensureGeneratorSessionTx gives every authoring session one reusable model
// session for generator and judge repairs. It is owned by the workflow, not by
// a specific AgentRun.
func ensureGeneratorSessionTx(ctx context.Context, tx *Tx, session *authoring.Session) (string, error) {
	if session == nil || strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.UserID) == "" {
		return "", errors.New("generator session requires authoring session identity")
	}
	if id := strings.TrimSpace(session.GeneratorSessionID); id != "" {
		if err := lockActiveSessionTx(ctx, tx, id); err != nil && !errors.Is(err, agent.ErrRunActive) {
			return "", err
		}
		return id, nil
	}
	id := agent.NewID("generator-session")
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions
		(id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at)
		VALUES (?, 'generator', 'authoring-session', ?, ?, ?, ?, ?)`,
		id, session.ID, session.UserID, agent.SessionActive, now, now); err != nil {
		return "", fmt.Errorf("create generator agent session: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET generator_session_id = ?, updated_at = ?
		WHERE id = ? AND generator_session_id = ''`, id, nowText(now), session.ID)
	if err != nil {
		return "", fmt.Errorf("bind generator agent session: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return "", errors.New("generator session changed while creating workflow")
	}
	session.GeneratorSessionID = id
	return id, nil
}

func scanGenerationWorkflow(row agentRow) (*generation.Workflow, error) {
	var workflow generation.Workflow
	var leaseExpiresAt, deadlineAt, pausedAt sql.NullTime
	err := row.Scan(&workflow.ID, &workflow.Source.Kind, &workflow.Source.Ref, &workflow.SourceRevision, &workflow.State, &workflow.ClassificationRoadmapRevision, &workflow.ClassificationFeedback,
		&workflow.SupersededByWorkflowID, &workflow.CandidateRevisionID, &workflow.ActiveAgentRunID, &workflow.StateAttempt, &workflow.LeaseOwner, &leaseExpiresAt,
		&workflow.NextRunAt, &deadlineAt, &pausedAt, &workflow.LastError, &workflow.CreatedAt, &workflow.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if leaseExpiresAt.Valid {
		value := leaseExpiresAt.Time.UTC()
		workflow.LeaseExpiresAt = &value
	}
	if deadlineAt.Valid {
		value := deadlineAt.Time.UTC()
		workflow.DeadlineAt = &value
	}
	if pausedAt.Valid {
		value := pausedAt.Time.UTC()
		workflow.DeadlinePausedAt = &value
	}
	workflow.NextRunAt = workflow.NextRunAt.UTC()
	workflow.CreatedAt = workflow.CreatedAt.UTC()
	workflow.UpdatedAt = workflow.UpdatedAt.UTC()
	return &workflow, nil
}

func authoringSourceRevision(workflow generation.Workflow) (int64, error) {
	if workflow.Source.Kind != generation.SourceAuthoring {
		return 0, errors.New("generation workflow source is not authoring")
	}
	revision, err := strconv.ParseInt(workflow.SourceRevision, 10, 64)
	if err != nil || revision < 0 {
		return 0, errors.New("authoring generation workflow has an invalid source revision")
	}
	return revision, nil
}

type generationConfirmationAction string

const (
	confirmationStart       generationConfirmationAction = "start"
	confirmationContent     generationConfirmationAction = "content"
	confirmationPublication generationConfirmationAction = "publication"
)

type generationConfirmationReceipt struct {
	SessionID           string
	Action              generationConfirmationAction
	IdempotencyKey      string
	WorkflowID          string
	PlanRevision        *int64
	CandidateRevisionID *string
	ProposalRevision    *int
	CreatedAt           time.Time
}

func lockAuthoringSessionTx(ctx context.Context, tx *Tx, sessionID, userID string) error {
	query := `SELECT id FROM authoring_sessions WHERE id = ?`
	args := []any{sessionID}
	if strings.TrimSpace(userID) != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	query += ` FOR UPDATE`
	var ignored string
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&ignored); errors.Is(err, sql.ErrNoRows) {
		return authoring.ErrNotFound
	} else if err != nil {
		return fmt.Errorf("lock authoring session: %w", err)
	}
	return nil
}

func generationConfirmationReceiptTx(ctx context.Context, tx *Tx, sessionID string, action generationConfirmationAction, idempotencyKey string) (*generationConfirmationReceipt, error) {
	value := &generationConfirmationReceipt{SessionID: sessionID, Action: action, IdempotencyKey: idempotencyKey}
	var planRevision sql.NullInt64
	var candidateID sql.NullString
	var proposalRevision sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT workflow_id, plan_revision, candidate_revision_id, proposal_revision, created_at
		FROM generation_confirmation_receipts WHERE session_id = ? AND action = ? AND idempotency_key = ? FOR UPDATE`,
		sessionID, action, idempotencyKey).Scan(&value.WorkflowID, &planRevision, &candidateID, &proposalRevision, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read generation confirmation receipt: %w", err)
	}
	if planRevision.Valid {
		item := planRevision.Int64
		value.PlanRevision = &item
	}
	if candidateID.Valid {
		item := candidateID.String
		value.CandidateRevisionID = &item
	}
	if proposalRevision.Valid {
		item := int(proposalRevision.Int64)
		value.ProposalRevision = &item
	}
	value.CreatedAt = value.CreatedAt.UTC()
	return value, nil
}

func insertGenerationConfirmationReceiptTx(ctx context.Context, tx *Tx, value generationConfirmationReceipt) error {
	if strings.TrimSpace(value.SessionID) == "" || strings.TrimSpace(string(value.Action)) == "" || strings.TrimSpace(value.IdempotencyKey) == "" ||
		strings.TrimSpace(value.WorkflowID) == "" || value.CreatedAt.IsZero() {
		return errors.New("generation confirmation receipt is incomplete")
	}
	var planRevision, candidateRevisionID, proposalRevision any
	if value.PlanRevision != nil {
		planRevision = *value.PlanRevision
	}
	if value.CandidateRevisionID != nil {
		candidateRevisionID = *value.CandidateRevisionID
	}
	if value.ProposalRevision != nil {
		proposalRevision = *value.ProposalRevision
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO generation_confirmation_receipts
		(session_id, action, idempotency_key, workflow_id, plan_revision, candidate_revision_id, proposal_revision, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, value.SessionID, value.Action, value.IdempotencyKey, value.WorkflowID,
		planRevision, candidateRevisionID, proposalRevision, value.CreatedAt.UTC()); err != nil {
		return fmt.Errorf("record generation confirmation receipt: %w", err)
	}
	return nil
}

func generationWorkflowForReceiptTx(ctx context.Context, tx *Tx, workflowID string) (*generation.Workflow, error) {
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ?`, workflowID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGenerationWorkflowNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read generation confirmation workflow: %w", err)
	}
	return workflow, nil
}
