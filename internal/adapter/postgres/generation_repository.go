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

	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	challengedomain "github.com/breakfix/breakfix/internal/domain/challenge"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/publication"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
)

var ErrGenerationWorkflowNotFound = errors.New("generation workflow not found")

const generationWorkflowColumns = `id, source_kind, source_ref, source_revision, state,
	COALESCE(candidate_revision_id, ''), workspace_snapshot_digest, COALESCE(active_agent_run_id, ''), state_version, runtime_attempt, lease_owner, lease_expires_at,
	next_run_at, last_error, finalizer_error_category, finalizer_last_error, finalizer_last_attempted_at, finalizer_next_retry_at, created_at, updated_at`
const generationWorkflowSelect = `SELECT ` + generationWorkflowColumns + ` FROM generation_workflows`

func (d *GenerationRepository) CreateGenerationWorkflow(ctx context.Context, sessionID, userID string, confirmation generation.StartConfirmation, now time.Time) (*generation.Workflow, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || !confirmation.Valid() || now.IsZero() {
		return nil, errors.New("generation workflow requires authoring session, user, idempotent plan confirmation, and current time")
	}
	requestDigest, err := generationActionRequestDigest(generationActionConfirmGeneration, confirmation)
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
	session, err := readAuthoringSessionTx(ctx, tx, sessionID, userID)
	if err != nil {
		return nil, err
	}
	if receipt, err := generationActionReceiptTx(ctx, tx, sessionID, generationActionConfirmGeneration, confirmation.IdempotencyKey, requestDigest); err != nil {
		return nil, err
	} else if receipt != nil {
		if receipt.PlanRevision == nil || *receipt.PlanRevision != confirmation.PlanRevision {
			return nil, authoring.ErrVersionConflict
		}
		return generationWorkflowForReceiptTx(ctx, tx, receipt.WorkflowID)
	}
	// A Plan revision is the business identity of one generation workflow. A
	// retried confirmation may carry a new transport idempotency key, so resolve
	// the existing workflow before validating the session's current interactive
	// state instead of letting the database unique index become the API result.
	planRevision := strconv.FormatInt(confirmation.PlanRevision, 10)
	existing, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE source_kind = ? AND source_ref = ? AND source_revision = ? FOR UPDATE`,
		generation.SourceAuthoring, sessionID, planRevision))
	if err == nil {
		if err := insertGenerationActionReceiptTx(ctx, tx, generationActionReceipt{
			SessionID: sessionID, Action: generationActionConfirmGeneration, IdempotencyKey: confirmation.IdempotencyKey,
			RequestDigest: requestDigest, WorkflowID: existing.ID, PlanRevision: &confirmation.PlanRevision, CreatedAt: now.UTC(),
		}); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit repeated generation confirmation: %w", err)
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("find existing generation workflow: %w", err)
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
		SourceRevision: planRevision,
		State:          generation.StateGenerating,
		StateVersion:   1,
		NextRunAt:      now.UTC(),
		CreatedAt:      now.UTC(),
		UpdatedAt:      now.UTC(),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO generation_workflows
		(id, source_kind, source_ref, source_revision, state, candidate_revision_id, workspace_snapshot_digest, active_agent_run_id, state_version, runtime_attempt, lease_owner, next_run_at, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, NULL, '', NULL, 1, 0, '', ?, '', ?, ?)`,
		workflow.ID, workflow.Source.Kind, workflow.Source.Ref, workflow.SourceRevision, workflow.State, workflow.NextRunAt, workflow.CreatedAt, workflow.UpdatedAt); err != nil {
		return nil, fmt.Errorf("insert generation workflow: %w", err)
	}
	if err := insertGenerationActionReceiptTx(ctx, tx, generationActionReceipt{
		SessionID: sessionID, Action: generationActionConfirmGeneration, IdempotencyKey: confirmation.IdempotencyKey,
		RequestDigest: requestDigest, WorkflowID: workflow.ID, PlanRevision: &confirmation.PlanRevision, CreatedAt: now.UTC(),
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

// GetGenerationWorkflowForUser is the ownership-fenced read used by the
// shared GeneratorService. The public workflow ID is not an authorization
// credential.
func (d *GenerationRepository) GetGenerationWorkflowForUser(ctx context.Context, workflowID, userID string) (*generation.Workflow, error) {
	if strings.TrimSpace(workflowID) == "" || strings.TrimSpace(userID) == "" {
		return nil, authoring.ErrNotFound
	}
	workflow, err := scanGenerationWorkflow(d.conn.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND source_kind = ? AND EXISTS (
		SELECT 1 FROM authoring_sessions session WHERE session.id = generation_workflows.source_ref AND session.user_id = ?
	)`, workflowID, generation.SourceAuthoring, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get owned generation workflow: %w", err)
	}
	return workflow, nil
}

// ListGenerationWorkflowsForUser lists the caller's unfinished workflows.
// Generator clients always select an explicit workflow before touching a
// workspace; there is deliberately no implicit "current" workspace.
func (d *GenerationRepository) ListGenerationWorkflowsForUser(ctx context.Context, userID string) ([]generation.Workflow, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, authoring.ErrNotFound
	}
	rows, err := d.conn.QueryContext(ctx, generationWorkflowSelect+` WHERE source_kind = ? AND state NOT IN (?, ?, ?)
		AND EXISTS (SELECT 1 FROM authoring_sessions session WHERE session.id = generation_workflows.source_ref AND session.user_id = ?)
		ORDER BY updated_at DESC, id DESC`, generation.SourceAuthoring, generation.StatePublished, generation.StateFailed, generation.StateCancelled, userID)
	if err != nil {
		return nil, fmt.Errorf("list owned generation workflows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	workflows := make([]generation.Workflow, 0)
	for rows.Next() {
		workflow, err := scanGenerationWorkflow(rows)
		if err != nil {
			return nil, err
		}
		workflows = append(workflows, *workflow)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate owned generation workflows: %w", err)
	}
	return workflows, nil
}

// ClaimGenerationWorkflow is the Runtime Worker's only claim path. Server-side
// Eino roles use ClaimGenerationAgentWorkflow and can never be obtained by a
// worker identity. Generating is user-directed workspace activity, not an
// agent claim. An expired Runtime lease consumes the current state's
// infrastructure retry budget before a replacement claim is issued.
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
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM generation_workflows
		WHERE state IN (?, ?, ?, ?)
		AND next_run_at <= ?
		AND (lease_expires_at IS NULL OR lease_expires_at <= ?)
		AND NOT (state = ? AND EXISTS (
			SELECT 1 FROM candidate_revisions candidate
			WHERE candidate.id = generation_workflows.candidate_revision_id
			AND candidate.publication -> 'artifact' IS NOT NULL
		))
		ORDER BY next_run_at, created_at, id
		FOR UPDATE SKIP LOCKED LIMIT 1`, generation.StateBuilding, generation.StateArtifactPublishing,
		generation.StateVerifying, generation.StateChallengePublishing, now, now, generation.StateChallengePublishing).Scan(&id)
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
	if !workflow.State.RuntimeState() {
		return nil, fmt.Errorf("generation workflow %s is not a runtime state", workflow.ID)
	}
	if workflow.LeaseExpiresAt != nil && !workflow.LeaseExpiresAt.After(now) {
		workflow, err = recoverExpiredGenerationRuntimeLeaseTx(ctx, tx, workflow, now)
		if err != nil {
			return nil, err
		}
		if workflow == nil {
			if err := tx.Commit(); err != nil {
				return nil, fmt.Errorf("commit exhausted runtime lease recovery: %w", err)
			}
			return nil, nil
		}
	}
	owner := strings.TrimSpace(workerID) + "-" + generation.NewID("lease")
	expires := now.Add(leaseTTL)
	workflow, err = scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET
		lease_owner = ?, lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND state_version = ? RETURNING `+generationWorkflowColumns, owner, expires, now, workflow.ID, workflow.StateVersion))
	if err != nil {
		return nil, fmt.Errorf("claim generation workflow: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generation claim: %w", err)
	}
	return &generation.Claim{Workflow: *workflow, LeaseCredential: generation.LeaseCredential{StateVersion: workflow.StateVersion, LeaseOwner: owner}}, nil
}

// ClaimGenerationAgentWorkflow is the Server-only claim path for Eino roles.
// Runtime states are structurally unavailable to this path.
func (d *GenerationRepository) ClaimGenerationAgentWorkflow(ctx context.Context, serverID string, leaseTTL time.Duration, now time.Time) (*generation.Claim, error) {
	if strings.TrimSpace(serverID) == "" || leaseTTL <= 0 || now.IsZero() {
		return nil, errors.New("generation agent claim requires server, lease ttl, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generation agent claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now = now.UTC()
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM generation_workflows
		WHERE state = ?
		AND next_run_at <= ?
		AND (lease_expires_at IS NULL OR lease_expires_at <= ?)
		ORDER BY next_run_at, created_at, id
		FOR UPDATE SKIP LOCKED LIMIT 1`, generation.StateJudging, now, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit generation agent empty claim: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select generation agent workflow: %w", err)
	}
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	if !workflow.State.AgentState() {
		return nil, fmt.Errorf("generation workflow %s is not an agent state", workflow.ID)
	}
	owner := strings.TrimSpace(serverID) + "-" + generation.NewID("agent-lease")
	expires := now.Add(leaseTTL)
	workflow, err = scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET
		lease_owner = ?, lease_expires_at = ?, updated_at = ?
		WHERE id = ? RETURNING `+generationWorkflowColumns,
		owner, expires, now, workflow.ID))
	if err != nil {
		return nil, fmt.Errorf("claim generation agent workflow: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generation agent claim: %w", err)
	}
	return &generation.Claim{Workflow: *workflow, LeaseCredential: generation.LeaseCredential{StateVersion: workflow.StateVersion, LeaseOwner: owner}}, nil
}

func (d *GenerationRepository) GetGenerationClaim(ctx context.Context, id string, credential generation.LeaseCredential, now time.Time) (*generation.Claim, error) {
	if strings.TrimSpace(id) == "" || !credential.Valid() || now.IsZero() {
		return nil, errors.New("generation workflow lease credentials are required")
	}
	workflow, err := scanGenerationWorkflow(d.conn.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND lease_owner = ?
		AND state_version = ? AND lease_expires_at > ?`, id, credential.LeaseOwner, credential.StateVersion, now.UTC()))
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
	result.Plan = plan.Plan
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

