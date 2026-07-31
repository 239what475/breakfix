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
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/worklist"
)

func (d *DB) CreateAuthoringSession(ctx context.Context, session authoring.Session, plan authoring.Plan) (*authoring.Session, error) {
	now := time.Now().UTC()
	session.State = authoring.StateDraftConversation
	session.CurrentRevision = 0
	session.VisibleRevision = 0
	session.CreatedAt = now
	session.UpdatedAt = now
	if session.RuntimeSessionID == "" {
		session.RuntimeSessionID = agentruntime.NewID("authoring-session")
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return nil, fmt.Errorf("marshal initial authoring plan: %w", err)
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions
		(id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		session.RuntimeSessionID, "authoring", "authoring-session", session.ID, session.UserID, agentruntime.SessionActive, now, now); err != nil {
		return nil, fmt.Errorf("insert authoring agent session: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO authoring_sessions
		(id, user_id, runtime_session_id, generator_session_id, generator_run_id, candidate_revision_id, state, current_revision, visible_revision, publish_challenge_id, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		session.ID, session.UserID, session.RuntimeSessionID, "", "", "", session.State, session.CurrentRevision, session.VisibleRevision, "", "", nowText(now), nowText(now)); err != nil {
		return nil, fmt.Errorf("insert authoring session: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO authoring_revisions (session_id, revision, plan_json, created_at)
		VALUES (?, ?, ?, ?)`, session.ID, 0, string(planJSON), nowText(now)); err != nil {
		return nil, fmt.Errorf("insert initial authoring revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &session, nil
}

func (d *DB) GetAuthoringSession(ctx context.Context, id, userID string) (*authoring.Session, error) {
	return d.readAuthoringSession(ctx, `SELECT id, user_id, runtime_session_id, generator_session_id, generator_run_id, candidate_revision_id, state, current_revision, visible_revision,
		publish_challenge_id, last_error, created_at, updated_at
		FROM authoring_sessions WHERE id = ? AND user_id = ?`, id, userID)
}

func (d *DB) GetLatestOpenAuthoringSession(ctx context.Context, userID string) (*authoring.Session, error) {
	return d.readAuthoringSession(ctx, `SELECT id, user_id, runtime_session_id, generator_session_id, generator_run_id, candidate_revision_id, state, current_revision, visible_revision,
		publish_challenge_id, last_error, created_at, updated_at
		FROM authoring_sessions WHERE user_id = ? AND state != ? ORDER BY updated_at DESC, id DESC LIMIT 1`, userID, authoring.StatePublished)
}

func (d *DB) GetAuthoringSessionInternal(ctx context.Context, id string) (*authoring.Session, error) {
	return d.readAuthoringSession(ctx, `SELECT id, user_id, runtime_session_id, generator_session_id, generator_run_id, candidate_revision_id, state, current_revision, visible_revision,
		publish_challenge_id, last_error, created_at, updated_at
		FROM authoring_sessions WHERE id = ?`, id)
}

func (d *DB) readAuthoringSession(ctx context.Context, query string, args ...any) (*authoring.Session, error) {
	var session authoring.Session
	var state string
	var createdAt, updatedAt string
	err := d.conn.QueryRowContext(ctx, query, args...).Scan(
		&session.ID, &session.UserID, &session.RuntimeSessionID, &session.GeneratorSessionID, &session.GeneratorRunID, &session.CandidateRevisionID,
		&state, &session.CurrentRevision, &session.VisibleRevision, &session.PublishChallengeID, &session.LastError, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read authoring session: %w", err)
	}
	session.State = authoring.SessionState(state)
	session.CreatedAt = parseAuthoringTime(createdAt)
	session.UpdatedAt = parseAuthoringTime(updatedAt)
	return &session, nil
}

func (d *DB) GetAuthoringRevision(ctx context.Context, sessionID string, revision int64) (*authoring.Revision, error) {
	var planJSON, candidateRevisionID, createdAt string
	var number int64
	err := d.conn.QueryRowContext(ctx, `SELECT revision, plan_json, candidate_revision_id, created_at FROM authoring_revisions
		WHERE session_id = ? AND revision = ?`, sessionID, revision).Scan(
		&number, &planJSON, &candidateRevisionID, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read authoring revision: %w", err)
	}
	return decodeAuthoringRevision(number, planJSON, candidateRevisionID, createdAt)
}

func decodeAuthoringRevision(number int64, planJSON, candidateRevisionID, createdAt string) (*authoring.Revision, error) {
	var plan authoring.Plan
	if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
		return nil, fmt.Errorf("decode authoring plan: %w", err)
	}
	return &authoring.Revision{Number: number, Plan: plan, CandidateRevisionID: candidateRevisionID, CreatedAt: parseAuthoringTime(createdAt)}, nil
}

// StartAuthoringRun atomically records a user message, creates its durable
// Agent Run, and snapshots a private Plan stage. Nothing is publicly revised
// until FinalizeAuthoringRun commits the corresponding attempt.
func (d *DB) StartAuthoringRun(ctx context.Context, sessionID, userID string, message agentruntime.Message, run agentruntime.CreateRun) (*authoring.Stage, *agentruntime.Run, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || message.Role != "user" || strings.TrimSpace(message.Content) == "" {
		return nil, nil, errors.New("authoring run requires session, user, and user message")
	}
	if err := agentruntime.ValidateCreateRun(run); err != nil {
		return nil, nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin authoring run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT id FROM authoring_sessions WHERE id = ? AND user_id = ? FOR UPDATE`, sessionID, userID); err != nil {
		return nil, nil, fmt.Errorf("lock authoring session: %w", err)
	}
	session, err := readAuthoringSessionTx(ctx, tx, sessionID, userID)
	if err != nil {
		return nil, nil, err
	}
	if !authoring.AllowsAgentPlanStage(session.State) || session.RuntimeSessionID == "" {
		return nil, nil, authoring.ErrInvalidState
	}
	if run.SessionID != session.RuntimeSessionID || run.Purpose != "authoring" || run.OwnerKind != "authoring-session" || run.OwnerRef != session.ID {
		return nil, nil, errors.New("authoring run ownership is invalid")
	}
	var runtimeStatus agentruntime.SessionStatus
	err = tx.QueryRowContext(ctx, `SELECT status FROM agent_sessions WHERE id = ? FOR UPDATE`, run.SessionID).Scan(&runtimeStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, agentruntime.ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("lock authoring agent session: %w", err)
	}
	if runtimeStatus != agentruntime.SessionActive {
		return nil, nil, errors.New("authoring agent session is not active")
	}
	if err := ensureNoActiveSessionRun(ctx, tx, run.SessionID); err != nil {
		return nil, nil, err
	}
	base, err := readAuthoringRevisionTx(ctx, tx, session.ID, session.CurrentRevision)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	if message.ID == "" {
		message.ID = agentruntime.NewID("authoring-message")
	}
	message.SessionID = run.SessionID
	message.CreatedAt = now
	if err := insertAgentMessageTx(ctx, tx, &message); err != nil {
		return nil, nil, err
	}
	created, err := createRunTx(ctx, tx, run, now)
	if err != nil {
		return nil, nil, err
	}
	planJSON, err := json.Marshal(base.Plan)
	if err != nil {
		return nil, nil, fmt.Errorf("encode initial authoring stage: %w", err)
	}
	stage := &authoring.Stage{
		RunID:         created.ID,
		SessionID:     session.ID,
		BaseRevision:  base.Number,
		StageRevision: base.Number,
		Plan:          base.Plan,
		Changes:       []authoring.Change{},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO authoring_stages
		(run_id, session_id, base_revision, stage_revision, plan_json, changes_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?::jsonb, '[]'::jsonb, ?, ?)`,
		stage.RunID, stage.SessionID, stage.BaseRevision, stage.StageRevision, string(planJSON), now, now); err != nil {
		return nil, nil, fmt.Errorf("insert authoring stage: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET updated_at = ? WHERE id = ?`, nowText(now), session.ID); err != nil {
		return nil, nil, fmt.Errorf("touch authoring session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit authoring run: %w", err)
	}
	return stage, created, nil
}

func (d *DB) GetAuthoringStage(ctx context.Context, runID string) (*authoring.Stage, error) {
	return readAuthoringStage(d.conn.QueryRowContext(ctx, `SELECT run_id, session_id, base_revision, stage_revision, plan_json, changes_json, created_at, updated_at
		FROM authoring_stages WHERE run_id = ?`, runID))
}

// UpdateAuthoringStage applies one Server-validated tool mutation. The lease
// and stage revision prevent stale Workers from changing a newer attempt.
func (d *DB) UpdateAuthoringStage(ctx context.Context, claim agentruntime.Claim, expectedStageRevision int64, plan authoring.Plan, change authoring.Change) (*authoring.Stage, error) {
	if !claim.Valid() || expectedStageRevision < 0 || strings.TrimSpace(change.Kind) == "" || strings.TrimSpace(change.Summary) == "" || strings.TrimSpace(change.DifficultyImpact) == "" {
		return nil, errors.New("authoring stage update is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin authoring stage update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateLeaseTx(ctx, tx, claim, time.Now().UTC()); err != nil {
		return nil, err
	}
	stage, err := readAuthoringStage(tx.QueryRowContext(ctx, `SELECT run_id, session_id, base_revision, stage_revision, plan_json, changes_json, created_at, updated_at
		FROM authoring_stages WHERE run_id = ? FOR UPDATE`, claim.Run.ID))
	if err != nil {
		return nil, err
	}
	if stage.StageRevision != expectedStageRevision {
		return nil, authoring.ErrVersionConflict
	}
	session, err := readAuthoringSessionTx(ctx, tx, stage.SessionID, "")
	if err != nil {
		return nil, err
	}
	if session.RuntimeSessionID != claim.Run.SessionID || !authoring.AllowsAgentPlanStage(session.State) {
		return nil, authoring.ErrInvalidState
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return nil, fmt.Errorf("encode staged authoring plan: %w", err)
	}
	// All staged mutations become the same single public Revision if the Run
	// succeeds, so expose that eventual revision rather than private stage hops.
	change.Revision = stage.BaseRevision + 1
	stage.Changes = append(stage.Changes, change)
	changesJSON, err := json.Marshal(stage.Changes)
	if err != nil {
		return nil, fmt.Errorf("encode staged authoring changes: %w", err)
	}
	now := time.Now().UTC()
	stage.StageRevision++
	stage.Plan = plan
	stage.UpdatedAt = now
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_stages
		SET stage_revision = ?, plan_json = ?::jsonb, changes_json = ?::jsonb, updated_at = ? WHERE run_id = ?`,
		stage.StageRevision, string(planJSON), string(changesJSON), now, stage.RunID); err != nil {
		return nil, fmt.Errorf("update authoring stage: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET updated_at = ? WHERE id = ?`, nowText(now), stage.SessionID); err != nil {
		return nil, fmt.Errorf("touch staged authoring session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit authoring stage update: %w", err)
	}
	return stage, nil
}

// FinalizeAuthoringRun atomically makes at most one public Revision visible,
// stores the final Assistant message, and completes the matching Agent Run.
func (d *DB) FinalizeAuthoringRun(ctx context.Context, claim agentruntime.Claim, content string) (*authoring.Revision, error) {
	content = strings.TrimSpace(content)
	if !claim.Valid() || content == "" {
		return nil, errors.New("authoring finalization requires lease and message")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin authoring finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	if err := validateLeaseTx(ctx, tx, claim, now); err != nil {
		return nil, err
	}
	stage, err := readAuthoringStage(tx.QueryRowContext(ctx, `SELECT run_id, session_id, base_revision, stage_revision, plan_json, changes_json, created_at, updated_at
		FROM authoring_stages WHERE run_id = ? FOR UPDATE`, claim.Run.ID))
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT id FROM authoring_sessions WHERE id = ? FOR UPDATE`, stage.SessionID); err != nil {
		return nil, fmt.Errorf("lock finalizing authoring session: %w", err)
	}
	session, err := readAuthoringSessionTx(ctx, tx, stage.SessionID, "")
	if err != nil {
		return nil, err
	}
	if session.RuntimeSessionID != claim.Run.SessionID || session.CurrentRevision != stage.BaseRevision || !authoring.AllowsAgentPlanStage(session.State) {
		return nil, authoring.ErrInvalidState
	}
	var revision *authoring.Revision
	if len(stage.Changes) > 0 {
		if err := stage.Plan.ValidateForGeneration(); err != nil {
			return nil, err
		}
		planJSON, err := json.Marshal(stage.Plan)
		if err != nil {
			return nil, fmt.Errorf("encode finalized authoring plan: %w", err)
		}
		next := stage.BaseRevision + 1
		if _, err := tx.ExecContext(ctx, `INSERT INTO authoring_revisions (session_id, revision, plan_json, created_at)
			VALUES (?, ?, ?, ?)`, session.ID, next, string(planJSON), nowText(now)); err != nil {
			return nil, fmt.Errorf("insert finalized authoring revision: %w", err)
		}
		nextState := authoring.NextPlanRevisionState(session.State)
		if nextState == authoring.StateRevisingAndVerifying {
			if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET current_revision = ?, state = ?, generator_run_id = '', candidate_revision_id = '', last_error = '', updated_at = ? WHERE id = ?`,
				next, nextState, nowText(now), session.ID); err != nil {
				return nil, fmt.Errorf("update finalized authoring session: %w", err)
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET current_revision = ?, state = ?, last_error = '', updated_at = ? WHERE id = ?`,
			next, nextState, nowText(now), session.ID); err != nil {
			return nil, fmt.Errorf("update finalized authoring session: %w", err)
		}
		revision = &authoring.Revision{Number: next, Plan: stage.Plan, CreatedAt: now}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET updated_at = ? WHERE id = ?`, nowText(now), session.ID); err != nil {
			return nil, fmt.Errorf("touch finalized authoring session: %w", err)
		}
		revision = &authoring.Revision{Number: session.CurrentRevision, Plan: stage.Plan, CreatedAt: now}
	}
	metadata, err := json.Marshal(struct {
		Changes []authoring.Change `json:"changes"`
	}{Changes: stage.Changes})
	if err != nil {
		return nil, fmt.Errorf("encode authoring final message metadata: %w", err)
	}
	message := agentruntime.Message{ID: agentruntime.NewID("authoring-message"), SessionID: claim.Run.SessionID, Role: "assistant", Content: content, Metadata: metadata, CreatedAt: now}
	if err := insertAgentMessageTx(ctx, tx, &message); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, completed_at = ?, updated_at = ?
		WHERE id = ? AND status = ?`, agentruntime.RunSucceeded, now, now, claim.Run.ID, agentruntime.RunRunning)
	if err != nil {
		return nil, fmt.Errorf("complete finalized authoring run: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, agentruntime.ErrLeaseLost
	}
	if err := completeAgentWorkItemTx(ctx, tx, claim, worklist.StateSucceeded, "", "", now); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM authoring_stages WHERE run_id = ?`, stage.RunID); err != nil {
		return nil, fmt.Errorf("delete finalized authoring stage: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit authoring finalization: %w", err)
	}
	return revision, nil
}

func validateLeaseTx(ctx context.Context, tx *Tx, claim agentruntime.Claim, now time.Time) error {
	_, err := lockAgentClaim(ctx, tx, claim, now)
	return err
}

func readAuthoringStage(row agentRow) (*authoring.Stage, error) {
	var stage authoring.Stage
	var planJSON, changesJSON []byte
	err := row.Scan(&stage.RunID, &stage.SessionID, &stage.BaseRevision, &stage.StageRevision, &planJSON, &changesJSON, &stage.CreatedAt, &stage.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read authoring stage: %w", err)
	}
	if err := json.Unmarshal(planJSON, &stage.Plan); err != nil {
		return nil, fmt.Errorf("decode authoring stage plan: %w", err)
	}
	if err := json.Unmarshal(changesJSON, &stage.Changes); err != nil {
		return nil, fmt.Errorf("decode authoring stage changes: %w", err)
	}
	return &stage, nil
}

func (d *DB) ReplaceAuthoringPlan(ctx context.Context, sessionID, userID string, expected int64, plan authoring.Plan, state authoring.SessionState) (*authoring.Revision, error) {
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return nil, fmt.Errorf("marshal authoring plan: %w", err)
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	session, err := readAuthoringSessionTx(ctx, tx, sessionID, userID)
	if err != nil {
		return nil, err
	}
	if session.CurrentRevision != expected {
		return nil, authoring.ErrVersionConflict
	}
	next := expected + 1
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO authoring_revisions (session_id, revision, plan_json, created_at)
		VALUES (?, ?, ?, ?)`, sessionID, next, string(planJSON), nowText(now)); err != nil {
		return nil, fmt.Errorf("insert authoring revision: %w", err)
	}
	// A changed intent starts a new candidate lineage while the previous
	// verified revision remains author-visible until its replacement passes.
	if state == authoring.StateRevisingAndVerifying {
		if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions
				SET current_revision = ?, state = ?, generator_run_id = '', candidate_revision_id = '', last_error = '', updated_at = ?
			WHERE id = ? AND user_id = ?`, next, state, nowText(now), sessionID, userID); err != nil {
			return nil, fmt.Errorf("update authoring session revision: %w", err)
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET current_revision = ?, state = ?, last_error = '', updated_at = ?
		WHERE id = ? AND user_id = ?`, next, state, nowText(now), sessionID, userID); err != nil {
		return nil, fmt.Errorf("update authoring session revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &authoring.Revision{Number: next, Plan: plan, CreatedAt: now}, nil
}

func readAuthoringSessionTx(ctx context.Context, tx *Tx, id, userID string) (*authoring.Session, error) {
	query := `SELECT id, user_id, runtime_session_id, generator_session_id, generator_run_id, candidate_revision_id, state, current_revision, visible_revision,
		publish_challenge_id, last_error, created_at, updated_at FROM authoring_sessions WHERE id = ?`
	args := []any{id}
	if userID != "" {
		query += " AND user_id = ?"
		args = append(args, userID)
	}
	var session authoring.Session
	var state, createdAt, updatedAt string
	err := tx.QueryRowContext(ctx, query, args...).Scan(&session.ID, &session.UserID, &session.RuntimeSessionID, &session.GeneratorSessionID,
		&session.GeneratorRunID, &session.CandidateRevisionID, &state, &session.CurrentRevision, &session.VisibleRevision,
		&session.PublishChallengeID, &session.LastError, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	session.State = authoring.SessionState(state)
	session.CreatedAt = parseAuthoringTime(createdAt)
	session.UpdatedAt = parseAuthoringTime(updatedAt)
	return &session, nil
}

func readAuthoringRevisionTx(ctx context.Context, tx *Tx, sessionID string, revision int64) (*authoring.Revision, error) {
	var planJSON, candidateRevisionID, createdAt string
	err := tx.QueryRowContext(ctx, `SELECT plan_json, candidate_revision_id, created_at
		FROM authoring_revisions WHERE session_id = ? AND revision = ?`, sessionID, revision).Scan(&planJSON, &candidateRevisionID, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return decodeAuthoringRevision(revision, planJSON, candidateRevisionID, createdAt)
}

func nowText(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseAuthoringTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
