package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/publication"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	scenariodomain "github.com/breakfix/breakfix/internal/domain/scenario"
)

var ErrGenerationWorkflowNotFound = errors.New("generation workflow not found")

const generationWorkflowColumns = `id, source_kind, source_ref, source_revision, state,
	COALESCE(candidate_revision_id, ''), workspace_snapshot_digest, COALESCE(active_agent_run_id, ''), state_version,
	agent_lease_owner, agent_lease_expires_at, next_run_at, last_error, finalizer_error_category, finalizer_last_error,
	finalizer_last_attempted_at, finalizer_next_retry_at, created_at, updated_at`
const generationWorkflowSelect = `SELECT ` + generationWorkflowColumns + ` FROM generation_workflows`

const candidateRevisionColumns = `id, source_kind, source_ref, source_revision, judge_run_id,
	COALESCE(parent_candidate_revision_id, ''), repair_reason, archive_path, archive_digest, content_revision, source_archive,
	runnable_revision_id, runnable_revision_digest, verification_report_id, verification_report_digest, failure, publication,
	created_at, updated_at, verified_at, published_at`
const candidateRevisionSelect = `SELECT ` + candidateRevisionColumns + ` FROM candidate_revisions`

func (d *GenerationRepository) CreateGenerationWorkflow(ctx context.Context, sessionID, userID string, confirmation generation.StartConfirmation, now time.Time) (*generation.Workflow, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || !confirmation.Valid() || now.IsZero() {
		return nil, errors.New("generation workflow requires authoring session, user, confirmation, and current time")
	}
	digest, err := generationActionRequestDigest(generationActionConfirmGeneration, confirmation)
	if err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generation workflow: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	if receipt, err := generationActionReceiptTx(ctx, tx, sessionID, generationActionConfirmGeneration, confirmation.IdempotencyKey, digest); err != nil {
		return nil, err
	} else if receipt != nil {
		if receipt.PlanRevision == nil || *receipt.PlanRevision != confirmation.PlanRevision {
			return nil, authoring.ErrVersionConflict
		}
		return generationWorkflowForReceiptTx(ctx, tx, receipt.WorkflowID)
	}
	planRevision := strconv.FormatInt(confirmation.PlanRevision, 10)
	if existing, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE source_kind = ? AND source_ref = ? AND source_revision = ? FOR UPDATE`, generation.SourceAuthoring, sessionID, planRevision)); err == nil {
		if err := insertGenerationActionReceiptTx(ctx, tx, generationActionReceipt{SessionID: sessionID, Action: generationActionConfirmGeneration, IdempotencyKey: confirmation.IdempotencyKey, RequestDigest: digest, WorkflowID: existing.ID, PlanRevision: &confirmation.PlanRevision, CreatedAt: now.UTC()}); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return existing, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	session, err := readAuthoringSessionTx(ctx, tx, sessionID, userID)
	if err != nil {
		return nil, err
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
	workflow := generation.Workflow{ID: generation.NewID("generation-workflow"), Source: generation.Source{Kind: generation.SourceAuthoring, Ref: sessionID}, SourceRevision: planRevision, State: generation.StateGenerating, StateVersion: 1, NextRunAt: now.UTC(), CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO generation_workflows (id, source_kind, source_ref, source_revision, state, candidate_revision_id, workspace_snapshot_digest, active_agent_run_id, state_version, agent_lease_owner, next_run_at, last_error, created_at, updated_at) VALUES (?, ?, ?, ?, ?, NULL, '', NULL, 1, '', ?, '', ?, ?)`, workflow.ID, workflow.Source.Kind, workflow.Source.Ref, workflow.SourceRevision, workflow.State, workflow.NextRunAt, workflow.CreatedAt, workflow.UpdatedAt); err != nil {
		return nil, fmt.Errorf("insert generation workflow: %w", err)
	}
	if err := insertGenerationActionReceiptTx(ctx, tx, generationActionReceipt{SessionID: sessionID, Action: generationActionConfirmGeneration, IdempotencyKey: confirmation.IdempotencyKey, RequestDigest: digest, WorkflowID: workflow.ID, PlanRevision: &confirmation.PlanRevision, CreatedAt: now.UTC()}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &workflow, nil
}

func (d *GenerationRepository) GetGenerationWorkflow(ctx context.Context, id string) (*generation.Workflow, error) {
	value, err := scanGenerationWorkflow(d.conn.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGenerationWorkflowNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get generation workflow: %w", err)
	}
	return value, nil
}

func (d *GenerationRepository) GetGenerationWorkflowForUser(ctx context.Context, workflowID, userID string) (*generation.Workflow, error) {
	if strings.TrimSpace(workflowID) == "" || strings.TrimSpace(userID) == "" {
		return nil, authoring.ErrNotFound
	}
	value, err := scanGenerationWorkflow(d.conn.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND source_kind = ? AND EXISTS (SELECT 1 FROM authoring_sessions session WHERE session.id = generation_workflows.source_ref AND session.user_id = ?)`, workflowID, generation.SourceAuthoring, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return value, nil
}

func (d *GenerationRepository) ListGenerationWorkflowsForUser(ctx context.Context, userID string) ([]generation.Workflow, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, authoring.ErrNotFound
	}
	rows, err := d.conn.QueryContext(ctx, generationWorkflowSelect+` WHERE source_kind = ? AND state NOT IN (?, ?, ?) AND EXISTS (SELECT 1 FROM authoring_sessions session WHERE session.id = generation_workflows.source_ref AND session.user_id = ?) ORDER BY updated_at DESC, id DESC`, generation.SourceAuthoring, generation.StatePublished, generation.StateFailed, generation.StateCancelled, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []generation.Workflow{}
	for rows.Next() {
		value, err := scanGenerationWorkflow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *value)
	}
	return result, rows.Err()
}

// ClaimGenerationAgentWorkflow is the only Generation lease. Runnable work
// is scheduled separately in runnable_actions and never uses this table.
func (d *GenerationRepository) ClaimGenerationAgentWorkflow(ctx context.Context, serverID string, ttl time.Duration, now time.Time) (*generation.Claim, error) {
	if strings.TrimSpace(serverID) == "" || ttl <= 0 || now.IsZero() {
		return nil, errors.New("generation agent claim is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM generation_workflows WHERE state = ? AND next_run_at <= ? AND (agent_lease_expires_at IS NULL OR agent_lease_expires_at <= ?) ORDER BY next_run_at, created_at, id FOR UPDATE SKIP LOCKED LIMIT 1`, generation.StateJudging, now.UTC(), now.UTC()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	owner := strings.TrimSpace(serverID) + "-" + generation.NewID("agent-lease")
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET agent_lease_owner = ?, agent_lease_expires_at = ?, updated_at = ? WHERE id = ? RETURNING `+generationWorkflowColumns, owner, now.UTC().Add(ttl), now.UTC(), id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &generation.Claim{Workflow: *workflow, LeaseCredential: generation.LeaseCredential{StateVersion: workflow.StateVersion, LeaseOwner: owner}}, nil
}

func (d *GenerationRepository) RenewGenerationLease(ctx context.Context, claim generation.Claim, ttl time.Duration, now time.Time) error {
	if !claim.Valid() || ttl <= 0 || now.IsZero() {
		return generation.ErrLeaseLost
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE generation_workflows SET agent_lease_expires_at = ?, updated_at = ? WHERE id = ? AND state = ? AND state_version = ? AND agent_lease_owner = ? AND agent_lease_expires_at > ?`, now.UTC().Add(ttl), now.UTC(), claim.Workflow.ID, generation.StateJudging, claim.StateVersion, claim.LeaseOwner, now.UTC())
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return generation.ErrLeaseLost
	}
	return nil
}

