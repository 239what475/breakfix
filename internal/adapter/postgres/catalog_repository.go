package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
)

var (
	ErrCatalogReleaseNotFound = catalogdomain.ErrReleaseNotFound
	ErrCatalogEntryNotFound   = errors.New("catalog release entry not found")
	ErrCatalogCommitNotFound  = errors.New("catalog release entry commit not found")
	ErrCatalogLeaseLost       = errors.New("catalog release lease was lost")
)

const catalogReleaseColumns = `id, name, version, bundle_digest, source_digest, state, deadline_at, commit_id,
	lease_owner, lease_expires_at, last_error, created_at, updated_at`
const catalogReleaseSelect = `SELECT ` + catalogReleaseColumns + ` FROM catalog_releases`

const catalogEntryColumns = `id, release_id, source_path, source_ref, title, content_revision, archive_sha256,
	execution_snapshot, state, attempt, lease_owner, lease_expires_at, next_run_at, build_output, artifact_reference,
	verify_environment, verification_report, last_error, created_at, updated_at`
const catalogEntrySelect = `SELECT ` + catalogEntryColumns + ` FROM catalog_release_entries`

const catalogCommitColumns = `id, release_id, entry_id, challenge_id, source_slug, state, artifact_reference,
	materialized_at, committed_at, created_at, updated_at`
const catalogCommitSelect = `SELECT ` + catalogCommitColumns + ` FROM catalog_release_entry_commits`

type scanner interface{ Scan(...any) error }

// CreateOrGetRelease reserves a durable Pending release before any OCI pull
// or source parsing begins. A bundle digest is the idempotency key, so retries
// and Server replicas never allocate a second release identity.
func (d *CatalogRepository) CreateOrGetRelease(ctx context.Context, release catalogdomain.Release) (*catalogdomain.Release, bool, error) {
	if release.State != catalogdomain.ReleasePending || !release.Valid() {
		return nil, false, errors.New("catalog release creation requires a valid pending release")
	}
	if release.ID != catalogdomain.ReleaseIDForBundle(release.BundleDigest) {
		return nil, false, errors.New("catalog release creation has an invalid identity")
	}

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin catalog release creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE bundle_digest = ? FOR UPDATE`, release.BundleDigest))
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("commit existing catalog release: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("read catalog release by digest: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_releases
		(id, name, version, bundle_digest, source_digest, state, deadline_at, commit_id, lease_owner, lease_expires_at, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', '', NULL, '', ?, ?)`,
		release.ID, release.Name, release.Version, release.BundleDigest, release.SourceDigest, release.State, release.DeadlineAt.UTC(), release.CreatedAt.UTC(), release.UpdatedAt.UTC()); err != nil {
		return nil, false, fmt.Errorf("insert catalog release: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit catalog release creation: %w", err)
	}
	return &release, true, nil
}

// InitializeRelease atomically records the immutable staged source metadata,
// its entries, and the transition into Installing. The source is already
// validated by the application layer; this repository still verifies every
// durable identity before making work claimable.
func (d *CatalogRepository) InitializeRelease(ctx context.Context, release catalogdomain.Release, entries []catalogdomain.Entry, now time.Time) (*catalogdomain.Release, error) {
	if release.State != catalogdomain.ReleasePending || !release.Valid() || strings.TrimSpace(release.Name) == "" || strings.TrimSpace(release.Version) == "" || !release.SourceDigest.Valid() || now.IsZero() {
		return nil, errors.New("catalog release initialization is invalid")
	}
	seenPath := make(map[string]struct{}, len(entries))
	seenRef := make(map[string]struct{}, len(entries))
	for index := range entries {
		entry := entries[index]
		if entry.ID != catalogdomain.EntryIDFor(release.ID, entry.SourcePath) || entry.ReleaseID != release.ID || entry.State != catalogdomain.EntryPending || !entry.Valid() {
			return nil, fmt.Errorf("catalog entry %d is invalid", index+1)
		}
		if _, exists := seenPath[entry.SourcePath]; exists {
			return nil, fmt.Errorf("duplicate catalog entry path %q", entry.SourcePath)
		}
		if _, exists := seenRef[entry.SourceRef]; exists {
			return nil, fmt.Errorf("duplicate catalog entry source_ref %q", entry.SourceRef)
		}
		seenPath[entry.SourcePath] = struct{}{}
		seenRef[entry.SourceRef] = struct{}{}
	}

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin catalog release initialization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, release.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, err
	}
	if current.State != catalogdomain.ReleasePending {
		if current.State.Terminal() {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return current, nil
		}
		if current.Name != release.Name || current.Version != release.Version || current.SourceDigest != release.SourceDigest {
			return nil, errors.New("catalog release source metadata conflicts with its durable identity")
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return current, nil
	}
	for _, entry := range entries {
		snapshot, err := marshalJSON(entry.Snapshot)
		if err != nil {
			return nil, fmt.Errorf("encode catalog entry %q snapshot: %w", entry.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_release_entries
			(id, release_id, source_path, source_ref, title, content_revision, archive_sha256, execution_snapshot, state,
			attempt, lease_owner, lease_expires_at, next_run_at, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, 0, '', NULL, ?, '', ?, ?)`,
			entry.ID, entry.ReleaseID, entry.SourcePath, entry.SourceRef, entry.Title, entry.ContentRevision, entry.ArchiveSHA256,
			snapshot, entry.State, entry.NextRunAt.UTC(), entry.CreatedAt.UTC(), entry.UpdatedAt.UTC()); err != nil {
			return nil, fmt.Errorf("insert catalog entry %q: %w", entry.ID, err)
		}
	}
	updated, err := scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases SET name = ?, version = ?, source_digest = ?, state = ?, last_error = '', updated_at = ?
		WHERE id = ? AND state = ? RETURNING `+catalogReleaseColumns,
		release.Name, release.Version, release.SourceDigest, catalogdomain.ReleaseInstalling, now.UTC(), release.ID, catalogdomain.ReleasePending))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogLeaseLost
	}
	if err != nil {
		return nil, fmt.Errorf("initialize catalog release: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit catalog release initialization: %w", err)
	}
	return updated, nil
}

