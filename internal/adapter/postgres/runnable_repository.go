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

const runnableMaxAttempts = 5

func (d *RunnableRepository) ScheduleMaterialization(ctx context.Context, spec runnable.RunnableSpec, stateVersion int64, now time.Time) (runnable.ActionIdentity, error) {
	if stateVersion < 1 || now.IsZero() {
		return runnable.ActionIdentity{}, errors.New("schedule runnable materialization is invalid")
	}
	specDigest, err := d.StoreRunnableSpec(ctx, spec, now)
	if err != nil {
		return runnable.ActionIdentity{}, err
	}
	identity := runnable.ActionIdentity{Content: spec.Identity, SpecDigest: specDigest, Phase: runnable.ActionMaterializeArtifact, StateVersion: stateVersion}
	if err := identity.Validate(); err != nil {
		return runnable.ActionIdentity{}, err
	}
	_, err = d.conn.ExecContext(ctx, `INSERT INTO runnable_actions
		(action_key, content_kind, content_id, content_revision, spec_digest, phase, state_version, state, attempt, lease_owner, next_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'queued', 0, '', ?, ?, ?)
		ON CONFLICT (action_key) DO NOTHING`, identity.Key(), identity.Content.Kind, identity.Content.ID, identity.Content.Revision, identity.SpecDigest, identity.Phase, identity.StateVersion, now.UTC(), now.UTC(), now.UTC())
	if err != nil {
		return runnable.ActionIdentity{}, fmt.Errorf("schedule runnable materialization: %w", err)
	}
	return identity, nil
}

func (d *RunnableRepository) ScheduleVerification(ctx context.Context, reference runnable.RevisionReference, stateVersion int64, now time.Time) (runnable.ActionIdentity, error) {
	if err := reference.Validate(); err != nil || stateVersion < 1 || now.IsZero() {
		return runnable.ActionIdentity{}, errors.New("schedule runnable verification is invalid")
	}
	revision, err := d.ResolveRunnableRevision(ctx, reference.ID, reference.Digest)
	if err != nil {
		return runnable.ActionIdentity{}, err
	}
	specDigest, err := revision.Spec.Digest()
	if err != nil {
		return runnable.ActionIdentity{}, err
	}
	identity := runnable.ActionIdentity{Content: revision.Spec.Identity, SpecDigest: specDigest, Phase: runnable.ActionVerify, StateVersion: stateVersion}
	_, err = d.conn.ExecContext(ctx, `INSERT INTO runnable_actions
		(action_key, content_kind, content_id, content_revision, spec_digest, phase, state_version, runnable_revision_digest, state, attempt, lease_owner, next_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'queued', 0, '', ?, ?, ?)
		ON CONFLICT (action_key) DO NOTHING`, identity.Key(), identity.Content.Kind, identity.Content.ID, identity.Content.Revision, identity.SpecDigest, identity.Phase, identity.StateVersion, reference.Digest, now.UTC(), now.UTC(), now.UTC())
	if err != nil {
		return runnable.ActionIdentity{}, fmt.Errorf("schedule runnable verification: %w", err)
	}
	return identity, nil
}

func (d *RunnableRepository) ClaimRunnableAction(ctx context.Context, owner string, ttl time.Duration, now time.Time) (*runnable.ActionContext, error) {
	if strings.TrimSpace(owner) == "" || ttl <= 0 || now.IsZero() {
		return nil, errors.New("runnable action claim is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin runnable action claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var row runnableActionRow
	err = tx.QueryRowContext(ctx, `SELECT action_key, content_kind, content_id, content_revision, spec_digest, phase, state_version, COALESCE(runnable_revision_digest, ''), attempt
		FROM runnable_actions WHERE ((state = 'queued' AND next_run_at <= ?) OR (state = 'running' AND lease_expires_at <= ?)) AND attempt < ?
		ORDER BY next_run_at, created_at, action_key FOR UPDATE SKIP LOCKED LIMIT 1`, now.UTC(), now.UTC(), runnableMaxAttempts).
		Scan(&row.key, &row.identity.Content.Kind, &row.identity.Content.ID, &row.identity.Content.Revision, &row.identity.SpecDigest, &row.identity.Phase, &row.identity.StateVersion, &row.revisionDigest, &row.attempt)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select runnable action: %w", err)
	}
	row.attempt++
	if _, err := tx.ExecContext(ctx, `UPDATE runnable_actions SET state = 'running', attempt = ?, lease_owner = ?, lease_expires_at = ?, updated_at = ? WHERE action_key = ?`, row.attempt, owner, now.UTC().Add(ttl), now.UTC(), row.key); err != nil {
		return nil, fmt.Errorf("claim runnable action: %w", err)
	}
	value, err := runnableActionContextTx(ctx, tx, row, owner)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit runnable action claim: %w", err)
	}
	return &value, nil
}

