package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

var (
	ErrRunnableRevisionNotFound   = errors.New("runnable revision not found")
	ErrVerificationReportNotFound = errors.New("runnable verification report not found")
)

// StoreRunnableSpec records a canonical immutable pre-build contract. The
// digest is the primary key, so equal specs are naturally idempotent.
func (d *RunnableRepository) StoreRunnableSpec(ctx context.Context, spec runnable.RunnableSpec, now time.Time) (string, error) {
	if now.IsZero() {
		return "", errors.New("store runnable spec requires a creation time")
	}
	digest, err := spec.Digest()
	if err != nil {
		return "", err
	}
	encoded, err := marshalJSON(spec)
	if err != nil {
		return "", fmt.Errorf("encode runnable spec: %w", err)
	}
	_, err = d.conn.ExecContext(ctx, `INSERT INTO runnable_specs
		(spec_digest, content_kind, content_id, content_revision, source_digest, spec, created_at)
		VALUES (?, ?, ?, ?, ?, ?::jsonb, ?)
		ON CONFLICT (spec_digest) DO NOTHING`,
		digest, spec.Identity.Kind, spec.Identity.ID, spec.Identity.Revision, spec.Source.Digest, encoded, now.UTC())
	if err != nil {
		return "", fmt.Errorf("store runnable spec: %w", err)
	}
	return digest, nil
}

