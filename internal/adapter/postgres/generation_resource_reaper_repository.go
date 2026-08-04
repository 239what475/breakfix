package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/generation"
)

const (
	resourceReapPending   = "pending"
	resourceReapRunning   = "running"
	resourceReapCompleted = "completed"
)

// ClaimGenerationResourceReap discovers due work from durable candidate
// records, then gives exactly one Server or Generate Worker an expiring lease.
// It is intentionally independent from GenerationWorkflow state transitions.
func (d *GenerationRepository) ClaimGenerationResourceReap(ctx context.Context, kind generation.ResourceReapKind, owner string, leaseTTL time.Duration, now time.Time) (*generation.ResourceReapClaim, error) {
	if !kind.Valid() || strings.TrimSpace(owner) == "" || leaseTTL <= 0 || now.IsZero() {
		return nil, errors.New("generation resource reap claim is invalid")
	}
	if err := d.EnsureGenerationResourceReaps(ctx, now.UTC()); err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin generation resource reap claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var candidateID string
	err = tx.QueryRowContext(ctx, `SELECT candidate_revision_id FROM generation_resource_reaps
		WHERE kind = ? AND state IN (?, ?) AND next_run_at <= ?
		AND (lease_expires_at IS NULL OR lease_expires_at <= ?)
		ORDER BY next_run_at, created_at, candidate_revision_id
		FOR UPDATE SKIP LOCKED LIMIT 1`, kind, resourceReapPending, resourceReapRunning, now.UTC(), now.UTC()).Scan(&candidateID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty generation resource reap claim: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select generation resource reap: %w", err)
	}
	leaseOwner := strings.TrimSpace(owner) + "-" + generation.NewID("resource-reap")
	var attempt int
	var deleteFinal bool
	if err := tx.QueryRowContext(ctx, `UPDATE generation_resource_reaps SET state = ?, lease_owner = ?, lease_expires_at = ?, updated_at = ?
		WHERE candidate_revision_id = ? AND kind = ?
		RETURNING attempt, delete_final_artifact`, resourceReapRunning, leaseOwner, now.UTC().Add(leaseTTL), now.UTC(), candidateID, kind).Scan(&attempt, &deleteFinal); err != nil {
		return nil, fmt.Errorf("claim generation resource reap: %w", err)
	}
	candidate, err := scanCandidateRevision(tx.QueryRowContext(ctx, candidateRevisionSelect+` WHERE id = ?`, candidateID))
	if err != nil {
		return nil, fmt.Errorf("read generation resource reap candidate: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit generation resource reap claim: %w", err)
	}
	return &generation.ResourceReapClaim{
		ResourceReap: generation.ResourceReap{
			CandidateRevisionID: candidateID,
			Kind:                kind,
			DeleteFinalArtifact: deleteFinal,
			Candidate:           candidate.WorkerView(),
		},
		ResourceReapCredential: generation.ResourceReapCredential{Attempt: attempt, LeaseOwner: leaseOwner},
	}, nil
}

// CompleteGenerationResourceReap finalizes one fenced infrastructure action.
// Failures remain pending with bounded backoff and never change workflow state.
func (d *GenerationRepository) CompleteGenerationResourceReap(ctx context.Context, claim generation.ResourceReapClaim, failure string, now time.Time) error {
	if claim.Valid() != nil || now.IsZero() {
		return errors.New("generation resource reap completion is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin generation resource reap completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if strings.TrimSpace(failure) == "" {
		result, err := tx.ExecContext(ctx, `UPDATE generation_resource_reaps SET state = ?, lease_owner = '', lease_expires_at = NULL,
			last_error = '', completed_at = ?, updated_at = ?
			WHERE candidate_revision_id = ? AND kind = ? AND state = ? AND attempt = ? AND lease_owner = ?`,
			resourceReapCompleted, now.UTC(), now.UTC(), claim.CandidateRevisionID, claim.Kind, resourceReapRunning, claim.Attempt, claim.LeaseOwner)
		if err != nil {
			return fmt.Errorf("complete generation resource reap: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return generation.ErrLeaseLost
		}
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE generation_resource_reaps SET state = ?, attempt = attempt + 1, lease_owner = '', lease_expires_at = NULL,
			next_run_at = ?, last_error = ?, updated_at = ?
			WHERE candidate_revision_id = ? AND kind = ? AND state = ? AND attempt = ? AND lease_owner = ?`,
			resourceReapPending, generation.NextRetry(claim.Attempt+1, now.UTC()), strings.TrimSpace(failure), now.UTC(),
			claim.CandidateRevisionID, claim.Kind, resourceReapRunning, claim.Attempt, claim.LeaseOwner)
		if err != nil {
			return fmt.Errorf("retry generation resource reap: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return generation.ErrLeaseLost
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit generation resource reap completion: %w", err)
	}
	return nil
}

// EnsureGenerationResourceReaps derives cleanup work from immutable candidate
// outputs. A candidate still referenced by a nonterminal workflow is never
// reaped, while its verification Environment may be reaped after reporting.
func (d *GenerationRepository) EnsureGenerationResourceReaps(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		return errors.New("generation resource reap discovery requires current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin generation resource reap discovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// A candidate stays live while the active workflow still needs its archive
	// or runtime artifact. Generating intentionally does not retain that
	// dependency: it is either the first candidate or a repair of a prior one.
	inactive := `NOT EXISTS (
		SELECT 1 FROM generation_workflows workflow
		WHERE workflow.candidate_revision_id = candidate_revisions.id
		AND workflow.state NOT IN ('Generating', 'Published', 'Failed', 'Cancelled', 'Superseded')
	)`
	queries := []struct {
		kind  generation.ResourceReapKind
		where string
	}{
		{generation.ResourceReapVerificationEnvironment, `verify_environment IS NOT NULL AND (verification_report IS NOT NULL OR ` + inactive + `)`},
		{generation.ResourceReapBuildArchive, `build_output IS NOT NULL AND execution_snapshot->>'runtime' = 'k8s' AND ` + inactive},
		{generation.ResourceReapNodeBuildImage, `build_output IS NOT NULL AND execution_snapshot->>'runtime' = 'node' AND ` + inactive},
		{generation.ResourceReapCandidateArtifact, `(artifact_reference IS NOT NULL OR build_output IS NOT NULL OR publication IS NOT NULL) AND ` + inactive},
	}
	for _, query := range queries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO generation_resource_reaps
			(candidate_revision_id, kind, delete_final_artifact, state, attempt, lease_owner, next_run_at, last_error, created_at, updated_at)
			SELECT id, ?, publication IS NOT NULL AND published_at IS NULL, ?, 0, '', ?, '', ?, ?
			FROM candidate_revisions WHERE `+query.where+`
			ON CONFLICT (candidate_revision_id, kind) DO UPDATE
			SET delete_final_artifact = generation_resource_reaps.delete_final_artifact OR EXCLUDED.delete_final_artifact`,
			query.kind, resourceReapPending, now.UTC(), now.UTC(), now.UTC()); err != nil {
			return fmt.Errorf("discover %s generation resource reaps: %w", query.kind, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit generation resource reap discovery: %w", err)
	}
	return nil
}