func (d *RunnableRepository) RenewRunnableAction(ctx context.Context, credential runnable.LeaseCredential, ttl time.Duration, now time.Time) error {
	if err := credential.Validate(); err != nil || ttl <= 0 || now.IsZero() {
		return runnable.ErrReapLeaseLost
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE runnable_actions SET lease_expires_at = ?, updated_at = ? WHERE action_key = ? AND state = 'running' AND lease_owner = ? AND lease_expires_at > ?`, now.UTC().Add(ttl), now.UTC(), credential.Identity.Key(), credential.LeaseOwner, now.UTC())
	if err != nil {
		return fmt.Errorf("renew runnable action: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return runnable.ErrReapLeaseLost
	}
	return nil
}

func (d *RunnableRepository) ReportRunnableActionFailure(ctx context.Context, credential runnable.LeaseCredential, class runnable.FailureClass, code, summary string, now time.Time) error {
	if err := credential.Validate(); err != nil || !class.Valid() || strings.TrimSpace(code) == "" || strings.TrimSpace(summary) == "" || now.IsZero() {
		return errors.New("runnable action failure is invalid")
	}
	state, retryAt := "failed", now.UTC()
	if class == runnable.FailureInfrastructure {
		state, retryAt = "queued", now.UTC().Add(time.Second)
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE runnable_actions SET state = ?, lease_owner = '', lease_expires_at = NULL, next_run_at = ?, failure_class = ?, failure_code = ?, failure_summary = ?, updated_at = ? WHERE action_key = ? AND state = 'running' AND lease_owner = ? AND lease_expires_at > ?`, state, retryAt, class, strings.TrimSpace(code), truncateRunnableDiagnostic(summary), now.UTC(), credential.Identity.Key(), credential.LeaseOwner, now.UTC())
	if err != nil {
		return fmt.Errorf("report runnable action failure: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return runnable.ErrReapLeaseLost
	}
	return nil
}

func (d *RunnableRepository) CompleteRunnableMaterialization(ctx context.Context, credential runnable.LeaseCredential, value runnable.StoredRevision, now time.Time) error {
	if err := credential.Validate(); err != nil || credential.Identity.Phase != runnable.ActionMaterializeArtifact || now.IsZero() {
		return errors.New("complete runnable materialization is invalid")
	}
	if err := value.Validate(); err != nil {
		return err
	}
	specDigest, err := value.Revision.Spec.Digest()
	if err != nil || credential.Identity.SpecDigest != specDigest || credential.Identity.Content != value.Revision.Spec.Identity {
		return errors.New("materialized runnable revision does not match its action")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin complete runnable materialization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var actionSpec string
	if err := tx.QueryRowContext(ctx, `SELECT spec_digest FROM runnable_actions WHERE action_key = ? AND state = 'running' AND lease_owner = ? AND lease_expires_at > ? FOR UPDATE`, credential.Identity.Key(), credential.LeaseOwner, now.UTC()).Scan(&actionSpec); errors.Is(err, sql.ErrNoRows) {
		return runnable.ErrReapLeaseLost
	} else if err != nil {
		return fmt.Errorf("lock runnable materialization action: %w", err)
	}
	if actionSpec != specDigest {
		return errors.New("materialized runnable revision spec does not match its action")
	}
	encoded, err := marshalJSON(value.Revision)
	if err != nil {
		return err
	}
	inserted, err := tx.ExecContext(ctx, `INSERT INTO runnable_revisions (id, runnable_revision_digest, spec_digest, revision, created_at)
		VALUES (?, ?, ?, ?::jsonb, ?) ON CONFLICT (id) DO NOTHING`, value.Reference.ID, value.Reference.Digest, specDigest, encoded, value.CreatedAt.UTC())
	if err != nil {
		return fmt.Errorf("store materialized runnable revision: %w", err)
	}
	if changed, _ := inserted.RowsAffected(); changed == 0 {
		var existing string
		if err := tx.QueryRowContext(ctx, `SELECT runnable_revision_digest FROM runnable_revisions WHERE id = ? FOR UPDATE`, value.Reference.ID).Scan(&existing); err != nil {
			return fmt.Errorf("read stored materialized runnable revision: %w", err)
		}
		if existing != value.Reference.Digest {
			return errors.New("runnable revision id is already bound to another digest")
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE runnable_actions SET state = 'completed', lease_owner = '', lease_expires_at = NULL, completed_at = ?, updated_at = ?
		WHERE action_key = ? AND state = 'running' AND lease_owner = ? AND lease_expires_at > ?`, now.UTC(), now.UTC(), credential.Identity.Key(), credential.LeaseOwner, now.UTC())
	if err != nil {
		return fmt.Errorf("complete runnable materialization: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return runnable.ErrReapLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit runnable materialization: %w", err)
	}
	return nil
}

func (d *RunnableRepository) CompleteRunnableVerification(ctx context.Context, credential runnable.LeaseCredential, value runnable.StoredVerificationReport, now time.Time) error {
	if err := credential.Validate(); err != nil || credential.Identity.Phase != runnable.ActionVerify || now.IsZero() {
		return errors.New("complete runnable verification is invalid")
	}
	if err := value.Validate(); err != nil {
		return err
	}
	revisionDigest, err := value.RunnableRevision.Digest()
	if err != nil || credential.Identity.SpecDigest != value.RunnableRevision.Artifact.BuiltFromSpecDigest || credential.Identity.Content != value.RunnableRevision.Spec.Identity {
		return errors.New("verification report does not match its action")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin complete runnable verification: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var actionRevision string
	if err := tx.QueryRowContext(ctx, `SELECT runnable_revision_digest FROM runnable_actions WHERE action_key = ? AND state = 'running' AND lease_owner = ? AND lease_expires_at > ? FOR UPDATE`, credential.Identity.Key(), credential.LeaseOwner, now.UTC()).Scan(&actionRevision); errors.Is(err, sql.ErrNoRows) {
		return runnable.ErrReapLeaseLost
	} else if err != nil {
		return fmt.Errorf("lock runnable verification action: %w", err)
	}
	if actionRevision != revisionDigest {
		return errors.New("verification report revision does not match its action")
	}
	encoded, err := marshalJSON(value.Report)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO runnable_verification_reports (id, verification_report_digest, runnable_revision_digest, report, created_at)
		VALUES (?, ?, ?, ?::jsonb, ?) ON CONFLICT (id) DO NOTHING`, value.Reference.ID, value.Reference.Digest, revisionDigest, encoded, value.CreatedAt.UTC()); err != nil {
		return fmt.Errorf("store runnable verification report: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runnable_actions SET state = 'completed', lease_owner = '', lease_expires_at = NULL, completed_at = ?, updated_at = ? WHERE action_key = ?`, now.UTC(), now.UTC(), credential.Identity.Key()); err != nil {
		return fmt.Errorf("complete runnable verification: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit runnable verification: %w", err)
	}
	return nil
}

type runnableActionRow struct {
	key            string
	identity       runnable.ActionIdentity
	revisionDigest string
	attempt        int
}

func runnableActionContextTx(ctx context.Context, tx *Tx, row runnableActionRow, owner string) (runnable.ActionContext, error) {
	credential := runnable.LeaseCredential{Identity: row.identity, LeaseOwner: owner}
	if row.identity.Phase == runnable.ActionMaterializeArtifact {
		var encoded []byte
		if err := tx.QueryRowContext(ctx, `SELECT spec FROM runnable_specs WHERE spec_digest = ?`, row.identity.SpecDigest).Scan(&encoded); err != nil {
			return runnable.ActionContext{}, fmt.Errorf("load runnable action spec: %w", err)
		}
		var spec runnable.RunnableSpec
		if err := json.Unmarshal(encoded, &spec); err != nil {
			return runnable.ActionContext{}, fmt.Errorf("decode runnable action spec: %w", err)
		}
		value := runnable.ActionContext{Credential: credential, Spec: &spec}
		return value, value.Validate()
	}
	if row.identity.Phase == runnable.ActionVerify {
		var encoded []byte
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM runnable_revisions WHERE runnable_revision_digest = ?`, row.revisionDigest).Scan(&encoded); err != nil {
			return runnable.ActionContext{}, fmt.Errorf("load runnable action revision: %w", err)
		}
		var revision runnable.RunnableRevision
		if err := json.Unmarshal(encoded, &revision); err != nil {
			return runnable.ActionContext{}, fmt.Errorf("decode runnable action revision: %w", err)
		}
		value := runnable.ActionContext{Credential: credential, RunnableRevision: &revision, RunnableRevisionDigest: row.revisionDigest}
		return value, value.Validate()
	}
	return runnable.ActionContext{}, errors.New("stored runnable action has an unsupported phase")
}

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
