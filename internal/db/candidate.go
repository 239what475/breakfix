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
	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/generator"
	"github.com/breakfix/breakfix/internal/taxonomy"
	"github.com/breakfix/breakfix/internal/worklist"
)

type CandidateWorkClaim struct {
	Work      worklist.Claim          `json:"work"`
	Candidate candidate.WorkerView    `json:"candidate"`
	Cleanup   *candidate.CleanupHints `json:"cleanup,omitempty"`
}

// FinalizeGeneratorCandidate is the only creation path for a CandidateRevision.
// The Generator and its in-run Judge share one AgentRun, so both lineage fields
// must identify the fenced Run that performed those two steps.
func (d *DB) FinalizeGeneratorCandidate(ctx context.Context, claim agentruntime.Claim, revision candidate.Revision, now time.Time) error {
	if !claim.Valid() || now.IsZero() {
		return errors.New("candidate handoff requires a valid generator claim and current time")
	}
	if revision.GeneratorRunID != claim.Run.ID || revision.JudgeRunID != claim.Run.ID || revision.GeneratorSessionID != claim.Run.SessionID {
		return errors.New("candidate lineage does not match the generator workflow run")
	}
	if err := revision.ValidateForCreate(); err != nil {
		return err
	}
	now = now.UTC()
	revision.CreatedAt = now
	revision.UpdatedAt = now

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin candidate handoff: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := lockAgentClaim(ctx, tx, claim, now); err != nil {
		return err
	}
	record, err := readGeneratorRun(tx.QueryRowContext(ctx, generatorRunColumns+` WHERE run_id = ? FOR UPDATE`, claim.Run.ID))
	if err != nil {
		return err
	}
	if record.GeneratorSessionID != revision.GeneratorSessionID || record.AuthoringSessionID != revision.AuthoringSessionID || record.AuthoringRevision != revision.AuthoringRevision {
		return authoring.ErrInvalidState
	}
	if record.CandidateRevisionID != "" && record.CandidateRevisionID != revision.ID {
		return authoring.ErrInvalidState
	}
	if _, err := tx.ExecContext(ctx, `SELECT id FROM authoring_sessions WHERE id = ? FOR UPDATE`, record.AuthoringSessionID); err != nil {
		return fmt.Errorf("lock candidate authoring session: %w", err)
	}
	session, err := readAuthoringSessionTx(ctx, tx, record.AuthoringSessionID, "")
	if err != nil {
		return err
	}
	if session.GeneratorRunID != claim.Run.ID || session.CurrentRevision != revision.AuthoringRevision ||
		(session.State != authoring.StateGeneratingAndVerifying && session.State != authoring.StateRevisingAndVerifying) {
		return authoring.ErrInvalidState
	}
	if err := insertCandidateRevisionTx(ctx, tx, revision); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE generator_runs SET candidate_revision_id = ?, updated_at = ? WHERE run_id = ?`, revision.ID, now, claim.Run.ID); err != nil {
		return fmt.Errorf("link generator candidate revision: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET candidate_revision_id = ?, last_error = '', updated_at = ? WHERE id = ?`, revision.ID, nowText(now), session.ID); err != nil {
		return fmt.Errorf("link authoring candidate revision: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_runs SET status = ?, completed_at = ?, updated_at = ? WHERE id = ? AND status = ?`,
		agentruntime.RunSucceeded, now, now, claim.Run.ID, agentruntime.RunRunning); err != nil {
		return fmt.Errorf("complete candidate generator run: %w", err)
	}
	if err := completeAgentWorkItemTx(ctx, tx, claim, worklist.StateSucceeded, "", "", now); err != nil {
		return err
	}
	if _, err := createWorkItemTx(ctx, tx, candidateWorkItem(revision.ID, worklist.KindBuild, now), now); err != nil {
		return fmt.Errorf("enqueue candidate build: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit candidate handoff: %w", err)
	}
	return nil
}

func (d *DB) GetCandidateRevision(ctx context.Context, id string) (*candidate.Revision, error) {
	revision, err := scanCandidateRevision(d.conn.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, candidate.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get candidate revision: %w", err)
	}
	return revision, nil
}