func (d *GenerationRepository) LoadGenerationContext(ctx context.Context, claim generation.Claim, now time.Time) (*generation.Context, error) {
	if !claim.Valid() || now.IsZero() {
		return nil, generation.ErrLeaseLost
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationAgentClaimTx(ctx, tx, claim, now)
	if err != nil {
		return nil, err
	}
	planRevision, err := authoringSourceRevision(*workflow)
	if err != nil {
		return nil, err
	}
	plan, err := readAuthoringRevisionTx(ctx, tx, workflow.Source.Ref, planRevision)
	if err != nil {
		return nil, err
	}
	result := &generation.Context{Workflow: *workflow, Plan: plan.Plan}
	if workflow.CandidateRevisionID != "" {
		candidate, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ?`, workflow.CandidateRevisionID))
		if err != nil {
			return nil, err
		}
		copy := *candidate
		copy.ArchivePath = ""
		result.Candidate = &copy
		if candidate.Failure != nil {
			result.Feedback = generation.Feedback{Summary: candidate.Failure.Summary, Issues: []generation.Issue{{Code: candidate.Failure.Code, Message: candidate.Failure.Summary}}}
		}
	}
	if result.Feedback.Empty() && workflow.LastError != "" {
		result.Feedback = generation.Feedback{Summary: workflow.LastError, Issues: []generation.Issue{{Code: "WORKFLOW_FEEDBACK", Message: workflow.LastError}}}
	}
	if err := result.Feedback.Validate(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (d *GenerationRepository) StartGenerationAgentRun(ctx context.Context, claim generation.Claim, input agent.CreateRun, now time.Time) (*agent.Run, error) {
	if !claim.Valid() || input.OwnerKind != "generation-workflow" || input.OwnerRef != claim.Workflow.ID || input.Purpose != "judge" || now.IsZero() {
		return nil, errors.New("generation agent run is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationAgentClaimTx(ctx, tx, claim, now)
	if err != nil {
		return nil, err
	}
	if workflow.ActiveAgentRunID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ? WHERE id = ? AND status = ?`, agent.RunInterrupted, "generation agent lease replaced", now.UTC(), now.UTC(), workflow.ActiveAgentRunID, agent.RunRunning); err != nil {
			return nil, err
		}
	}
	created, err := createRunTx(ctx, tx, input, now.UTC())
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET active_agent_run_id = ?, updated_at = ? WHERE id = ?`, created.ID, now.UTC(), workflow.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return created, nil
}

func (d *GenerationRepository) RetryGenerationAgentRun(ctx context.Context, claim generation.Claim, runID, message string, now time.Time) (*agent.Run, error) {
	if !claim.Valid() || strings.TrimSpace(runID) == "" || strings.TrimSpace(message) == "" || now.IsZero() {
		return nil, generation.ErrLeaseLost
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationAgentClaimTx(ctx, tx, claim, now)
	if err != nil {
		return nil, err
	}
	if workflow.ActiveAgentRunID != runID {
		return nil, generation.ErrLeaseLost
	}
	run, err := scanAgentRun(tx.QueryRowContext(ctx, agentRunSelect+` WHERE id = ? FOR UPDATE`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agent.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if run.Attempt < agent.MaxAttempts && run.DeadlineAt.After(now.UTC()) {
		next, err := scanAgentRun(tx.QueryRowContext(ctx, `UPDATE agent_runs SET attempt = attempt + 1, last_error = ?, updated_at = ? WHERE id = ? AND status = ? AND attempt = ? RETURNING `+agentRunColumns, strings.TrimSpace(message), now.UTC(), run.ID, agent.RunRunning, run.Attempt))
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return next, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ? WHERE id = ? AND status = ?`, agent.RunFailed, strings.TrimSpace(message), now.UTC(), now.UTC(), run.ID, agent.RunRunning); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1, active_agent_run_id = NULL, agent_lease_owner = '', agent_lease_expires_at = NULL, last_error = ?, updated_at = ? WHERE id = ?`, generation.StateFailed, strings.TrimSpace(message), now.UTC(), workflow.ID); err != nil {
		return nil, err
	}
	return nil, tx.Commit()
}

func (d *GenerationRepository) InterruptActiveGenerationAgentRuns(ctx context.Context, reason string, now time.Time) error {
	if strings.TrimSpace(reason) == "" || now.IsZero() {
		return errors.New("generation agent interruption is invalid")
	}
	_, err := d.conn.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ? WHERE id IN (SELECT active_agent_run_id FROM generation_workflows WHERE state = ? AND active_agent_run_id IS NOT NULL) AND status = ?`, agent.RunInterrupted, reason, now.UTC(), now.UTC(), generation.StateJudging, agent.RunRunning)
	if err != nil {
		return err
	}
	_, err = d.conn.ExecContext(ctx, `UPDATE generation_workflows SET active_agent_run_id = NULL, agent_lease_owner = '', agent_lease_expires_at = NULL, next_run_at = ?, updated_at = ? WHERE state = ?`, now.UTC(), now.UTC(), generation.StateJudging)
	return err
}

