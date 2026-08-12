package postgres

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
	challengedomain "github.com/breakfix/breakfix/internal/domain/challenge"
)

func (d *AuthoringRepository) CreateAuthoringSession(ctx context.Context, session authoring.Session, plan authoring.Plan) (*authoring.Session, error) {
	if strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.UserID) == "" {
		return nil, errors.New("authoring session requires id and user")
	}
	now := time.Now().UTC()
	session.RevisionChallengeID = ""
	session.RevisionBaseActiveRevisionID = ""
	prepareNewAuthoringSession(&session, now)
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin authoring session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertNewAuthoringSessionTx(ctx, tx, session, plan); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit authoring session: %w", err)
	}
	return &session, nil
}

// CreateChallengeRevisionSession starts a fresh authoring conversation for an
// author-owned active Challenge. The copied plan is only a discussion seed;
// every resulting candidate still runs the complete generation and verification
// lifecycle before it can move the active revision pointer.
func (d *AuthoringRepository) CreateChallengeRevisionSession(ctx context.Context, userID, challengeID string) (*authoring.Session, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(challengeID) == "" {
		return nil, errors.New("challenge revision session requires user and challenge")
	}
	now := time.Now().UTC()
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin challenge revision session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	target, err := lockPersistedChallengeTx(ctx, tx, challengeID)
	if err != nil {
		return nil, err
	}
	if target.SourceKind != challengedomain.SourceAuthoring || target.OwnerUserID != userID || target.State != challengedomain.StateActive {
		return nil, authoring.ErrInvalidState
	}
	active, err := lockPersistedChallengeRevisionTx(ctx, tx, target.ActiveRevisionID)
	if err != nil {
		return nil, err
	}
	if active.ChallengeID != target.ID || active.State != challengedomain.RevisionActive {
		return nil, authoring.ErrInvalidState
	}
	var sourceNumber int64
	if err := tx.QueryRowContext(ctx, `SELECT current_revision FROM authoring_sessions WHERE id = ? FOR UPDATE`, target.SourceRef).Scan(&sourceNumber); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, authoring.ErrNotFound
		}
		return nil, fmt.Errorf("read original authoring session: %w", err)
	}
	sourcePlan, err := readAuthoringRevisionTx(ctx, tx, target.SourceRef, sourceNumber)
	if err != nil {
		return nil, err
	}
	session := authoring.Session{
		ID: authoring.NewID("author"), UserID: userID,
		RevisionChallengeID: target.ID, RevisionBaseActiveRevisionID: target.ActiveRevisionID,
	}
	prepareNewAuthoringSession(&session, now)
	if err := insertNewAuthoringSessionTx(ctx, tx, session, sourcePlan.Plan.Clone()); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit challenge revision session: %w", err)
	}
	return &session, nil
}

func prepareNewAuthoringSession(session *authoring.Session, now time.Time) {
	session.State = authoring.StateDraftConversation
	session.CurrentRevision = 0
	session.VisibleRevision = 0
	session.PublishChallengeID = ""
	session.LastError = ""
	session.CreatedAt = now.UTC()
	session.UpdatedAt = now.UTC()
	if session.RuntimeSessionID == "" {
		session.RuntimeSessionID = agent.NewID("authoring-session")
	}
}