func (d *DB) ClaimCandidateWork(ctx context.Context, kind worklist.Kind, workerID string, leaseTTL time.Duration, now time.Time) (*CandidateWorkClaim, error) {
	if kind == worklist.KindAgent || !worklist.ValidKind(kind) {
		return nil, errors.New("candidate work claim requires a candidate work kind")
	}
	if kind != worklist.KindArtifactCleanup {
		if err := d.ExpireCandidateWork(ctx, kind, now); err != nil {
			return nil, err
		}
	}
	claim, err := d.ClaimWorkItem(ctx, kind, workerID, leaseTTL, now)
	if err != nil || claim == nil {
		return nil, err
	}
	revision, err := d.GetCandidateRevision(ctx, claim.Item.SubjectID)
	if err != nil {
		return nil, err
	}
	if claim.Item.SubjectType != worklist.SubjectCandidateRevision || !validStateForWorkKind(kind, revision.State) {
		return nil, fmt.Errorf("%w: work item %s does not match candidate state", candidate.ErrInvalidState, claim.Item.ID)
	}
	result := &CandidateWorkClaim{Work: *claim, Candidate: revision.WorkerView()}
	if kind == worklist.KindArtifactCleanup {
		build, err := d.GetWorkItemForSubject(ctx, worklist.KindBuild, worklist.SubjectCandidateRevision, revision.ID)
		if err != nil {
			return nil, fmt.Errorf("read build cleanup identity: %w", err)
		}
		result.Cleanup = &candidate.CleanupHints{BuildWorkItemID: build.ID, BuildAttempts: int64(build.Attempt)}
		if err := result.Cleanup.Validate(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (d *DB) GetCandidateWorkClaim(ctx context.Context, kind worklist.Kind, credential worklist.Credential, now time.Time) (*CandidateWorkClaim, error) {
	if kind == worklist.KindAgent || !worklist.ValidKind(kind) || !credential.Valid() || now.IsZero() {
		return nil, errors.New("candidate work credentials and kind are required")
	}
	item, err := d.GetWorkItem(ctx, credential.WorkItemID)
	if err != nil {
		return nil, err
	}
	claim := worklist.Claim{Item: *item, LeaseOwner: credential.LeaseOwner}
	if item.Kind != kind || item.SubjectType != worklist.SubjectCandidateRevision || item.Attempt != credential.Attempt || item.LeaseOwner != credential.LeaseOwner {
		return nil, worklist.ErrLeaseLost
	}
	if err := d.ValidateWorkItemClaim(ctx, claim, now); err != nil {
		return nil, err
	}
	revision, err := d.GetCandidateRevision(ctx, item.SubjectID)
	if err != nil {
		return nil, err
	}
	if !validStateForWorkKind(kind, revision.State) {
		return nil, candidate.ErrInvalidState
	}
	result := &CandidateWorkClaim{Work: claim, Candidate: revision.WorkerView()}
	if kind == worklist.KindArtifactCleanup {
		build, err := d.GetWorkItemForSubject(ctx, worklist.KindBuild, worklist.SubjectCandidateRevision, revision.ID)
		if err != nil {
			return nil, fmt.Errorf("read build cleanup identity: %w", err)
		}
		result.Cleanup = &candidate.CleanupHints{BuildWorkItemID: build.ID, BuildAttempts: int64(build.Attempt)}
		if err := result.Cleanup.Validate(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// ExpireCandidateWork closes a due stage and its CandidateRevision together.
// Server-owned recovery invokes it periodically; candidate claims also invoke
// it before the generic WorkItem claim transaction as a race-safe last check.
func (d *DB) ExpireCandidateWork(ctx context.Context, kind worklist.Kind, now time.Time) error {
	if kind == worklist.KindAgent || kind == worklist.KindArtifactCleanup || !worklist.ValidKind(kind) || now.IsZero() {
		return errors.New("candidate expiry requires a deadline-bound candidate work kind and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin candidate work expiry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	failure := candidate.Failure{
		Class: candidate.FailureInfrastructure, Code: "DEADLINE_EXCEEDED",
		Summary: "candidate stage deadline exceeded",
	}
	failureJSON, err := marshalJSON(failure)
	if err != nil {
		return err
	}
	rows, err := tx.raw.QueryContext(ctx, bind(`UPDATE work_items SET state = ?, lease_owner = '', lease_expires_at = NULL,
		error_code = ?, error_summary = ?, updated_at = ? WHERE kind = ? AND subject_type = ?
		AND state IN (?, ?) AND deadline_at <= ? RETURNING subject_id`),
		worklist.StateFailed, failure.Code, failure.Summary, now.UTC(), kind, worklist.SubjectCandidateRevision,
		worklist.StatePending, worklist.StateRunning, now.UTC())
	if err != nil {
		return fmt.Errorf("expire candidate work items: %w", err)
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan expired candidate: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate expired candidates: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close expired candidate rows: %w", err)
	}
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET state = ?, failure = ?::jsonb, updated_at = ?
			WHERE id = ? AND state = ?`, candidate.StateInfrastructureFailed, failureJSON, now.UTC(), id, stateForWorkKind(kind))
		if err != nil {
			return fmt.Errorf("fail expired candidate %s: %w", id, err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return fmt.Errorf("%w: expired %s work does not match candidate %s", candidate.ErrInvalidState, kind, id)
		}
		result, err = tx.ExecContext(ctx, `UPDATE authoring_sessions SET state = ?, last_error = ?, updated_at = ?
			WHERE id = (SELECT authoring_session_id FROM candidate_revisions WHERE id = ?)
			AND current_revision = (SELECT authoring_revision FROM candidate_revisions WHERE id = ?)
			AND candidate_revision_id = ?`, authoring.StateInfrastructureFailed, failure.Summary,
			nowText(now), id, id, id)
		if err != nil {
			return fmt.Errorf("project expired candidate %s to authoring: %w", id, err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return fmt.Errorf("%w: expired candidate %s is not the active authoring candidate", authoring.ErrInvalidState, id)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO work_items
			(id, kind, subject_type, subject_id, state, next_run_at, execution_timeout_millis, deadline_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 0, NULL, ?, ?) ON CONFLICT (kind, subject_type, subject_id) DO NOTHING`,
			worklist.NewID("work"), worklist.KindArtifactCleanup, worklist.SubjectCandidateRevision, id,
			worklist.StatePending, now.UTC(), now.UTC(), now.UTC()); err != nil {
			return fmt.Errorf("enqueue expired candidate cleanup: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit candidate work expiry: %w", err)
	}
	return nil
}

func (d *DB) CompleteCandidateBuild(ctx context.Context, claim worklist.Claim, output candidate.BuildOutput, now time.Time) error {
	return d.advanceCandidate(ctx, claim, candidate.StateBuilding, candidate.StatePublishingArtifact, output, worklist.KindArtifactPublish, now)
}

func (d *DB) CompleteCandidateArtifactPublish(ctx context.Context, claim worklist.Claim, artifact candidate.ArtifactReference, now time.Time) error {
	return d.advanceCandidate(ctx, claim, candidate.StatePublishingArtifact, candidate.StateVerifying, artifact, worklist.KindVerify, now)
}

func (d *DB) CompleteCandidateVerification(ctx context.Context, claim worklist.Claim, report candidate.VerificationReport, now time.Time) error {
	if now.IsZero() {
		return errors.New("verification completion requires current time")
	}
	tx, revision, err := d.lockCandidateClaim(ctx, claim, worklist.KindVerify, candidate.StateVerifying, now)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := report.Validate(revision.Snapshot); err != nil {
		return err
	}
	if !report.Passed {
		return errors.New("failed verification must use the artifact failure transition")
	}
	if _, err := tx.ExecContext(ctx, `SELECT id FROM authoring_sessions WHERE id = ? FOR UPDATE`, revision.AuthoringSessionID); err != nil {
		return fmt.Errorf("lock verified candidate authoring session: %w", err)
	}
	session, err := readAuthoringSessionTx(ctx, tx, revision.AuthoringSessionID, "")
	if err != nil {
		return err
	}
	if session.CandidateRevisionID != revision.ID || session.CurrentRevision != revision.AuthoringRevision ||
		(session.State != authoring.StateGeneratingAndVerifying && session.State != authoring.StateRevisingAndVerifying) {
		return authoring.ErrInvalidState
	}
	authoringRevision, err := readAuthoringRevisionTx(ctx, tx, session.ID, session.CurrentRevision)
	if err != nil {
		return err
	}
	if authoringRevision.CandidateRevisionID != "" && authoringRevision.CandidateRevisionID != revision.ID {
		return authoring.ErrInvalidState
	}
	var previousCandidateID string
	if session.VisibleRevision <= session.CurrentRevision {
		previous, previousErr := readAuthoringRevisionTx(ctx, tx, session.ID, session.VisibleRevision)
		if previousErr != nil && !errors.Is(previousErr, authoring.ErrNotFound) {
			return previousErr
		}
		if previous != nil {
			previousCandidateID = previous.CandidateRevisionID
		}
	}
	encoded, err := marshalJSON(report)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET state = ?, verification_report = ?::jsonb,
		failure = NULL, verified_at = ?, updated_at = ? WHERE id = ? AND state = ?`,
		candidate.StateVerified, encoded, now.UTC(), now.UTC(), revision.ID, candidate.StateVerifying)
	if err != nil {
		return fmt.Errorf("record verified candidate: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return candidate.ErrInvalidState
	}
	result, err = tx.ExecContext(ctx, `UPDATE authoring_revisions SET candidate_revision_id = ?
		WHERE session_id = ? AND revision = ? AND candidate_revision_id IN ('', ?)`,
		revision.ID, session.ID, session.CurrentRevision, revision.ID)
	if err != nil {
		return fmt.Errorf("project verified candidate to authoring revision: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return authoring.ErrInvalidState
	}
	result, err = tx.ExecContext(ctx, `UPDATE authoring_sessions SET state = ?, visible_revision = ?, last_error = '', updated_at = ?
		WHERE id = ? AND candidate_revision_id = ?`, authoring.StateAwaitingVerifiedReview, session.CurrentRevision,
		nowText(now), session.ID, revision.ID)
	if err != nil {
		return fmt.Errorf("mark candidate ready for author review: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return authoring.ErrInvalidState
	}
	if previousCandidateID != "" && previousCandidateID != revision.ID {
		result, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET state = ?, superseded_by = ?, updated_at = ?
			WHERE id = ? AND state = ?`, candidate.StateSuperseded, revision.ID, now.UTC(), previousCandidateID, candidate.StateVerified)
		if err != nil {
			return fmt.Errorf("supersede previous candidate: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed == 1 {
			if _, err := createWorkItemTx(ctx, tx, candidateWorkItem(previousCandidateID, worklist.KindArtifactCleanup, now), now); err != nil {
				return fmt.Errorf("enqueue superseded candidate cleanup: %w", err)
			}
		}
	}
	if err := completeCandidateWorkItemTx(ctx, tx, claim, worklist.StateSucceeded, "", "", now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit verified candidate: %w", err)
	}
	return nil
}

func (d *DB) RecordCandidateVerificationEnvironment(ctx context.Context, claim worklist.Claim, environment candidate.VerificationEnvironment, now time.Time) error {
	if now.IsZero() {
		return errors.New("verification environment recording requires current time")
	}
	tx, revision, err := d.lockCandidateClaim(ctx, claim, worklist.KindVerify, candidate.StateVerifying, now)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := environment.Validate(revision.Snapshot.Runtime); err != nil {
		return err
	}
	if environment.WorkItemID != claim.Item.ID || environment.Attempt != int64(claim.Item.Attempt) {
		return worklist.ErrLeaseLost
	}
	if revision.VerifyEnvironment != nil {
		if revision.VerifyEnvironment.WorkItemID == claim.Item.ID && revision.VerifyEnvironment.Attempt == int64(claim.Item.Attempt) && *revision.VerifyEnvironment != environment {
			return fmt.Errorf("%w: verification environment identity changed within one attempt", candidate.ErrInvalidState)
		}
		if *revision.VerifyEnvironment == environment {
			return tx.Commit()
		}
	}
	encoded, err := marshalJSON(environment)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET verify_environment = ?::jsonb, updated_at = ?
		WHERE id = ? AND state = ?`, encoded, now.UTC(), revision.ID, candidate.StateVerifying)
	if err != nil {
		return fmt.Errorf("record verification environment: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return candidate.ErrInvalidState
	}
	return tx.Commit()
}

func (d *DB) BeginAuthoringCandidatePublish(ctx context.Context, sessionID, userID, candidateID string, publication candidate.Publication, now time.Time) error {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userID) == "" || strings.TrimSpace(candidateID) == "" || now.IsZero() {
		return errors.New("challenge publication requires authoring session, user, candidate, and current time")
	}
	if err := publication.ValidateIntent(); err != nil {
		return err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin challenge publication: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT id FROM authoring_sessions WHERE id = ? AND user_id = ? FOR UPDATE`, sessionID, userID); err != nil {
		return fmt.Errorf("lock publishing authoring session: %w", err)
	}
	session, err := readAuthoringSessionTx(ctx, tx, sessionID, userID)
	if err != nil {
		return err
	}
	if session.State != authoring.StateAwaitingVerifiedReview || session.CurrentRevision != session.VisibleRevision || session.CandidateRevisionID != candidateID {
		return authoring.ErrInvalidState
	}
	authoringRevision, err := readAuthoringRevisionTx(ctx, tx, sessionID, session.CurrentRevision)
	if err != nil {
		return err
	}
	if authoringRevision.CandidateRevisionID != candidateID {
		return authoring.ErrInvalidState
	}
	revision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, candidateID))
	if errors.Is(err, sql.ErrNoRows) {
		return candidate.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock verified candidate: %w", err)
	}
	if revision.State == candidate.StatePublishingChallenge && revision.Publication != nil {
		existing := *revision.Publication
		existing.Artifact = nil
		if existing != publication {
			return candidate.ErrInvalidState
		}
		return tx.Commit()
	}
	if revision.State != candidate.StateVerified || revision.Publication != nil {
		return candidate.ErrInvalidState
	}
	encoded, err := marshalJSON(publication)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET state = ?, publication = ?::jsonb, updated_at = ?
		WHERE id = ? AND state = ? AND publication IS NULL`, candidate.StatePublishingChallenge, encoded, now.UTC(), candidateID, candidate.StateVerified)
	if err != nil {
		return fmt.Errorf("record challenge publication intent: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return candidate.ErrInvalidState
	}
	if _, err := createWorkItemTx(ctx, tx, candidateWorkItem(candidateID, worklist.KindChallengePublish, now), now); err != nil {
		return fmt.Errorf("enqueue challenge publication: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET state = ?, publish_challenge_id = ?, last_error = '', updated_at = ?
		WHERE id = ? AND candidate_revision_id = ?`, authoring.StatePublishing, publication.ChallengeID, nowText(now), sessionID, candidateID); err != nil {
		return fmt.Errorf("mark authoring challenge publishing: %w", err)
	}
	return tx.Commit()
}

// RecordCandidateChallengeArtifact persists the immutable result of the
// Publisher's external side effect before Server materializes the catalog
// directory. Keeping the WorkItem running preserves its fence while making
// the filesystem step recoverable after a Server restart.
func (d *DB) RecordCandidateChallengeArtifact(ctx context.Context, claim worklist.Claim, artifact candidate.ArtifactReference, now time.Time) error {
	if now.IsZero() {
		return errors.New("challenge publication artifact requires current time")
	}
	tx, revision, err := d.lockCandidateClaim(ctx, claim, worklist.KindChallengePublish, candidate.StatePublishingChallenge, now)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := artifact.Validate(revision.Snapshot.Runtime); err != nil {
		return err
	}
	if revision.Publication == nil {
		return candidate.ErrInvalidState
	}
	if revision.Publication.Artifact != nil {
		if *revision.Publication.Artifact != artifact {
			return candidate.ErrInvalidState
		}
		return tx.Commit()
	}
	publication := *revision.Publication
	publication.Artifact = &artifact
	encoded, err := marshalJSON(publication)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET publication = ?::jsonb, updated_at = ?
		WHERE id = ? AND state = ?`, encoded, now.UTC(), revision.ID, candidate.StatePublishingChallenge)
	if err != nil {
		return fmt.Errorf("record challenge publication artifact: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return candidate.ErrInvalidState
	}
	return tx.Commit()
}

func (d *DB) CompleteCandidateChallengePublish(ctx context.Context, claim worklist.Claim, artifact candidate.ArtifactReference, challengeRevision string, now time.Time) error {
	if strings.TrimSpace(challengeRevision) == "" || now.IsZero() {
		return errors.New("challenge publication completion requires revision and current time")
	}
	tx, revision, err := d.lockCandidateClaim(ctx, claim, worklist.KindChallengePublish, candidate.StatePublishingChallenge, now)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := completeCandidateWorkItemTx(ctx, tx, claim, worklist.StateSucceeded, "", "", now); err != nil {
		return err
	}
	if err := completeCandidateChallengePublicationTx(ctx, tx, revision, artifact, challengeRevision, now); err != nil {
		return err
	}
	return tx.Commit()
}

// ListRecoverableCandidatePublications returns only candidates whose final
// artifact identity was durably recorded but whose catalog publication was
// not committed. The Server can reconstruct their exact directory without
// consulting a Worker or discovering provider resources.
func (d *DB) ListRecoverableCandidatePublications(ctx context.Context, limit int) ([]candidate.Revision, error) {
	if limit <= 0 || limit > 1000 {
		return nil, errors.New("publication recovery limit must be between 1 and 1000")
	}
	rows, err := d.conn.QueryContext(ctx, candidateRevisionSelect+` WHERE publication IS NOT NULL
		AND publication->'artifact' IS NOT NULL AND state IN (?, ?)
		ORDER BY updated_at, id LIMIT ?`, candidate.StatePublishingChallenge, candidate.StateInfrastructureFailed, limit)
	if err != nil {
		return nil, fmt.Errorf("list recoverable candidate publications: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]candidate.Revision, 0)
	for rows.Next() {
		revision, err := scanCandidateRevision(rows)
		if err != nil {
			return nil, fmt.Errorf("scan recoverable candidate publication: %w", err)
		}
		result = append(result, *revision)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recoverable candidate publications: %w", err)
	}
	return result, nil
}

// RecoverCandidateChallengePublish commits an already materialized, exactly
// validated publication without trusting a stale Worker lease. It is the only
// claimless candidate transition and is restricted to Server-owned recovery.
func (d *DB) RecoverCandidateChallengePublish(ctx context.Context, candidateID string, artifact candidate.ArtifactReference, challengeRevision string, now time.Time) error {
	if strings.TrimSpace(candidateID) == "" || strings.TrimSpace(challengeRevision) == "" || now.IsZero() {
		return errors.New("challenge publication recovery requires candidate, revision, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin challenge publication recovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	revision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? FOR UPDATE`, candidateID))
	if errors.Is(err, sql.ErrNoRows) {
		return candidate.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock recoverable candidate publication: %w", err)
	}
	if revision.State == candidate.StatePublished && revision.Publication != nil && revision.Publication.Artifact != nil {
		if *revision.Publication.Artifact != artifact {
			return candidate.ErrInvalidState
		}
		return tx.Commit()
	}
	if revision.State != candidate.StatePublishingChallenge && revision.State != candidate.StateInfrastructureFailed {
		return candidate.ErrInvalidState
	}
	if revision.Publication == nil || revision.Publication.Artifact == nil || *revision.Publication.Artifact != artifact {
		return candidate.ErrInvalidState
	}
	result, err := tx.ExecContext(ctx, `UPDATE work_items SET state = ?, lease_owner = '', lease_expires_at = NULL,
		error_code = '', error_summary = '', updated_at = ? WHERE kind = ? AND subject_type = ? AND subject_id = ?
		AND state IN (?, ?, ?)`, worklist.StateSucceeded, now.UTC(), worklist.KindChallengePublish,
		worklist.SubjectCandidateRevision, revision.ID, worklist.StatePending, worklist.StateRunning, worklist.StateFailed)
	if err != nil {
		return fmt.Errorf("recover challenge publication work item: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return candidate.ErrInvalidState
	}
	if err := completeCandidateChallengePublicationTx(ctx, tx, revision, artifact, challengeRevision, now); err != nil {
		return err
	}
	return tx.Commit()
}