// FailPendingRelease records a deterministic OCI/source contract error before
// any entry is claimable. Provider errors intentionally leave Pending for a
// later retry until the release deadline expires.
func (d *CatalogRepository) FailPendingRelease(ctx context.Context, releaseID, summary string, now time.Time) (*catalogdomain.Release, error) {
	if strings.TrimSpace(releaseID) == "" || strings.TrimSpace(summary) == "" || now.IsZero() {
		return nil, errors.New("catalog pending release failure is invalid")
	}
	updated, err := scanCatalogRelease(d.conn.QueryRowContext(ctx, `UPDATE catalog_releases SET state = ?, last_error = ?, updated_at = ?
		WHERE id = ? AND state = ? RETURNING `+catalogReleaseColumns,
		catalogdomain.ReleaseFailed, strings.TrimSpace(summary), now.UTC(), strings.TrimSpace(releaseID), catalogdomain.ReleasePending))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogLeaseLost
	}
	if err != nil {
		return nil, fmt.Errorf("fail pending catalog release: %w", err)
	}
	return updated, nil
}

// FailRelease records a deterministic source consistency failure discovered
// before a phase is claimed. It is intentionally limited to non-terminal
// releases; a concurrent successful commit remains authoritative.
func (d *CatalogRepository) FailRelease(ctx context.Context, releaseID, summary string, now time.Time) (*catalogdomain.Release, error) {
	if strings.TrimSpace(releaseID) == "" || strings.TrimSpace(summary) == "" || now.IsZero() {
		return nil, errors.New("catalog release failure is invalid")
	}
	updated, err := scanCatalogRelease(d.conn.QueryRowContext(ctx, `UPDATE catalog_releases SET state = ?, lease_owner = '', lease_expires_at = NULL,
		last_error = ?, updated_at = ? WHERE id = ? AND state IN (?, ?, ?) RETURNING `+catalogReleaseColumns,
		catalogdomain.ReleaseFailed, strings.TrimSpace(summary), now.UTC(), strings.TrimSpace(releaseID),
		catalogdomain.ReleasePending, catalogdomain.ReleaseInstalling, catalogdomain.ReleaseCommitting))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogLeaseLost
	}
	if err != nil {
		return nil, fmt.Errorf("fail catalog release: %w", err)
	}
	return updated, nil
}

func (d *CatalogRepository) ReleaseByDigest(ctx context.Context, digest catalogdomain.BundleDigest) (*catalogdomain.Release, error) {
	release, err := scanCatalogRelease(d.conn.QueryRowContext(ctx, catalogReleaseSelect+` WHERE bundle_digest = ?`, digest))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read catalog release by digest: %w", err)
	}
	return release, nil
}

func (d *CatalogRepository) Release(ctx context.Context, id string) (*catalogdomain.Release, error) {
	release, err := scanCatalogRelease(d.conn.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read catalog release: %w", err)
	}
	return release, nil
}

