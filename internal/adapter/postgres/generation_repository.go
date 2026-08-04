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

	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

var ErrGenerationWorkflowNotFound = errors.New("generation workflow not found")

const generationWorkflowColumns = `id, source_kind, source_ref, source_revision, state, cleanup_intent,
	COALESCE(candidate_revision_id, ''), COALESCE(active_agent_run_id, ''), state_attempt, lease_owner, lease_expires_at, next_run_at,
	deadline_at, deadline_paused_at, last_error, created_at, updated_at`
const generationWorkflowSelect = `SELECT ` + generationWorkflowColumns + ` FROM generation_workflows`

func (d *GenerationRepository) CreateGenerationWorkflow(ctx context.Context, sessionID, userID string, revision int64, now time.Time) (*generation.Workflow, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || revision < 0 || now.IsZero() {
		return nil, errors.New("generation workflow requires authoring session, user, revision, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generation workflow: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	session, err := readAuthoringSessionTx(ctx, tx, sessionID, userID)
	if err != nil {
		return nil, err
	}
	if session.CurrentRevision != revision || session.State != authoring.StateIntentReview {
		return nil, authoring.ErrInvalidState
	}
	plan, err := readAuthoringRevisionTx(ctx, tx, sessionID, revision)
	if err != nil {
		return nil, err
	}
	if err := plan.Plan.ValidateForGeneration(); err != nil {
		return nil, err
	}
	if _, err := ensureGeneratorSessionTx(ctx, tx, session); err != nil {
		return nil, err
	}
	workflow := generation.Workflow{
		ID:             generation.NewID("generation-workflow"),
		Source:         generation.Source{Kind: generation.SourceAuthoring, Ref: sessionID},
		SourceRevision: strconv.FormatInt(revision, 10),
		State:          generation.StateQueued,
		NextRunAt:      now.UTC(),
		CreatedAt:      now.UTC(),
		UpdatedAt:      now.UTC(),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO generation_workflows
		(id, source_kind, source_ref, source_revision, state, cleanup_intent, candidate_revision_id, active_agent_run_id,
		state_attempt, lease_owner, next_run_at, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '', NULL, NULL, 0, '', ?, '', ?, ?)`,
		workflow.ID, workflow.Source.Kind, workflow.Source.Ref, workflow.SourceRevision, workflow.State, workflow.NextRunAt, workflow.CreatedAt, workflow.UpdatedAt); err != nil {
		return nil, fmt.Errorf("insert generation workflow: %w", err)
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
		AND state NOT IN (?, ?, ?) ORDER BY created_at DESC LIMIT 1`, generation.SourceAuthoring, sessionID, generation.StateCompleted, generation.StateFailed, generation.StateCancelled))
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
		WHERE state NOT IN (?, ?, ?, ?)
		AND next_run_at <= ?
		AND (lease_expires_at IS NULL OR lease_expires_at <= ?)
		ORDER BY next_run_at, created_at, id
		FOR UPDATE SKIP LOCKED LIMIT 1`, generation.StateNeedsAuthorReview, generation.StateCompleted, generation.StateFailed, generation.StateCancelled, now, now).Scan(&id)
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
	state := workflow.State
	if state == generation.StateQueued {
		state = generation.StateGenerating
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
		state = ?, state_attempt = ?, lease_owner = ?, lease_expires_at = ?, deadline_at = COALESCE(deadline_at, ?), updated_at = ?
		WHERE id = ? RETURNING `+generationWorkflowColumns, state, stateAttempt, owner, expires, deadline, now, workflow.ID))
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
	if input.Purpose != "generator" && input.Purpose != "judge" {
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
		(input.Purpose == "judge" && workflow.State != generation.StateJudging) {
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
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, cleanup_intent = ?, state_attempt = 0, last_error = '', next_run_at = ?, updated_at = ? WHERE id = ?`,
		next, generation.CleanupIntent(""), now.UTC(), now.UTC(), workflow.ID); err != nil {
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

func (d *GenerationRepository) ResumeGenerationForRevision(ctx context.Context, sessionID, userID string, revision int64, now time.Time) (*generation.Workflow, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || revision < 0 || now.IsZero() {
		return nil, errors.New("resume generation requires authoring session, user, revision, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin resume generation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	session, err := readAuthoringSessionTx(ctx, tx, sessionID, userID)
	if err != nil {
		return nil, err
	}
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE source_kind = ? AND source_ref = ? AND state = ? FOR UPDATE`, generation.SourceAuthoring, sessionID, generation.StateNeedsAuthorReview))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrInvalidState
	}
	if err != nil {
		return nil, err
	}
	if session.CurrentRevision != revision || workflow.DeadlinePausedAt == nil || workflow.DeadlineAt == nil {
		return nil, authoring.ErrInvalidState
	}
	plan, err := readAuthoringRevisionTx(ctx, tx, sessionID, revision)
	if err != nil {
		return nil, err
	}
	if err := plan.Plan.ValidateForGeneration(); err != nil {
		return nil, err
	}
	deadline := workflow.DeadlineAt.Add(now.UTC().Sub(*workflow.DeadlinePausedAt))
	updated, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET source_revision = ?, state = ?,
		state_attempt = 0, deadline_at = ?, deadline_paused_at = NULL, next_run_at = ?, last_error = '', updated_at = ?
		WHERE id = ? RETURNING `+generationWorkflowColumns, strconv.FormatInt(revision, 10), generation.StateGenerating, deadline, now.UTC(), now.UTC(), workflow.ID))
	if err != nil {
		return nil, fmt.Errorf("resume generation workflow: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func (d *GenerationRepository) BeginGenerationPublication(ctx context.Context, sessionID, userID string, publication generation.Publication, now time.Time) (*generation.Workflow, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || now.IsZero() || publication.ValidateIntent() != nil {
		return nil, errors.New("generation publication requires authoring session, user, publication intent, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generation publication: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := readAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE source_kind = ? AND source_ref = ? AND state = ? FOR UPDATE`, generation.SourceAuthoring, sessionID, generation.StateNeedsAuthorReview))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrInvalidState
	}
	if err != nil {
		return nil, err
	}
	if workflow.CandidateRevisionID == "" || workflow.DeadlinePausedAt == nil || workflow.DeadlineAt == nil {
		return nil, authoring.ErrInvalidState
	}
	candidateRevision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, workflow.CandidateRevisionID))
	if err != nil {
		return nil, err
	}
	if candidateRevision.Verification == nil || !candidateRevision.Verification.Passed || candidateRevision.Artifact == nil {
		return nil, authoring.ErrInvalidState
	}
	encodedPublication, err := marshalJSON(publication)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET publication = ?::jsonb, updated_at = ? WHERE id = ?`, encodedPublication, now.UTC(), candidateRevision.ID); err != nil {
		return nil, fmt.Errorf("record challenge publication intent: %w", err)
	}
	deadline := workflow.DeadlineAt.Add(now.UTC().Sub(*workflow.DeadlinePausedAt))
	updated, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET state = ?, state_attempt = 0,
		deadline_at = ?, deadline_paused_at = NULL, next_run_at = ?, last_error = '', updated_at = ?
		WHERE id = ? RETURNING `+generationWorkflowColumns, generation.StateChallengePublishing, deadline, now.UTC(), now.UTC(), workflow.ID))
	if err != nil {
		return nil, fmt.Errorf("start challenge publication: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func (d *GenerationRepository) CompleteGenerationChallengePublish(ctx context.Context, claim generation.Claim, artifact generation.ArtifactReference, now time.Time) error {
	if !claim.Valid() || now.IsZero() {
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
	if err := artifact.Validate(candidateRevision.Snapshot.Runtime); err != nil {
		return err
	}
	if candidateRevision.Publication == nil {
		return errors.New("generation challenge publication has no publication intent")
	}
	publication := *candidateRevision.Publication
	if err := publication.ValidateIntent(); err != nil {
		return err
	}
	publication.Artifact = &artifact
	encodedPublication, err := marshalJSON(publication)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET publication = ?::jsonb, published_at = ?, updated_at = ? WHERE id = ?`, encodedPublication, now.UTC(), now.UTC(), candidateRevision.ID); err != nil {
		return fmt.Errorf("record challenge publication: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET state = ?, publish_challenge_id = ?, updated_at = ? WHERE id = ?`, authoring.StatePublished, publication.ChallengeID, nowText(now), workflow.Source.Ref); err != nil {
		return fmt.Errorf("mark authoring session published: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, cleanup_intent = ?, state_attempt = 0,
			active_agent_run_id = NULL, last_error = '', next_run_at = ?, updated_at = ? WHERE id = ?`, generation.StateCleaningUp, generation.CleanupCompleted, now.UTC(), now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("start generation cleanup: %w", err)
	}
	return tx.Commit()
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
	updated, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET active_agent_run_id = NULL, state_attempt = state_attempt + 1,
		lease_owner = '', lease_expires_at = NULL, last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ? RETURNING `+generationWorkflowColumns,
		failure.Summary, generation.NextRetry(workflow.StateAttempt+1, now), now.UTC(), workflow.ID))
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
	if workflow.ActiveAgentRunID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
			WHERE id = ? AND status = ?`, agent.RunFailed, failure.Summary, now.UTC(), now.UTC(), workflow.ActiveAgentRunID, agent.RunRunning); err != nil {
			return fmt.Errorf("fail generation agent run: %w", err)
		}
	}
	if workflow.Source.Kind != generation.SourceAuthoring {
		return errors.New("generation workflow source is invalid")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, active_agent_run_id = NULL, state_attempt = 0,
		last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ?`, generation.StateGenerating, failure.Summary, now.UTC(), now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("return artifact failure to generator: %w", err)
	}
	return tx.Commit()
}