func completeCandidateChallengePublicationTx(ctx context.Context, tx *Tx, revision *candidate.Revision, artifact candidate.ArtifactReference, challengeRevision string, now time.Time) error {
	if revision == nil || revision.Publication == nil || revision.Publication.Artifact == nil || *revision.Publication.Artifact != artifact {
		return candidate.ErrInvalidState
	}
	if err := artifact.Validate(revision.Snapshot.Runtime); err != nil {
		return err
	}
	publication := *revision.Publication
	encoded, err := marshalJSON(publication)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET state = ?, publication = ?::jsonb,
		failure = NULL, published_at = ?, updated_at = ? WHERE id = ? AND state IN (?, ?)`, candidate.StatePublished,
		encoded, now.UTC(), now.UTC(), revision.ID, candidate.StatePublishingChallenge, candidate.StateInfrastructureFailed)
	if err != nil {
		return fmt.Errorf("complete challenge publication: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return candidate.ErrInvalidState
	}
	cleanup := candidateWorkItem(revision.ID, worklist.KindArtifactCleanup, now)
	if _, err := tx.ExecContext(ctx, `INSERT INTO work_items
		(id, kind, subject_type, subject_id, state, next_run_at, execution_timeout_millis, deadline_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 0, NULL, ?, ?) ON CONFLICT (kind, subject_type, subject_id) DO NOTHING`,
		cleanup.ID, cleanup.Kind, cleanup.SubjectType, cleanup.SubjectID, worklist.StatePending,
		cleanup.NextRunAt.UTC(), now.UTC(), now.UTC()); err != nil {
		return fmt.Errorf("enqueue published candidate cleanup: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO taxonomy_mappings
		(id, challenge_id, challenge_revision, base_revision, state, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, ?, ?) ON CONFLICT(challenge_id, challenge_revision) DO NOTHING`,
		worklist.NewID("mapping"), publication.ChallengeID, challengeRevision, taxonomy.MappingPending, now.UTC(), now.UTC()); err != nil {
		return fmt.Errorf("enqueue published challenge taxonomy mapping: %w", err)
	}
	result, err = tx.ExecContext(ctx, `UPDATE authoring_sessions SET state = ?, publish_challenge_id = ?, last_error = '', updated_at = ?
		WHERE id = ? AND current_revision = ? AND candidate_revision_id = ? AND state IN (?, ?)`,
		authoring.StatePublished, publication.ChallengeID, nowText(now), revision.AuthoringSessionID,
		revision.AuthoringRevision, revision.ID, authoring.StatePublishing, authoring.StateInfrastructureFailed)
	if err != nil {
		return fmt.Errorf("complete authoring challenge publication: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return authoring.ErrInvalidState
	}
	return nil
}