func (d *GenerationRepository) FindSubmittedGenerationCandidate(ctx context.Context, sessionID, userID string, submission generation.CandidateSubmission) (*generation.Revision, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || !submission.Valid() {
		return nil, errors.New("candidate submission is invalid")
	}
	digest, err := generationActionRequestDigest(generationActionSubmitCandidate, submission)
	if err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	receipt, err := generationActionReceiptTx(ctx, tx, sessionID, generationActionSubmitCandidate, submission.IdempotencyKey, digest)
	if err != nil {
		return nil, err
	}
	if receipt == nil {
		return nil, tx.Commit()
	}
	if receipt.WorkflowID != submission.WorkflowID || receipt.CandidateRevisionID == nil {
		return nil, authoring.ErrVersionConflict
	}
	return scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ?`, *receipt.CandidateRevisionID))
}

func (d *GenerationRepository) SubmitGenerationCandidate(ctx context.Context, sessionID, userID string, submission generation.CandidateSubmission, revision generation.Revision, now time.Time) (*generation.Revision, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || !submission.Valid() || now.IsZero() || revision.ID == "" || revision.Source.Kind != "" || revision.Source.Ref != "" || revision.SourceRevision != "" {
		return nil, errors.New("candidate submission is invalid")
	}
	digest, err := generationActionRequestDigest(generationActionSubmitCandidate, submission)
	if err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	if receipt, err := generationActionReceiptTx(ctx, tx, sessionID, generationActionSubmitCandidate, submission.IdempotencyKey, digest); err != nil {
		return nil, err
	} else if receipt != nil {
		if receipt.WorkflowID != submission.WorkflowID || receipt.CandidateRevisionID == nil {
			return nil, authoring.ErrVersionConflict
		}
		return scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ?`, *receipt.CandidateRevisionID))
	}
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND source_kind = ? AND source_ref = ? AND state = ? FOR UPDATE`, submission.WorkflowID, generation.SourceAuthoring, sessionID, generation.StateGenerating))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrInvalidState
	}
	if err != nil {
		return nil, err
	}
	workspace, err := scanGeneratorWorkspace(tx.QueryRowContext(ctx, generatorWorkspaceSelect+` WHERE workflow_id = ? AND state = ? AND active_turn_id = ? ORDER BY created_at DESC, workspace_id DESC LIMIT 1 FOR UPDATE`, workflow.ID, generation.WorkspaceActive, submission.TurnID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrWorkspaceTurnLost
	}
	if err != nil {
		return nil, err
	}
	revision.Source, revision.SourceRevision, revision.CreatedAt, revision.UpdatedAt = workflow.Source, workflow.SourceRevision, now.UTC(), now.UTC()
	if workflow.CandidateRevisionID != "" {
		revision.ParentCandidateID, revision.RepairReason = workflow.CandidateRevisionID, workflow.LastError
	}
	if err := revision.ValidateForCreate(); err != nil {
		return nil, err
	}
	if err := insertCandidateRevisionTx(ctx, tx, revision); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generator_workspaces SET active_turn_id = '', idle_since = NULL, updated_at = ? WHERE workspace_id = ? AND active_turn_id = ?`, now.UTC(), workspace.ID, submission.TurnID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, candidate_revision_id = ?, workspace_snapshot_digest = '', active_agent_run_id = NULL, state_version = state_version + 1, last_error = '', next_run_at = ?, updated_at = ? WHERE id = ?`, generation.StateJudging, revision.ID, now.UTC(), now.UTC(), workflow.ID); err != nil {
		return nil, err
	}
	if err := insertGenerationActionReceiptTx(ctx, tx, generationActionReceipt{SessionID: sessionID, Action: generationActionSubmitCandidate, IdempotencyKey: submission.IdempotencyKey, RequestDigest: digest, WorkflowID: workflow.ID, CandidateRevisionID: &revision.ID, CreatedAt: now.UTC()}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &revision, nil
}

func (d *GenerationRepository) FinalizeGenerationJudgement(ctx context.Context, claim generation.Claim, runID string, approved bool, feedback string, now time.Time) error {
	if !claim.Valid() || strings.TrimSpace(runID) == "" || now.IsZero() || (!approved && strings.TrimSpace(feedback) == "") {
		return errors.New("generation judgement finalization is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationAgentClaimTx(ctx, tx, claim, now)
	if err != nil {
		return err
	}
	if workflow.ActiveAgentRunID != runID || workflow.CandidateRevisionID == "" {
		return generation.ErrLeaseLost
	}
	if err := completeRunTx(ctx, tx, runID, now.UTC()); err != nil {
		return err
	}
	if approved {
		if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET judge_run_id = ?, updated_at = ? WHERE id = ?`, runID, now.UTC(), workflow.CandidateRevisionID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1, active_agent_run_id = NULL, agent_lease_owner = '', agent_lease_expires_at = NULL, last_error = '', next_run_at = ?, updated_at = ? WHERE id = ?`, generation.StateMaterializingArtifact, now.UTC(), now.UTC(), workflow.ID)
	} else {
		failure, encodeErr := marshalJSON(generation.Failure{Class: generation.FailureArtifact, Code: "JUDGE_REJECT", Summary: feedback})
		if encodeErr != nil {
			return encodeErr
		}
		if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET judge_run_id = ?, failure = ?::jsonb, updated_at = ? WHERE id = ?`, runID, failure, now.UTC(), workflow.CandidateRevisionID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1, active_agent_run_id = NULL, agent_lease_owner = '', agent_lease_expires_at = NULL, last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ?`, generation.StateGenerating, feedback, now.UTC(), now.UTC(), workflow.ID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ListRunnableGenerationCandidates is the Server restart outbox. It exposes
// no provider lease or artifact data; callers schedule only public actions.
func (d *GenerationRepository) ListRunnableGenerationCandidates(ctx context.Context) ([]generation.Workflow, error) {
	rows, err := d.conn.QueryContext(ctx, generationWorkflowSelect+` WHERE state IN (?, ?) ORDER BY next_run_at, created_at, id`, generation.StateMaterializingArtifact, generation.StateVerifying)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []generation.Workflow{}
	for rows.Next() {
		value, err := scanGenerationWorkflow(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *value)
	}
	return result, rows.Err()
}

func (d *GenerationRepository) CandidateForWorkflow(ctx context.Context, workflowID string) (*generation.Revision, error) {
	var candidateID string
	if err := d.conn.QueryRowContext(ctx, `SELECT candidate_revision_id FROM generation_workflows WHERE id = ?`, workflowID).Scan(&candidateID); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGenerationWorkflowNotFound
	} else if err != nil {
		return nil, err
	}
	return d.GetCandidateRevision(ctx, candidateID)
}

func (d *GenerationRepository) MarkGenerationCandidateMaterialized(ctx context.Context, workflowID string, reference runnable.RevisionReference, now time.Time) error {
	if strings.TrimSpace(workflowID) == "" || reference.Validate() != nil || now.IsZero() {
		return errors.New("generation materialization is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? FOR UPDATE`, workflowID))
	if err != nil {
		return err
	}
	if workflow.State != generation.StateMaterializingArtifact || workflow.CandidateRevisionID == "" {
		return generation.ErrCandidateInvalidState
	}
	candidate, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, workflow.CandidateRevisionID))
	if err != nil {
		return err
	}
	if err := validateGenerationRunnableRevisionTx(ctx, tx, *candidate, reference); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET runnable_revision_id = ?, runnable_revision_digest = ?, updated_at = ? WHERE id = ?`, reference.ID, reference.Digest, now.UTC(), candidate.ID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1, next_run_at = ?, updated_at = ? WHERE id = ? AND state = ? AND state_version = ?`, generation.StateVerifying, now.UTC(), now.UTC(), workflow.ID, generation.StateMaterializingArtifact, workflow.StateVersion)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return generation.ErrLeaseLost
	}
	return tx.Commit()
}

