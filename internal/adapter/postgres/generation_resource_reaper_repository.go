package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/generation"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
)

const (
	resourceReapPending   = "pending"
	resourceReapRunning   = "running"
	resourceReapCompleted = "completed"
)

// ClaimGenerationResourceReap discovers due work from durable candidate
// records, then gives exactly one Runtime Worker an expiring lease.
// It is intentionally independent from GenerationWorkflow state transitions.
func (d *GenerationRepository) ClaimGenerationResourceReap(ctx context.Context, owner string, leaseTTL time.Duration, now time.Time) (*runtime.ReapClaim, error) {
	if strings.TrimSpace(owner) == "" || leaseTTL <= 0 || now.IsZero() {
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
	var kind runtime.ReapKind
	err = tx.QueryRowContext(ctx, `SELECT candidate_revision_id, kind FROM generation_resource_reaps
		WHERE state IN (?, ?) AND next_run_at <= ?
		AND (lease_expires_at IS NULL OR lease_expires_at <= ?)
		ORDER BY next_run_at, CASE kind WHEN 'verification-environment' THEN 0 WHEN 'node-build-image' THEN 1 ELSE 2 END, created_at, candidate_revision_id
		FOR UPDATE SKIP LOCKED LIMIT 1`, resourceReapPending, resourceReapRunning, now.UTC(), now.UTC()).Scan(&candidateID, &kind)
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
	reap := runtime.Reap{
		Scope: runtime.ScopeGenerationWorkflow, ResourceID: candidateID, Kind: kind, DeleteFinalArtifact: deleteFinal,
		Snapshot: candidate.Snapshot, Build: candidate.Build, Artifact: candidate.Artifact,
		VerificationEnvironment: candidate.VerifyEnvironment,
	}
	if candidate.Publication != nil {
		reap.ChallengeID = candidate.Publication.ChallengeID
		reap.FinalArtifact = candidate.Publication.Artifact
	}
	if err := reap.Valid(); err != nil {
		return nil, fmt.Errorf("load generation resource reap: %w", err)
	}
	return &runtime.ReapClaim{
		Reap:           reap,
		ReapCredential: runtime.ReapCredential{Attempt: attempt, LeaseOwner: leaseOwner},
	}, nil
}

// CompleteGenerationResourceReap finalizes one fenced infrastructure action.
// Failures remain pending with bounded backoff and never change workflow state.
func (d *GenerationRepository) CompleteGenerationResourceReap(ctx context.Context, claim runtime.ReapClaim, failure string, now time.Time) error {
	if claim.Valid() != nil || claim.Scope != runtime.ScopeGenerationWorkflow || now.IsZero() {
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
			resourceReapCompleted, now.UTC(), now.UTC(), claim.ResourceID, claim.Kind, resourceReapRunning, claim.Attempt, claim.LeaseOwner)
		if err != nil {
			return fmt.Errorf("complete generation resource reap: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return runtime.ErrLeaseLost
		}
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE generation_resource_reaps SET state = ?, attempt = attempt + 1, lease_owner = '', lease_expires_at = NULL,
			next_run_at = ?, last_error = ?, updated_at = ?
			WHERE candidate_revision_id = ? AND kind = ? AND state = ? AND attempt = ? AND lease_owner = ?`,
			resourceReapPending, generation.NextRetry(claim.Attempt+1, now.UTC()), strings.TrimSpace(failure), now.UTC(),
			claim.ResourceID, claim.Kind, resourceReapRunning, claim.Attempt, claim.LeaseOwner)
		if err != nil {
			return fmt.Errorf("retry generation resource reap: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return runtime.ErrLeaseLost
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
		AND workflow.state NOT IN ('Generating', 'Published', 'Failed', 'Cancelled')
	)`
	queries := []struct {
		kind  runtime.ReapKind
		where string
	}{
		{runtime.ReapVerificationEnvironment, `verify_environment IS NOT NULL AND (verification_report IS NOT NULL OR ` + inactive + `)`},
		{runtime.ReapNodeBuildImage, `build_output IS NOT NULL AND execution_snapshot->>'runtime' = 'node' AND ` + inactive},
		{runtime.ReapCandidateArtifact, `(artifact_reference IS NOT NULL OR build_output IS NOT NULL OR publication IS NOT NULL) AND ` + inactive},
	}
	for _, query := range queries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO generation_resource_reaps
			(candidate_revision_id, kind, delete_final_artifact, state, attempt, lease_owner, next_run_at, last_error, created_at, updated_at)
			SELECT id, ?, publication -> 'artifact' IS NOT NULL AND published_at IS NULL, ?, 0, '', ?, '', ?, ?
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