// StoreRunnableRevision persists an already complete immutable revision. It
// always saves its embedded spec first and never creates an artifact table.
func (d *RunnableRepository) StoreRunnableRevision(ctx context.Context, value runnable.StoredRevision) error {
	if err := value.Validate(); err != nil {
		return err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin store runnable revision: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	specDigest, err := insertRunnableSpecTx(ctx, tx, value.Revision.Spec, value.CreatedAt)
	if err != nil {
		return err
	}
	encoded, err := marshalJSON(value.Revision)
	if err != nil {
		return fmt.Errorf("encode runnable revision: %w", err)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO runnable_revisions
		(id, runnable_revision_digest, spec_digest, revision, created_at)
		VALUES (?, ?, ?, ?::jsonb, ?)
		ON CONFLICT (id) DO NOTHING`, value.Reference.ID, value.Reference.Digest, specDigest, encoded, value.CreatedAt.UTC())
	if err != nil {
		return fmt.Errorf("store runnable revision: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		var existing string
		if err := tx.QueryRowContext(ctx, `SELECT runnable_revision_digest FROM runnable_revisions WHERE id = ? FOR UPDATE`, value.Reference.ID).Scan(&existing); err != nil {
			return fmt.Errorf("read stored runnable revision: %w", err)
		}
		if existing != value.Reference.Digest {
			return errors.New("runnable revision id is already bound to another digest")
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit runnable revision: %w", err)
	}
	return nil
}

func insertRunnableSpecTx(ctx context.Context, tx *Tx, spec runnable.RunnableSpec, now time.Time) (string, error) {
	digest, err := spec.Digest()
	if err != nil {
		return "", err
	}
	encoded, err := marshalJSON(spec)
	if err != nil {
		return "", fmt.Errorf("encode runnable spec: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runnable_specs
		(spec_digest, content_kind, content_id, content_revision, source_digest, spec, created_at)
		VALUES (?, ?, ?, ?, ?, ?::jsonb, ?)
		ON CONFLICT (spec_digest) DO NOTHING`,
		digest, spec.Identity.Kind, spec.Identity.ID, spec.Identity.Revision, spec.Source.Digest, encoded, now.UTC()); err != nil {
		return "", fmt.Errorf("store runnable spec: %w", err)
	}
	return digest, nil
}

// ResolveRunnableRevision implements runtimeenvironment.RevisionResolver
// without importing the controller package. Both the storage ID and digest
// are checked before the complete revision is returned.
func (d *RunnableRepository) ResolveRunnableRevision(ctx context.Context, id, digest string) (runnable.RunnableRevision, error) {
	if strings.TrimSpace(id) == "" || !runnable.ValidDigest(digest) {
		return runnable.RunnableRevision{}, errors.New("runnable revision reference is invalid")
	}
	var encoded []byte
	if err := d.conn.QueryRowContext(ctx, `SELECT revision FROM runnable_revisions
		WHERE id = ? AND runnable_revision_digest = ?`, id, digest).Scan(&encoded); errors.Is(err, sql.ErrNoRows) {
		return runnable.RunnableRevision{}, ErrRunnableRevisionNotFound
	} else if err != nil {
		return runnable.RunnableRevision{}, fmt.Errorf("resolve runnable revision: %w", err)
	}
	var revision runnable.RunnableRevision
	if err := json.Unmarshal(encoded, &revision); err != nil {
		return runnable.RunnableRevision{}, fmt.Errorf("decode runnable revision: %w", err)
	}
	computed, err := revision.Digest()
	if err != nil {
		return runnable.RunnableRevision{}, fmt.Errorf("validate stored runnable revision: %w", err)
	}
	if computed != digest {
		return runnable.RunnableRevision{}, errors.New("stored runnable revision digest does not match its value")
	}
	return revision, nil
}

func (d *RunnableRepository) StoreVerificationReport(ctx context.Context, value runnable.StoredVerificationReport) error {
	if err := value.Validate(); err != nil {
		return err
	}
	revisionDigest, err := value.RunnableRevision.Digest()
	if err != nil {
		return err
	}
	encoded, err := marshalJSON(value.Report)
	if err != nil {
		return fmt.Errorf("encode verification report: %w", err)
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin store verification report: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM runnable_revisions WHERE runnable_revision_digest = ?)`, revisionDigest).Scan(&exists); err != nil {
		return fmt.Errorf("find runnable revision for verification report: %w", err)
	}
	if !exists {
		return ErrRunnableRevisionNotFound
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO runnable_verification_reports
		(id, verification_report_digest, runnable_revision_digest, report, created_at)
		VALUES (?, ?, ?, ?::jsonb, ?)
		ON CONFLICT (id) DO NOTHING`, value.Reference.ID, value.Reference.Digest, revisionDigest, encoded, value.CreatedAt.UTC())
	if err != nil {
		return fmt.Errorf("store verification report: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		var existing string
		if err := tx.QueryRowContext(ctx, `SELECT verification_report_digest FROM runnable_verification_reports WHERE id = ? FOR UPDATE`, value.Reference.ID).Scan(&existing); err != nil {
			return fmt.Errorf("read stored verification report: %w", err)
		}
		if existing != value.Reference.Digest {
			return errors.New("verification report id is already bound to another digest")
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit verification report: %w", err)
	}
	return nil
}

func (d *RunnableRepository) ResolveVerificationReport(ctx context.Context, id, digest string, revision runnable.RunnableRevision) (runnable.VerificationReport, error) {
	if strings.TrimSpace(id) == "" || !runnable.ValidDigest(digest) || revision.Validate() != nil {
		return runnable.VerificationReport{}, errors.New("verification report reference is invalid")
	}
	revisionDigest, err := revision.Digest()
	if err != nil {
		return runnable.VerificationReport{}, err
	}
	var encoded []byte
	if err := d.conn.QueryRowContext(ctx, `SELECT report FROM runnable_verification_reports
		WHERE id = ? AND verification_report_digest = ? AND runnable_revision_digest = ?`, id, digest, revisionDigest).Scan(&encoded); errors.Is(err, sql.ErrNoRows) {
		return runnable.VerificationReport{}, ErrVerificationReportNotFound
	} else if err != nil {
		return runnable.VerificationReport{}, fmt.Errorf("resolve verification report: %w", err)
	}
	var report runnable.VerificationReport
	if err := json.Unmarshal(encoded, &report); err != nil {
		return runnable.VerificationReport{}, fmt.Errorf("decode verification report: %w", err)
	}
	computed, err := report.Digest(revision)
	if err != nil {
		return runnable.VerificationReport{}, fmt.Errorf("validate stored verification report: %w", err)
	}
	if computed != digest {
		return runnable.VerificationReport{}, errors.New("stored verification report digest does not match its value")
	}
	return report, nil
}

func (d *RunnableRepository) Enqueue(ctx context.Context, request runnable.ReapRequest) error {
	if err := request.Valid(); err != nil {
		return err
	}
	encoded, err := marshalJSON(request)
	if err != nil {
		return fmt.Errorf("encode runnable reap request: %w", err)
	}
	now := time.Now().UTC()
	result, err := d.conn.ExecContext(ctx, `INSERT INTO runnable_reaps
		(reap_key, runnable_revision_digest, reap_request, state, attempt, lease_owner, next_attempt_at, last_error, created_at, updated_at)
		VALUES (?, ?, ?::jsonb, ?, 0, '', ?, '', ?, ?)
		ON CONFLICT (reap_key) DO NOTHING`, request.Key(), request.Revision, encoded, runnable.ReapQueued, now, now, now)
	if err != nil {
		return fmt.Errorf("enqueue runnable reap: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 0 {
		return nil
	}
	record, err := d.Get(ctx, request.Key())
	if err != nil {
		return err
	}
	if record.Request.Revision != request.Revision {
		return errors.New("runnable reap request fence differs from existing request")
	}
	return nil
}

func (d *RunnableRepository) Get(ctx context.Context, key string) (runnable.ReapRecord, error) {
	if strings.TrimSpace(key) == "" {
		return runnable.ReapRecord{}, runnable.ErrReapNotFound
	}
	record, err := scanRunnableReap(d.conn.QueryRowContext(ctx, runnableReapSelect+` WHERE reap_key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return runnable.ReapRecord{}, runnable.ErrReapNotFound
	}
	if err != nil {
		return runnable.ReapRecord{}, fmt.Errorf("get runnable reap: %w", err)
	}
	return record, nil
}

func (d *RunnableRepository) Claim(ctx context.Context, owner string, ttl time.Duration, now time.Time) (*runnable.ReapClaim, error) {
	if strings.TrimSpace(owner) == "" || ttl <= 0 || now.IsZero() {
		return nil, errors.New("runnable reap claim is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin runnable reap claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	record, err := scanRunnableReap(tx.QueryRowContext(ctx, runnableReapSelect+`
		WHERE ((state = ? AND next_attempt_at <= ?) OR (state = ? AND lease_expires_at <= ?))
		ORDER BY next_attempt_at, created_at, reap_key FOR UPDATE SKIP LOCKED LIMIT 1`, runnable.ReapQueued, now.UTC(), runnable.ReapClaimed, now.UTC()))
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty runnable reap claim: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select runnable reap claim: %w", err)
	}
	record.State = runnable.ReapClaimed
	record.Attempt++
	record.LeaseOwner = strings.TrimSpace(owner)
	record.LeaseExpires = now.UTC().Add(ttl)
	result, err := tx.ExecContext(ctx, `UPDATE runnable_reaps SET state = ?, attempt = ?, lease_owner = ?, lease_expires_at = ?, updated_at = ?
		WHERE reap_key = ?`, record.State, record.Attempt, record.LeaseOwner, record.LeaseExpires, now.UTC(), record.Request.Key())
	if err != nil {
		return nil, fmt.Errorf("claim runnable reap: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, runnable.ErrReapLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit runnable reap claim: %w", err)
	}
	return &runnable.ReapClaim{Record: record}, nil
}

func (d *RunnableRepository) Complete(ctx context.Context, claim runnable.ReapClaim, succeeded bool, diagnostic string, now, retryAt time.Time) error {
	if err := claim.Record.Request.Valid(); err != nil || now.IsZero() || !now.Before(claim.Record.LeaseExpires) {
		return runnable.ErrReapLeaseLost
	}
	if succeeded {
		result, err := d.conn.ExecContext(ctx, `UPDATE runnable_reaps SET state = ?, lease_owner = '', lease_expires_at = NULL,
			last_error = '', completed_at = ?, updated_at = ?
			WHERE reap_key = ? AND state = ? AND attempt = ? AND lease_owner = ? AND lease_expires_at = ? AND runnable_revision_digest = ? AND lease_expires_at > ?`,
			runnable.ReapSucceeded, now.UTC(), now.UTC(), claim.Record.Request.Key(), runnable.ReapClaimed, claim.Record.Attempt, claim.Record.LeaseOwner, claim.Record.LeaseExpires.UTC(), claim.Record.Request.Revision, now.UTC())
		if err != nil {
			return fmt.Errorf("complete runnable reap: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return runnable.ErrReapLeaseLost
		}
		return nil
	}
	if retryAt.IsZero() {
		return errors.New("runnable reap retry requires a next attempt time")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE runnable_reaps SET state = ?, lease_owner = '', lease_expires_at = NULL,
		next_attempt_at = ?, last_error = ?, updated_at = ?
		WHERE reap_key = ? AND state = ? AND attempt = ? AND lease_owner = ? AND lease_expires_at = ? AND runnable_revision_digest = ? AND lease_expires_at > ?`,
		runnable.ReapQueued, retryAt.UTC(), truncateRunnableDiagnostic(diagnostic), now.UTC(), claim.Record.Request.Key(), runnable.ReapClaimed, claim.Record.Attempt, claim.Record.LeaseOwner, claim.Record.LeaseExpires.UTC(), claim.Record.Request.Revision, now.UTC())
	if err != nil {
		return fmt.Errorf("retry runnable reap: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return runnable.ErrReapLeaseLost
	}
	return nil
}

const runnableReapColumns = `reap_key, runnable_revision_digest, reap_request, state, attempt, lease_owner, lease_expires_at,
	next_attempt_at, last_error, created_at, updated_at, completed_at`
const runnableReapSelect = `SELECT ` + runnableReapColumns + ` FROM runnable_reaps`

func scanRunnableReap(row agentRow) (runnable.ReapRecord, error) {
	var key, digest string
	var request []byte
	var state runnable.ReapState
	var expires, completed sql.NullTime
	var record runnable.ReapRecord
	var created, updated time.Time
	err := row.Scan(&key, &digest, &request, &state, &record.Attempt, &record.LeaseOwner, &expires, &record.NextAttemptAt, &record.LastError, &created, &updated, &completed)
	if err != nil {
		return runnable.ReapRecord{}, err
	}
	if err := json.Unmarshal(request, &record.Request); err != nil {
		return runnable.ReapRecord{}, fmt.Errorf("decode runnable reap request: %w", err)
	}
	if record.Request.Key() != key || record.Request.Revision != digest || (state != runnable.ReapQueued && state != runnable.ReapClaimed && state != runnable.ReapSucceeded) {
		return runnable.ReapRecord{}, errors.New("stored runnable reap is inconsistent")
	}
	if err := record.Request.Valid(); err != nil {
		return runnable.ReapRecord{}, fmt.Errorf("validate stored runnable reap: %w", err)
	}
	record.State = state
	record.NextAttemptAt = record.NextAttemptAt.UTC()
	if expires.Valid {
		record.LeaseExpires = expires.Time.UTC()
	}
	if completed.Valid {
		value := completed.Time.UTC()
		record.CompletedAt = &value
	}
	return record, nil
}

func truncateRunnableDiagnostic(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > runnable.MaxSummaryLength {
		return value[:runnable.MaxSummaryLength]
	}
	return value
}