func (d *GenerationRepository) MarkGenerationCandidateVerified(ctx context.Context, workflowID string, reference runnable.VerificationReportReference, now time.Time) error {
	if strings.TrimSpace(workflowID) == "" || reference.Validate() != nil || now.IsZero() {
		return errors.New("generation verification is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? FOR UPDATE`, workflowID))
	if err != nil {
		return err
	}
	if workflow.State != generation.StateVerifying || workflow.CandidateRevisionID == "" {
		return generation.ErrCandidateInvalidState
	}
	candidate, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, workflow.CandidateRevisionID))
	if err != nil {
		return err
	}
	if candidate.RunnableRevisionRef == nil {
		return generation.ErrCandidateInvalidState
	}
	passed, err := validateGenerationVerificationReportTx(ctx, tx, *candidate.RunnableRevisionRef, reference)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET verification_report_id = ?, verification_report_digest = ?, verified_at = ?, updated_at = ? WHERE id = ?`, reference.ID, reference.Digest, now.UTC(), now.UTC(), candidate.ID); err != nil {
		return err
	}
	state, lastError := generation.StateNeedsAuthorReview, ""
	if !passed {
		state, lastError = generation.StateGenerating, "runnable verification did not pass"
		failure, _ := marshalJSON(generation.Failure{Class: generation.FailureArtifact, Code: "VERIFY_FAILED", Summary: lastError})
		if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET failure = ?::jsonb, updated_at = ? WHERE id = ?`, failure, now.UTC(), candidate.ID); err != nil {
			return err
		}
	}
	if passed {
		revision, err := authoringSourceRevision(*workflow)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE authoring_revisions SET candidate_revision_id = ? WHERE session_id = ? AND revision = ?`, candidate.ID, workflow.Source.Ref, revision); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET visible_revision = ?, updated_at = ? WHERE id = ?`, revision, nowText(now), workflow.Source.Ref); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1, last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ?`, state, lastError, now.UTC(), now.UTC(), workflow.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *GenerationRepository) ConfirmGenerationContent(ctx context.Context, sessionID, userID string, confirmation generation.ContentConfirmation, metadata generation.PublicationMetadata, now time.Time) (*generation.Workflow, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || !confirmation.Valid() || metadata.Validate() != nil || now.IsZero() {
		return nil, errors.New("content confirmation is invalid")
	}
	digest, err := generationActionRequestDigest(generationActionConfirmContent, confirmation)
	if err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	if receipt, err := generationActionReceiptTx(ctx, tx, sessionID, generationActionConfirmContent, confirmation.IdempotencyKey, digest); err != nil {
		return nil, err
	} else if receipt != nil {
		return generationWorkflowForReceiptTx(ctx, tx, receipt.WorkflowID)
	}
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND source_ref = ? FOR UPDATE`, confirmation.WorkflowID, sessionID))
	if err != nil {
		return nil, authoring.ErrInvalidState
	}
	if workflow.State != generation.StateNeedsAuthorReview || workflow.CandidateRevisionID != confirmation.CandidateRevisionID {
		return nil, generation.ErrCandidateInvalidState
	}
	candidate, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, confirmation.CandidateRevisionID))
	if err != nil {
		return nil, err
	}
	if candidate.RunnableRevisionRef == nil || candidate.VerificationReportRef == nil {
		return nil, generation.ErrCandidateInvalidState
	}
	if passed, err := validateGenerationVerificationReportTx(ctx, tx, *candidate.RunnableRevisionRef, *candidate.VerificationReportRef); err != nil || !passed {
		return nil, generation.ErrCandidateInvalidState
	}
	session, err := readAuthoringSessionTx(ctx, tx, sessionID, userID)
	if err != nil {
		return nil, err
	}
	publication, err := prepareGenerationPublicationTx(ctx, tx, *session, candidate, metadata, now)
	if err != nil {
		return nil, err
	}
	encoded, err := marshalJSON(publication)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET publication = ?::jsonb, updated_at = ? WHERE id = ?`, encoded, now.UTC(), candidate.ID); err != nil {
		return nil, err
	}
	updated, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1, last_error = '', next_run_at = ?, updated_at = ? WHERE id = ? RETURNING `+generationWorkflowColumns, generation.StatePublishing, now.UTC(), now.UTC(), workflow.ID))
	if err != nil {
		return nil, err
	}
	if err := insertGenerationActionReceiptTx(ctx, tx, generationActionReceipt{SessionID: sessionID, Action: generationActionConfirmContent, IdempotencyKey: confirmation.IdempotencyKey, RequestDigest: digest, WorkflowID: workflow.ID, CandidateRevisionID: &candidate.ID, CreatedAt: now.UTC()}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func (d *GenerationRepository) RequestGenerationContentChanges(ctx context.Context, sessionID, userID string, request generation.ContentChangeRequest, now time.Time) (*generation.Workflow, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || !request.Valid() || now.IsZero() {
		return nil, errors.New("content change request is invalid")
	}
	digest, err := generationActionRequestDigest(generationActionRequestContentChanges, request)
	if err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	if receipt, err := generationActionReceiptTx(ctx, tx, sessionID, generationActionRequestContentChanges, request.IdempotencyKey, digest); err != nil {
		return nil, err
	} else if receipt != nil {
		return generationWorkflowForReceiptTx(ctx, tx, receipt.WorkflowID)
	}
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND source_ref = ? FOR UPDATE`, request.WorkflowID, sessionID))
	if err != nil {
		return nil, authoring.ErrInvalidState
	}
	if workflow.State != generation.StateNeedsAuthorReview || workflow.CandidateRevisionID != request.CandidateRevisionID {
		return nil, generation.ErrCandidateInvalidState
	}
	updated, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1, last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ? RETURNING `+generationWorkflowColumns, generation.StateGenerating, request.Feedback, now.UTC(), now.UTC(), workflow.ID))
	if err != nil {
		return nil, err
	}
	if err := insertGenerationActionReceiptTx(ctx, tx, generationActionReceipt{SessionID: sessionID, Action: generationActionRequestContentChanges, IdempotencyKey: request.IdempotencyKey, RequestDigest: digest, WorkflowID: workflow.ID, CandidateRevisionID: &request.CandidateRevisionID, CreatedAt: now.UTC()}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func (d *GenerationRepository) PendingGenerationPublicationFinalizations(ctx context.Context, now time.Time) ([]generation.PublicationFinalization, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT `+generationWorkflowColumns+` FROM generation_workflows WHERE state = ? AND (finalizer_next_retry_at IS NULL OR finalizer_next_retry_at <= ?) ORDER BY updated_at, id`, generation.StatePublishing, now.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []generation.PublicationFinalization{}
	for rows.Next() {
		workflow, err := scanGenerationWorkflow(rows)
		if err != nil {
			return nil, err
		}
		candidate, err := d.GetCandidateRevision(ctx, workflow.CandidateRevisionID)
		if err != nil {
			return nil, err
		}
		result = append(result, generation.PublicationFinalization{Workflow: *workflow, Candidate: *candidate})
	}
	return result, rows.Err()
}

func (d *GenerationRepository) RecordGenerationPublicationFinalizerFailure(ctx context.Context, workflowID, candidateID string, diagnostic publication.Diagnostic) (*generation.Workflow, error) {
	if strings.TrimSpace(workflowID) == "" || strings.TrimSpace(candidateID) == "" || diagnostic.Validate() != nil {
		return nil, errors.New("generation finalizer diagnostic is invalid")
	}
	state, next := generation.StatePublishing, diagnostic.LastAttemptedAt.UTC()
	if diagnostic.NextRetryAt != nil {
		next = diagnostic.NextRetryAt.UTC()
	}
	if diagnostic.Category == publication.CategoryDeterministic {
		state = generation.StateFailed
	}
	var retry any
	if diagnostic.NextRetryAt != nil {
		retry = diagnostic.NextRetryAt.UTC()
	}
	value, err := scanGenerationWorkflow(d.conn.QueryRowContext(ctx, `UPDATE generation_workflows SET state = ?, next_run_at = ?, last_error = ?, finalizer_error_category = ?, finalizer_last_error = ?, finalizer_last_attempted_at = ?, finalizer_next_retry_at = ?, updated_at = ? WHERE id = ? AND candidate_revision_id = ? AND state = ? RETURNING `+generationWorkflowColumns, state, next, diagnostic.LastError, diagnostic.Category, diagnostic.LastError, diagnostic.LastAttemptedAt.UTC(), retry, diagnostic.LastAttemptedAt.UTC(), workflowID, candidateID, generation.StatePublishing))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrCandidateInvalidState
	}
	if err != nil {
		return nil, err
	}
	return value, nil
}

func (d *GenerationRepository) FinalizeGenerationScenarioPublication(ctx context.Context, workflowID, candidateID, contentRevision, materializedRevision string, scenarioType scenario.ScenarioType, tags []string, now time.Time) error {
	if strings.TrimSpace(workflowID) == "" || strings.TrimSpace(candidateID) == "" || !scenario.ValidRevision(contentRevision) || !scenario.ValidRevision(materializedRevision) || !scenarioType.Valid() || now.IsZero() {
		return errors.New("generation publication finalization is invalid")
	}
	canonical, err := scenario.NormalizeTags(tags)
	if err != nil || !slices.Equal(canonical, tags) || (scenarioType == scenario.ScenarioDocumentationExample && len(tags) != 0) {
		return errors.New("generation publication tags are invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND state = ? FOR UPDATE`, workflowID, generation.StatePublishing))
	if err != nil {
		return generation.ErrCandidateInvalidState
	}
	if workflow.CandidateRevisionID != candidateID {
		return generation.ErrCandidateInvalidState
	}
	candidate, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, candidateID))
	if err != nil {
		return err
	}
	if candidate.Publication == nil || candidate.PublishedAt != nil || candidate.ContentRevision != contentRevision || candidate.RunnableRevisionRef == nil || candidate.VerificationReportRef == nil {
		return generation.ErrCandidateInvalidState
	}
	publication := *candidate.Publication
	if err := publication.ValidateIntent(); err != nil {
		return err
	}
	session, err := readAuthoringSessionTx(ctx, tx, workflow.Source.Ref, "")
	if err != nil {
		return err
	}
	if session.RevisionScenarioID == "" {
		stable := scenariodomain.Scenario{ID: publication.ScenarioID, SourceKind: scenariodomain.SourceAuthoring, SourceRef: workflow.Source.Ref, OwnerUserID: session.UserID, State: scenariodomain.StateActive, ActiveRevisionID: publication.ScenarioRevisionID, SourceSlug: publication.SourceSlug, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
		published := scenarioRevisionFromPublication(publication.ScenarioTitle, scenarioType, tags, contentRevision, materializedRevision, *candidate.RunnableRevisionRef, *candidate.VerificationReportRef, publication.ScenarioRevisionID, publication.ScenarioID, workflow.Source.Ref, workflow.SourceRevision, "", publication.SourceSlug, publication.TargetPath, scenariodomain.SourceAuthoring, now.UTC())
		if err := insertPersistedScenarioTx(ctx, tx, stable); err != nil {
			return err
		}
		if err := insertPersistedScenarioRevisionTx(ctx, tx, published); err != nil {
			return err
		}
	} else if err := finalizeAuthoringRevisionTx(ctx, tx, *session, workflow, candidate, publication, scenarioType, tags, materializedRevision, now.UTC()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET published_at = ?, updated_at = ? WHERE id = ?`, now.UTC(), now.UTC(), candidate.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET state = ?, publish_scenario_id = ?, updated_at = ? WHERE id = ?`, authoring.StatePublished, publication.ScenarioID, nowText(now), workflow.Source.Ref); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1, last_error = '', finalizer_error_category = '', finalizer_last_error = '', finalizer_last_attempted_at = NULL, finalizer_next_retry_at = NULL, updated_at = ? WHERE id = ?`, generation.StatePublished, now.UTC(), workflow.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *GenerationRepository) ReportGenerationArtifactFailure(ctx context.Context, claim generation.Claim, expected generation.WorkflowState, failure generation.Failure, now time.Time) error {
	if !claim.Valid() || expected != generation.StateJudging || failure.Validate() != nil || now.IsZero() {
		return generation.ErrLeaseLost
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationAgentClaimTx(ctx, tx, claim, now)
	if err != nil {
		return err
	}
	if workflow.CandidateRevisionID == "" {
		return generation.ErrCandidateInvalidState
	}
	encoded, err := marshalJSON(failure)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET failure = ?::jsonb, updated_at = ? WHERE id = ?`, encoded, now.UTC(), workflow.CandidateRevisionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1, active_agent_run_id = NULL, agent_lease_owner = '', agent_lease_expires_at = NULL, last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ?`, generation.StateGenerating, failure.Summary, now.UTC(), now.UTC(), workflow.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *GenerationRepository) CancelGenerationWorkflow(ctx context.Context, sessionID, userID string, cancellation generation.Cancellation, now time.Time) (*generation.Workflow, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || !cancellation.Valid() || now.IsZero() {
		return nil, errors.New("generation cancellation is invalid")
	}
	digest, err := generationActionRequestDigest(generationActionCancelGeneration, cancellation)
	if err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	if receipt, err := generationActionReceiptTx(ctx, tx, sessionID, generationActionCancelGeneration, cancellation.IdempotencyKey, digest); err != nil {
		return nil, err
	} else if receipt != nil {
		return generationWorkflowForReceiptTx(ctx, tx, receipt.WorkflowID)
	}
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND source_ref = ? FOR UPDATE`, cancellation.WorkflowID, sessionID))
	if err != nil {
		return nil, authoring.ErrNotFound
	}
	if workflow.State.Terminal() {
		return nil, generation.ErrCandidateInvalidState
	}
	updated, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1, active_agent_run_id = NULL, agent_lease_owner = '', agent_lease_expires_at = NULL, last_error = 'cancelled', updated_at = ? WHERE id = ? RETURNING `+generationWorkflowColumns, generation.StateCancelled, now.UTC(), workflow.ID))
	if err != nil {
		return nil, err
	}
	if err := insertGenerationActionReceiptTx(ctx, tx, generationActionReceipt{SessionID: sessionID, Action: generationActionCancelGeneration, IdempotencyKey: cancellation.IdempotencyKey, RequestDigest: digest, WorkflowID: workflow.ID, CreatedAt: now.UTC()}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func (d *GenerationRepository) GetCandidateRevision(ctx context.Context, id string) (*generation.Revision, error) {
	value, err := scanCandidateRevision(d.conn.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrCandidateNotFound
	}
	if err != nil {
		return nil, err
	}
	return value, nil
}

func validateGenerationRunnableRevisionTx(ctx context.Context, tx *Tx, candidate generation.Revision, reference runnable.RevisionReference) error {
	var kind, contentID, contentRevision string
	err := tx.QueryRowContext(ctx, `SELECT specs.content_kind, specs.content_id, specs.content_revision FROM runnable_revisions revisions JOIN runnable_specs specs ON specs.spec_digest = revisions.spec_digest WHERE revisions.id = ? AND revisions.runnable_revision_digest = ? FOR UPDATE`, reference.ID, reference.Digest).Scan(&kind, &contentID, &contentRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrRunnableRevisionNotFound
	}
	if err != nil {
		return err
	}
	if kind != "operations" || contentID != candidate.ID || contentRevision != candidate.ContentRevision {
		return errors.New("generation runnable revision does not match candidate")
	}
	return nil
}

func validateGenerationVerificationReportTx(ctx context.Context, tx *Tx, revision runnable.RevisionReference, reference runnable.VerificationReportReference) (bool, error) {
	var encoded []byte
	var revisionDigest string
	err := tx.QueryRowContext(ctx, `SELECT report, runnable_revision_digest FROM runnable_verification_reports WHERE id = ? AND verification_report_digest = ? FOR UPDATE`, reference.ID, reference.Digest).Scan(&encoded, &revisionDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrVerificationReportNotFound
	}
	if err != nil {
		return false, err
	}
	if revisionDigest != revision.Digest {
		return false, errors.New("generation report does not match runnable revision")
	}
	var report runnable.VerificationReport
	if err := json.Unmarshal(encoded, &report); err != nil {
		return false, err
	}
	return report.Passed, nil
}

func finalizeAuthoringRevisionTx(ctx context.Context, tx *Tx, session authoring.Session, workflow *generation.Workflow, candidate *generation.Revision, publication generation.Publication, scenarioType scenario.ScenarioType, tags []string, materializedRevision string, now time.Time) error {
	if workflow == nil || candidate == nil || publication.ScenarioID != session.RevisionScenarioID || publication.BaseActiveRevisionID != session.RevisionBaseActiveRevisionID {
		return scenariodomain.ErrRevisionConflict
	}
	target, err := lockPersistedScenarioTx(ctx, tx, session.RevisionScenarioID)
	if err != nil {
		return err
	}
	if target.SourceKind != scenariodomain.SourceAuthoring || target.OwnerUserID != session.UserID || target.State != scenariodomain.StateActive || target.ActiveRevisionID != publication.BaseActiveRevisionID || target.SourceSlug != publication.SourceSlug {
		return scenariodomain.ErrRevisionConflict
	}
	previous, err := lockPersistedScenarioRevisionTx(ctx, tx, target.ActiveRevisionID)
	if err != nil {
		return err
	}
	if previous.ScenarioID != target.ID || previous.State != scenariodomain.RevisionActive || candidate.RunnableRevisionRef == nil || candidate.VerificationReportRef == nil {
		return generation.ErrCandidateInvalidState
	}
	published := scenarioRevisionFromPublication(publication.ScenarioTitle, scenarioType, tags, candidate.ContentRevision, materializedRevision, *candidate.RunnableRevisionRef, *candidate.VerificationReportRef, publication.ScenarioRevisionID, target.ID, workflow.Source.Ref, workflow.SourceRevision, target.ActiveRevisionID, target.SourceSlug, publication.TargetPath, scenariodomain.SourceAuthoring, now)
	if _, err := tx.ExecContext(ctx, `UPDATE scenario_revisions SET state = ? WHERE id = ? AND state = ?`, scenariodomain.RevisionSuperseded, previous.ID, scenariodomain.RevisionActive); err != nil {
		return err
	}
	if err := insertPersistedScenarioRevisionTx(ctx, tx, published); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE scenarios SET active_revision_id = ?, updated_at = ? WHERE id = ? AND active_revision_id = ? AND state = ?`, publication.ScenarioRevisionID, now.UTC(), target.ID, target.ActiveRevisionID, scenariodomain.StateActive)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return scenariodomain.ErrRevisionConflict
	}
	return nil
}