func (d *DB) CompleteCandidateCleanup(ctx context.Context, claim worklist.Claim, now time.Time) error {
	if !claim.Valid() || claim.Item.Kind != worklist.KindArtifactCleanup || now.IsZero() {
		return errors.New("candidate cleanup completion requires a valid cleanup claim")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin candidate cleanup completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var state candidate.State
	err = tx.QueryRowContext(ctx, `SELECT c.state FROM candidate_revisions c JOIN work_items w ON w.subject_id = c.id
		WHERE w.id = ? AND w.kind = ? AND w.subject_type = ? AND w.subject_id = ? AND w.state = ?
		AND w.attempt = ? AND w.lease_owner = ? AND w.lease_expires_at > ? FOR UPDATE OF w, c`,
		claim.Item.ID, worklist.KindArtifactCleanup, worklist.SubjectCandidateRevision, claim.Item.SubjectID,
		worklist.StateRunning, claim.Item.Attempt, claim.LeaseOwner, now.UTC()).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return worklist.ErrLeaseLost
	}
	if err != nil {
		return fmt.Errorf("lock candidate cleanup claim: %w", err)
	}
	if !validStateForWorkKind(worklist.KindArtifactCleanup, state) {
		return candidate.ErrInvalidState
	}
	if err := completeCandidateWorkItemTx(ctx, tx, claim, worklist.StateSucceeded, "", "", now); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) FailCandidateArtifact(ctx context.Context, claim worklist.Claim, failure candidate.Failure, report *candidate.VerificationReport, now time.Time) error {
	if failure.Class != candidate.FailureArtifact {
		return errors.New("artifact transition requires artifact failure class")
	}
	return d.failCandidate(ctx, claim, failure, report, candidate.StateArtifactFailed, now)
}