func (d *GenerationRepository) CompleteGenerationCleanup(ctx context.Context, claim generation.Claim, now time.Time) error {
	if !claim.Valid() || now.IsZero() {
		return errors.New("generation cleanup finalization is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin generation cleanup finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationClaimTx(ctx, tx, claim, generation.StateCleaningUp, now.UTC())
	if err != nil {
		return err
	}
	var next generation.WorkflowState
	switch workflow.CleanupIntent {
	case generation.CleanupCompleted:
		next = generation.StateCompleted
	case generation.CleanupFailed:
		next = generation.StateFailed
	case generation.CleanupCancelled:
		next = generation.StateCancelled
	default:
		return errors.New("generation cleanup intent is invalid")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, cleanup_intent = '', lease_owner = '', lease_expires_at = NULL,
		active_agent_run_id = NULL, updated_at = ? WHERE id = ?`, next, now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("complete generation cleanup: %w", err)
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
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, cleanup_intent = ?, lease_owner = '', lease_expires_at = NULL,
		active_agent_run_id = NULL, last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ?`,
		generation.StateCleaningUp, generation.CleanupCancelled, reason, now.UTC(), now.UTC(), id); err != nil {
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
			WHERE state NOT IN (?, ?, ?, ?, ?) AND deadline_at IS NOT NULL AND deadline_at <= ?
		)`, agent.RunFailed, "generation execution deadline exceeded", now, now,
		"generation-workflow", agent.RunRunning, generation.StateNeedsAuthorReview, generation.StateCleaningUp,
		generation.StateCompleted, generation.StateFailed, generation.StateCancelled, now.UTC()); err != nil {
		return fmt.Errorf("fail expired generation agent runs: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, cleanup_intent = ?, lease_owner = '', lease_expires_at = NULL,
		active_agent_run_id = NULL, deadline_paused_at = NULL, last_error = 'generation execution deadline exceeded', next_run_at = ?, updated_at = ?
		WHERE state NOT IN (?, ?, ?, ?, ?) AND deadline_at IS NOT NULL AND deadline_at <= ?`,
		generation.StateCleaningUp, generation.CleanupFailed, now, now, generation.StateNeedsAuthorReview, generation.StateCleaningUp,
		generation.StateCompleted, generation.StateFailed, generation.StateCancelled, now)
	if err != nil {
		return fmt.Errorf("expire generation workflows: %w", err)
	}
	_ = result
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
		archive_path, archive_sha256, execution_snapshot, created_at, updated_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?::jsonb, ?, ?)`,
		revision.ID, revision.Source.Kind, revision.Source.Ref, revision.SourceRevision, revision.GeneratorSessionID, revision.GeneratorRunID,
		revision.JudgeRunID, revision.ArchivePath, revision.ArchiveSHA256, snapshot, revision.CreatedAt, revision.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert candidate revision: %w", err)
	}
	return nil
}

const candidateRevisionColumns = `id, source_kind, source_ref, source_revision, COALESCE(generator_session_id, ''), COALESCE(generator_run_id, ''), judge_run_id,
	archive_path, archive_sha256, execution_snapshot, build_output, artifact_reference, verify_environment, verification_report,
	failure, publication, created_at, updated_at, verified_at, published_at`
const candidateRevisionSelect = `SELECT ` + candidateRevisionColumns + ` FROM candidate_revisions`

func scanCandidateRevision(row agentRow) (*generation.Revision, error) {
	var revision generation.Revision
	var snapshot []byte
	var build, artifact, verifyEnvironment, verification, failure, publication []byte
	var verifiedAt, publishedAt sql.NullTime
	err := row.Scan(&revision.ID, &revision.Source.Kind, &revision.Source.Ref, &revision.SourceRevision, &revision.GeneratorSessionID,
		&revision.GeneratorRunID, &revision.JudgeRunID, &revision.ArchivePath, &revision.ArchiveSHA256, &snapshot, &build,
		&artifact, &verifyEnvironment, &verification, &failure, &publication, &revision.CreatedAt, &revision.UpdatedAt, &verifiedAt, &publishedAt)
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
		{failure, &revision.Failure, "failure"}, {publication, &revision.Publication, "publication"},
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
	var cleanup string
	var leaseExpiresAt, deadlineAt, pausedAt sql.NullTime
	err := row.Scan(&workflow.ID, &workflow.Source.Kind, &workflow.Source.Ref, &workflow.SourceRevision, &workflow.State, &cleanup,
		&workflow.CandidateRevisionID, &workflow.ActiveAgentRunID, &workflow.StateAttempt, &workflow.LeaseOwner, &leaseExpiresAt,
		&workflow.NextRunAt, &deadlineAt, &pausedAt, &workflow.LastError, &workflow.CreatedAt, &workflow.UpdatedAt)
	if err != nil {
		return nil, err
	}
	workflow.CleanupIntent = generation.CleanupIntent(cleanup)
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