func prepareGenerationPublicationTx(ctx context.Context, tx *Tx, session authoring.Session, candidate *generation.Revision, metadata generation.PublicationMetadata, now time.Time) (generation.Publication, error) {
	if candidate == nil || candidate.RunnableRevisionRef == nil || candidate.VerificationReportRef == nil || metadata.Validate() != nil || now.IsZero() {
		return generation.Publication{}, generation.ErrCandidateInvalidState
	}
	value := generation.Publication{CandidateRevisionID: candidate.ID, IntentRevision: 1, ScenarioTitle: metadata.Title, RequestedAt: now.UTC()}
	if session.RevisionScenarioID == "" {
		value.ScenarioID, value.ScenarioRevisionID = scenario.NewID(), scenario.NewRevisionID()
		value.SourceSlug = scenario.SourceSlugFor(metadata.Title, value.ScenarioID)
	} else {
		target, err := lockPersistedScenarioTx(ctx, tx, session.RevisionScenarioID)
		if err != nil {
			return generation.Publication{}, err
		}
		if target.SourceKind != scenariodomain.SourceAuthoring || target.OwnerUserID != session.UserID || target.State != scenariodomain.StateActive || target.ActiveRevisionID != session.RevisionBaseActiveRevisionID {
			return generation.Publication{}, scenariodomain.ErrRevisionConflict
		}
		value.ScenarioID, value.ScenarioRevisionID, value.BaseActiveRevisionID, value.SourceSlug = target.ID, scenario.NewRevisionID(), target.ActiveRevisionID, target.SourceSlug
	}
	value.TargetPath = scenario.MaterializedPath(value.SourceSlug, value.ScenarioRevisionID)
	return value, value.ValidateIntent()
}