func (d *CatalogRepository) Entries(ctx context.Context, releaseID string) ([]catalogdomain.Entry, error) {
	rows, err := d.conn.QueryContext(ctx, catalogEntrySelect+` WHERE release_id = ? ORDER BY source_path, id`, strings.TrimSpace(releaseID))
	if err != nil {
		return nil, fmt.Errorf("list catalog release entries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	entries := make([]catalogdomain.Entry, 0)
	for rows.Next() {
		entry, scanErr := scanCatalogEntry(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		entries = append(entries, *entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalog release entries: %w", err)
	}
	return entries, nil
}

// InstalledEntries are the durable source bindings from successfully visible
// releases. They let a later full portable snapshot skip already verified
// content rather than rebuilding it.
func (d *CatalogRepository) InstalledEntries(ctx context.Context) ([]catalogdomain.Entry, error) {
	rows, err := d.conn.QueryContext(ctx, catalogEntrySelect+` WHERE release_id IN (SELECT id FROM catalog_releases WHERE state = ?) ORDER BY source_ref, release_id`, catalogdomain.ReleaseReady)
	if err != nil {
		return nil, fmt.Errorf("list installed catalog entries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	entries := make([]catalogdomain.Entry, 0)
	for rows.Next() {
		entry, scanErr := scanCatalogEntry(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		entries = append(entries, *entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate installed catalog entries: %w", err)
	}
	return entries, nil
}

func (d *CatalogRepository) ClaimEntry(ctx context.Context, releaseID, workerID string, leaseTTL time.Duration, now time.Time) (*catalogdomain.EntryClaim, error) {
	if strings.TrimSpace(releaseID) == "" || strings.TrimSpace(workerID) == "" || leaseTTL <= 0 || now.IsZero() {
		return nil, errors.New("catalog entry claim requires release, worker, lease ttl, and current time")
	}
	now = now.UTC()
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin catalog entry claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := expireCatalogReleasesTx(ctx, tx, now); err != nil {
		return nil, err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT entry.id FROM catalog_release_entries entry
		JOIN catalog_releases release ON release.id = entry.release_id
		WHERE release.id = ? AND release.state = ? AND entry.state IN (?, ?, ?, ?) AND entry.next_run_at <= ?
		AND (entry.lease_expires_at IS NULL OR entry.lease_expires_at <= ?)
		ORDER BY entry.next_run_at, entry.created_at, entry.id FOR UPDATE OF entry SKIP LOCKED LIMIT 1`,
		releaseID, catalogdomain.ReleaseInstalling, catalogdomain.EntryPending, catalogdomain.EntryBuilding,
		catalogdomain.EntryArtifactPublishing, catalogdomain.EntryVerifying, now, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty catalog entry claim: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select catalog entry claim: %w", err)
	}
	entry, err := scanCatalogEntry(tx.QueryRowContext(ctx, catalogEntrySelect+` WHERE id = ? FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	release, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, entry.ReleaseID))
	if err != nil {
		return nil, err
	}
	state := entry.State
	if state == catalogdomain.EntryPending {
		state = catalogdomain.EntryBuilding
	}
	owner := catalogLeaseOwner(workerID)
	expires := now.Add(leaseTTL)
	entry, err = scanCatalogEntry(tx.QueryRowContext(ctx, `UPDATE catalog_release_entries SET state = ?, attempt = attempt + 1,
		lease_owner = ?, lease_expires_at = ?, last_error = '', updated_at = ? WHERE id = ? RETURNING `+catalogEntryColumns,
		state, owner, expires, now, entry.ID))
	if err != nil {
		return nil, fmt.Errorf("claim catalog entry: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit catalog entry claim: %w", err)
	}
	return &catalogdomain.EntryClaim{Release: *release, Entry: *entry}, nil
}

func (d *CatalogRepository) RenewEntryLease(ctx context.Context, claim catalogdomain.EntryClaim, leaseTTL time.Duration, now time.Time) error {
	if !claim.Valid() || leaseTTL <= 0 || now.IsZero() {
		return errors.New("catalog entry lease renewal is invalid")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE catalog_release_entries SET lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND lease_owner = ? AND attempt = ? AND lease_expires_at > ?`, now.UTC().Add(leaseTTL), now.UTC(),
		claim.Entry.ID, claim.Entry.LeaseOwner, claim.Entry.Attempt, now.UTC())
	if err != nil {
		return fmt.Errorf("renew catalog entry lease: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrCatalogLeaseLost
	}
	return nil
}

func (d *CatalogRepository) CompleteEntryBuild(ctx context.Context, claim catalogdomain.EntryClaim, output execution.BuildOutput, now time.Time) (*catalogdomain.EntryClaim, error) {
	if !claim.Valid() || output.Validate(claim.Entry.Snapshot.Runtime) != nil || now.IsZero() {
		return nil, errors.New("catalog build completion is invalid")
	}
	encoded, err := marshalJSON(output)
	if err != nil {
		return nil, err
	}
	return d.transitionEntry(ctx, claim, catalogdomain.EntryBuilding, catalogdomain.EntryArtifactPublishing, now,
		`build_output = ?::jsonb, lease_owner = '', lease_expires_at = NULL, last_error = ''`, encoded)
}

func (d *CatalogRepository) CompleteEntryArtifactPublish(ctx context.Context, claim catalogdomain.EntryClaim, artifact execution.ArtifactReference, now time.Time) (*catalogdomain.EntryClaim, error) {
	if !claim.Valid() || artifact.Validate(claim.Entry.Snapshot.Runtime) != nil || now.IsZero() {
		return nil, errors.New("catalog artifact publication is invalid")
	}
	encoded, err := marshalJSON(artifact)
	if err != nil {
		return nil, err
	}
	return d.transitionEntry(ctx, claim, catalogdomain.EntryArtifactPublishing, catalogdomain.EntryVerifying, now,
		`artifact_reference = ?::jsonb, lease_owner = '', lease_expires_at = NULL, last_error = ''`, encoded)
}

func (d *CatalogRepository) RecordEntryVerificationEnvironment(ctx context.Context, claim catalogdomain.EntryClaim, environment execution.VerificationEnvironment, now time.Time) (*catalogdomain.EntryClaim, error) {
	if !claim.Valid() || environment.Validate(claim.Entry.Snapshot.Runtime) != nil || environment.WorkflowID != claim.Entry.ID || now.IsZero() {
		return nil, errors.New("catalog verification environment is invalid")
	}
	encoded, err := marshalJSON(environment)
	if err != nil {
		return nil, err
	}
	return d.transitionEntry(ctx, claim, catalogdomain.EntryVerifying, catalogdomain.EntryVerifying, now, `verify_environment = ?::jsonb`, encoded)
}

func (d *CatalogRepository) CompleteEntryVerification(ctx context.Context, claim catalogdomain.EntryClaim, report execution.VerificationReport, now time.Time) (*catalogdomain.EntryClaim, error) {
	if !claim.Valid() || !report.Passed || report.Validate(claim.Entry.Snapshot) != nil || now.IsZero() {
		return nil, errors.New("catalog verification completion is invalid")
	}
	encoded, err := marshalJSON(report)
	if err != nil {
		return nil, err
	}
	return d.transitionEntry(ctx, claim, catalogdomain.EntryVerifying, catalogdomain.EntryReadyToCommit, now,
		`verification_report = ?::jsonb, lease_owner = '', lease_expires_at = NULL, last_error = ''`, encoded)
}

func (d *CatalogRepository) RetryEntry(ctx context.Context, claim catalogdomain.EntryClaim, summary string, nextRunAt, now time.Time) error {
	if !claim.Valid() || !claim.Entry.State.Leaseable() || strings.TrimSpace(summary) == "" || nextRunAt.IsZero() || now.IsZero() {
		return errors.New("catalog entry retry is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin catalog entry retry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := lockCatalogEntryClaimTx(ctx, tx, claim, claim.Entry.State, now.UTC()); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE catalog_release_entries SET lease_owner = '', lease_expires_at = NULL,
		next_run_at = ?, last_error = ?, updated_at = ? WHERE id = ?`, nextRunAt.UTC(), strings.TrimSpace(summary), now.UTC(), claim.Entry.ID)
	if err != nil {
		return fmt.Errorf("retry catalog entry: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrCatalogLeaseLost
	}
	return tx.Commit()
}

func (d *CatalogRepository) FailEntry(ctx context.Context, claim catalogdomain.EntryClaim, summary string, report *execution.VerificationReport, now time.Time) error {
	if !claim.Valid() || strings.TrimSpace(summary) == "" || now.IsZero() {
		return errors.New("catalog entry failure is invalid")
	}
	var encoded any
	if report != nil {
		value, err := marshalJSON(report)
		if err != nil {
			return err
		}
		encoded = value
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin catalog entry failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	release, err := lockCatalogEntryClaimTx(ctx, tx, claim, claim.Entry.State, now.UTC())
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_release_entries SET state = ?, lease_owner = '', lease_expires_at = NULL,
		verification_report = COALESCE(?::jsonb, verification_report), last_error = ?, updated_at = ? WHERE id = ?`,
		catalogdomain.EntryFailed, encoded, strings.TrimSpace(summary), now.UTC(), claim.Entry.ID); err != nil {
		return fmt.Errorf("fail catalog entry: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_releases SET state = ?, lease_owner = '', lease_expires_at = NULL,
		last_error = ?, updated_at = ? WHERE id = ?`, catalogdomain.ReleaseFailed, strings.TrimSpace(summary), now.UTC(), release.ID); err != nil {
		return fmt.Errorf("fail catalog release: %w", err)
	}
	return tx.Commit()
}

// PrepareReleaseCommit reserves every final challenge identity in one durable
// transaction after all entries have independently verified successfully.
func (d *CatalogRepository) PrepareReleaseCommit(ctx context.Context, releaseID string, intents []catalogdomain.Commit, now time.Time) (*catalogdomain.Release, []catalogdomain.Commit, error) {
	if strings.TrimSpace(releaseID) == "" || now.IsZero() {
		return nil, nil, errors.New("catalog release commit preparation is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("begin catalog release commit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	release, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, releaseID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if release.State == catalogdomain.ReleaseReady || release.State == catalogdomain.ReleaseCommitting {
		commits, listErr := listCatalogCommitsTx(ctx, tx, release.ID)
		if listErr != nil {
			return nil, nil, listErr
		}
		if err := tx.Commit(); err != nil {
			return nil, nil, err
		}
		return release, commits, nil
	}
	if release.State != catalogdomain.ReleaseInstalling {
		return nil, nil, errors.New("catalog release cannot enter commit")
	}
	var unfinished bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM catalog_release_entries WHERE release_id = ? AND state <> ?)`,
		release.ID, catalogdomain.EntryReadyToCommit).Scan(&unfinished); err != nil {
		return nil, nil, fmt.Errorf("check catalog entry readiness: %w", err)
	}
	if unfinished {
		return nil, nil, errors.New("catalog release has unverified entries")
	}
	if release.CommitID != "" && release.CommitID != catalogdomain.CommitIDForRelease(release.ID) {
		return nil, nil, errors.New("catalog release has an invalid commit identity")
	}
	seen := make(map[string]struct{}, len(intents))
	for _, commit := range intents {
		if commit.ID != catalogdomain.EntryCommitIDFor(release.ID, commit.EntryID) || commit.ReleaseID != release.ID ||
			commit.State != catalogdomain.CommitPrepared || !commit.Valid() {
			return nil, nil, fmt.Errorf("catalog commit intent %q is invalid", commit.EntryID)
		}
		if _, exists := seen[commit.EntryID]; exists {
			return nil, nil, fmt.Errorf("duplicate catalog commit entry %q", commit.EntryID)
		}
		seen[commit.EntryID] = struct{}{}
		var owned bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM catalog_release_entries WHERE id = ? AND release_id = ?)`, commit.EntryID, release.ID).Scan(&owned); err != nil {
			return nil, nil, err
		}
		if !owned {
			return nil, nil, fmt.Errorf("catalog commit entry %q does not belong to release", commit.EntryID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_release_entry_commits
			(id, release_id, entry_id, challenge_id, source_slug, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, commit.ID, commit.ReleaseID, commit.EntryID, commit.ChallengeID,
			commit.SourceSlug, commit.State, commit.CreatedAt.UTC(), commit.UpdatedAt.UTC()); err != nil {
			return nil, nil, fmt.Errorf("insert catalog entry commit %q: %w", commit.EntryID, err)
		}
	}
	var entryCount, intentCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_release_entries WHERE release_id = ?`, release.ID).Scan(&entryCount); err != nil {
		return nil, nil, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_release_entry_commits WHERE release_id = ?`, release.ID).Scan(&intentCount); err != nil {
		return nil, nil, err
	}
	if entryCount != intentCount || entryCount != len(intents) {
		return nil, nil, errors.New("catalog release commit intents do not cover every entry")
	}
	release, err = scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases SET state = ?, commit_id = ?, last_error = '', updated_at = ?
		WHERE id = ? RETURNING `+catalogReleaseColumns, catalogdomain.ReleaseCommitting, catalogdomain.CommitIDForRelease(release.ID), now.UTC(), release.ID))
	if err != nil {
		return nil, nil, fmt.Errorf("begin catalog release commit: %w", err)
	}
	commits, err := listCatalogCommitsTx(ctx, tx, release.ID)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("commit catalog release preparation: %w", err)
	}
	return release, commits, nil
}

func (d *CatalogRepository) Commits(ctx context.Context, releaseID string) ([]catalogdomain.Commit, error) {
	rows, err := d.conn.QueryContext(ctx, catalogCommitSelect+` WHERE release_id = ? ORDER BY entry_id`, strings.TrimSpace(releaseID))
	if err != nil {
		return nil, fmt.Errorf("list catalog release commits: %w", err)
	}
	defer func() { _ = rows.Close() }()
	commits := make([]catalogdomain.Commit, 0)
	for rows.Next() {
		commit, scanErr := scanCatalogCommit(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		commits = append(commits, *commit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalog release commits: %w", err)
	}
	return commits, nil
}

func (d *CatalogRepository) ClaimReleaseCommit(ctx context.Context, releaseID, workerID string, leaseTTL time.Duration, now time.Time) (*catalogdomain.Release, error) {
	if strings.TrimSpace(releaseID) == "" || strings.TrimSpace(workerID) == "" || leaseTTL <= 0 || now.IsZero() {
		return nil, errors.New("catalog release commit claim is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin catalog commit claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := expireCatalogReleasesTx(ctx, tx, now.UTC()); err != nil {
		return nil, err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM catalog_releases WHERE id = ? AND state = ? AND (lease_expires_at IS NULL OR lease_expires_at <= ?)
		FOR UPDATE SKIP LOCKED`, releaseID, catalogdomain.ReleaseCommitting, now.UTC()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select catalog commit claim: %w", err)
	}
	owner := catalogLeaseOwner(workerID)
	release, err := scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases SET lease_owner = ?, lease_expires_at = ?, updated_at = ?
		WHERE id = ? RETURNING `+catalogReleaseColumns, owner, now.UTC().Add(leaseTTL), now.UTC(), id))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return release, nil
}

func (d *CatalogRepository) RenewReleaseCommitLease(ctx context.Context, release catalogdomain.Release, leaseTTL time.Duration, now time.Time) error {
	if !release.Valid() || release.State != catalogdomain.ReleaseCommitting || strings.TrimSpace(release.LeaseOwner) == "" || leaseTTL <= 0 || now.IsZero() {
		return errors.New("catalog release commit lease renewal is invalid")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE catalog_releases SET lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND state = ? AND lease_owner = ? AND lease_expires_at > ?`, now.UTC().Add(leaseTTL), now.UTC(),
		release.ID, catalogdomain.ReleaseCommitting, release.LeaseOwner, now.UTC())
	if err != nil {
		return fmt.Errorf("renew catalog release commit lease: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrCatalogLeaseLost
	}
	return nil
}

// FailReleaseCommit records a deterministic commit-time source or artifact
// inconsistency while the caller still owns the release commit lease. Transient
// Registry, filesystem, and database failures are deliberately not routed
// through this method and remain recoverable until the deadline.
func (d *CatalogRepository) FailReleaseCommit(ctx context.Context, release catalogdomain.Release, summary string, now time.Time) error {
	if !releaseCommitClaimValid(release) || strings.TrimSpace(summary) == "" || now.IsZero() {
		return errors.New("catalog release commit failure is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockCatalogReleaseCommitTx(ctx, tx, release, now.UTC()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_releases SET state = ?, lease_owner = '', lease_expires_at = NULL,
		last_error = ?, updated_at = ? WHERE id = ?`, catalogdomain.ReleaseFailed, strings.TrimSpace(summary), now.UTC(), release.ID); err != nil {
		return fmt.Errorf("fail catalog release commit: %w", err)
	}
	return tx.Commit()
}

func (d *CatalogRepository) CompleteCommitArtifact(ctx context.Context, release catalogdomain.Release, commit catalogdomain.Commit, artifact execution.ArtifactReference, now time.Time) (*catalogdomain.Commit, error) {
	if !releaseCommitClaimValid(release) || commit.ReleaseID != release.ID || commit.State != catalogdomain.CommitPrepared || artifact.Validate(artifact.Runtime) != nil || now.IsZero() {
		return nil, errors.New("catalog commit artifact completion is invalid")
	}
	encoded, err := marshalJSON(artifact)
	if err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockCatalogReleaseCommitTx(ctx, tx, release, now.UTC()); err != nil {
		return nil, err
	}
	updated, err := scanCatalogCommit(tx.QueryRowContext(ctx, `UPDATE catalog_release_entry_commits SET state = ?, artifact_reference = ?::jsonb, updated_at = ?
		WHERE id = ? AND release_id = ? AND state = ? RETURNING `+catalogCommitColumns,
		catalogdomain.CommitArtifactPublished, encoded, now.UTC(), commit.ID, release.ID, catalogdomain.CommitPrepared))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogLeaseLost
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func (d *CatalogRepository) MarkCommitMaterialized(ctx context.Context, release catalogdomain.Release, commit catalogdomain.Commit, now time.Time) (*catalogdomain.Commit, error) {
	if !releaseCommitClaimValid(release) || commit.ReleaseID != release.ID || commit.State != catalogdomain.CommitArtifactPublished || now.IsZero() {
		return nil, errors.New("catalog materialization completion is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockCatalogReleaseCommitTx(ctx, tx, release, now.UTC()); err != nil {
		return nil, err
	}
	updated, err := scanCatalogCommit(tx.QueryRowContext(ctx, `UPDATE catalog_release_entry_commits SET state = ?, materialized_at = ?, updated_at = ?
		WHERE id = ? AND release_id = ? AND state = ? RETURNING `+catalogCommitColumns,
		catalogdomain.CommitMaterialized, now.UTC(), now.UTC(), commit.ID, release.ID, catalogdomain.CommitArtifactPublished))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogLeaseLost
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

// CompleteReleaseCommit is the sole public visibility transition. It writes
// the immutable current Roadmap revision, marks release commits complete, and
// establishes the Roadmap processed baseline under one write lock.
func (d *CatalogRepository) CompleteReleaseCommit(ctx context.Context, release catalogdomain.Release, value roadmap.Revision, now time.Time) (*catalogdomain.Release, error) {
	if !releaseCommitClaimValid(release) || now.IsZero() {
		return nil, errors.New("catalog release completion is invalid")
	}
	canonical, encoded, revisionID, err := canonicalRoadmap(value)
	if err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockCatalogReleaseCommitTx(ctx, tx, release, now.UTC()); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT revision_id FROM roadmap_current WHERE singleton = TRUE FOR UPDATE`); err != nil {
		return nil, fmt.Errorf("lock roadmap current revision: %w", err)
	}
	if err := ensureRoadmapPublicationAllowedTx(ctx, tx, now); err != nil {
		return nil, err
	}
	commits, err := listCatalogCommitsTx(ctx, tx, release.ID)
	if err != nil {
		return nil, err
	}
	entries, err := listCatalogEntriesTx(ctx, tx, release.ID)
	if err != nil {
		return nil, err
	}
	entriesByID := make(map[string]catalogdomain.Entry, len(entries))
	for _, entry := range entries {
		entriesByID[entry.ID] = entry
	}
	bindingsByChallengeID := make(map[string]roadmap.ChallengeBinding, len(canonical.ChallengeBindings))
	for _, binding := range canonical.ChallengeBindings {
		bindingsByChallengeID[binding.Challenge.ID] = binding
	}
	for _, commit := range commits {
		if commit.State != catalogdomain.CommitMaterialized {
			return nil, errors.New("catalog release has incomplete materialization")
		}
		entry, exists := entriesByID[commit.EntryID]
		if !exists {
			return nil, fmt.Errorf("catalog commit %q has no release entry", commit.EntryID)
		}
		binding, exists := bindingsByChallengeID[commit.ChallengeID]
		if !exists || binding.Challenge.SourceRef != entry.SourceRef || binding.Challenge.Title != entry.Title || binding.Challenge.ContentRevision != string(entry.ContentRevision) {
			return nil, fmt.Errorf("catalog commit %q does not match its roadmap binding", commit.EntryID)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO roadmap_revisions (id, content_json, created_at) VALUES (?, ?::jsonb, ?)
		ON CONFLICT (id) DO NOTHING`, revisionID, encoded, now.UTC()); err != nil {
		return nil, fmt.Errorf("store catalog roadmap revision: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE roadmap_current SET revision_id = ?, updated_at = ? WHERE singleton = TRUE`, revisionID, now.UTC()); err != nil {
		return nil, fmt.Errorf("publish catalog roadmap revision: %w", err)
	}
	for _, commit := range commits {
		binding := bindingsByChallengeID[commit.ChallengeID]
		if _, err := tx.ExecContext(ctx, `INSERT INTO roadmap_entries (challenge_id, topic_id, topic_processed, challenge_processed, created_at)
			VALUES (?, ?, TRUE, TRUE, ?) ON CONFLICT (challenge_id) DO NOTHING`, binding.Challenge.ID, binding.Topic.ID, now.UTC()); err != nil {
			return nil, fmt.Errorf("store catalog roadmap baseline: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_release_entry_commits SET state = ?, committed_at = ?, updated_at = ?
		WHERE release_id = ? AND state = ?`, catalogdomain.CommitCommitted, now.UTC(), now.UTC(), release.ID, catalogdomain.CommitMaterialized); err != nil {
		return nil, fmt.Errorf("complete catalog entry commits: %w", err)
	}
	updated, err := scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases SET state = ?, lease_owner = '', lease_expires_at = NULL,
		last_error = '', updated_at = ? WHERE id = ? RETURNING `+catalogReleaseColumns, catalogdomain.ReleaseReady, now.UTC(), release.ID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	canonical.Revision = revisionID
	return updated, nil
}

func (d *CatalogRepository) transitionEntry(ctx context.Context, claim catalogdomain.EntryClaim, expected, next catalogdomain.EntryState, now time.Time, assignment string, args ...any) (*catalogdomain.EntryClaim, error) {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	release, err := lockCatalogEntryClaimTx(ctx, tx, claim, expected, now.UTC())
	if err != nil {
		return nil, err
	}
	query := `UPDATE catalog_release_entries SET state = ?, ` + assignment + `, updated_at = ? WHERE id = ? RETURNING ` + catalogEntryColumns
	values := make([]any, 0, len(args)+3)
	values = append(values, next)
	values = append(values, args...)
	values = append(values, now.UTC(), claim.Entry.ID)
	entry, err := scanCatalogEntry(tx.QueryRowContext(ctx, query, values...))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &catalogdomain.EntryClaim{Release: *release, Entry: *entry}, nil
}

func lockCatalogEntryClaimTx(ctx context.Context, tx *Tx, claim catalogdomain.EntryClaim, expected catalogdomain.EntryState, now time.Time) (*catalogdomain.Release, error) {
	entry, err := scanCatalogEntry(tx.QueryRowContext(ctx, catalogEntrySelect+` WHERE id = ? FOR UPDATE`, claim.Entry.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogEntryNotFound
	}
	if err != nil {
		return nil, err
	}
	if entry.ReleaseID != claim.Release.ID || entry.State != expected || entry.LeaseOwner != claim.Entry.LeaseOwner || entry.Attempt != claim.Entry.Attempt ||
		entry.LeaseExpires == nil || !entry.LeaseExpires.After(now) {
		return nil, ErrCatalogLeaseLost
	}
	release, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, entry.ReleaseID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, err
	}
	if release.State != catalogdomain.ReleaseInstalling {
		return nil, ErrCatalogLeaseLost
	}
	return release, nil
}

func lockCatalogReleaseCommitTx(ctx context.Context, tx *Tx, release catalogdomain.Release, now time.Time) error {
	current, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, release.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCatalogReleaseNotFound
	}
	if err != nil {
		return err
	}
	if current.State != catalogdomain.ReleaseCommitting || current.LeaseOwner != release.LeaseOwner || current.LeaseExpires == nil || !current.LeaseExpires.After(now) {
		return ErrCatalogLeaseLost
	}
	return nil
}

func releaseCommitClaimValid(release catalogdomain.Release) bool {
	return release.Valid() && release.State == catalogdomain.ReleaseCommitting && strings.TrimSpace(release.LeaseOwner) != ""
}

func expireCatalogReleasesTx(ctx context.Context, tx *Tx, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_releases SET state = ?, lease_owner = '', lease_expires_at = NULL,
		last_error = 'catalog release installation deadline exceeded', updated_at = ?
		WHERE state IN (?, ?, ?) AND deadline_at <= ?`, catalogdomain.ReleaseFailed, now,
		catalogdomain.ReleasePending, catalogdomain.ReleaseInstalling, catalogdomain.ReleaseCommitting, now); err != nil {
		return fmt.Errorf("expire catalog release deadlines: %w", err)
	}
	return nil
}

func listCatalogCommitsTx(ctx context.Context, tx *Tx, releaseID string) ([]catalogdomain.Commit, error) {
	rows, err := tx.QueryContext(ctx, catalogCommitSelect+` WHERE release_id = ? ORDER BY entry_id`, releaseID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	commits := make([]catalogdomain.Commit, 0)
	for rows.Next() {
		commit, scanErr := scanCatalogCommit(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		commits = append(commits, *commit)
	}
	return commits, rows.Err()
}

func listCatalogEntriesTx(ctx context.Context, tx *Tx, releaseID string) ([]catalogdomain.Entry, error) {
	rows, err := tx.QueryContext(ctx, catalogEntrySelect+` WHERE release_id = ? ORDER BY source_path, id`, releaseID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	entries := make([]catalogdomain.Entry, 0)
	for rows.Next() {
		entry, scanErr := scanCatalogEntry(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		entries = append(entries, *entry)
	}
	return entries, rows.Err()
}

func scanCatalogRelease(row scanner) (*catalogdomain.Release, error) {
	var value catalogdomain.Release
	var digest, sourceDigest string
	var expires sql.NullTime
	if err := row.Scan(&value.ID, &value.Name, &value.Version, &digest, &sourceDigest, &value.State, &value.DeadlineAt, &value.CommitID,
		&value.LeaseOwner, &expires, &value.LastError, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return nil, err
	}
	value.BundleDigest = catalogdomain.BundleDigest(digest)
	value.SourceDigest = catalogdomain.ContentRevision(sourceDigest)
	if expires.Valid {
		at := expires.Time.UTC()
		value.LeaseExpires = &at
	}
	value.DeadlineAt = value.DeadlineAt.UTC()
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	if !value.Valid() {
		return nil, errors.New("stored catalog release is invalid")
	}
	return &value, nil
}

func scanCatalogEntry(row scanner) (*catalogdomain.Entry, error) {
	var value catalogdomain.Entry
	var revision string
	var snapshot, build, artifact, environment, report []byte
	var expires sql.NullTime
	if err := row.Scan(&value.ID, &value.ReleaseID, &value.SourcePath, &value.SourceRef, &value.Title, &revision, &value.ArchiveSHA256,
		&snapshot, &value.State, &value.Attempt, &value.LeaseOwner, &expires, &value.NextRunAt, &build, &artifact, &environment,
		&report, &value.LastError, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return nil, err
	}
	value.ContentRevision = catalogdomain.ContentRevision(revision)
	if err := json.Unmarshal(snapshot, &value.Snapshot); err != nil {
		return nil, fmt.Errorf("decode catalog entry snapshot: %w", err)
	}
	if err := decodeOptionalJSON(build, &value.Build); err != nil {
		return nil, fmt.Errorf("decode catalog entry build: %w", err)
	}
	if err := decodeOptionalJSON(artifact, &value.Artifact); err != nil {
		return nil, fmt.Errorf("decode catalog entry artifact: %w", err)
	}
	if err := decodeOptionalJSON(environment, &value.VerifyEnvironment); err != nil {
		return nil, fmt.Errorf("decode catalog entry verification environment: %w", err)
	}
	if err := decodeOptionalJSON(report, &value.Verification); err != nil {
		return nil, fmt.Errorf("decode catalog entry verification report: %w", err)
	}
	if expires.Valid {
		at := expires.Time.UTC()
		value.LeaseExpires = &at
	}
	value.NextRunAt = value.NextRunAt.UTC()
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	if !value.Valid() {
		return nil, errors.New("stored catalog entry is invalid")
	}
	return &value, nil
}

func scanCatalogCommit(row scanner) (*catalogdomain.Commit, error) {
	var value catalogdomain.Commit
	var artifact []byte
	var materialized, committed sql.NullTime
	if err := row.Scan(&value.ID, &value.ReleaseID, &value.EntryID, &value.ChallengeID, &value.SourceSlug, &value.State, &artifact,
		&materialized, &committed, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return nil, err
	}
	if err := decodeOptionalJSON(artifact, &value.Artifact); err != nil {
		return nil, fmt.Errorf("decode catalog commit artifact: %w", err)
	}
	if materialized.Valid {
		at := materialized.Time.UTC()
		value.MaterializedAt = &at
	}
	if committed.Valid {
		at := committed.Time.UTC()
		value.CommittedAt = &at
	}
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	if !value.Valid() {
		return nil, errors.New("stored catalog commit is invalid")
	}
	return &value, nil
}

func decodeOptionalJSON(data []byte, target any) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	return json.Unmarshal(data, target)
}

func catalogLeaseOwner(workerID string) string {
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		return strings.TrimSpace(workerID) + "-" + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return strings.TrimSpace(workerID) + "-" + hex.EncodeToString(data[:])
}
