package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/authoring"
)

func (d *DB) CreateAuthoringSession(ctx context.Context, session authoring.Session, plan authoring.Plan) (*authoring.Session, error) {
	if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.UserID) == "" {
		return nil, errors.New("authoring session requires id and user")
	}
	now := time.Now().UTC()
	session.State = authoring.StateDraftConversation
	session.CurrentRevision = 0
	session.VisibleRevision = 0
	session.CreatedAt = now
	session.UpdatedAt = now
	if session.RuntimeSessionID == "" {
		session.RuntimeSessionID = agent.NewID("authoring-session")
	}
	planJSON, err := marshalJSON(plan)
	if err != nil {
		return nil, fmt.Errorf("encode initial authoring plan: %w", err)
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin authoring session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions
		(id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at)
		VALUES (?, 'authoring', 'authoring-session', ?, ?, ?, ?, ?)`,
		session.RuntimeSessionID, session.ID, session.UserID, agent.SessionActive, now, now); err != nil {
		return nil, fmt.Errorf("create authoring agent session: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO authoring_sessions
		(id, user_id, runtime_session_id, generator_session_id, state, current_revision, visible_revision, publish_challenge_id, last_error, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, 0, 0, '', '', ?, ?)`,
		session.ID, session.UserID, session.RuntimeSessionID, session.State, nowText(now), nowText(now)); err != nil {
		return nil, fmt.Errorf("create authoring session: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO authoring_revisions (session_id, revision, plan_json, candidate_revision_id, created_at)
		VALUES (?, 0, ?::jsonb, '', ?)`, session.ID, planJSON, nowText(now)); err != nil {
		return nil, fmt.Errorf("create initial authoring revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit authoring session: %w", err)
	}
	return &session, nil
}

func (d *DB) GetAuthoringSession(ctx context.Context, id, userID string) (*authoring.Session, error) {
	return d.readAuthoringSession(ctx, `SELECT id, user_id, runtime_session_id, generator_session_id, state, current_revision, visible_revision,
		publish_challenge_id, last_error, created_at, updated_at FROM authoring_sessions WHERE id = ? AND user_id = ?`, id, userID)
}

func (d *DB) GetLatestOpenAuthoringSession(ctx context.Context, userID string) (*authoring.Session, error) {
	return d.readAuthoringSession(ctx, `SELECT id, user_id, runtime_session_id, generator_session_id, state, current_revision, visible_revision,
		publish_challenge_id, last_error, created_at, updated_at FROM authoring_sessions
		WHERE user_id = ? AND state <> ? ORDER BY updated_at DESC, id DESC LIMIT 1`, userID, authoring.StatePublished)
}

func (d *DB) GetAuthoringSessionInternal(ctx context.Context, id string) (*authoring.Session, error) {
	return d.readAuthoringSession(ctx, `SELECT id, user_id, runtime_session_id, generator_session_id, state, current_revision, visible_revision,
		publish_challenge_id, last_error, created_at, updated_at FROM authoring_sessions WHERE id = ?`, id)
}

func (d *DB) readAuthoringSession(ctx context.Context, query string, args ...any) (*authoring.Session, error) {
	var session authoring.Session
	var state, createdAt, updatedAt string
	err := d.conn.QueryRowContext(ctx, query, args...).Scan(&session.ID, &session.UserID, &session.RuntimeSessionID, &session.GeneratorSessionID,
		&state, &session.CurrentRevision, &session.VisibleRevision, &session.PublishChallengeID, &session.LastError, &createdAt, &updatedAt)
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
	return readAuthoringRevision(d.conn.QueryRowContext(ctx, `SELECT revision, plan_json, candidate_revision_id, created_at
		FROM authoring_revisions WHERE session_id = ? AND revision = ?`, sessionID, revision))
}

func readAuthoringRevision(row agentRow) (*authoring.Revision, error) {
	var revision authoring.Revision
	var planJSON, createdAt string
	err := row.Scan(&revision.Number, &planJSON, &revision.CandidateRevisionID, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read authoring revision: %w", err)
	}
	if err := json.Unmarshal([]byte(planJSON), &revision.Plan); err != nil {
		return nil, fmt.Errorf("decode authoring plan: %w", err)
	}
	revision.CreatedAt = parseAuthoringTime(createdAt)
	return &revision, nil
}

func readAuthoringRevisionTx(ctx context.Context, tx *Tx, sessionID string, number int64) (*authoring.Revision, error) {
	return readAuthoringRevision(tx.QueryRowContext(ctx, `SELECT revision, plan_json, candidate_revision_id, created_at
		FROM authoring_revisions WHERE session_id = ? AND revision = ?`, sessionID, number))
}

// StartAuthoringRun persists the user message and starts a direct Server-owned
// model call. No worker queue participates in an authoring conversation.
func (d *DB) StartAuthoringRun(ctx context.Context, sessionID, userID string, message agent.Message, run agent.CreateRun) (*authoring.Stage, *agent.Run, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || message.Role != "user" || strings.TrimSpace(message.Content) == "" {
		return nil, nil, errors.New("authoring run requires a user message")
	}
	if err := agent.ValidateCreateRun(run); err != nil {
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
	if !authoring.AllowsAgentPlanStage(session.State) || session.RuntimeSessionID == "" || run.SessionID != session.RuntimeSessionID ||
		run.Purpose != "authoring" || run.OwnerKind != "authoring-session" || run.OwnerRef != session.ID {
		return nil, nil, authoring.ErrInvalidState
	}
	if err := lockActiveSessionTx(ctx, tx, run.SessionID); err != nil {
		return nil, nil, err
	}
	base, err := readAuthoringRevisionTx(ctx, tx, session.ID, session.CurrentRevision)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	if message.ID == "" {
		message.ID = agent.NewID("authoring-message")
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
	planJSON, err := marshalJSON(base.Plan)
	if err != nil {
		return nil, nil, err
	}
	stage := &authoring.Stage{RunID: created.ID, SessionID: session.ID, BaseRevision: base.Number, StageRevision: base.Number, Plan: base.Plan, Changes: []authoring.Change{}, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO authoring_stages
		(run_id, session_id, base_revision, stage_revision, plan_json, changes_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?::jsonb, '[]'::jsonb, ?, ?)`, stage.RunID, stage.SessionID, stage.BaseRevision, stage.StageRevision, planJSON, now, now); err != nil {
		return nil, nil, fmt.Errorf("create authoring stage: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET updated_at = ? WHERE id = ?`, nowText(now), session.ID); err != nil {
		return nil, nil, err
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

func (d *DB) LoadAuthoringExecution(ctx context.Context, runID string) (*authoring.Stage, []agent.Message, error) {
	stage, err := d.GetAuthoringStage(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	var sessionID string
	err = d.conn.QueryRowContext(ctx, `SELECT runtime_session_id FROM authoring_sessions WHERE id = ?`, stage.SessionID).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, authoring.ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("load authoring session for execution: %w", err)
	}
	messages, err := d.ListMessages(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	return stage, messages, nil
}

func (d *DB) UpdateAuthoringStage(ctx context.Context, runID string, expectedStageRevision int64, plan authoring.Plan, change authoring.Change) (*authoring.Stage, error) {
	if strings.TrimSpace(runID) == "" || expectedStageRevision < 0 || strings.TrimSpace(change.Kind) == "" || strings.TrimSpace(change.Summary) == "" || strings.TrimSpace(change.DifficultyImpact) == "" {
		return nil, errors.New("authoring stage update is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin authoring stage update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stage, err := readAuthoringStage(tx.QueryRowContext(ctx, `SELECT run_id, session_id, base_revision, stage_revision, plan_json, changes_json, created_at, updated_at
		FROM authoring_stages WHERE run_id = ? FOR UPDATE`, runID))
	if err != nil {
		return nil, err
	}
	if stage.StageRevision != expectedStageRevision {
		return nil, authoring.ErrVersionConflict
	}
	if err := requireRunningAuthoringRunTx(ctx, tx, stage.RunID, stage.SessionID); err != nil {
		return nil, err
	}
	planJSON, err := marshalJSON(plan)
	if err != nil {
		return nil, err
	}
	change.Revision = stage.BaseRevision + 1
	stage.Changes = append(stage.Changes, change)
	changesJSON, err := marshalJSON(stage.Changes)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	stage.StageRevision++
	stage.Plan = plan
	stage.UpdatedAt = now
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_stages SET stage_revision = ?, plan_json = ?::jsonb, changes_json = ?::jsonb, updated_at = ? WHERE run_id = ?`,
		stage.StageRevision, planJSON, changesJSON, now, stage.RunID); err != nil {
		return nil, fmt.Errorf("update authoring stage: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET updated_at = ? WHERE id = ?`, nowText(now), stage.SessionID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return stage, nil
}

func (d *DB) FinalizeAuthoringRun(ctx context.Context, runID, content string, now time.Time) (*authoring.Revision, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(content) == "" || now.IsZero() {
		return nil, errors.New("authoring finalization requires run and content")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin authoring finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stage, err := readAuthoringStage(tx.QueryRowContext(ctx, `SELECT run_id, session_id, base_revision, stage_revision, plan_json, changes_json, created_at, updated_at
		FROM authoring_stages WHERE run_id = ? FOR UPDATE`, runID))
	if err != nil {
		return nil, err
	}
	if err := requireRunningAuthoringRunTx(ctx, tx, stage.RunID, stage.SessionID); err != nil {
		return nil, err
	}
	session, err := readAuthoringSessionTx(ctx, tx, stage.SessionID, "")
	if err != nil {
		return nil, err
	}
	if session.CurrentRevision != stage.BaseRevision || !authoring.AllowsAgentPlanStage(session.State) {
		return nil, authoring.ErrInvalidState
	}
	revision := &authoring.Revision{Number: session.CurrentRevision, Plan: stage.Plan, CreatedAt: now.UTC()}
	if len(stage.Changes) > 0 {
		if err := stage.Plan.ValidateForGeneration(); err != nil {
			return nil, err
		}
		planJSON, err := marshalJSON(stage.Plan)
		if err != nil {
			return nil, err
		}
		revision.Number = stage.BaseRevision + 1
		if _, err := tx.ExecContext(ctx, `INSERT INTO authoring_revisions (session_id, revision, plan_json, candidate_revision_id, created_at)
			VALUES (?, ?, ?::jsonb, '', ?)`, stage.SessionID, revision.Number, planJSON, nowText(now)); err != nil {
			return nil, fmt.Errorf("create finalized authoring revision: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET current_revision = ?, state = ?, last_error = '', updated_at = ? WHERE id = ?`,
			revision.Number, authoring.StateIntentReview, nowText(now), stage.SessionID); err != nil {
			return nil, fmt.Errorf("advance authoring revision: %w", err)
		}
	}
	metadata, err := marshalJSON(struct {
		Changes []authoring.Change `json:"changes"`
	}{Changes: stage.Changes})
	if err != nil {
		return nil, err
	}
	var runtimeSessionID string
	if err := tx.QueryRowContext(ctx, `SELECT runtime_session_id FROM authoring_sessions WHERE id = ?`, stage.SessionID).Scan(&runtimeSessionID); err != nil {
		return nil, err
	}
	message := agent.Message{ID: agent.NewID("authoring-message"), SessionID: runtimeSessionID, Role: "assistant", Content: strings.TrimSpace(content), Metadata: []byte(metadata), CreatedAt: now.UTC()}
	if err := insertAgentMessageTx(ctx, tx, &message); err != nil {
		return nil, err
	}
	if err := completeRunTx(ctx, tx, stage.RunID, now.UTC()); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM authoring_stages WHERE run_id = ?`, stage.RunID); err != nil {
		return nil, fmt.Errorf("delete completed authoring stage: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return revision, nil
}

func (d *DB) ReplaceAuthoringPlan(ctx context.Context, sessionID, userID string, expected int64, plan authoring.Plan, _ authoring.SessionState) (*authoring.Revision, error) {
	if err := plan.ValidateForGeneration(); err != nil {
		return nil, err
	}
	planJSON, err := marshalJSON(plan)
	if err != nil {
		return nil, err
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO authoring_revisions (session_id, revision, plan_json, candidate_revision_id, created_at)
		VALUES (?, ?, ?::jsonb, '', ?)`, sessionID, next, planJSON, nowText(now)); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET current_revision = ?, state = ?, last_error = '', updated_at = ? WHERE id = ? AND user_id = ?`,
		next, authoring.StateIntentReview, nowText(now), sessionID, userID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &authoring.Revision{Number: next, Plan: plan, CreatedAt: now}, nil
}

func readAuthoringSessionTx(ctx context.Context, tx *Tx, id, userID string) (*authoring.Session, error) {
	query := `SELECT id, user_id, runtime_session_id, generator_session_id, state, current_revision, visible_revision,
		publish_challenge_id, last_error, created_at, updated_at FROM authoring_sessions WHERE id = ?`
	args := []any{id}
	if userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	var session authoring.Session
	var state, createdAt, updatedAt string
	err := tx.QueryRowContext(ctx, query, args...).Scan(&session.ID, &session.UserID, &session.RuntimeSessionID, &session.GeneratorSessionID,
		&state, &session.CurrentRevision, &session.VisibleRevision, &session.PublishChallengeID, &session.LastError, &createdAt, &updatedAt)
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

func readAuthoringStage(row agentRow) (*authoring.Stage, error) {
	var stage authoring.Stage
	var planJSON, changesJSON []byte
	err := row.Scan(&stage.RunID, &stage.SessionID, &stage.BaseRevision, &stage.StageRevision, &planJSON, &changesJSON, &stage.CreatedAt, &stage.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(planJSON, &stage.Plan); err != nil {
		return nil, fmt.Errorf("decode authoring stage plan: %w", err)
	}
	if err := json.Unmarshal(changesJSON, &stage.Changes); err != nil {
		return nil, fmt.Errorf("decode authoring stage changes: %w", err)
	}
	return &stage, nil
}

func requireRunningAuthoringRunTx(ctx context.Context, tx *Tx, runID, authoringSessionID string) error {
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs
		WHERE id = ? AND purpose = 'authoring' AND owner_kind = 'authoring-session' AND owner_ref = ? AND status = ?)`,
		runID, authoringSessionID, agent.RunRunning).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return agent.ErrRunActive
	}
	return nil
}

func nowText(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseAuthoringTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