func lockGenerationAgentClaimTx(ctx context.Context, tx *Tx, claim generation.Claim, now time.Time) (*generation.Workflow, error) {
	value, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND state = ? AND state_version = ? AND agent_lease_owner = ? AND agent_lease_expires_at > ? FOR UPDATE`, claim.Workflow.ID, generation.StateJudging, claim.StateVersion, claim.LeaseOwner, now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrLeaseLost
	}
	if err != nil {
		return nil, err
	}
	return value, nil
}

func insertCandidateRevisionTx(ctx context.Context, tx *Tx, value generation.Revision) error {
	if err := value.ValidateForCreate(); err != nil {
		return err
	}
	source, err := marshalJSON(value.SourceArchive)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO candidate_revisions (id, source_kind, source_ref, source_revision, judge_run_id, parent_candidate_revision_id, repair_reason, archive_path, archive_digest, content_revision, source_archive, created_at, updated_at) VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?::jsonb, ?, ?)`, value.ID, value.Source.Kind, value.Source.Ref, value.SourceRevision, value.JudgeRunID, value.ParentCandidateID, value.RepairReason, value.ArchivePath, value.ArchiveDigest, value.ContentRevision, source, value.CreatedAt.UTC(), value.UpdatedAt.UTC())
	return err
}

