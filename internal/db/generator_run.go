package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/generator"
)

const generatorOwnerKind = "authoring-session"

// StartGeneratorRun atomically starts the Generator side of a confirmed
// Authoring revision. A repair Run deliberately reuses the existing Generator
// Session and therefore its workspace; a new author revision clears that
// Session before this method is called.
func (d *DB) StartGeneratorRun(ctx context.Context, sessionID, userID string, expectedRevision int64, run agentruntime.CreateRun, input generator.RunInput) (*authoring.Session, *agentruntime.Run, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || expectedRevision < 0 {
		return nil, nil, errors.New("generator run requires authoring session, user, and revision")
	}
	if input.AuthoringSessionID != sessionID || input.Revision != expectedRevision {
		return nil, nil, errors.New("generator run input does not match authoring revision")
	}
	if run.ID == "" || run.Purpose != generator.RuntimePurpose || run.OwnerKind != generatorOwnerKind || run.OwnerRef != sessionID || run.Model == "" || run.PromptVersion == "" || run.ExecutionTimeout <= 0 {
		return nil, nil, errors.New("generator run ownership or runtime metadata is invalid")
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return nil, nil, fmt.Errorf("encode generator run input: %w", err)
	}
	run.Input = inputJSON
	run.InputRevision = strconv.FormatInt(expectedRevision, 10)

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin generator run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT id FROM authoring_sessions WHERE id = ? AND user_id = ? FOR UPDATE`, sessionID, userID); err != nil {
		return nil, nil, fmt.Errorf("lock generator authoring session: %w", err)
	}
	session, err := readAuthoringSessionTx(ctx, tx, sessionID, userID)
	if err != nil {
		return nil, nil, err
	}
	if session.CurrentRevision != expectedRevision {
		return nil, nil, authoring.ErrInvalidState
	}
	if session.State != authoring.StateIntentReview && session.State != authoring.StateRevisingAndVerifying && session.State != authoring.StateGeneratingAndVerifying {
		return nil, nil, authoring.ErrInvalidState
	}
	if session.State == authoring.StateRevisingAndVerifying && session.GeneratorRunID != "" {
		existing, err := scanAgentRun(tx.QueryRowContext(ctx, agentRunSelect+` WHERE id = ?`, session.GeneratorRunID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, authoring.ErrInvalidState
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read active revised generator run: %w", err)
		}
		record, err := readGeneratorRun(tx.QueryRowContext(ctx, generatorRunColumns+` WHERE run_id = ?`, existing.ID))
		if err != nil {
			return nil, nil, fmt.Errorf("read active revised generator record: %w", err)
		}
		if existing.SessionID != session.GeneratorSessionID || record.AuthoringSessionID != session.ID || record.AuthoringRevision != expectedRevision {
			return nil, nil, authoring.ErrInvalidState
		}
		if err := tx.Commit(); err != nil {
			return nil, nil, fmt.Errorf("commit existing revised generator run: %w", err)
		}
		return session, existing, nil
	}
	if session.State == authoring.StateGeneratingAndVerifying {
		if strings.TrimSpace(input.SeedCandidateRevisionID) == "" || input.Feedback.Empty() {
			return nil, nil, authoring.ErrInvalidState
		}
	} else if session.State == authoring.StateIntentReview && (!input.Feedback.Empty() || strings.TrimSpace(input.SeedCandidateRevisionID) != "") {
		return nil, nil, authoring.ErrInvalidState
	} else if session.State == authoring.StateRevisingAndVerifying && strings.TrimSpace(input.SeedCandidateRevisionID) == "" {
		return nil, nil, authoring.ErrInvalidState
	}

	generatorSessionID := strings.TrimSpace(session.GeneratorSessionID)
	now := time.Now().UTC()
	if session.State == authoring.StateRevisingAndVerifying {
		if generatorSessionID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_sessions SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
				agentruntime.SessionClosed, now, generatorSessionID, agentruntime.SessionActive); err != nil {
				return nil, nil, fmt.Errorf("close superseded generator session: %w", err)
			}
		}
		generatorSessionID = generator.NewSessionID()
		ownerRef := session.ID + ":" + strconv.FormatInt(expectedRevision, 10)
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions
			(id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			generatorSessionID, generator.RuntimePurpose, "authoring-generator", ownerRef, session.UserID, agentruntime.SessionActive, now, now); err != nil {
			return nil, nil, fmt.Errorf("insert replacement generator agent session: %w", err)
		}
	} else if generatorSessionID == "" {
		generatorSessionID = generator.NewSessionID()
		ownerRef := session.ID + ":" + strconv.FormatInt(expectedRevision, 10)
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_sessions
			(id, purpose, owner_kind, owner_ref, user_ref, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			generatorSessionID, generator.RuntimePurpose, "authoring-generator", ownerRef, session.UserID, agentruntime.SessionActive, now, now); err != nil {
			return nil, nil, fmt.Errorf("insert generator agent session: %w", err)
		}
	} else {
		var status agentruntime.SessionStatus
		err := tx.QueryRowContext(ctx, `SELECT status FROM agent_sessions WHERE id = ? FOR UPDATE`, generatorSessionID).Scan(&status)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, agentruntime.ErrNotFound
		}
		if err != nil {
			return nil, nil, fmt.Errorf("lock generator agent session: %w", err)
		}
		if status != agentruntime.SessionActive {
			return nil, nil, errors.New("generator agent session is not active")
		}
	}
	if err := ensureNoActiveSessionRun(ctx, tx, generatorSessionID); err != nil {
		return nil, nil, err
	}
	run.SessionID = generatorSessionID
	if err := agentruntime.ValidateCreateRun(run); err != nil {
		return nil, nil, err
	}
	created, err := createRunTx(ctx, tx, run, now)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO generator_runs
		(run_id, generator_session_id, authoring_session_id, authoring_revision, seed_candidate_revision_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		created.ID, generatorSessionID, session.ID, expectedRevision, input.SeedCandidateRevisionID, now, now); err != nil {
		return nil, nil, fmt.Errorf("insert generator run record: %w", err)
	}
	state := authoring.StateGeneratingAndVerifying
	if session.State == authoring.StateRevisingAndVerifying {
		state = authoring.StateRevisingAndVerifying
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions
		SET state = ?, generator_session_id = ?, generator_run_id = ?, candidate_revision_id = '', last_error = '', updated_at = ?
		WHERE id = ?`, state, generatorSessionID, created.ID, nowText(now), session.ID); err != nil {
		return nil, nil, fmt.Errorf("mark generator run active: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit generator run: %w", err)
	}
	updated, err := d.GetAuthoringSession(ctx, sessionID, userID)
	if err != nil {
		return nil, nil, err
	}
	return updated, created, nil
}