func insertNewAuthoringSessionTx(ctx context.Context, tx *Tx, session authoring.Session, plan authoring.Plan) error {
	planJSON, err := marshalJSON(plan)
	if err != nil {
		return fmt.Errorf("encode initial authoring plan: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions
		(id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at)
		VALUES (?, 'authoring', 'authoring-session', ?, ?, ?, ?, ?)`,
		session.RuntimeSessionID, session.ID, session.UserID, agent.SessionActive, session.CreatedAt.UTC(), session.UpdatedAt.UTC()); err != nil {
		return fmt.Errorf("create authoring agent session: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO authoring_sessions
		(id, user_id, runtime_session_id, state, current_revision, visible_revision, publish_challenge_id,
		revision_challenge_id, revision_base_active_revision_id, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, 0, '', ?, ?, '', ?, ?)`,
		session.ID, session.UserID, session.RuntimeSessionID, session.State, session.RevisionChallengeID,
		session.RevisionBaseActiveRevisionID, nowText(session.CreatedAt), nowText(session.UpdatedAt)); err != nil {
		return fmt.Errorf("create authoring session: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO authoring_revisions (session_id, revision, plan_json, candidate_revision_id, created_at)
		VALUES (?, 0, ?::jsonb, '', ?)`, session.ID, planJSON, nowText(session.CreatedAt)); err != nil {
		return fmt.Errorf("create initial authoring revision: %w", err)
	}
	return nil
}

func (d *AuthoringRepository) GetAuthoringSession(ctx context.Context, id, userID string) (*authoring.Session, error) {
	return d.readAuthoringSession(ctx, `SELECT id, user_id, runtime_session_id, state, current_revision, visible_revision,
		publish_challenge_id, revision_challenge_id, revision_base_active_revision_id, last_error, created_at, updated_at FROM authoring_sessions WHERE id = ? AND user_id = ?`, id, userID)
}

func (d *AuthoringRepository) GetLatestOpenAuthoringSession(ctx context.Context, userID string) (*authoring.Session, error) {
	return d.readAuthoringSession(ctx, `SELECT id, user_id, runtime_session_id, state, current_revision, visible_revision,
		publish_challenge_id, revision_challenge_id, revision_base_active_revision_id, last_error, created_at, updated_at FROM authoring_sessions
		WHERE user_id = ? AND state <> ? ORDER BY updated_at DESC, id DESC LIMIT 1`, userID, authoring.StatePublished)
}

func (d *AuthoringRepository) GetAuthoringSessionInternal(ctx context.Context, id string) (*authoring.Session, error) {
	return d.readAuthoringSession(ctx, `SELECT id, user_id, runtime_session_id, state, current_revision, visible_revision,
		publish_challenge_id, revision_challenge_id, revision_base_active_revision_id, last_error, created_at, updated_at FROM authoring_sessions WHERE id = ?`, id)
}

func (d *AuthoringRepository) readAuthoringSession(ctx context.Context, query string, args ...any) (*authoring.Session, error) {
	var session authoring.Session
	var state, createdAt, updatedAt string
	err := d.conn.QueryRowContext(ctx, query, args...).Scan(&session.ID, &session.UserID, &session.RuntimeSessionID,
		&state, &session.CurrentRevision, &session.VisibleRevision, &session.PublishChallengeID, &session.RevisionChallengeID,
		&session.RevisionBaseActiveRevisionID, &session.LastError, &createdAt, &updatedAt)
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

func (d *AuthoringRepository) GetAuthoringRevision(ctx context.Context, sessionID string, revision int64) (*authoring.Revision, error) {
	return readAuthoringRevision(d.conn.QueryRowContext(ctx, `SELECT revision, plan_json, candidate_revision_id, created_at
		FROM authoring_revisions WHERE session_id = ? AND revision = ?`, sessionID, revision))
}

// ListMessages is part of the authoring runtime port: authoring conversations
// are stored in the shared agent tables but are read only through the owning
// authoring aggregate.
func (d *AuthoringRepository) ListMessages(ctx context.Context, sessionID string) ([]agent.Message, error) {
	return listAgentMessages(ctx, d.conn, sessionID)
}

// GetRun is exposed through the Authoring aggregate so an interactive turn
// never reaches into a generic Agent repository for its execution fence.
func (d *AuthoringRepository) GetRun(ctx context.Context, id string) (*agent.Run, error) {
	run, err := scanAgentRun(d.conn.QueryRowContext(ctx, agentRunSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agent.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get authoring run: %w", err)
	}
	return run, nil
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
func (d *AuthoringRepository) StartAuthoringRun(ctx context.Context, sessionID, userID string, message agent.Message, run agent.CreateRun) (*authoring.Stage, *agent.Run, error) {
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
	stage, err := createAuthoringStageTx(ctx, tx, session.ID, base, *created, now)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET updated_at = ? WHERE id = ?`, nowText(now), session.ID); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit authoring run: %w", err)
	}
	return stage, created, nil
}

func (d *AuthoringRepository) GetAuthoringStage(ctx context.Context, runID string) (*authoring.Stage, error) {
	return readAuthoringStage(d.conn.QueryRowContext(ctx, `SELECT run_id, session_id, base_revision, stage_revision, run_attempt, plan_json, changes_json, created_at, updated_at
		FROM authoring_stages WHERE run_id = ?`, runID))
}

func (d *AuthoringRepository) LoadAuthoringExecution(ctx context.Context, runID string) (*authoring.Stage, []agent.Message, error) {
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
	messages, err := listAgentMessages(ctx, d.conn, sessionID)
	if err != nil {
		return nil, nil, err
	}
	return stage, messages, nil
}

func (d *AuthoringRepository) UpdateAuthoringStage(ctx context.Context, runID string, expectedAttempt int, expectedStageRevision int64, plan authoring.Plan, change authoring.Change) (*authoring.Stage, error) {
	if strings.TrimSpace(runID) == "" || expectedAttempt < 1 || expectedStageRevision < 0 || strings.TrimSpace(change.Kind) == "" || strings.TrimSpace(change.Summary) == "" || strings.TrimSpace(change.DifficultyImpact) == "" {
		return nil, errors.New("authoring stage update is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin authoring stage update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stage, err := readAuthoringStage(tx.QueryRowContext(ctx, `SELECT run_id, session_id, base_revision, stage_revision, run_attempt, plan_json, changes_json, created_at, updated_at
		FROM authoring_stages WHERE run_id = ? FOR UPDATE`, runID))
	if err != nil {
		return nil, err
	}
	if stage.RunAttempt != expectedAttempt {
		return nil, agent.ErrRunActive
	}
	if stage.StageRevision != expectedStageRevision {
		return nil, authoring.ErrVersionConflict
	}
	if err := requireRunningAuthoringRunTx(ctx, tx, stage.RunID, stage.SessionID, expectedAttempt); err != nil {
		return nil, err
	}
	if _, err := lockAuthoringPlanSessionTx(ctx, tx, stage.SessionID, ""); err != nil {
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

func (d *AuthoringRepository) FinalizeAuthoringRun(ctx context.Context, runID string, expectedAttempt int, content string, now time.Time) (*authoring.Revision, error) {
	if strings.TrimSpace(runID) == "" || expectedAttempt < 1 || strings.TrimSpace(content) == "" || now.IsZero() {
		return nil, errors.New("authoring finalization requires run and content")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin authoring finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stage, err := readAuthoringStage(tx.QueryRowContext(ctx, `SELECT run_id, session_id, base_revision, stage_revision, run_attempt, plan_json, changes_json, created_at, updated_at
		FROM authoring_stages WHERE run_id = ? FOR UPDATE`, runID))
	if err != nil {
		return nil, err
	}
	if stage.RunAttempt != expectedAttempt {
		return nil, agent.ErrRunActive
	}
	if err := requireRunningAuthoringRunTx(ctx, tx, stage.RunID, stage.SessionID, expectedAttempt); err != nil {
		return nil, err
	}
	session, err := lockAuthoringPlanSessionTx(ctx, tx, stage.SessionID, "")
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

// RetryAuthoringRun advances a known technical attempt while carrying the
// same Run identity and private Plan stage forward. The stage's RunAttempt is
// updated in the same transaction so a late tool call from the old Eino
// instance cannot change the newer attempt.
func (d *AuthoringRepository) RetryAuthoringRun(ctx context.Context, runID string, expectedAttempt int, message string, now time.Time) (*agent.Run, error) {
	if strings.TrimSpace(runID) == "" || expectedAttempt < 1 || strings.TrimSpace(message) == "" || now.IsZero() {
		return nil, errors.New("authoring retry is incomplete")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin authoring retry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stage, err := readAuthoringStage(tx.QueryRowContext(ctx, `SELECT run_id, session_id, base_revision, stage_revision, run_attempt, plan_json, changes_json, created_at, updated_at
		FROM authoring_stages WHERE run_id = ? FOR UPDATE`, runID))
	if err != nil {
		return nil, err
	}
	run, err := scanAgentRun(tx.QueryRowContext(ctx, agentRunSelect+` WHERE id = ? FOR UPDATE`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agent.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if stage.RunAttempt != expectedAttempt || run.Attempt != expectedAttempt || run.Status != agent.RunRunning ||
		run.Purpose != "authoring" || run.OwnerKind != "authoring-session" || run.OwnerRef != stage.SessionID {
		return nil, agent.ErrRunActive
	}
	if run.Attempt < agent.MaxAttempts && run.DeadlineAt.After(now.UTC()) {
		next, err := scanAgentRun(tx.QueryRowContext(ctx, `UPDATE agent_runs SET attempt = attempt + 1, last_error = ?, updated_at = ?
			WHERE id = ? AND status = ? AND attempt = ? RETURNING `+agentRunColumns,
			strings.TrimSpace(message), now.UTC(), runID, agent.RunRunning, expectedAttempt))
		if err != nil {
			return nil, fmt.Errorf("advance authoring attempt: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE authoring_stages SET run_attempt = ?, updated_at = ? WHERE run_id = ?`, next.Attempt, now.UTC(), runID); err != nil {
			return nil, fmt.Errorf("fence authoring stage attempt: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET last_error = ?, updated_at = ? WHERE id = ?`, strings.TrimSpace(message), nowText(now), stage.SessionID); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return next, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
		WHERE id = ? AND status = ? AND attempt = ?`, agent.RunFailed, strings.TrimSpace(message), now.UTC(), now.UTC(), runID, agent.RunRunning, expectedAttempt); err != nil {
		return nil, fmt.Errorf("fail exhausted authoring run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM authoring_stages WHERE run_id = ?`, runID); err != nil {
		return nil, fmt.Errorf("delete failed authoring stage: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET last_error = ?, updated_at = ? WHERE id = ?`, strings.TrimSpace(message), nowText(now), stage.SessionID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return nil, nil
}

// RestartInterruptedAuthoringRun abandons an incomplete private stage and
// creates a replacement Run from the committed Plan and user message. The
// old stage is never resumed or published after a Server interruption.
func (d *AuthoringRepository) RestartInterruptedAuthoringRun(ctx context.Context, runID, reason string, now time.Time) (*agent.Run, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(reason) == "" || now.IsZero() {
		return nil, errors.New("restart authoring run is incomplete")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin authoring restart: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	prior, err := scanAgentRun(tx.QueryRowContext(ctx, agentRunSelect+` WHERE id = ? FOR UPDATE`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agent.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if prior.Status != agent.RunRunning || prior.Purpose != "authoring" || prior.OwnerKind != "authoring-session" || strings.TrimSpace(prior.SessionID) == "" {
		return nil, agent.ErrRunActive
	}
	session, err := lockAuthoringPlanSessionTx(ctx, tx, prior.OwnerRef, "")
	if err != nil {
		return nil, err
	}
	if session.RuntimeSessionID != prior.SessionID {
		return nil, authoring.ErrInvalidState
	}
	if err := lockAgentSessionTx(ctx, tx, prior.SessionID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
		WHERE id = ? AND status = ?`, agent.RunInterrupted, strings.TrimSpace(reason), now.UTC(), now.UTC(), prior.ID, agent.RunRunning); err != nil {
		return nil, fmt.Errorf("interrupt prior authoring run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM authoring_stages WHERE run_id = ?`, prior.ID); err != nil {
		return nil, fmt.Errorf("discard interrupted authoring stage: %w", err)
	}
	if !authoring.AllowsAgentPlanStage(session.State) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	base, err := readAuthoringRevisionTx(ctx, tx, session.ID, session.CurrentRevision)
	if err != nil {
		return nil, err
	}
	input, err := json.Marshal(struct {
		BaseRevision int64 `json:"base_revision"`
	}{BaseRevision: base.Number})
	if err != nil {
		return nil, fmt.Errorf("encode restarted authoring input: %w", err)
	}
	replacement, err := createRunTx(ctx, tx, agent.CreateRun{
		ID:            agent.NewID("authoring-run"),
		SessionID:     prior.SessionID,
		Purpose:       prior.Purpose,
		OwnerKind:     prior.OwnerKind,
		OwnerRef:      prior.OwnerRef,
		InputRevision: fmt.Sprintf("%d", base.Number),
		Input:         input,
		Model:         prior.Model,
		PromptVersion: prior.PromptVersion,
	}, now.UTC())
	if err != nil {
		return nil, err
	}
	if _, err := createAuthoringStageTx(ctx, tx, session.ID, base, *replacement, now.UTC()); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET last_error = '', updated_at = ? WHERE id = ?`, nowText(now), session.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return replacement, nil
}

func (d *AuthoringRepository) ReplaceAuthoringPlan(ctx context.Context, sessionID, userID string, expected int64, plan authoring.Plan, _ authoring.SessionState) (*authoring.Revision, error) {
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
	session, err := lockAuthoringPlanSessionTx(ctx, tx, sessionID, userID)
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
	query := `SELECT id, user_id, runtime_session_id, state, current_revision, visible_revision,
		publish_challenge_id, revision_challenge_id, revision_base_active_revision_id, last_error, created_at, updated_at FROM authoring_sessions WHERE id = ?`
	args := []any{id}
	if userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	var session authoring.Session
	var state, createdAt, updatedAt string
	err := tx.QueryRowContext(ctx, query, args...).Scan(&session.ID, &session.UserID, &session.RuntimeSessionID,
		&state, &session.CurrentRevision, &session.VisibleRevision, &session.PublishChallengeID, &session.RevisionChallengeID,
		&session.RevisionBaseActiveRevisionID, &session.LastError, &createdAt, &updatedAt)
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

// lockAuthoringPlanSessionTx serializes authoring-stage writes with explicit
// generation confirmation and cancellation for the same authoring session.
func lockAuthoringPlanSessionTx(ctx context.Context, tx *Tx, id, userID string) (*authoring.Session, error) {
	query := `SELECT id FROM authoring_sessions WHERE id = ?`
	args := []any{id}
	if userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	query += ` FOR UPDATE`
	var locked string
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&locked); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, authoring.ErrNotFound
		}
		return nil, fmt.Errorf("lock authoring plan session: %w", err)
	}
	return readAuthoringSessionTx(ctx, tx, id, userID)
}

func createAuthoringStageTx(ctx context.Context, tx *Tx, sessionID string, base *authoring.Revision, run agent.Run, now time.Time) (*authoring.Stage, error) {
	if base == nil || run.Attempt < 1 {
		return nil, errors.New("authoring stage requires a base revision and active run")
	}
	planJSON, err := marshalJSON(base.Plan)
	if err != nil {
		return nil, err
	}
	stage := &authoring.Stage{
		RunID: run.ID, SessionID: sessionID, BaseRevision: base.Number, StageRevision: base.Number,
		RunAttempt: run.Attempt, Plan: base.Plan, Changes: []authoring.Change{}, CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO authoring_stages
		(run_id, session_id, base_revision, stage_revision, run_attempt, plan_json, changes_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?::jsonb, '[]'::jsonb, ?, ?)`,
		stage.RunID, stage.SessionID, stage.BaseRevision, stage.StageRevision, stage.RunAttempt, planJSON, now.UTC(), now.UTC()); err != nil {
		return nil, fmt.Errorf("create authoring stage: %w", err)
	}
	return stage, nil
}

func readAuthoringStage(row agentRow) (*authoring.Stage, error) {
	var stage authoring.Stage
	var planJSON, changesJSON []byte
	err := row.Scan(&stage.RunID, &stage.SessionID, &stage.BaseRevision, &stage.StageRevision, &stage.RunAttempt, &planJSON, &changesJSON, &stage.CreatedAt, &stage.UpdatedAt)
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

func requireRunningAuthoringRunTx(ctx context.Context, tx *Tx, runID, authoringSessionID string, attempt int) error {
	var exists bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs
		WHERE id = ? AND purpose = 'authoring' AND owner_kind = 'authoring-session' AND owner_ref = ? AND status = ? AND attempt = ?)`,
		runID, authoringSessionID, agent.RunRunning, attempt).Scan(&exists)
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