func scanCandidateRevision(row scanner) (*generation.Revision, error) {
	var value generation.Revision
	var source, failure, publicationJSON []byte
	var runnableID, runnableDigest, reportID, reportDigest sql.NullString
	var verified, published sql.NullTime
	err := row.Scan(&value.ID, &value.Source.Kind, &value.Source.Ref, &value.SourceRevision, &value.JudgeRunID, &value.ParentCandidateID, &value.RepairReason, &value.ArchivePath, &value.ArchiveDigest, &value.ContentRevision, &source, &runnableID, &runnableDigest, &reportID, &reportDigest, &failure, &publicationJSON, &value.CreatedAt, &value.UpdatedAt, &verified, &published)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(source, &value.SourceArchive); err != nil {
		return nil, err
	}
	if runnableID.Valid != runnableDigest.Valid || reportID.Valid != reportDigest.Valid {
		return nil, errors.New("stored candidate public references are incomplete")
	}
	if runnableID.Valid {
		value.RunnableRevisionRef = &runnable.RevisionReference{ID: runnableID.String, Digest: runnableDigest.String}
	}
	if reportID.Valid {
		value.VerificationReportRef = &runnable.VerificationReportReference{ID: reportID.String, Digest: reportDigest.String}
	}
	if len(failure) > 0 && string(failure) != "null" {
		if err := json.Unmarshal(failure, &value.Failure); err != nil {
			return nil, err
		}
	}
	if len(publicationJSON) > 0 && string(publicationJSON) != "null" {
		if err := json.Unmarshal(publicationJSON, &value.Publication); err != nil {
			return nil, err
		}
	}
	if verified.Valid {
		at := verified.Time.UTC()
		value.VerifiedAt = &at
	}
	if published.Valid {
		at := published.Time.UTC()
		value.PublishedAt = &at
	}
	value.CreatedAt, value.UpdatedAt = value.CreatedAt.UTC(), value.UpdatedAt.UTC()
	return &value, nil
}