func (d *DB) GetGeneratorRun(ctx context.Context, runID string) (*generator.Record, error) {
	return readGeneratorRun(d.conn.QueryRowContext(ctx, generatorRunColumns+` WHERE run_id = ?`, runID))
}

// ReconcileFailedGeneratorRuns releases AuthoringSessions whose current
// Generator Run reached a terminal technical failure. The update is
// intentionally idempotent: a newer authoring revision no longer points at the
// failed run and is never overwritten by delayed recovery.
func (d *DB) ReconcileFailedGeneratorRuns(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		return 0, errors.New("failed generator reconciliation requires current time")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE authoring_sessions AS session SET
		state = ?, generator_run_id = '', last_error = run.last_error, updated_at = ?
		FROM generator_runs AS generator_run
		JOIN agent_runs AS run ON run.id = generator_run.run_id
		WHERE session.id = generator_run.authoring_session_id
		AND session.current_revision = generator_run.authoring_revision
		AND session.generator_run_id = run.id
		AND session.state IN (?, ?)
		AND run.purpose = ? AND run.status = ?`,
		authoring.StateInfrastructureFailed, nowText(now), authoring.StateGeneratingAndVerifying,
		authoring.StateRevisingAndVerifying, generator.RuntimePurpose, agentruntime.RunFailed)
	if err != nil {
		return 0, fmt.Errorf("project failed generator runs: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count projected failed generator runs: %w", err)
	}
	return changed, nil
}

// MarkGeneratorWorkspaceInitialized makes materialization idempotent across
// attempt retries. The caller must materialize before marking so a crash only
// causes a deterministic reset from the same immutable seed artifact.
func (d *DB) MarkGeneratorWorkspaceInitialized(ctx context.Context, claim agentruntime.Claim) error {
	if !claim.Valid() {
		return errors.New("generator workspace initialization requires a lease")
	}
	now := time.Now().UTC()
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin generator workspace initialization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := validateLeaseTx(ctx, tx, claim, now); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE generator_runs SET workspace_initialized_at = ?, updated_at = ?
		WHERE run_id = ? AND workspace_initialized_at IS NULL`, now, now, claim.Run.ID)
	if err != nil {
		return fmt.Errorf("mark generator workspace initialized: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("generator workspace is already initialized")
	}
	return tx.Commit()
}

const generatorRunColumns = `SELECT run_id, generator_session_id, authoring_session_id, authoring_revision,
	seed_candidate_revision_id, workspace_initialized_at IS NOT NULL, candidate_revision_id, created_at, updated_at FROM generator_runs`

func readGeneratorRun(row agentRow) (*generator.Record, error) {
	var record generator.Record
	if err := row.Scan(&record.RunID, &record.GeneratorSessionID, &record.AuthoringSessionID, &record.AuthoringRevision,
		&record.SeedCandidateRevisionID, &record.WorkspaceInitialized, &record.CandidateRevisionID, &record.CreatedAt, &record.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, generator.ErrNotFound
		}
		return nil, fmt.Errorf("read generator run: %w", err)
	}
	return &record, nil
}