// LoadGenerationRuntimeAction returns the narrow, immutable input surface for
// one leased Runtime Worker action. It intentionally does not expose authoring
// Plan, feedback, workspace binding, or Server filesystem paths.
func (d *GenerationRepository) LoadGenerationRuntimeAction(ctx context.Context, claim generation.Claim, now time.Time) (*runtime.Context, error) {
	if !claim.Valid() || now.IsZero() || !claim.Workflow.State.RuntimeState() {
		return nil, errors.New("generation runtime action requires a valid runtime claim")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generation runtime action: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationClaimTx(ctx, tx, claim, claim.Workflow.State, now.UTC())
	if err != nil {
		return nil, err
	}
	if workflow.CandidateRevisionID == "" {
		return nil, generation.ErrCandidateInvalidState
	}
	revision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ?`, workflow.CandidateRevisionID))
	if err != nil {
		return nil, err
	}
	identity := runtime.Identity{
		Scope: runtime.ScopeGenerationWorkflow, OwnerID: workflow.ID, CandidateID: revision.ID,
		State: runtime.State(workflow.State), StateVersion: workflow.StateVersion,
	}
	context := runtime.Context{
		Identity: identity, LeaseOwner: claim.LeaseOwner, ArchiveSHA256: revision.ArchiveSHA256,
		Snapshot: revision.Snapshot, Build: revision.Build, Artifact: revision.Artifact,
		VerificationEnvironment: revision.VerifyEnvironment,
	}
	if revision.Publication != nil {
		context.ChallengeID = revision.Publication.ChallengeID
		context.ChallengeRevisionID = revision.Publication.ChallengeRevisionID
	}
	if err := context.Valid(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generation runtime action: %w", err)
	}
	return &context, nil
}

func (d *GenerationRepository) RenewGenerationLease(ctx context.Context, claim generation.Claim, leaseTTL time.Duration, now time.Time) error {
	if !claim.Valid() || leaseTTL <= 0 || now.IsZero() {
		return errors.New("generation workflow lease renewal is invalid")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE generation_workflows SET lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND state_version = ? AND lease_owner = ? AND lease_expires_at > ?`,
		now.UTC().Add(leaseTTL), now.UTC(), claim.Workflow.ID, claim.StateVersion, claim.LeaseOwner, now.UTC())
	if err != nil {
		return fmt.Errorf("renew generation workflow lease: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return generationLeaseLost()
	}
	return nil
}

// RefreshGenerationClaim returns the current state under an already-held
// lease. State transitions release leases, so only a non-transitioning action
// such as recording a verification Environment can return a refreshed claim.
func (d *GenerationRepository) RefreshGenerationClaim(ctx context.Context, claim generation.Claim, now time.Time) (*generation.Claim, error) {
	if !claim.Valid() || now.IsZero() {
		return nil, errors.New("generation workflow claim and current time are required")
	}
	workflow, err := scanGenerationWorkflow(d.conn.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND state_version = ? AND lease_owner = ? AND lease_expires_at > ?`,
		claim.Workflow.ID, claim.StateVersion, claim.LeaseOwner, now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generationLeaseLost()
	}
	if err != nil {
		return nil, fmt.Errorf("refresh generation workflow claim: %w", err)
	}
	return &generation.Claim{Workflow: *workflow, LeaseCredential: generation.LeaseCredential{
		StateVersion: workflow.StateVersion,
		LeaseOwner:   workflow.LeaseOwner,
	}}, nil
}

func (d *GenerationRepository) StartGenerationAgentRun(ctx context.Context, claim generation.Claim, input agent.CreateRun, now time.Time) (*agent.Run, error) {
	if !claim.Valid() || now.IsZero() || input.OwnerKind != "generation-workflow" || input.OwnerRef != claim.Workflow.ID {
		return nil, errors.New("generation agent run ownership is invalid")
	}
	if input.Purpose != "judge" {
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
	if workflow.State != generation.StateJudging {
		return nil, errors.New("generation agent run purpose does not match workflow state")
	}
	if workflow.Source.Kind != generation.SourceAuthoring {
		return nil, errors.New("catalog release workflows do not run generator agents")
	}
	if workflow.ActiveAgentRunID != "" {
		// A prior process died after starting its call. It cannot be resumed, so
		// retain the durable record and start a fresh call under the new lease.
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
			WHERE id = ? AND status = ?`, agent.RunInterrupted, "generation agent lease replaced before completion", now, now,
			workflow.ActiveAgentRunID, agent.RunRunning); err != nil {
			return nil, fmt.Errorf("abandon prior generation agent run: %w", err)
		}
	}
	if input.SessionID != "" {
		return nil, errors.New("generation agent runs must not use an agent session")
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

// RetryGenerationAgentRun performs a technical retry of one logical AgentRun.
// It keeps the workflow claim and workspace intact, creates no candidate
// revision, and never turns a recovered process interruption into attempt six.
// A nil run means the five-attempt budget or the run deadline was exhausted.
func (d *GenerationRepository) RetryGenerationAgentRun(ctx context.Context, claim generation.Claim, runID, message string, now time.Time) (*agent.Run, error) {
	if !claim.Valid() || strings.TrimSpace(runID) == "" || strings.TrimSpace(message) == "" || now.IsZero() {
		return nil, errors.New("generation agent retry is incomplete")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generation agent retry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := lockGenerationClaimTx(ctx, tx, claim, claim.Workflow.State, now.UTC())
	if err != nil {
		return nil, err
	}
	if !workflow.State.AgentState() {
		return nil, errors.New("generation agent retry requires a Judge workflow")
	}
	if workflow.ActiveAgentRunID != runID {
		return nil, generationLeaseLost()
	}
	run, err := scanAgentRun(tx.QueryRowContext(ctx, agentRunSelect+` WHERE id = ? FOR UPDATE`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agent.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if run.Status != agent.RunRunning {
		return nil, agent.ErrRunActive
	}
	if run.Attempt < agent.MaxAttempts && run.DeadlineAt.After(now.UTC()) {
		next, err := scanAgentRun(tx.QueryRowContext(ctx, `UPDATE agent_runs SET attempt = attempt + 1, last_error = ?, updated_at = ?
			WHERE id = ? AND status = ? AND attempt = ? RETURNING `+agentRunColumns,
			strings.TrimSpace(message), now.UTC(), runID, agent.RunRunning, run.Attempt))
		if err != nil {
			return nil, fmt.Errorf("advance generation agent attempt: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return next, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
		WHERE id = ? AND status = ?`, agent.RunFailed, strings.TrimSpace(message), now.UTC(), now.UTC(), runID, agent.RunRunning); err != nil {
		return nil, fmt.Errorf("fail exhausted generation agent run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, active_agent_run_id = NULL,
		state_version = state_version + 1, runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL,
		last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ?`,
		generation.StateFailed, strings.TrimSpace(message), now.UTC(), now.UTC(), workflow.ID); err != nil {
		return nil, fmt.Errorf("finish exhausted generation agent run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return nil, nil
}

// InterruptActiveGenerationAgentRuns is startup recovery for the Server-owned
// Judge. Runtime actions continue under their own lease, while
// user-directed Generator workspaces are retired by WorkspaceReaper.
func (d *GenerationRepository) InterruptActiveGenerationAgentRuns(ctx context.Context, reason string, now time.Time) error {
	if strings.TrimSpace(reason) == "" || now.IsZero() {
		return errors.New("generation agent interruption requires reason and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin interrupt generation agent runs: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(active_agent_run_id, '') FROM generation_workflows
		WHERE state = ? FOR UPDATE`, generation.StateJudging)
	if err != nil {
		return fmt.Errorf("list active generation agent runs: %w", err)
	}
	type activeRun struct {
		workflowID string
		runID      string
	}
	active := make([]activeRun, 0)
	for rows.Next() {
		var value activeRun
		if err := rows.Scan(&value.workflowID, &value.runID); err != nil {
			_ = rows.Close()
			return err
		}
		active = append(active, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close active generation agent run list: %w", err)
	}
	for _, value := range active {
		if value.runID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
				WHERE id = ? AND status = ?`, agent.RunInterrupted, strings.TrimSpace(reason), now.UTC(), now.UTC(), value.runID, agent.RunRunning); err != nil {
				return fmt.Errorf("interrupt generation agent run: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET active_agent_run_id = NULL, lease_owner = '', lease_expires_at = NULL,
			next_run_at = ?, updated_at = ? WHERE id = ?`, now.UTC(), now.UTC(), value.workflowID); err != nil {
			return fmt.Errorf("release interrupted generation claim: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// FindSubmittedGenerationCandidate resolves a completed idempotent submission
// before GeneratorService creates an archive. That keeps a repeated request
// from allocating a second candidate archive on disk.
func (d *GenerationRepository) FindSubmittedGenerationCandidate(ctx context.Context, sessionID, userID string, submission generation.CandidateSubmission) (*generation.Revision, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || !submission.Valid() {
		return nil, errors.New("candidate submission requires authoring ownership, workflow turn, and idempotency key")
	}
	requestDigest, err := generationActionRequestDigest(generationActionSubmitCandidate, submission)
	if err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin submitted candidate lookup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	receipt, err := generationActionReceiptTx(ctx, tx, sessionID, generationActionSubmitCandidate, submission.IdempotencyKey, requestDigest)
	if err != nil {
		return nil, err
	}
	if receipt == nil {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if receipt.WorkflowID != submission.WorkflowID || receipt.CandidateRevisionID == nil {
		return nil, authoring.ErrVersionConflict
	}
	revision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ?`, *receipt.CandidateRevisionID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return revision, nil
}

// SubmitGenerationCandidate persists one archive submitted by a bound
// Generator turn. GeneratorService allocates the candidate ID before writing
// the immutable archive; clients never supply that ID or lineage.
func (d *GenerationRepository) SubmitGenerationCandidate(ctx context.Context, sessionID, userID string, submission generation.CandidateSubmission, revision generation.Revision, now time.Time) (*generation.Revision, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || !submission.Valid() || now.IsZero() {
		return nil, errors.New("candidate submission requires authoring ownership, workflow turn, idempotency key, and current time")
	}
	if submission.WorkflowID == "" || revision.JudgeRunID != "" || strings.TrimSpace(revision.ID) == "" || revision.Source.Kind != "" || revision.Source.Ref != "" || revision.SourceRevision != "" {
		return nil, errors.New("candidate submission requires a Server-assigned identity and no caller-supplied lineage")
	}
	if strings.TrimSpace(revision.ArchivePath) == "" || !generation.ValidSHA256(revision.ArchiveSHA256) || revision.Snapshot.Validate() != nil {
		return nil, errors.New("candidate submission requires a valid immutable archive and execution snapshot")
	}
	requestDigest, err := generationActionRequestDigest(generationActionSubmitCandidate, submission)
	if err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin candidate submission: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	if receipt, err := generationActionReceiptTx(ctx, tx, sessionID, generationActionSubmitCandidate, submission.IdempotencyKey, requestDigest); err != nil {
		return nil, err
	} else if receipt != nil {
		if receipt.WorkflowID != submission.WorkflowID || receipt.CandidateRevisionID == nil {
			return nil, authoring.ErrVersionConflict
		}
		return scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ?`, *receipt.CandidateRevisionID))
	}
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND source_kind = ? AND source_ref = ? AND state = ? FOR UPDATE`,
		submission.WorkflowID, generation.SourceAuthoring, sessionID, generation.StateGenerating))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrInvalidState
	}
	if err != nil {
		return nil, fmt.Errorf("lock candidate submission workflow: %w", err)
	}
	workspace, err := scanGeneratorWorkspace(tx.QueryRowContext(ctx, generatorWorkspaceSelect+` WHERE workflow_id = ? AND state = ?
		AND active_turn_id = ? ORDER BY created_at DESC, workspace_id DESC LIMIT 1 FOR UPDATE`,
		workflow.ID, generation.WorkspaceActive, submission.TurnID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrWorkspaceTurnLost
	}
	if err != nil {
		return nil, fmt.Errorf("lock candidate submission workspace: %w", err)
	}
	_ = workspace
	revision.Source = workflow.Source
	revision.SourceRevision = workflow.SourceRevision
	if workflow.CandidateRevisionID != "" {
		revision.ParentCandidateID = workflow.CandidateRevisionID
		revision.RepairReason = strings.TrimSpace(workflow.LastError)
	}
	revision.CreatedAt = now.UTC()
	revision.UpdatedAt = now.UTC()
	if err := revision.ValidateForCreate(); err != nil {
		return nil, err
	}
	if err := insertCandidateRevisionTx(ctx, tx, revision); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generator_workspaces SET active_turn_id = '', idle_since = NULL, updated_at = ? WHERE workspace_id = ? AND active_turn_id = ?`,
		now.UTC(), workspace.ID, submission.TurnID); err != nil {
		return nil, fmt.Errorf("release submitted generator workspace turn: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, candidate_revision_id = ?, workspace_snapshot_digest = '', active_agent_run_id = NULL,
		state_version = state_version + 1, runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL,
		last_error = '', next_run_at = ?, updated_at = ? WHERE id = ?`,
		generation.StateJudging, revision.ID, now.UTC(), now.UTC(), workflow.ID); err != nil {
		return nil, fmt.Errorf("advance submitted candidate workflow: %w", err)
	}
	if err := insertGenerationActionReceiptTx(ctx, tx, generationActionReceipt{
		SessionID: sessionID, Action: generationActionSubmitCandidate, IdempotencyKey: submission.IdempotencyKey,
		RequestDigest: requestDigest, WorkflowID: workflow.ID, CandidateRevisionID: &revision.ID, CreatedAt: now.UTC(),
	}); err != nil {
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
		if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, active_agent_run_id = NULL,
			state_version = state_version + 1, runtime_attempt = 1, lease_owner = '', lease_expires_at = NULL,
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
		if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, active_agent_run_id = NULL,
			state_version = state_version + 1, runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL,
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
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1,
		runtime_attempt = ?, lease_owner = '', lease_expires_at = NULL, last_error = '', next_run_at = ?, updated_at = ? WHERE id = ?`,
		next, runtimeAttemptForState(next), now.UTC(), now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("advance generation workflow: %w", err)
	}
	return tx.Commit()
}

func (d *GenerationRepository) RecordGenerationVerificationEnvironment(ctx context.Context, claim generation.Claim, environment generation.VerificationEnvironment, now time.Time) error {
	if !claim.Valid() || now.IsZero() || environment.WorkflowID != claim.Workflow.ID || environment.Attempt != claim.StateVersion {
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
		if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1,
			runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL, last_error = '', updated_at = ? WHERE id = ?`, generation.StateNeedsAuthorReview, now.UTC(), workflow.ID); err != nil {
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
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1,
		runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL, last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ?`, generation.StateGenerating, report.Summary, now.UTC(), now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("return failed verification to generator: %w", err)
	}
	return tx.Commit()
}

// ConfirmGenerationContent freezes one verified candidate and immediately
// creates its publication intent. Type and tags remain in the immutable
// portable manifest and are written by the publication finalizer.
func (d *GenerationRepository) ConfirmGenerationContent(ctx context.Context, sessionID, userID string, confirmation generation.ContentConfirmation, metadata generation.PublicationMetadata, now time.Time) (*generation.Workflow, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || !confirmation.Valid() || metadata.Validate() != nil || now.IsZero() {
		return nil, errors.New("content confirmation requires authoring session, user, verified manifest metadata, idempotent candidate confirmation, and current time")
	}
	requestDigest, err := generationActionRequestDigest(generationActionConfirmContent, confirmation)
	if err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin content confirmation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	session, err := readAuthoringSessionTx(ctx, tx, sessionID, userID)
	if err != nil {
		return nil, err
	}
	if receipt, err := generationActionReceiptTx(ctx, tx, sessionID, generationActionConfirmContent, confirmation.IdempotencyKey, requestDigest); err != nil {
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
	if workflow.CandidateRevisionID != confirmation.CandidateRevisionID {
		return nil, authoring.ErrInvalidState
	}
	candidateRevision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, workflow.CandidateRevisionID))
	if err != nil {
		return nil, err
	}
	if candidateRevision.Verification == nil || !candidateRevision.Verification.Passed || candidateRevision.Artifact == nil {
		return nil, authoring.ErrInvalidState
	}
	publication, err := prepareGenerationPublicationTx(ctx, tx, *session, candidateRevision, metadata, now.UTC())
	if err != nil {
		return nil, err
	}
	encodedPublication, err := marshalJSON(publication)
	if err != nil {
		return nil, err
	}
	updated, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET state = ?,
		state_version = state_version + 1, runtime_attempt = 1, lease_owner = '', lease_expires_at = NULL,
		next_run_at = ?, last_error = '', updated_at = ? WHERE id = ? RETURNING `+generationWorkflowColumns,
		generation.StateChallengePublishing, now.UTC(), now.UTC(), workflow.ID))
	if err != nil {
		return nil, fmt.Errorf("start challenge publication: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET publication = ?::jsonb, updated_at = ? WHERE id = ?`, encodedPublication, now.UTC(), candidateRevision.ID); err != nil {
		return nil, fmt.Errorf("record challenge publication intent: %w", err)
	}
	if err := insertGenerationActionReceiptTx(ctx, tx, generationActionReceipt{
		SessionID: sessionID, Action: generationActionConfirmContent, IdempotencyKey: confirmation.IdempotencyKey,
		RequestDigest: requestDigest, WorkflowID: workflow.ID, CandidateRevisionID: &confirmation.CandidateRevisionID, CreatedAt: now.UTC(),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

// RequestGenerationContentChanges returns the reviewed candidate to the same
// user-owned Generator workspace. The candidate remains immutable history;
// the next submission becomes its child revision after another real quality
// gate run.
func (d *GenerationRepository) RequestGenerationContentChanges(ctx context.Context, sessionID, userID string, request generation.ContentChangeRequest, now time.Time) (*generation.Workflow, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || !request.Valid() || now.IsZero() {
		return nil, errors.New("content change request requires an authoring session, reviewed candidate, feedback, idempotency key, and current time")
	}
	requestDigest, err := generationActionRequestDigest(generationActionRequestContentChanges, request)
	if err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin content change request: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	if receipt, err := generationActionReceiptTx(ctx, tx, sessionID, generationActionRequestContentChanges, request.IdempotencyKey, requestDigest); err != nil {
		return nil, err
	} else if receipt != nil {
		if receipt.WorkflowID != request.WorkflowID || receipt.CandidateRevisionID == nil || *receipt.CandidateRevisionID != request.CandidateRevisionID {
			return nil, authoring.ErrVersionConflict
		}
		return generationWorkflowForReceiptTx(ctx, tx, receipt.WorkflowID)
	}
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND source_kind = ? AND source_ref = ? AND state = ? FOR UPDATE`,
		request.WorkflowID, generation.SourceAuthoring, sessionID, generation.StateNeedsAuthorReview))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrInvalidState
	}
	if err != nil {
		return nil, fmt.Errorf("lock content change workflow: %w", err)
	}
	if workflow.CandidateRevisionID != request.CandidateRevisionID {
		return nil, authoring.ErrVersionConflict
	}
	candidateRevision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, workflow.CandidateRevisionID))
	if err != nil {
		return nil, err
	}
	if candidateRevision.Verification == nil || !candidateRevision.Verification.Passed || candidateRevision.Artifact == nil {
		return nil, authoring.ErrInvalidState
	}
	updated, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1,
		runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL, active_agent_run_id = NULL,
		last_error = ?, next_run_at = ?, updated_at = ?
		WHERE id = ? RETURNING `+generationWorkflowColumns,
		generation.StateGenerating, strings.TrimSpace(request.Feedback), now.UTC(), now.UTC(), workflow.ID))
	if err != nil {
		return nil, fmt.Errorf("return generation workflow to content repair: %w", err)
	}
	if err := insertGenerationActionReceiptTx(ctx, tx, generationActionReceipt{
		SessionID: sessionID, Action: generationActionRequestContentChanges, IdempotencyKey: request.IdempotencyKey,
		RequestDigest: requestDigest, WorkflowID: workflow.ID, CandidateRevisionID: &request.CandidateRevisionID, CreatedAt: now.UTC(),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

// RecordGenerationChallengePublicationResult durably stores a successful
// provider promotion before Server materializes source or updates the active
// Runtime Worker lease is released without changing state, so a later Server
// finalizer can recover without repeating promotion.
func (d *GenerationRepository) RecordGenerationChallengePublicationResult(ctx context.Context, claim generation.Claim, artifact generation.ArtifactReference, now time.Time) error {
	if !claim.Valid() || now.IsZero() {
		return errors.New("generation challenge promotion result is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin generation challenge promotion result: %w", err)
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
	if err := artifact.Validate(candidateRevision.Snapshot.Runtime); err != nil || candidateRevision.Publication == nil {
		return generation.ErrCandidateInvalidState
	}
	publication := *candidateRevision.Publication
	if publication.StagingArtifact == nil || candidateRevision.Artifact == nil || *publication.StagingArtifact != *candidateRevision.Artifact || publication.Runtime != candidateRevision.Snapshot.Runtime {
		return generation.ErrCandidateInvalidState
	}
	if publication.Artifact != nil && *publication.Artifact != artifact {
		return generation.ErrCandidateInvalidState
	}
	publication.Artifact = &artifact
	if err := publication.ValidatePromotionResult(); err != nil {
		return err
	}
	encodedPublication, err := marshalJSON(publication)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET publication = ?::jsonb, updated_at = ? WHERE id = ?`, encodedPublication, now.UTC(), candidateRevision.ID); err != nil {
		return fmt.Errorf("record generation challenge promotion result: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET lease_owner = '', lease_expires_at = NULL, last_error = '',
		finalizer_error_category = '', finalizer_last_error = '', finalizer_last_attempted_at = NULL, finalizer_next_retry_at = NULL, updated_at = ?
		WHERE id = ? AND state = ? AND state_version = ?`, now.UTC(), workflow.ID, generation.StateChallengePublishing, workflow.StateVersion); err != nil {
		return fmt.Errorf("release generation challenge promotion lease: %w", err)
	}
	return tx.Commit()
}

// PendingGenerationPublicationFinalizations returns only promotion results
// that still need Server-owned source materialization and transactional
// Catalog publication. No Runtime Worker action is returned for these rows.
func (d *GenerationRepository) PendingGenerationPublicationFinalizations(ctx context.Context, now time.Time) ([]generation.PublicationFinalization, error) {
	if now.IsZero() {
		return nil, errors.New("generation publication finalizer listing requires current time")
	}
	rows, err := d.conn.QueryContext(ctx, generationWorkflowSelect+` WHERE state = ?
		AND (finalizer_next_retry_at IS NULL OR finalizer_next_retry_at <= ?)
		AND EXISTS (
			SELECT 1 FROM candidate_revisions candidate
			WHERE candidate.id = generation_workflows.candidate_revision_id
			AND candidate.publication -> 'artifact' IS NOT NULL
			AND candidate.published_at IS NULL
		) ORDER BY updated_at, id`, generation.StateChallengePublishing, now.UTC())
	if err != nil {
		return nil, fmt.Errorf("list pending generation publication finalizers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]generation.PublicationFinalization, 0)
	for rows.Next() {
		workflow, err := scanGenerationWorkflow(rows)
		if err != nil {
			return nil, err
		}
		revision, err := d.GetCandidateRevision(ctx, workflow.CandidateRevisionID)
		if err != nil {
			return nil, err
		}
		result = append(result, generation.PublicationFinalization{Workflow: *workflow, Candidate: *revision})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending generation publication finalizers: %w", err)
	}
	return result, nil
}

// RecordGenerationPublicationFinalizerFailure persists the Server-owned
// materialization outcome after Runtime Worker promotion has already been
// recorded. Transient failures keep ChallengePublishing and its retry time;
// deterministic failures close the workflow without touching the published
// artifact intent.
func (d *GenerationRepository) RecordGenerationPublicationFinalizerFailure(ctx context.Context, workflowID, candidateRevisionID string, diagnostic publication.Diagnostic) (*generation.Workflow, error) {
	if strings.TrimSpace(workflowID) == "" || strings.TrimSpace(candidateRevisionID) == "" || diagnostic.Validate() != nil {
		return nil, errors.New("generation publication finalizer diagnostic is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generation publication finalizer failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND state = ? FOR UPDATE`,
		strings.TrimSpace(workflowID), generation.StateChallengePublishing))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generation.ErrCandidateInvalidState
	}
	if err != nil {
		return nil, fmt.Errorf("lock generation publication finalizer workflow: %w", err)
	}
	if workflow.CandidateRevisionID != strings.TrimSpace(candidateRevisionID) {
		return nil, generation.ErrCandidateInvalidState
	}
	candidateRevision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, workflow.CandidateRevisionID))
	if err != nil {
		return nil, err
	}
	if candidateRevision.Publication == nil || candidateRevision.PublishedAt != nil {
		return nil, generation.ErrCandidateInvalidState
	}
	if diagnostic.Category == publication.CategoryDeterministic {
		failure, marshalErr := marshalJSON(generation.Failure{Class: generation.FailureArtifact, Code: "PUBLICATION_FINALIZER", Summary: diagnostic.LastError})
		if marshalErr != nil {
			return nil, marshalErr
		}
		if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET failure = ?::jsonb, updated_at = ? WHERE id = ?`, failure, diagnostic.LastAttemptedAt.UTC(), candidateRevision.ID); err != nil {
			return nil, fmt.Errorf("record generation publication finalizer evidence: %w", err)
		}
	}
	var nextRunAt any = diagnostic.LastAttemptedAt.UTC()
	if diagnostic.NextRetryAt != nil {
		nextRunAt = diagnostic.NextRetryAt.UTC()
	}
	state := generation.StateChallengePublishing
	stateVersion := workflow.StateVersion
	runtimeAttempt := workflow.RuntimeAttempt
	if diagnostic.Category == publication.CategoryDeterministic {
		state = generation.StateFailed
		stateVersion++
		runtimeAttempt = 0
	}
	updated, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = ?, runtime_attempt = ?,
		lease_owner = '', lease_expires_at = NULL, next_run_at = ?, last_error = ?, finalizer_error_category = ?, finalizer_last_error = ?,
		finalizer_last_attempted_at = ?, finalizer_next_retry_at = ?, updated_at = ?
		WHERE id = ? AND state = ? RETURNING `+generationWorkflowColumns,
		state, stateVersion, runtimeAttempt, nextRunAt, diagnostic.LastError, diagnostic.Category, diagnostic.LastError,
		diagnostic.LastAttemptedAt.UTC(), func() any {
			if diagnostic.NextRetryAt == nil {
				return nil
			}
			return diagnostic.NextRetryAt.UTC()
		}(), diagnostic.LastAttemptedAt.UTC(), workflow.ID, generation.StateChallengePublishing))
	if err != nil {
		return nil, fmt.Errorf("persist generation publication finalizer failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generation publication finalizer failure: %w", err)
	}
	return updated, nil
}

// FinalizeGenerationChallengePublication makes one already-promoted artifact
// visible after Server has idempotently materialized its source directory. It
// never calls a provider and is safe to retry after a Server interruption.
func (d *GenerationRepository) FinalizeGenerationChallengePublication(ctx context.Context, workflowID, candidateRevisionID, contentRevision, materializedRevision string, scenarioType challenge.ScenarioType, scenarioTags []string, now time.Time) error {
	if strings.TrimSpace(workflowID) == "" || strings.TrimSpace(candidateRevisionID) == "" ||
		!challenge.ValidRevision(contentRevision) || !challenge.ValidRevision(materializedRevision) || !scenarioType.Valid() || now.IsZero() {
		return errors.New("generation challenge publication finalization is invalid")
	}
	canonicalTags, err := challenge.NormalizeTags(scenarioTags)
	if err != nil || !slices.Equal(canonicalTags, scenarioTags) || (scenarioType == challenge.ScenarioDocumentationExample && len(scenarioTags) != 0) {
		return errors.New("generation challenge publication tags are invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin challenge publication finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND state = ? FOR UPDATE`, workflowID, generation.StateChallengePublishing))
	if errors.Is(err, sql.ErrNoRows) {
		return generation.ErrCandidateInvalidState
	}
	if err != nil {
		return err
	}
	if workflow.CandidateRevisionID != candidateRevisionID || workflow.Source.Kind != generation.SourceAuthoring {
		return generation.ErrCandidateInvalidState
	}
	candidateRevision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, workflow.CandidateRevisionID))
	if err != nil {
		return err
	}
	if candidateRevision.Source != workflow.Source || candidateRevision.SourceRevision != workflow.SourceRevision || candidateRevision.Publication == nil || candidateRevision.PublishedAt != nil {
		return generation.ErrCandidateInvalidState
	}
	publication := *candidateRevision.Publication
	if publication.StagingArtifact == nil || candidateRevision.Artifact == nil || *publication.StagingArtifact != *candidateRevision.Artifact || publication.Runtime != candidateRevision.Snapshot.Runtime {
		return generation.ErrCandidateInvalidState
	}
	if err := publication.ValidatePromotionResult(); err != nil {
		return err
	}
	publication.ContentRevision = contentRevision
	if err := publication.ValidateFinal(); err != nil {
		return err
	}
	session, err := readAuthoringSessionTx(ctx, tx, workflow.Source.Ref, "")
	if err != nil {
		return err
	}
	if session.RevisionChallengeID == "" {
		stable := challengedomain.Challenge{ID: publication.ChallengeID, SourceKind: challengedomain.SourceAuthoring, SourceRef: workflow.Source.Ref,
			OwnerUserID: session.UserID, State: challengedomain.StateActive, ActiveRevisionID: publication.ChallengeRevisionID,
			SourceSlug: publication.SourceSlug, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
		published := challengeRevisionFromPublication(publication.ChallengeTitle, publication.Runtime, scenarioType, scenarioTags, publication.ContentRevision, materializedRevision,
			*publication.Artifact, publication.ChallengeRevisionID, publication.ChallengeID, workflow.Source.Ref, workflow.SourceRevision,
			"", publication.SourceSlug, publication.TargetPath, challengedomain.SourceAuthoring, now.UTC())
		if err = insertPersistedChallengeTx(ctx, tx, stable); err == nil {
			err = insertPersistedChallengeRevisionTx(ctx, tx, published)
		}
	} else {
		err = finalizeAuthoringRevisionTx(ctx, tx, *session, workflow, publication, scenarioType, scenarioTags, materializedRevision, now.UTC())
	}
	if errors.Is(err, challengedomain.ErrRevisionConflict) {
		return failStaleRevisionPublicationTx(ctx, tx, workflow, candidateRevision, session, err, now.UTC())
	}
	if err != nil {
		return err
	}
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
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1,
		runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL, active_agent_run_id = NULL,
		last_error = '', finalizer_error_category = '', finalizer_last_error = '', finalizer_last_attempted_at = NULL,
		finalizer_next_retry_at = NULL, updated_at = ? WHERE id = ?`, generation.StatePublished, now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("complete generation publication: %w", err)
	}
	return tx.Commit()
}

// failStaleRevisionPublicationTx ends only a stale revision workflow. Its
// stable Challenge and active revision remain untouched, while the author can
// see the conflict and begin a new revision session from the current active
// state. Leaving this workflow in ChallengePublishing would make a known
// optimistic-concurrency conflict retry forever.
func failStaleRevisionPublicationTx(ctx context.Context, tx *Tx, workflow *generation.Workflow, candidate *generation.Revision, session *authoring.Session, cause error, now time.Time) error {
	if workflow == nil || candidate == nil || session == nil {
		return generation.ErrCandidateInvalidState
	}
	summary := strings.TrimSpace(cause.Error())
	failure, err := marshalJSON(generation.Failure{Class: generation.FailureArtifact, Code: "REVISION_CONFLICT", Summary: summary})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET failure = ?::jsonb, updated_at = ? WHERE id = ?`, failure, now.UTC(), candidate.ID); err != nil {
		return fmt.Errorf("record stale revision publication failure: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET last_error = ?, updated_at = ? WHERE id = ?`, summary, nowText(now), session.ID); err != nil {
		return fmt.Errorf("record stale revision authoring error: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1,
		runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL, active_agent_run_id = NULL,
		last_error = ?, updated_at = ? WHERE id = ?`, generation.StateFailed, summary, now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("fail stale revision workflow: %w", err)
	}
	return tx.Commit()
}

func prepareGenerationPublicationTx(ctx context.Context, tx *Tx, session authoring.Session, candidate *generation.Revision, metadata generation.PublicationMetadata, now time.Time) (generation.Publication, error) {
	if candidate == nil || candidate.Artifact == nil || candidate.Verification == nil || !candidate.Verification.Passed || metadata.Validate() != nil || metadata.Runtime != candidate.Snapshot.Runtime || now.IsZero() {
		return generation.Publication{}, generation.ErrCandidateInvalidState
	}
	staging := *candidate.Artifact
	publication := generation.Publication{CandidateRevisionID: candidate.ID, IntentRevision: 1, ChallengeTitle: metadata.Title,
		Runtime: metadata.Runtime, RequestedAt: now.UTC(), StagingArtifact: &staging}
	if session.RevisionChallengeID == "" {
		publication.ChallengeID = challenge.NewID()
		publication.ChallengeRevisionID = challenge.NewRevisionID()
		publication.SourceSlug = challenge.SourceSlugFor(metadata.Title, publication.ChallengeID)
	} else {
		target, err := lockPersistedChallengeTx(ctx, tx, session.RevisionChallengeID)
		if err != nil {
			return generation.Publication{}, err
		}
		if target.SourceKind != challengedomain.SourceAuthoring || target.OwnerUserID != session.UserID || target.State != challengedomain.StateActive || target.ActiveRevisionID != session.RevisionBaseActiveRevisionID {
			return generation.Publication{}, challengedomain.ErrRevisionConflict
		}
		active, err := lockPersistedChallengeRevisionTx(ctx, tx, target.ActiveRevisionID)
		if err != nil {
			return generation.Publication{}, err
		}
		if active.ChallengeID != target.ID || active.State != challengedomain.RevisionActive || active.Runtime != metadata.Runtime {
			return generation.Publication{}, generation.ErrCandidateInvalidState
		}
		publication.ChallengeID = target.ID
		publication.ChallengeRevisionID = challenge.NewRevisionID()
		publication.BaseActiveRevisionID = target.ActiveRevisionID
		publication.SourceSlug = target.SourceSlug
	}
	publication.TargetPath = challenge.MaterializedPath(publication.SourceSlug, publication.ChallengeRevisionID)
	if err := publication.ValidateIntent(); err != nil {
		return generation.Publication{}, err
	}
	return publication, nil
}

func finalizeAuthoringRevisionTx(ctx context.Context, tx *Tx, session authoring.Session, workflow *generation.Workflow, publication generation.Publication, scenarioType challenge.ScenarioType, scenarioTags []string, materializedRevision string, now time.Time) error {
	if workflow == nil || publication.ChallengeID != session.RevisionChallengeID || publication.BaseActiveRevisionID != session.RevisionBaseActiveRevisionID {
		return challengedomain.ErrRevisionConflict
	}
	target, err := lockPersistedChallengeTx(ctx, tx, session.RevisionChallengeID)
	if err != nil {
		return err
	}
	if target.SourceKind != challengedomain.SourceAuthoring || target.OwnerUserID != session.UserID || target.State != challengedomain.StateActive || target.ActiveRevisionID != publication.BaseActiveRevisionID || target.SourceSlug != publication.SourceSlug {
		return challengedomain.ErrRevisionConflict
	}
	previous, err := lockPersistedChallengeRevisionTx(ctx, tx, target.ActiveRevisionID)
	if err != nil {
		return err
	}
	if previous.ChallengeID != target.ID || previous.State != challengedomain.RevisionActive || previous.Runtime != publication.Runtime {
		return generation.ErrCandidateInvalidState
	}
	published := challengeRevisionFromPublication(publication.ChallengeTitle, publication.Runtime, scenarioType, scenarioTags, publication.ContentRevision, materializedRevision,
		*publication.Artifact, publication.ChallengeRevisionID, target.ID, workflow.Source.Ref, workflow.SourceRevision,
		target.ActiveRevisionID, target.SourceSlug, publication.TargetPath, challengedomain.SourceAuthoring, now)
	if _, err := tx.ExecContext(ctx, `UPDATE challenge_revisions SET state = ? WHERE id = ? AND state = ?`, challengedomain.RevisionSuperseded, previous.ID, challengedomain.RevisionActive); err != nil {
		return fmt.Errorf("supersede challenge revision: %w", err)
	}
	if err := insertPersistedChallengeRevisionTx(ctx, tx, published); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE challenges SET active_revision_id = ?, updated_at = ? WHERE id = ? AND active_revision_id = ? AND state = ?`,
		publication.ChallengeRevisionID, now, target.ID, target.ActiveRevisionID, challengedomain.StateActive)
	if err != nil {
		return fmt.Errorf("switch active challenge revision: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return challengedomain.ErrRevisionConflict
	}
	return nil
}

func (d *GenerationRepository) ReportGenerationInfrastructureFailure(ctx context.Context, claim generation.Claim, expected generation.WorkflowState, failure generation.Failure, now time.Time) (*generation.Workflow, error) {
	if !claim.Valid() || !expected.RuntimeState() || failure.Class != generation.FailureInfrastructure || failure.Validate() != nil || now.IsZero() {
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
	var updated *generation.Workflow
	if workflow.RuntimeAttempt >= generation.MaxRuntimeAttempts {
		updated, err = scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1,
			runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL, last_error = ?, next_run_at = ?, updated_at = ?
			WHERE id = ? RETURNING `+generationWorkflowColumns, generation.StateFailed, failure.Summary, now.UTC(), now.UTC(), workflow.ID))
	} else {
		nextAttempt := workflow.RuntimeAttempt + 1
		updated, err = scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET runtime_attempt = ?,
			lease_owner = '', lease_expires_at = NULL, last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ? RETURNING `+generationWorkflowColumns,
			nextAttempt, failure.Summary, generation.NextRetry(nextAttempt, now), now.UTC(), workflow.ID))
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
	if !claim.Valid() || (!expected.AgentState() && !expected.RuntimeState()) || expected == generation.StateChallengePublishing ||
		failure.Class != generation.FailureArtifact || failure.Validate() != nil || now.IsZero() {
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
		return errors.New("generation artifact failure has no candidate revision")
	}
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
	if workflow.ActiveAgentRunID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
			WHERE id = ? AND status = ?`, agent.RunFailed, failure.Summary, now.UTC(), now.UTC(), workflow.ActiveAgentRunID, agent.RunRunning); err != nil {
			return fmt.Errorf("fail generation agent run: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, active_agent_run_id = NULL,
		state_version = state_version + 1, runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL,
		last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ?`,
		generation.StateGenerating, failure.Summary, now.UTC(), now.UTC(), workflow.ID); err != nil {
		return fmt.Errorf("return artifact failure to generation: %w", err)
	}
	return tx.Commit()
}

func (d *GenerationRepository) CancelGenerationWorkflow(ctx context.Context, sessionID, userID string, cancellation generation.Cancellation, now time.Time) (*generation.Workflow, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || !cancellation.Valid() || now.IsZero() {
		return nil, errors.New("authoring generation cancellation requires session, user, workflow, idempotency key, and current time")
	}
	requestDigest, err := generationActionRequestDigest(generationActionCancelGeneration, cancellation)
	if err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin authoring generation cancellation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockAuthoringSessionTx(ctx, tx, sessionID, userID); err != nil {
		return nil, err
	}
	if receipt, err := generationActionReceiptTx(ctx, tx, sessionID, generationActionCancelGeneration, cancellation.IdempotencyKey, requestDigest); err != nil {
		return nil, err
	} else if receipt != nil {
		if receipt.WorkflowID != cancellation.WorkflowID {
			return nil, authoring.ErrVersionConflict
		}
		return generationWorkflowForReceiptTx(ctx, tx, receipt.WorkflowID)
	}
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND source_kind = ? AND source_ref = ? FOR UPDATE`,
		cancellation.WorkflowID, generation.SourceAuthoring, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrInvalidState
	}
	if err != nil {
		return nil, fmt.Errorf("load authoring generation workflow for cancellation: %w", err)
	}
	if workflow.State == generation.StateCancelled {
		if err := insertGenerationActionReceiptTx(ctx, tx, generationActionReceipt{
			SessionID: sessionID, Action: generationActionCancelGeneration, IdempotencyKey: cancellation.IdempotencyKey,
			RequestDigest: requestDigest, WorkflowID: workflow.ID, CreatedAt: now.UTC(),
		}); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit idempotent authoring generation cancellation: %w", err)
		}
		return workflow, nil
	}
	if workflow.State.Terminal() {
		return nil, authoring.ErrInvalidState
	}
	const reason = "author cancelled generation workflow"
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
		WHERE owner_kind = ? AND owner_ref = ? AND status = ?`, agent.RunCancelled, reason, now.UTC(), now.UTC(),
		"generation-workflow", workflow.ID, agent.RunRunning); err != nil {
		return nil, fmt.Errorf("cancel generation agent runs: %w", err)
	}
	updated, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1,
		runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL, active_agent_run_id = NULL,
		workspace_snapshot_digest = '', last_error = ?, next_run_at = ?, updated_at = ? WHERE id = ? RETURNING `+generationWorkflowColumns,
		generation.StateCancelled, reason, now.UTC(), now.UTC(), workflow.ID))
	if err != nil {
		return nil, fmt.Errorf("cancel authoring generation workflow: %w", err)
	}
	if err := insertGenerationActionReceiptTx(ctx, tx, generationActionReceipt{
		SessionID: sessionID, Action: generationActionCancelGeneration, IdempotencyKey: cancellation.IdempotencyKey,
		RequestDigest: requestDigest, WorkflowID: workflow.ID, CreatedAt: now.UTC(),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit authoring generation cancellation: %w", err)
	}
	return updated, nil
}

func lockGenerationClaimTx(ctx context.Context, tx *Tx, claim generation.Claim, expected generation.WorkflowState, now time.Time) (*generation.Workflow, error) {
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ? AND state = ? AND state_version = ?
		AND lease_owner = ? AND lease_expires_at > ? FOR UPDATE`, claim.Workflow.ID, expected, claim.StateVersion, claim.LeaseOwner, now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generationLeaseLost()
	}
	if err != nil {
		return nil, fmt.Errorf("lock generation workflow lease: %w", err)
	}
	return workflow, nil
}

func generationLeaseLost() error { return generation.ErrLeaseLost }

func runtimeAttemptForState(state generation.WorkflowState) int {
	if state.RuntimeState() {
		return 1
	}
	return 0
}

// recoverExpiredGenerationRuntimeLeaseTx charges one infrastructure attempt
// before another Runtime Worker can take over the same external action. The
// state version is deliberately unchanged: retries must use the same
// provider-side identity and create-or-get semantics.
func recoverExpiredGenerationRuntimeLeaseTx(ctx context.Context, tx *Tx, workflow *generation.Workflow, now time.Time) (*generation.Workflow, error) {
	if workflow == nil || !workflow.State.RuntimeState() || workflow.LeaseExpiresAt == nil || workflow.LeaseExpiresAt.After(now) {
		return workflow, nil
	}
	if workflow.RuntimeAttempt >= generation.MaxRuntimeAttempts {
		if _, err := tx.ExecContext(ctx, `UPDATE generation_workflows SET state = ?, state_version = state_version + 1,
			runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL, last_error = ?, next_run_at = ?, updated_at = ?
			WHERE id = ? AND state_version = ? AND lease_owner = ? AND lease_expires_at <= ?`,
			generation.StateFailed, "runtime worker lease expired", now.UTC(), now.UTC(), workflow.ID, workflow.StateVersion, workflow.LeaseOwner, now.UTC()); err != nil {
			return nil, fmt.Errorf("fail exhausted expired runtime lease: %w", err)
		}
		return nil, nil
	}
	nextAttempt := workflow.RuntimeAttempt + 1
	updated, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, `UPDATE generation_workflows SET runtime_attempt = ?, lease_owner = '',
		lease_expires_at = NULL, last_error = ?, next_run_at = ?, updated_at = ?
		WHERE id = ? AND state_version = ? AND lease_owner = ? AND lease_expires_at <= ? RETURNING `+generationWorkflowColumns,
		nextAttempt, "runtime worker lease expired", now.UTC(), now.UTC(), workflow.ID, workflow.StateVersion, workflow.LeaseOwner, now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, generationLeaseLost()
	}
	if err != nil {
		return nil, fmt.Errorf("recover expired runtime lease: %w", err)
	}
	return updated, nil
}

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

func insertCandidateRevisionTx(ctx context.Context, tx *Tx, revision generation.Revision) error {
	snapshot, err := marshalJSON(revision.Snapshot)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO candidate_revisions
		(id, source_kind, source_ref, source_revision, judge_run_id,
		parent_candidate_revision_id, repair_reason, archive_path, archive_sha256, execution_snapshot, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?::jsonb, ?, ?)`,
		revision.ID, revision.Source.Kind, revision.Source.Ref, revision.SourceRevision,
		revision.JudgeRunID, revision.ParentCandidateID, revision.RepairReason, revision.ArchivePath, revision.ArchiveSHA256, snapshot, revision.CreatedAt, revision.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert candidate revision: %w", err)
	}
	return nil
}

const candidateRevisionColumns = `id, source_kind, source_ref, source_revision, judge_run_id,
	COALESCE(parent_candidate_revision_id, ''), repair_reason, archive_path, archive_sha256, execution_snapshot, build_output, artifact_reference,
	verify_environment, verification_report, failure, publication, created_at, updated_at, verified_at, published_at`
const candidateRevisionSelect = `SELECT ` + candidateRevisionColumns + ` FROM candidate_revisions`

func scanCandidateRevision(row agentRow) (*generation.Revision, error) {
	var revision generation.Revision
	var snapshot []byte
	var build, artifact, verifyEnvironment, verification, failure, publication []byte
	var verifiedAt, publishedAt sql.NullTime
	err := row.Scan(&revision.ID, &revision.Source.Kind, &revision.Source.Ref, &revision.SourceRevision,
		&revision.JudgeRunID, &revision.ParentCandidateID, &revision.RepairReason, &revision.ArchivePath, &revision.ArchiveSHA256, &snapshot,
		&build, &artifact, &verifyEnvironment, &verification, &failure, &publication, &revision.CreatedAt, &revision.UpdatedAt, &verifiedAt, &publishedAt)
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

func scanGenerationWorkflow(row agentRow) (*generation.Workflow, error) {
	var workflow generation.Workflow
	var leaseExpiresAt, finalizerLastAttemptedAt, finalizerNextRetryAt sql.NullTime
	err := row.Scan(&workflow.ID, &workflow.Source.Kind, &workflow.Source.Ref, &workflow.SourceRevision, &workflow.State,
		&workflow.CandidateRevisionID, &workflow.WorkspaceSnapshotDigest, &workflow.ActiveAgentRunID, &workflow.StateVersion, &workflow.RuntimeAttempt, &workflow.LeaseOwner, &leaseExpiresAt,
		&workflow.NextRunAt, &workflow.LastError, &workflow.FinalizerErrorCategory, &workflow.FinalizerLastError, &finalizerLastAttemptedAt, &finalizerNextRetryAt,
		&workflow.CreatedAt, &workflow.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if leaseExpiresAt.Valid {
		value := leaseExpiresAt.Time.UTC()
		workflow.LeaseExpiresAt = &value
	}
	if finalizerLastAttemptedAt.Valid {
		value := finalizerLastAttemptedAt.Time.UTC()
		workflow.FinalizerLastAttemptedAt = &value
	}
	if finalizerNextRetryAt.Valid {
		value := finalizerNextRetryAt.Time.UTC()
		workflow.FinalizerNextRetryAt = &value
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
	} else if err != nil {
		return fmt.Errorf("lock authoring session: %w", err)
	}
	return nil
}

func generationActionRequestDigest(action generationAction, request any) (string, error) {
	canonical, err := json.Marshal(struct {
		Action  generationAction `json:"action"`
		Request any              `json:"request"`
	}{Action: action, Request: request})
	if err != nil {
		return "", fmt.Errorf("encode generation action receipt: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func generationActionReceiptTx(ctx context.Context, tx *Tx, sessionID string, action generationAction, idempotencyKey, requestDigest string) (*generationActionReceipt, error) {
	value := &generationActionReceipt{SessionID: sessionID, Action: action, IdempotencyKey: idempotencyKey}
	var planRevision sql.NullInt64
	var candidateID sql.NullString
	var proposalRevision sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT request_digest, workflow_id, plan_revision, candidate_revision_id, proposal_revision, created_at
		FROM generation_action_receipts WHERE session_id = ? AND action = ? AND idempotency_key = ? FOR UPDATE`,
		sessionID, action, idempotencyKey).Scan(&value.RequestDigest, &value.WorkflowID, &planRevision, &candidateID, &proposalRevision, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read generation action receipt: %w", err)
	}
	if value.RequestDigest != requestDigest {
		return nil, authoring.ErrVersionConflict
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

func insertGenerationActionReceiptTx(ctx context.Context, tx *Tx, value generationActionReceipt) error {
	if strings.TrimSpace(value.SessionID) == "" || strings.TrimSpace(string(value.Action)) == "" || strings.TrimSpace(value.IdempotencyKey) == "" ||
		strings.TrimSpace(value.RequestDigest) == "" || strings.TrimSpace(value.WorkflowID) == "" || value.CreatedAt.IsZero() {
		return errors.New("generation action receipt is incomplete")
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO generation_action_receipts
		(session_id, action, idempotency_key, request_digest, workflow_id, plan_revision, candidate_revision_id, proposal_revision, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.SessionID, value.Action, value.IdempotencyKey, value.RequestDigest, value.WorkflowID,
		planRevision, candidateRevisionID, proposalRevision, value.CreatedAt.UTC()); err != nil {
		return fmt.Errorf("record generation action receipt: %w", err)
	}
	return nil
}

func generationWorkflowForReceiptTx(ctx context.Context, tx *Tx, workflowID string) (*generation.Workflow, error) {
	workflow, err := scanGenerationWorkflow(tx.QueryRowContext(ctx, generationWorkflowSelect+` WHERE id = ?`, workflowID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGenerationWorkflowNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read generation action workflow: %w", err)
	}
	return workflow, nil
}