func scanGenerationWorkflow(row scanner) (*generation.Workflow, error) {
	var value generation.Workflow
	var lease, attempted, retry sql.NullTime
	err := row.Scan(&value.ID, &value.Source.Kind, &value.Source.Ref, &value.SourceRevision, &value.State, &value.CandidateRevisionID, &value.WorkspaceSnapshotDigest, &value.ActiveAgentRunID, &value.StateVersion, &value.AgentLeaseOwner, &lease, &value.NextRunAt, &value.LastError, &value.FinalizerErrorCategory, &value.FinalizerLastError, &attempted, &retry, &value.CreatedAt, &value.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if lease.Valid {
		at := lease.Time.UTC()
		value.AgentLeaseExpiresAt = &at
	}
	if attempted.Valid {
		at := attempted.Time.UTC()
		value.FinalizerLastAttemptedAt = &at
	}
	if retry.Valid {
		at := retry.Time.UTC()
		value.FinalizerNextRetryAt = &at
	}
	value.NextRunAt, value.CreatedAt, value.UpdatedAt = value.NextRunAt.UTC(), value.CreatedAt.UTC(), value.UpdatedAt.UTC()
	if !value.Valid() {
		return nil, errors.New("stored generation workflow is invalid")
	}
	return &value, nil
}

func authoringSourceRevision(workflow generation.Workflow) (int64, error) {
	if workflow.Source.Kind != generation.SourceAuthoring {
		return 0, errors.New("generation source is not authoring")
	}
	value, err := strconv.ParseInt(workflow.SourceRevision, 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("generation source revision is invalid")
	}
	return value, nil
}

type generationAction string

const (
	generationActionConfirmGeneration     generationAction = "confirm-generation"
	generationActionSubmitCandidate       generationAction = "submit-candidate"
	generationActionConfirmContent        generationAction = "confirm-content"
	generationActionRequestContentChanges generationAction = "request-content-changes"
	generationActionCancelGeneration      generationAction = "cancel-generation"
)

type generationActionReceipt struct {
	SessionID           string
	Action              generationAction
	IdempotencyKey      string
	RequestDigest       string
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
	} else {
		return err
	}
}
func generationActionRequestDigest(action generationAction, request any) (string, error) {
	encoded, err := json.Marshal(struct {
		Action  generationAction `json:"action"`
		Request any              `json:"request"`
	}{action, request})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
func generationActionReceiptTx(ctx context.Context, tx *Tx, sessionID string, action generationAction, idempotencyKey, digest string) (*generationActionReceipt, error) {
	value := &generationActionReceipt{SessionID: sessionID, Action: action, IdempotencyKey: idempotencyKey}
	var plan sql.NullInt64
	var candidate sql.NullString
	var proposal sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT request_digest, workflow_id, plan_revision, candidate_revision_id, proposal_revision, created_at FROM generation_action_receipts WHERE session_id = ? AND action = ? AND idempotency_key = ? FOR UPDATE`, sessionID, action, idempotencyKey).Scan(&value.RequestDigest, &value.WorkflowID, &plan, &candidate, &proposal, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if value.RequestDigest != digest {
		return nil, authoring.ErrVersionConflict
	}
	if plan.Valid {
		x := plan.Int64
		value.PlanRevision = &x
	}
	if candidate.Valid {
		x := candidate.String
		value.CandidateRevisionID = &x
	}
	if proposal.Valid {
		x := int(proposal.Int64)
		value.ProposalRevision = &x
	}
	return value, nil
}
func insertGenerationActionReceiptTx(ctx context.Context, tx *Tx, value generationActionReceipt) error {
	var plan, candidate, proposal any
	if value.PlanRevision != nil {
		plan = *value.PlanRevision
	}
	if value.CandidateRevisionID != nil {
		candidate = *value.CandidateRevisionID
	}
	if value.ProposalRevision != nil {
		proposal = *value.ProposalRevision
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO generation_action_receipts (session_id, action, idempotency_key, request_digest, workflow_id, plan_revision, candidate_revision_id, proposal_revision, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.SessionID, value.Action, value.IdempotencyKey, value.RequestDigest, value.WorkflowID, plan, candidate, proposal, value.CreatedAt.UTC())
	return err
}
func generationWorkflowForReceiptTx(ctx context.Context, tx *Tx, workflowID string) (*generation.Workflow, error) {
	value, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ?`, workflowID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGenerationWorkflowNotFound
	}
	return value, err
}