func (d *DB) FailCandidateInfrastructure(ctx context.Context, claim worklist.Claim, failure candidate.Failure, now time.Time) error {
	if failure.Class != candidate.FailureInfrastructure {
		return errors.New("infrastructure transition requires infrastructure failure class")
	}
	return d.failCandidate(ctx, claim, failure, nil, candidate.StateInfrastructureFailed, now)
}

func (d *DB) advanceCandidate(ctx context.Context, claim worklist.Claim, from, to candidate.State, value any, nextKind worklist.Kind, now time.Time) error {
	if now.IsZero() {
		return errors.New("candidate transition requires current time")
	}
	kind := claim.Item.Kind
	tx, revision, err := d.lockCandidateClaim(ctx, claim, kind, from, now)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var column string
	switch typed := value.(type) {
	case candidate.BuildOutput:
		if kind != worklist.KindBuild || nextKind != worklist.KindArtifactPublish {
			return candidate.ErrInvalidState
		}
		if err := typed.Validate(revision.Snapshot.Runtime); err != nil {
			return err
		}
		column = "build_output"
	case candidate.ArtifactReference:
		if kind != worklist.KindArtifactPublish || nextKind != worklist.KindVerify {
			return candidate.ErrInvalidState
		}
		if err := typed.Validate(revision.Snapshot.Runtime); err != nil {
			return err
		}
		column = "artifact_reference"
	default:
		return errors.New("unsupported candidate transition result")
	}
	encoded, err := marshalJSON(value)
	if err != nil {
		return err
	}
	query := `UPDATE candidate_revisions SET state = ?, ` + column + ` = ?::jsonb, failure = NULL, updated_at = ? WHERE id = ? AND state = ?`
	result, err := tx.ExecContext(ctx, query, to, encoded, now.UTC(), revision.ID, from)
	if err != nil {
		return fmt.Errorf("advance candidate revision: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return candidate.ErrInvalidState
	}
	if err := completeCandidateWorkItemTx(ctx, tx, claim, worklist.StateSucceeded, "", "", now); err != nil {
		return err
	}
	if _, err := createWorkItemTx(ctx, tx, candidateWorkItem(revision.ID, nextKind, now), now); err != nil {
		return fmt.Errorf("enqueue candidate %s: %w", nextKind, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit candidate transition: %w", err)
	}
	return nil
}

func (d *DB) failCandidate(ctx context.Context, claim worklist.Claim, failure candidate.Failure, report *candidate.VerificationReport, state candidate.State, now time.Time) error {
	if now.IsZero() {
		return errors.New("candidate failure requires current time")
	}
	if err := failure.Validate(); err != nil {
		return err
	}
	expected := stateForWorkKind(claim.Item.Kind)
	tx, revision, err := d.lockCandidateClaim(ctx, claim, claim.Item.Kind, expected, now)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	failureJSON, err := marshalJSON(failure)
	if err != nil {
		return err
	}
	var reportJSON any
	if report != nil {
		if err := report.Validate(revision.Snapshot); err != nil {
			return err
		}
		if report.Passed {
			return errors.New("artifact failure report cannot pass")
		}
		reportJSON, err = marshalJSON(*report)
		if err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE candidate_revisions SET state = ?, failure = ?::jsonb,
		verification_report = COALESCE(?::jsonb, verification_report), updated_at = ? WHERE id = ? AND state = ?`,
		state, failureJSON, reportJSON, now.UTC(), revision.ID, expected)
	if err != nil {
		return fmt.Errorf("fail candidate revision: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return candidate.ErrInvalidState
	}
	if err := completeCandidateWorkItemTx(ctx, tx, claim, worklist.StateFailed, failure.Code, failure.Summary, now); err != nil {
		return err
	}
	if _, err := createWorkItemTx(ctx, tx, candidateWorkItem(revision.ID, worklist.KindArtifactCleanup, now), now); err != nil {
		return fmt.Errorf("enqueue candidate cleanup: %w", err)
	}
	if state == candidate.StateArtifactFailed {
		if err := createGeneratorRepairRunTx(ctx, tx, revision, failure, report, now); err != nil {
			return err
		}
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET state = ?, last_error = ?, updated_at = ?
			WHERE id = ? AND current_revision = ? AND candidate_revision_id = ?`,
			authoring.StateInfrastructureFailed, failure.Summary, nowText(now), revision.AuthoringSessionID,
			revision.AuthoringRevision, revision.ID)
		if err != nil {
			return fmt.Errorf("mark authoring infrastructure failure: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return authoring.ErrInvalidState
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit candidate failure: %w", err)
	}
	return nil
}

func createGeneratorRepairRunTx(ctx context.Context, tx *Tx, revision *candidate.Revision, failure candidate.Failure, report *candidate.VerificationReport, now time.Time) error {
	if revision == nil {
		return candidate.ErrInvalidState
	}
	if _, err := tx.ExecContext(ctx, `SELECT id FROM authoring_sessions WHERE id = ? FOR UPDATE`, revision.AuthoringSessionID); err != nil {
		return fmt.Errorf("lock authoring session for candidate repair: %w", err)
	}
	session, err := readAuthoringSessionTx(ctx, tx, revision.AuthoringSessionID, "")
	if err != nil {
		return err
	}
	if session.CurrentRevision != revision.AuthoringRevision || session.CandidateRevisionID != revision.ID ||
		(session.State != authoring.StateGeneratingAndVerifying && session.State != authoring.StateRevisingAndVerifying) {
		return authoring.ErrInvalidState
	}
	if err := ensureNoActiveSessionRun(ctx, tx, revision.GeneratorSessionID); err != nil {
		return err
	}
	sourceRun, err := scanAgentRun(tx.QueryRowContext(ctx, agentRunSelect+` WHERE id = ? FOR UPDATE`, revision.GeneratorRunID))
	if err != nil {
		return fmt.Errorf("read generator run for candidate repair: %w", err)
	}
	feedback := generatorFeedbackForCandidate(failure, report)
	input := generator.RunInput{
		AuthoringSessionID: revision.AuthoringSessionID, Revision: revision.AuthoringRevision,
		SeedCandidateRevisionID: revision.ID, Feedback: feedback,
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode generator repair input: %w", err)
	}
	created, err := createRunTx(ctx, tx, agentruntime.CreateRun{
		ID: generator.NewRunID(), SessionID: revision.GeneratorSessionID, Purpose: generator.RuntimePurpose,
		OwnerKind: generatorOwnerKind, OwnerRef: revision.AuthoringSessionID,
		InputRevision: strconv.FormatInt(revision.AuthoringRevision, 10), Input: inputJSON,
		Model: sourceRun.Model, PromptVersion: sourceRun.PromptVersion, ExecutionTimeout: generator.RunDeadline,
	}, now.UTC())
	if err != nil {
		return fmt.Errorf("create generator repair run: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO generator_runs
		(run_id, generator_session_id, authoring_session_id, authoring_revision, seed_candidate_revision_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, created.ID, revision.GeneratorSessionID, revision.AuthoringSessionID,
		revision.AuthoringRevision, revision.ID, now.UTC(), now.UTC()); err != nil {
		return fmt.Errorf("insert generator repair record: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE authoring_sessions SET generator_run_id = ?, last_error = '', updated_at = ?
		WHERE id = ? AND candidate_revision_id = ?`, created.ID, nowText(now), revision.AuthoringSessionID, revision.ID); err != nil {
		return fmt.Errorf("link generator repair run: %w", err)
	}
	return nil
}

func generatorFeedbackForCandidate(failure candidate.Failure, report *candidate.VerificationReport) generator.Feedback {
	feedback := generator.Feedback{
		Summary: failure.Summary,
		Issues:  []generator.Issue{{Code: failure.Code, Message: failure.Summary}},
	}
	if report == nil {
		return feedback
	}
	for _, answer := range report.Answers {
		if answer.ExitCode != 0 {
			feedback.Issues = append(feedback.Issues, generator.Issue{
				Code: "ANSWER_FAILED", Message: fmt.Sprintf("%s 的 answer.sh 退出码为 %d：%s", answer.Location, answer.ExitCode, strings.TrimSpace(answer.Stderr)),
			})
		}
	}
	for _, checkpoint := range report.Checkpoints {
		if !checkpoint.Passed {
			feedback.Issues = append(feedback.Issues, generator.Issue{
				Code: "CHECKPOINT_FAILED", Message: fmt.Sprintf("检查点 %s 未通过：%s %s", checkpoint.ID, checkpoint.Summary, checkpoint.Details),
			})
		}
	}
	return feedback
}

func (d *DB) FindLatestAuthoringCandidate(ctx context.Context, sessionID string, maximumRevision int64) (*candidate.Revision, error) {
	if strings.TrimSpace(sessionID) == "" || maximumRevision < 0 {
		return nil, errors.New("authoring candidate lookup requires session and revision")
	}
	var candidateID string
	err := d.conn.QueryRowContext(ctx, `SELECT candidate_revision_id FROM authoring_revisions
		WHERE session_id = ? AND revision <= ? AND candidate_revision_id <> '' ORDER BY revision DESC LIMIT 1`,
		sessionID, maximumRevision).Scan(&candidateID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find latest authoring candidate: %w", err)
	}
	return d.GetCandidateRevision(ctx, candidateID)
}

func (d *DB) lockCandidateClaim(ctx context.Context, claim worklist.Claim, kind worklist.Kind, state candidate.State, now time.Time) (*Tx, *candidate.Revision, error) {
	if !claim.Valid() || claim.Item.Kind != kind || claim.Item.SubjectType != worklist.SubjectCandidateRevision || now.IsZero() {
		return nil, nil, errors.New("candidate transition requires a valid matching work claim")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin candidate transition: %w", err)
	}
	var candidateID string
	err = tx.QueryRowContext(ctx, `SELECT subject_id FROM work_items WHERE id = ? AND kind = ? AND subject_type = ?
		AND subject_id = ? AND state = ? AND attempt = ? AND lease_owner = ? AND lease_expires_at > ?
		AND (deadline_at IS NULL OR deadline_at > ?) FOR UPDATE`, claim.Item.ID, kind, worklist.SubjectCandidateRevision,
		claim.Item.SubjectID, worklist.StateRunning, claim.Item.Attempt, claim.LeaseOwner, now.UTC(), now.UTC()).Scan(&candidateID)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return nil, nil, worklist.ErrLeaseLost
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, nil, fmt.Errorf("lock candidate work claim: %w", err)
	}
	revision, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ? AND state = ? FOR UPDATE`, candidateID, state))
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return nil, nil, candidate.ErrInvalidState
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, nil, fmt.Errorf("lock candidate revision: %w", err)
	}
	return tx, revision, nil
}

func completeCandidateWorkItemTx(ctx context.Context, tx *Tx, claim worklist.Claim, state worklist.State, code, summary string, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE work_items SET state = ?, lease_owner = '', lease_expires_at = NULL,
		error_code = ?, error_summary = ?, updated_at = ? WHERE id = ? AND kind = ? AND subject_type = ?
		AND subject_id = ? AND state = ? AND attempt = ? AND lease_owner = ?`,
		state, code, summary, now.UTC(), claim.Item.ID, claim.Item.Kind, worklist.SubjectCandidateRevision,
		claim.Item.SubjectID, worklist.StateRunning, claim.Item.Attempt, claim.LeaseOwner)
	return workItemMutationResult(result, err, "complete candidate work item")
}

func insertCandidateRevisionTx(ctx context.Context, tx *Tx, revision candidate.Revision) error {
	snapshot, err := marshalJSON(revision.Snapshot)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO candidate_revisions
		(id, authoring_session_id, authoring_revision, generator_session_id, generator_run_id, judge_run_id,
		archive_path, archive_sha256, execution_snapshot, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?)`, revision.ID, revision.AuthoringSessionID,
		revision.AuthoringRevision, revision.GeneratorSessionID, revision.GeneratorRunID, revision.JudgeRunID,
		revision.ArchivePath, revision.ArchiveSHA256, snapshot, revision.State, revision.CreatedAt, revision.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert candidate revision: %w", err)
	}
	return nil
}

func candidateWorkItem(candidateID string, kind worklist.Kind, now time.Time) worklist.CreateItem {
	timeout := time.Duration(0)
	if kind != worklist.KindArtifactCleanup {
		timeout = candidate.StageDeadline
	}
	return worklist.CreateItem{
		ID: worklist.NewID("work"), Kind: kind, SubjectType: worklist.SubjectCandidateRevision,
		SubjectID: candidateID, NextRunAt: now.UTC(), ExecutionTimeout: timeout,
	}
}

func stateForWorkKind(kind worklist.Kind) candidate.State {
	switch kind {
	case worklist.KindBuild:
		return candidate.StateBuilding
	case worklist.KindArtifactPublish:
		return candidate.StatePublishingArtifact
	case worklist.KindVerify:
		return candidate.StateVerifying
	case worklist.KindChallengePublish:
		return candidate.StatePublishingChallenge
	default:
		return ""
	}
}

func validStateForWorkKind(kind worklist.Kind, state candidate.State) bool {
	if kind != worklist.KindArtifactCleanup {
		return state == stateForWorkKind(kind)
	}
	switch state {
	case candidate.StatePublished, candidate.StateArtifactFailed, candidate.StateInfrastructureFailed,
		candidate.StateCancelled, candidate.StateSuperseded:
		return true
	default:
		return false
	}
}

func marshalJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode candidate value: %w", err)
	}
	return string(encoded), nil
}

const candidateRevisionColumns = `id, authoring_session_id, authoring_revision, generator_session_id,
	generator_run_id, judge_run_id, archive_path, archive_sha256, execution_snapshot, state, build_output,
	artifact_reference, verify_environment, verification_report, failure, publication, superseded_by, created_at, updated_at,
	verified_at, published_at`

const candidateRevisionSelect = `SELECT ` + candidateRevisionColumns + ` FROM candidate_revisions`

func scanCandidateRevision(row agentRow) (*candidate.Revision, error) {
	var revision candidate.Revision
	var snapshot []byte
	var build, artifact, verifyEnvironment, verification, failure, publication []byte
	var verifiedAt, publishedAt sql.NullTime
	if err := row.Scan(&revision.ID, &revision.AuthoringSessionID, &revision.AuthoringRevision,
		&revision.GeneratorSessionID, &revision.GeneratorRunID, &revision.JudgeRunID, &revision.ArchivePath,
		&revision.ArchiveSHA256, &snapshot, &revision.State, &build, &artifact, &verifyEnvironment, &verification, &failure,
		&publication, &revision.SupersededBy, &revision.CreatedAt, &revision.UpdatedAt, &verifiedAt, &publishedAt); err != nil {
		return nil, err
	}
	if err := unmarshalCandidateJSON(snapshot, &revision.Snapshot, "execution snapshot"); err != nil {
		return nil, err
	}
	for _, optional := range []struct {
		data  []byte
		value any
		name  string
	}{
		{build, &revision.Build, "build output"}, {artifact, &revision.Artifact, "artifact reference"},
		{verifyEnvironment, &revision.VerifyEnvironment, "verification environment"},
		{verification, &revision.Verification, "verification report"}, {failure, &revision.Failure, "failure"},
		{publication, &revision.Publication, "publication"},
	} {
		if len(optional.data) > 0 {
			if err := unmarshalCandidateJSON(optional.data, optional.value, optional.name); err != nil {
				return nil, err
			}
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

func unmarshalCandidateJSON(data []byte, target any, name string) error {
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode candidate %s: %w", name, err)
	}
	return nil
}
