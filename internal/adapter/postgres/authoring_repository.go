package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	challengedomain "github.com/breakfix/breakfix/internal/domain/challenge"
	"github.com/breakfix/breakfix/internal/domain/generation"
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
// model call. A receipt makes a retried HTTP request return its original Run
// instead of appending another message or starting another Eino execution.
func (d *AuthoringRepository) StartAuthoringRun(ctx context.Context, sessionID, userID, idempotencyKey string, message agent.Message, run agent.CreateRun) (*authoring.Stage, *agent.Run, bool, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || idempotencyKey == "" || len(idempotencyKey) > 200 || message.Role != "user" || strings.TrimSpace(message.Content) == "" {
		return nil, nil, false, errors.New("authoring run requires a user message and idempotency key")
	}
	if err := agent.ValidateCreateRun(run); err != nil {
		return nil, nil, false, err
	}
	requestDigest, err := authoringMessageRequestDigest(message.Content)
	if err != nil {
		return nil, nil, false, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, false, fmt.Errorf("begin authoring run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT id FROM authoring_sessions WHERE id = ? AND user_id = ? FOR UPDATE`, sessionID, userID); err != nil {
		return nil, nil, false, fmt.Errorf("lock authoring session: %w", err)
	}
	session, err := readAuthoringSessionTx(ctx, tx, sessionID, userID)
	if err != nil {
		return nil, nil, false, err
	}
	if receipt, err := authoringMessageReceiptTx(ctx, tx, session.ID, idempotencyKey); err != nil {
		return nil, nil, false, err
	} else if receipt != nil {
		if receipt.RequestDigest != requestDigest {
			return nil, nil, false, authoring.ErrVersionConflict
		}
		persisted, err := scanAgentRun(tx.QueryRowContext(ctx, agentRunSelect+` WHERE id = ?`, receipt.RunID))
		if err != nil {
			return nil, nil, false, fmt.Errorf("read idempotent authoring run: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, nil, false, fmt.Errorf("commit idempotent authoring run: %w", err)
		}
		return nil, persisted, false, nil
	}
	if !authoring.AllowsAgentPlanStage(session.State) || session.RuntimeSessionID == "" || run.SessionID != session.RuntimeSessionID ||
		run.Purpose != "authoring" || run.OwnerKind != "authoring-session" || run.OwnerRef != session.ID {
		return nil, nil, false, authoring.ErrInvalidState
	}
	if err := lockActiveSessionTx(ctx, tx, run.SessionID); err != nil {
		return nil, nil, false, err
	}
	base, err := readAuthoringRevisionTx(ctx, tx, session.ID, session.CurrentRevision)
	if err != nil {
		return nil, nil, false, err
	}
	now := time.Now().UTC()
	if message.ID == "" {
		message.ID = agent.NewID("authoring-message")
	}
	message.SessionID = run.SessionID
	message.CreatedAt = now
	if err := insertAgentMessageTx(ctx, tx, &message); err != nil {
		return nil, nil, false, err
	}
	created, err := createRunTx(ctx, tx, run, now)
	if err != nil {
		return nil, nil, false, err
	}
	stage, err := createAuthoringStageTx(ctx, tx, session.ID, base, *created, now)
	if err != nil {
		return nil, nil, false, err
	}
	if err := insertAuthoringMessageReceiptTx(ctx, tx, authoringMessageReceipt{
		SessionID: session.ID, IdempotencyKey: idempotencyKey, RequestDigest: requestDigest,
		MessageID: message.ID, RunID: created.ID, CreatedAt: now,
	}); err != nil {
		return nil, nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET updated_at = ? WHERE id = ?`, nowText(now), session.ID); err != nil {
		return nil, nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, false, fmt.Errorf("commit authoring run: %w", err)
	}
	return stage, created, true, nil
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

func (d *AuthoringRepository) UpdateAuthoringStage(ctx context.Context, runID string, expectedAttempt int, expectedStageRevision int64, operation authoring.StageOperation, plan authoring.Plan, change authoring.Change) (*authoring.Stage, error) {
	if strings.TrimSpace(runID) == "" || expectedAttempt < 1 || expectedStageRevision < 0 || strings.TrimSpace(operation.ID) == "" || strings.TrimSpace(operation.RequestDigest) == "" || strings.TrimSpace(change.Kind) == "" || strings.TrimSpace(change.Summary) == "" {
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
	if err := requireRunningAuthoringRunTx(ctx, tx, stage.RunID, stage.SessionID, expectedAttempt); err != nil {
		return nil, err
	}
	if replay, digest, err := readAuthoringStageOperationTx(ctx, tx, *stage, operation.ID); err != nil {
		return nil, err
	} else if replay != nil {
		if digest != operation.RequestDigest {
			return nil, authoring.ErrVersionConflict
		}
		return replay, nil
	}
	if stage.StageRevision != expectedStageRevision {
		return nil, authoring.ErrVersionConflict
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO authoring_stage_operations
		(run_id, operation_id, request_digest, stage_revision, plan_json, changes_json, updated_at)
		VALUES (?, ?, ?, ?, ?::jsonb, ?::jsonb, ?)`,
		stage.RunID, operation.ID, operation.RequestDigest, stage.StageRevision, planJSON, changesJSON, now); err != nil {
		return nil, fmt.Errorf("record authoring stage operation: %w", err)
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
	content = strings.TrimSpace(content)
	if strings.TrimSpace(runID) == "" || expectedAttempt < 1 || strings.TrimSpace(content) == "" || now.IsZero() {
		return nil, errors.New("authoring finalization requires run and content")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin authoring finalization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	run, err := scanAgentRun(tx.QueryRowContext(ctx, agentRunSelect+` WHERE id = ? FOR UPDATE`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agent.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if run.Purpose != "authoring" || run.OwnerKind != "authoring-session" || run.Attempt != expectedAttempt || strings.TrimSpace(run.SessionID) == "" || strings.TrimSpace(run.OwnerRef) == "" {
		return nil, agent.ErrRunActive
	}
	if run.Status == agent.RunSucceeded {
		revision, err := replayCompletedAuthoringRunTx(ctx, tx, *run, content)
		if err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit replayed authoring finalization: %w", err)
		}
		return revision, nil
	}
	if run.Status != agent.RunRunning {
		return nil, agent.ErrRunActive
	}
	stage, err := readAuthoringStage(tx.QueryRowContext(ctx, `SELECT run_id, session_id, base_revision, stage_revision, run_attempt, plan_json, changes_json, created_at, updated_at
		FROM authoring_stages WHERE run_id = ? FOR UPDATE`, runID))
	if err != nil {
		return nil, err
	}
	if stage.RunAttempt != expectedAttempt || stage.SessionID != run.OwnerRef {
		return nil, agent.ErrRunActive
	}
	session, err := lockAuthoringPlanSessionTx(ctx, tx, stage.SessionID, "")
	if err != nil {
		return nil, err
	}
	if session.RuntimeSessionID != run.SessionID || session.CurrentRevision != stage.BaseRevision || !authoring.AllowsAgentPlanStage(session.State) {
		return nil, authoring.ErrInvalidState
	}
	revision := &authoring.Revision{Number: session.CurrentRevision, Plan: stage.Plan, CreatedAt: now.UTC()}
	if len(stage.Changes) > 0 {
		if err := stage.Plan.ValidateForGeneration(); err != nil {
			return nil, fmt.Errorf("%w: %v", authoring.ErrInvalidState, err)
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
	message := agent.Message{ID: authoring.RunCompletionMessageID(run.ID), SessionID: run.SessionID, Role: "assistant", Content: content, Metadata: []byte(metadata), CreatedAt: now.UTC()}
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

func replayCompletedAuthoringRunTx(ctx context.Context, tx *Tx, run agent.Run, content string) (*authoring.Revision, error) {
	message, err := scanAgentMessage(tx.QueryRowContext(ctx, `SELECT id, session_id, sequence, role, content, metadata_json, created_at
		FROM agent_messages WHERE id = ?`, authoring.RunCompletionMessageID(run.ID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, authoring.ErrInvalidState
	}
	if err != nil {
		return nil, fmt.Errorf("read completed authoring message: %w", err)
	}
	if message.SessionID != run.SessionID || message.Role != "assistant" || message.Content != content {
		return nil, authoring.ErrVersionConflict
	}
	var metadata struct {
		Changes []authoring.Change `json:"changes"`
	}
	if err := json.Unmarshal(message.Metadata, &metadata); err != nil {
		return nil, fmt.Errorf("decode completed authoring changes: %w", err)
	}
	baseRevision, err := strconv.ParseInt(run.InputRevision, 10, 64)
	if err != nil || baseRevision < 0 {
		return nil, authoring.ErrVersionConflict
	}
	revisionNumber := baseRevision
	if len(metadata.Changes) > 0 {
		revisionNumber++
	}
	return readAuthoringRevisionTx(ctx, tx, run.OwnerRef, revisionNumber)
}

// TerminateAuthoringRun atomically ends a direct Server-owned turn, discards
// its private Plan stage, and appends one durable recovery event. It never
// constructs a replacement run: the next author message is the only way to
// start another Eino execution.
func (d *AuthoringRepository) TerminateAuthoringRun(ctx context.Context, runID string, reason authoring.RunTerminationReason, diagnostic string, now time.Time) error {
	if strings.TrimSpace(runID) == "" || !reason.Valid() || now.IsZero() {
		return errors.New("authoring run termination is incomplete")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin authoring run termination: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	run, err := scanAgentRun(tx.QueryRowContext(ctx, agentRunSelect+` WHERE id = ? FOR UPDATE`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return agent.ErrNotFound
	}
	if err != nil {
		return err
	}
	if run.Purpose != "authoring" || run.OwnerKind != "authoring-session" || strings.TrimSpace(run.SessionID) == "" || strings.TrimSpace(run.OwnerRef) == "" {
		return agent.ErrRunActive
	}
	session, err := lockAuthoringPlanSessionTx(ctx, tx, run.OwnerRef, "")
	if err != nil {
		return err
	}
	if session.RuntimeSessionID != run.SessionID {
		return authoring.ErrInvalidState
	}
	if err := lockAgentSessionTx(ctx, tx, run.SessionID); err != nil {
		return err
	}
	status := agent.RunInterrupted
	if reason.Kind() == authoring.RunTerminationFailed {
		status = agent.RunFailed
	}
	if run.Status == agent.RunRunning {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, last_error = ?, completed_at = ?, updated_at = ?
			WHERE id = ? AND status = ?`, status, strings.TrimSpace(diagnostic), now.UTC(), now.UTC(), run.ID, agent.RunRunning); err != nil {
			return fmt.Errorf("terminate authoring run: %w", err)
		}
	} else if run.Status != status {
		return agent.ErrRunActive
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM authoring_stages WHERE run_id = ?`, run.ID); err != nil {
		return fmt.Errorf("discard terminated authoring stage: %w", err)
	}
	recovery, err := authoringRunRecoveryTx(ctx, tx, session.ID, reason)
	if err != nil {
		return err
	}
	event, err := authoring.NewRunEvent(run.ID, reason, recovery)
	if err != nil {
		return err
	}
	content, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode authoring run event: %w", err)
	}
	eventID := authoring.RunEventMessageID(run.ID, reason)
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_messages WHERE id = ?)`, eventID).Scan(&exists); err != nil {
		return fmt.Errorf("check authoring run event: %w", err)
	}
	if !exists {
		message := agent.Message{ID: eventID, SessionID: run.SessionID, Role: "event", Content: string(content), CreatedAt: now.UTC()}
		if err := insertAgentMessageTx(ctx, tx, &message); err != nil {
			return fmt.Errorf("append authoring run event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit authoring run termination: %w", err)
	}
	return nil
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

// SaveGenerationPlan persists an external Generator client's complete Plan
// request with a user-scoped receipt. A lost response for the first request
// can therefore return the same newly created AuthoringSession on retry.
func (d *AuthoringRepository) SaveGenerationPlan(ctx context.Context, userID, sessionID, newSessionID string, expected int64, idempotencyKey string, plan authoring.Plan) (*authoring.Session, *authoring.Revision, error) {
	userID = strings.TrimSpace(userID)
	sessionID = strings.TrimSpace(sessionID)
	newSessionID = strings.TrimSpace(newSessionID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if userID == "" || expected < 0 || idempotencyKey == "" || len(idempotencyKey) > 200 {
		return nil, nil, errors.New("generation plan requires user, non-negative expected revision, and idempotency key")
	}
	if sessionID == "" && (expected != 0 || newSessionID == "") {
		return nil, nil, authoring.ErrVersionConflict
	}
	if err := plan.ValidateForGeneration(); err != nil {
		return nil, nil, err
	}
	planJSON, err := marshalJSON(plan)
	if err != nil {
		return nil, nil, fmt.Errorf("encode generation plan: %w", err)
	}
	digest := sha256.Sum256([]byte(planJSON))
	planSHA256 := hex.EncodeToString(digest[:])
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin generation plan: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var receiptSessionID, receiptSHA256 string
	var receiptExpected, receiptRevision int64
	err = tx.QueryRowContext(ctx, `SELECT session_id, expected_revision, plan_revision, plan_sha256
		FROM generation_plan_receipts WHERE user_id = ? AND idempotency_key = ? FOR UPDATE`, userID, idempotencyKey).
		Scan(&receiptSessionID, &receiptExpected, &receiptRevision, &receiptSHA256)
	if err == nil {
		if receiptExpected != expected || receiptSHA256 != planSHA256 || (sessionID != "" && sessionID != receiptSessionID) {
			return nil, nil, authoring.ErrVersionConflict
		}
		session, err := readAuthoringSessionTx(ctx, tx, receiptSessionID, userID)
		if err != nil {
			return nil, nil, err
		}
		revision, err := readAuthoringRevisionTx(ctx, tx, receiptSessionID, receiptRevision)
		if err != nil {
			return nil, nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, nil, err
		}
		return session, revision, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, fmt.Errorf("read generation plan receipt: %w", err)
	}

	var session *authoring.Session
	now := time.Now().UTC()
	if sessionID == "" {
		session = &authoring.Session{ID: newSessionID, UserID: userID}
		prepareNewAuthoringSession(session, now)
		if err := insertNewAuthoringSessionTx(ctx, tx, *session, authoring.Plan{}); err != nil {
			return nil, nil, err
		}
	} else {
		session, err = lockAuthoringPlanSessionTx(ctx, tx, sessionID, userID)
		if err != nil {
			return nil, nil, err
		}
	}
	if session.CurrentRevision != expected {
		return nil, nil, authoring.ErrVersionConflict
	}
	next := expected + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO authoring_revisions (session_id, revision, plan_json, candidate_revision_id, created_at)
		VALUES (?, ?, ?::jsonb, '', ?)`, session.ID, next, planJSON, nowText(now)); err != nil {
		return nil, nil, fmt.Errorf("persist generation plan revision: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET current_revision = ?, state = ?, last_error = '', updated_at = ? WHERE id = ? AND user_id = ?`,
		next, authoring.StateIntentReview, nowText(now), session.ID, userID); err != nil {
		return nil, nil, fmt.Errorf("advance generation plan revision: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO generation_plan_receipts
		(user_id, idempotency_key, session_id, expected_revision, plan_revision, plan_sha256, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, userID, idempotencyKey, session.ID, expected, next, planSHA256, now.UTC()); err != nil {
		return nil, nil, fmt.Errorf("record generation plan receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit generation plan: %w", err)
	}
	session.CurrentRevision = next
	session.State = authoring.StateIntentReview
	session.LastError = ""
	session.UpdatedAt = now
	return session, &authoring.Revision{Number: next, Plan: plan, CreatedAt: now}, nil
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

func readAuthoringStageOperationTx(ctx context.Context, tx *Tx, current authoring.Stage, operationID string) (*authoring.Stage, string, error) {
	var stageRevision int64
	var requestDigest string
	var planJSON, changesJSON []byte
	var updatedAt time.Time
	err := tx.QueryRowContext(ctx, `SELECT request_digest, stage_revision, plan_json, changes_json, updated_at
		FROM authoring_stage_operations WHERE run_id = ? AND operation_id = ?`, current.RunID, operationID).
		Scan(&requestDigest, &stageRevision, &planJSON, &changesJSON, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("read authoring stage operation: %w", err)
	}
	replay := current
	replay.StageRevision = stageRevision
	replay.UpdatedAt = updatedAt
	if err := json.Unmarshal(planJSON, &replay.Plan); err != nil {
		return nil, "", fmt.Errorf("decode authoring operation plan: %w", err)
	}
	if err := json.Unmarshal(changesJSON, &replay.Changes); err != nil {
		return nil, "", fmt.Errorf("decode authoring operation changes: %w", err)
	}
	return &replay, requestDigest, nil
}

type authoringMessageReceipt struct {
	SessionID      string
	IdempotencyKey string
	RequestDigest  string
	MessageID      string
	RunID          string
	CreatedAt      time.Time
}

func authoringMessageRequestDigest(content string) (string, error) {
	canonical, err := json.Marshal(struct {
		Content string `json:"content"`
	}{Content: strings.TrimSpace(content)})
	if err != nil {
		return "", fmt.Errorf("encode authoring message receipt: %w", err)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func authoringMessageReceiptTx(ctx context.Context, tx *Tx, sessionID, idempotencyKey string) (*authoringMessageReceipt, error) {
	value := &authoringMessageReceipt{SessionID: sessionID, IdempotencyKey: idempotencyKey}
	err := tx.QueryRowContext(ctx, `SELECT request_digest, message_id, run_id, created_at
		FROM authoring_message_receipts WHERE session_id = ? AND idempotency_key = ? FOR UPDATE`, sessionID, idempotencyKey).
		Scan(&value.RequestDigest, &value.MessageID, &value.RunID, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read authoring message receipt: %w", err)
	}
	value.CreatedAt = value.CreatedAt.UTC()
	return value, nil
}

func insertAuthoringMessageReceiptTx(ctx context.Context, tx *Tx, value authoringMessageReceipt) error {
	if strings.TrimSpace(value.SessionID) == "" || strings.TrimSpace(value.IdempotencyKey) == "" || strings.TrimSpace(value.RequestDigest) == "" ||
		strings.TrimSpace(value.MessageID) == "" || strings.TrimSpace(value.RunID) == "" || value.CreatedAt.IsZero() {
		return errors.New("authoring message receipt is incomplete")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO authoring_message_receipts
		(session_id, idempotency_key, request_digest, message_id, run_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, value.SessionID, value.IdempotencyKey, value.RequestDigest, value.MessageID, value.RunID, value.CreatedAt.UTC()); err != nil {
		return fmt.Errorf("record authoring message receipt: %w", err)
	}
	return nil
}

func authoringRunRecoveryTx(ctx context.Context, tx *Tx, sessionID string, reason authoring.RunTerminationReason) (string, error) {
	if reason == authoring.RunTerminationDeadlineExceeded || reason == authoring.RunTerminationPermanentExecutorError {
		var hasWorkspace bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1
			FROM generation_workflows workflow
			JOIN generator_workspaces workspace ON workspace.workflow_id = workflow.id
			WHERE workflow.source_kind = ? AND workflow.source_ref = ? AND workflow.state = ? AND workspace.state = ?
		)`, generation.SourceAuthoring, sessionID, generation.StateGenerating, generation.WorkspaceActive).Scan(&hasWorkspace)
		if err != nil {
			return "", fmt.Errorf("read authoring workspace recovery: %w", err)
		}
		if hasWorkspace {
			return "workspace", nil
		}
	}
	var hasSnapshot bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM generation_workflows
		WHERE source_kind = ? AND source_ref = ? AND state = ? AND workspace_snapshot_digest <> ''
	)`, generation.SourceAuthoring, sessionID, generation.StateGenerating).Scan(&hasSnapshot)
	if err != nil {
		return "", fmt.Errorf("read authoring snapshot recovery: %w", err)
	}
	if hasSnapshot {
		return "snapshot", nil
	}
	var hasCandidate bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM generation_workflows
		WHERE source_kind = ? AND source_ref = ? AND candidate_revision_id IS NOT NULL
	)`, generation.SourceAuthoring, sessionID).Scan(&hasCandidate)
	if err != nil {
		return "", fmt.Errorf("read authoring candidate recovery: %w", err)
	}
	if hasCandidate {
		return "candidate", nil
	}
	return "empty", nil
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
