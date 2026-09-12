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

	contentchallenge "github.com/breakfix/breakfix/internal/content/challenge"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	challengedomain "github.com/breakfix/breakfix/internal/domain/challenge"
	"github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/publication"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
)

var (
	ErrCatalogReleaseNotFound = catalogdomain.ErrReleaseNotFound
	ErrCatalogEntryNotFound   = errors.New("catalog release entry not found")
	ErrCatalogCommitNotFound  = errors.New("catalog release entry commit not found")
	ErrCatalogLeaseLost       = runtime.ErrLeaseLost
)

const catalogReleaseColumns = `id, name, version, bundle_digest, source_digest, state, source_attempt, next_run_at, commit_id,
	last_error, finalizer_error_category, finalizer_last_error, finalizer_last_attempted_at, finalizer_next_retry_at, created_at, updated_at`
const catalogReleaseSelect = `SELECT ` + catalogReleaseColumns + ` FROM catalog_releases`

const catalogEntryColumns = `id, release_id, source_path, source_ref, title, scenario_type, tags, content_revision, archive_sha256,
	execution_snapshot, state, state_version, runtime_attempt, lease_owner, lease_expires_at, next_run_at, build_output,
	artifact_reference, verify_environment, verification_report, last_error, created_at, updated_at`
const catalogEntrySelect = `SELECT ` + catalogEntryColumns + ` FROM catalog_release_entries`

const catalogCommitColumns = `id, release_id, entry_id, challenge_id, challenge_revision_id, source_slug, state, state_version, runtime_attempt,
	lease_owner, lease_expires_at, next_run_at, last_error, artifact_reference, materialized_at, committed_at, created_at, updated_at`
const catalogCommitReturningColumns = `commit.id, commit.release_id, commit.entry_id, commit.challenge_id, commit.challenge_revision_id, commit.source_slug, commit.state, commit.state_version, commit.runtime_attempt,
	commit.lease_owner, commit.lease_expires_at, commit.next_run_at, commit.last_error, commit.artifact_reference, commit.materialized_at, commit.committed_at, commit.created_at, commit.updated_at`
const catalogCommitSelect = `SELECT ` + catalogCommitColumns + ` FROM catalog_release_entry_commits`

type scanner interface{ Scan(...any) error }

// CreateOrGetRelease reserves a release identity before Server source staging.
// Bundle digest is immutable and is the only installation idempotency key.
func (d *CatalogRepository) CreateOrGetRelease(ctx context.Context, release catalogdomain.Release) (*catalogdomain.Release, bool, error) {
	if release.State != catalogdomain.ReleasePending || !release.Valid() || release.SourceAttempt != 1 {
		return nil, false, errors.New("catalog release creation requires a valid pending source attempt")
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
		(id, name, version, bundle_digest, source_digest, state, source_attempt, next_run_at, commit_id, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', '', ?, ?)`,
		release.ID, release.Name, release.Version, release.BundleDigest, release.SourceDigest, release.State, release.SourceAttempt,
		release.NextRunAt.UTC(), release.CreatedAt.UTC(), release.UpdatedAt.UTC()); err != nil {
		return nil, false, fmt.Errorf("insert catalog release: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit catalog release creation: %w", err)
	}
	return &release, true, nil
}

// InitializeRelease commits immutable staged source metadata and creates the
// first runtime state for every new entry. Entry Building is claimable as soon
// as this transaction commits; Server never runs those external actions.
func (d *CatalogRepository) InitializeRelease(ctx context.Context, release catalogdomain.Release, entries []catalogdomain.Entry, now time.Time) (*catalogdomain.Release, error) {
	if release.State != catalogdomain.ReleasePending || !release.Valid() || strings.TrimSpace(release.Name) == "" || strings.TrimSpace(release.Version) == "" || !release.SourceDigest.Valid() || now.IsZero() {
		return nil, errors.New("catalog release initialization is invalid")
	}
	seenPath := make(map[string]struct{}, len(entries))
	seenRef := make(map[string]struct{}, len(entries))
	for index := range entries {
		entry := entries[index]
		if entry.ID != catalogdomain.EntryIDFor(release.ID, entry.SourcePath) || entry.ReleaseID != release.ID || entry.State != catalogdomain.EntryBuilding || entry.StateVersion != 1 || entry.RuntimeAttempt != 1 || !entry.Valid() {
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
		tags, err := marshalJSON(entry.Tags)
		if err != nil {
			return nil, fmt.Errorf("encode catalog entry %q tags: %w", entry.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_release_entries
			(id, release_id, source_path, source_ref, title, scenario_type, tags, content_revision, archive_sha256, execution_snapshot, state,
			state_version, runtime_attempt, lease_owner, lease_expires_at, next_run_at, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?::jsonb, ?, ?, ?, '', NULL, ?, '', ?, ?)`,
			entry.ID, entry.ReleaseID, entry.SourcePath, entry.SourceRef, entry.Title, entry.Type, tags, entry.ContentRevision, entry.ArchiveSHA256,
			snapshot, entry.State, entry.StateVersion, entry.RuntimeAttempt, entry.NextRunAt.UTC(), entry.CreatedAt.UTC(), entry.UpdatedAt.UTC()); err != nil {
			return nil, fmt.Errorf("insert catalog entry %q: %w", entry.ID, err)
		}
	}
	updated, err := scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases
		SET name = ?, version = ?, source_digest = ?, state = ?, last_error = '', updated_at = ?
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

func (d *CatalogRepository) ReportSourceInfrastructureFailure(ctx context.Context, releaseID, summary string, now time.Time) (*catalogdomain.Release, error) {
	if strings.TrimSpace(releaseID) == "" || strings.TrimSpace(summary) == "" || now.IsZero() {
		return nil, errors.New("catalog source failure is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin catalog source failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	release, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, strings.TrimSpace(releaseID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, err
	}
	if release.State != catalogdomain.ReleasePending {
		return nil, ErrCatalogLeaseLost
	}
	if release.SourceAttempt >= 5 {
		release, err = scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases SET state = ?, last_error = ?, updated_at = ?
			WHERE id = ? RETURNING `+catalogReleaseColumns, catalogdomain.ReleaseFailed, strings.TrimSpace(summary), now.UTC(), release.ID))
	} else {
		release, err = scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases SET source_attempt = source_attempt + 1,
			next_run_at = ?, last_error = ?, updated_at = ? WHERE id = ? RETURNING `+catalogReleaseColumns,
			catalogRetryAt(release.SourceAttempt+1, now.UTC()), strings.TrimSpace(summary), now.UTC(), release.ID))
	}
	if err != nil {
		return nil, fmt.Errorf("record catalog source failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit catalog source failure: %w", err)
	}
	return release, nil
}

func (d *CatalogRepository) FailPendingRelease(ctx context.Context, releaseID, summary string, now time.Time) (*catalogdomain.Release, error) {
	return d.failReleaseState(ctx, releaseID, []catalogdomain.ReleaseState{catalogdomain.ReleasePending}, summary, now)
}

func (d *CatalogRepository) FailRelease(ctx context.Context, releaseID, summary string, now time.Time) (*catalogdomain.Release, error) {
	return d.failReleaseState(ctx, releaseID, []catalogdomain.ReleaseState{catalogdomain.ReleasePending, catalogdomain.ReleaseInstalling, catalogdomain.ReleaseCommitting}, summary, now)
}

// RecordCatalogFinalizerFailure persists the Server-owned Catalog commit
// finalizer outcome. A transient failure keeps the release in Committing (or
// Installing for a source-side finalizer) and schedules the next pass from the
// durable timestamp. A deterministic failure closes the release as Failed.
func (d *CatalogRepository) RecordCatalogFinalizerFailure(ctx context.Context, releaseID string, diagnostic publication.Diagnostic) (*catalogdomain.Release, error) {
	if strings.TrimSpace(releaseID) == "" || diagnostic.Validate() != nil {
		return nil, errors.New("catalog finalizer diagnostic is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin catalog finalizer failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	release, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, strings.TrimSpace(releaseID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock catalog finalizer release: %w", err)
	}
	if release.State != catalogdomain.ReleaseInstalling && release.State != catalogdomain.ReleaseCommitting {
		return nil, ErrCatalogLeaseLost
	}
	state := release.State
	nextRunAt := diagnostic.LastAttemptedAt.UTC()
	if diagnostic.NextRetryAt != nil {
		nextRunAt = diagnostic.NextRetryAt.UTC()
	}
	if diagnostic.Category == publication.CategoryDeterministic {
		state = catalogdomain.ReleaseFailed
	}
	updated, err := scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases SET state = ?, next_run_at = ?, last_error = ?,
		finalizer_error_category = ?, finalizer_last_error = ?, finalizer_last_attempted_at = ?, finalizer_next_retry_at = ?, updated_at = ?
		WHERE id = ? AND state IN (?, ?) RETURNING `+catalogReleaseColumns,
		state, nextRunAt, diagnostic.LastError, diagnostic.Category, diagnostic.LastError, diagnostic.LastAttemptedAt.UTC(), func() any {
			if diagnostic.NextRetryAt == nil {
				return nil
			}
			return diagnostic.NextRetryAt.UTC()
		}(), diagnostic.LastAttemptedAt.UTC(), release.ID, catalogdomain.ReleaseInstalling, catalogdomain.ReleaseCommitting))
	if err != nil {
		return nil, fmt.Errorf("persist catalog finalizer failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit catalog finalizer failure: %w", err)
	}
	return updated, nil
}

func (d *CatalogRepository) failReleaseState(ctx context.Context, releaseID string, allowed []catalogdomain.ReleaseState, summary string, now time.Time) (*catalogdomain.Release, error) {
	if strings.TrimSpace(releaseID) == "" || strings.TrimSpace(summary) == "" || now.IsZero() || len(allowed) == 0 {
		return nil, errors.New("catalog release failure is invalid")
	}
	values := make([]any, 0, len(allowed)+3)
	values = append(values, catalogdomain.ReleaseFailed, strings.TrimSpace(summary), now.UTC(), strings.TrimSpace(releaseID))
	for _, state := range allowed {
		values = append(values, state)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(allowed)), ",")
	updated, err := scanCatalogRelease(d.conn.QueryRowContext(ctx, `UPDATE catalog_releases SET state = ?, last_error = ?, updated_at = ?
		WHERE id = ? AND state IN (`+placeholders+`) RETURNING `+catalogReleaseColumns, values...))
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

// CatalogBootstrapState returns the complete baseline decision input. It does
// not infer cleanup from provider fields: Runtime Worker must have completed
// every discovered reap before a failed bootstrap stops blocking a new
// digest.
func (d *CatalogRepository) CatalogBootstrapState(ctx context.Context) (catalogdomain.BootstrapState, error) {
	rows, err := d.conn.QueryContext(ctx, catalogReleaseSelect+` ORDER BY created_at, id`)
	if err != nil {
		return catalogdomain.BootstrapState{}, fmt.Errorf("list catalog bootstrap releases: %w", err)
	}
	defer func() { _ = rows.Close() }()
	releases := make([]catalogdomain.Release, 0)
	for rows.Next() {
		release, scanErr := scanCatalogRelease(rows)
		if scanErr != nil {
			return catalogdomain.BootstrapState{}, fmt.Errorf("scan catalog bootstrap release: %w", scanErr)
		}
		releases = append(releases, *release)
	}
	if err := rows.Err(); err != nil {
		return catalogdomain.BootstrapState{}, fmt.Errorf("iterate catalog bootstrap releases: %w", err)
	}
	state := catalogdomain.BootstrapState{Releases: releases}
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM challenges`).Scan(&state.PublishedChallengeCount); err != nil {
		return catalogdomain.BootstrapState{}, fmt.Errorf("count published challenges for catalog bootstrap: %w", err)
	}
	if err := d.conn.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM catalog_runtime_resource_reaps reap
		JOIN catalog_releases release ON release.id = reap.release_id
		WHERE release.state = ? AND reap.state <> ?
	)`, catalogdomain.ReleaseFailed, resourceReapCompleted).Scan(&state.FailedCleanupPending); err != nil {
		return catalogdomain.BootstrapState{}, fmt.Errorf("read failed catalog cleanup state: %w", err)
	}
	return state, nil
}

func (d *CatalogRepository) Entries(ctx context.Context, releaseID string) ([]catalogdomain.Entry, error) {
	return listCatalogEntries(ctx, d.conn, releaseID)
}

func (d *CatalogRepository) Entry(ctx context.Context, id string) (*catalogdomain.Entry, error) {
	entry, err := scanCatalogEntry(d.conn.QueryRowContext(ctx, catalogEntrySelect+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogEntryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read catalog entry: %w", err)
	}
	return entry, nil
}

// ClaimCatalogRuntimeAction exposes exactly one lease-fenced Catalog Entry or
// Commit external action. It never reads staged source bytes or calls a
// provider; those remain respectively in Server and Runtime Worker.
func (d *CatalogRepository) ClaimCatalogRuntimeAction(ctx context.Context, owner string, leaseTTL time.Duration, now time.Time) (*runtime.Context, error) {
	if strings.TrimSpace(owner) == "" || leaseTTL <= 0 || now.IsZero() {
		return nil, errors.New("catalog runtime action claim is invalid")
	}
	now = now.UTC()
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin catalog runtime action claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var entryID string
	err = tx.QueryRowContext(ctx, `SELECT entry.id FROM catalog_release_entries entry
		JOIN catalog_releases release ON release.id = entry.release_id
		WHERE release.state = ? AND entry.state IN (?, ?, ?) AND entry.next_run_at <= ?
		AND (entry.lease_expires_at IS NULL OR entry.lease_expires_at <= ?)
		ORDER BY entry.next_run_at, entry.created_at, entry.id FOR UPDATE OF entry SKIP LOCKED LIMIT 1`,
		catalogdomain.ReleaseInstalling, catalogdomain.EntryBuilding, catalogdomain.EntryArtifactPublishing, catalogdomain.EntryVerifying, now, now).Scan(&entryID)
	if err == nil {
		entry, loadErr := scanCatalogEntry(tx.QueryRowContext(ctx, catalogEntrySelect+` WHERE id = ? FOR UPDATE`, entryID))
		if loadErr != nil {
			return nil, loadErr
		}
		release, loadErr := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, entry.ReleaseID))
		if loadErr != nil {
			return nil, loadErr
		}
		if entry.LeaseExpires != nil && !entry.LeaseExpires.After(now) {
			if err := recoverExpiredCatalogEntryRuntimeLeaseTx(ctx, tx, *release, *entry, now); err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, fmt.Errorf("commit expired catalog entry lease recovery: %w", err)
			}
			return nil, nil
		}
		leaseOwner := catalogLeaseOwner(owner)
		entry, loadErr = scanCatalogEntry(tx.QueryRowContext(ctx, `UPDATE catalog_release_entries
			SET lease_owner = ?, lease_expires_at = ?, last_error = '', updated_at = ?
			WHERE id = ? RETURNING `+catalogEntryColumns, leaseOwner, now.Add(leaseTTL), now, entry.ID))
		if loadErr != nil {
			return nil, fmt.Errorf("claim catalog entry action: %w", loadErr)
		}
		action, actionErr := catalogEntryAction(*release, *entry)
		if actionErr != nil {
			return nil, actionErr
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit catalog entry action claim: %w", err)
		}
		return action, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("select catalog entry action: %w", err)
	}

	var commitID string
	err = tx.QueryRowContext(ctx, `SELECT commit.id FROM catalog_release_entry_commits commit
		JOIN catalog_releases release ON release.id = commit.release_id
		WHERE release.state = ? AND commit.state = ? AND commit.next_run_at <= ?
		AND (commit.lease_expires_at IS NULL OR commit.lease_expires_at <= ?)
		ORDER BY commit.next_run_at, commit.created_at, commit.id FOR UPDATE OF commit SKIP LOCKED LIMIT 1`,
		catalogdomain.ReleaseCommitting, catalogdomain.CommitPrepared, now, now).Scan(&commitID)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty catalog action claim: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select catalog commit action: %w", err)
	}
	commit, err := scanCatalogCommit(tx.QueryRowContext(ctx, catalogCommitSelect+` WHERE id = ? FOR UPDATE`, commitID))
	if err != nil {
		return nil, err
	}
	entry, err := scanCatalogEntry(tx.QueryRowContext(ctx, catalogEntrySelect+` WHERE id = ?`, commit.EntryID))
	if err != nil {
		return nil, err
	}
	release, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, commit.ReleaseID))
	if err != nil {
		return nil, err
	}
	if commit.LeaseExpires != nil && !commit.LeaseExpires.After(now) {
		if err := recoverExpiredCatalogCommitRuntimeLeaseTx(ctx, tx, *release, *commit, now); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit expired catalog commit lease recovery: %w", err)
		}
		return nil, nil
	}
	leaseOwner := catalogLeaseOwner(owner)
	commit, err = scanCatalogCommit(tx.QueryRowContext(ctx, `UPDATE catalog_release_entry_commits
		SET lease_owner = ?, lease_expires_at = ?, last_error = '', updated_at = ?
		WHERE id = ? RETURNING `+catalogCommitColumns, leaseOwner, now.Add(leaseTTL), now, commit.ID))
	if err != nil {
		return nil, fmt.Errorf("claim catalog commit action: %w", err)
	}
	action, err := catalogCommitAction(*release, *entry, *commit)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit catalog commit action claim: %w", err)
	}
	return action, nil
}

func (d *CatalogRepository) GetCatalogRuntimeAction(ctx context.Context, credential runtime.Credential, now time.Time) (*runtime.Context, error) {
	if !credential.Valid() || now.IsZero() {
		return nil, errors.New("catalog runtime action credentials are invalid")
	}
	switch credential.Identity.Scope {
	case runtime.ScopeCatalogEntry:
		entry, err := scanCatalogEntry(d.conn.QueryRowContext(ctx, catalogEntrySelect+` WHERE id = ?`, credential.Identity.OwnerID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, runtime.ErrActionNotFound
		}
		if err != nil {
			return nil, err
		}
		release, err := d.Release(ctx, entry.ReleaseID)
		if err != nil {
			return nil, err
		}
		action, err := catalogEntryAction(*release, *entry)
		if err != nil {
			return nil, err
		}
		if action.Identity != credential.Identity || action.LeaseOwner != credential.LeaseOwner || entry.LeaseExpires == nil || !entry.LeaseExpires.After(now.UTC()) {
			return nil, runtime.ErrLeaseLost
		}
		return action, nil
	case runtime.ScopeCatalogCommit:
		commit, err := scanCatalogCommit(d.conn.QueryRowContext(ctx, catalogCommitSelect+` WHERE id = ?`, credential.Identity.OwnerID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, runtime.ErrActionNotFound
		}
		if err != nil {
			return nil, err
		}
		entry, err := scanCatalogEntry(d.conn.QueryRowContext(ctx, catalogEntrySelect+` WHERE id = ?`, commit.EntryID))
		if err != nil {
			return nil, err
		}
		release, err := d.Release(ctx, commit.ReleaseID)
		if err != nil {
			return nil, err
		}
		action, err := catalogCommitAction(*release, *entry, *commit)
		if err != nil {
			return nil, err
		}
		if action.Identity != credential.Identity || action.LeaseOwner != credential.LeaseOwner || commit.LeaseExpires == nil || !commit.LeaseExpires.After(now.UTC()) {
			return nil, runtime.ErrLeaseLost
		}
		return action, nil
	default:
		return nil, runtime.ErrActionNotFound
	}
}

func (d *CatalogRepository) RenewCatalogRuntimeLease(ctx context.Context, credential runtime.Credential, leaseTTL time.Duration, now time.Time) error {
	if !credential.Valid() || leaseTTL <= 0 || now.IsZero() {
		return errors.New("catalog runtime lease renewal is invalid")
	}
	now = now.UTC()
	var query string
	var releaseState catalogdomain.ReleaseState
	var actionState any
	switch credential.Identity.Scope {
	case runtime.ScopeCatalogEntry:
		query = `UPDATE catalog_release_entries entry SET lease_expires_at = ?, updated_at = ?
			FROM catalog_releases release WHERE entry.id = ? AND entry.release_id = release.id AND release.state = ?
			AND entry.state = ? AND entry.state_version = ? AND entry.lease_owner = ? AND entry.lease_expires_at > ?`
		releaseState = catalogdomain.ReleaseInstalling
		actionState = string(credential.Identity.State)
	case runtime.ScopeCatalogCommit:
		query = `UPDATE catalog_release_entry_commits commit SET lease_expires_at = ?, updated_at = ?
			FROM catalog_releases release WHERE commit.id = ? AND commit.release_id = release.id AND release.state = ?
			AND commit.state = ? AND commit.state_version = ? AND commit.lease_owner = ? AND commit.lease_expires_at > ?`
		releaseState = catalogdomain.ReleaseCommitting
		actionState = catalogdomain.CommitPrepared
	default:
		return runtime.ErrActionNotFound
	}
	result, err := d.conn.ExecContext(ctx, query, now.Add(leaseTTL), now, credential.Identity.OwnerID, releaseState,
		actionState, credential.Identity.StateVersion, credential.LeaseOwner, now)
	if err != nil {
		return fmt.Errorf("renew catalog runtime lease: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return runtime.ErrLeaseLost
	}
	return nil
}

func (d *CatalogRepository) CompleteCatalogBuild(ctx context.Context, action runtime.Context, output execution.BuildOutput, now time.Time) error {
	if action.Identity.Scope != runtime.ScopeCatalogEntry || action.Identity.State != runtime.StateBuilding || output.Validate(action.Snapshot.Runtime) != nil || now.IsZero() {
		return errors.New("catalog build completion is invalid")
	}
	encoded, err := marshalJSON(output)
	if err != nil {
		return err
	}
	return d.transitionCatalogEntry(ctx, action, catalogdomain.EntryBuilding, catalogdomain.EntryArtifactPublishing, now,
		`build_output = ?::jsonb`, encoded)
}

func (d *CatalogRepository) CompleteCatalogArtifactPublish(ctx context.Context, action runtime.Context, artifact execution.ArtifactReference, now time.Time) error {
	if action.Identity.Scope != runtime.ScopeCatalogEntry || action.Identity.State != runtime.StateArtifactPublishing || artifact.Validate(action.Snapshot.Runtime) != nil || now.IsZero() {
		return errors.New("catalog artifact publication is invalid")
	}
	encoded, err := marshalJSON(artifact)
	if err != nil {
		return err
	}
	return d.transitionCatalogEntry(ctx, action, catalogdomain.EntryArtifactPublishing, catalogdomain.EntryVerifying, now,
		`artifact_reference = ?::jsonb`, encoded)
}

func (d *CatalogRepository) RecordCatalogVerificationEnvironment(ctx context.Context, action runtime.Context, environment execution.VerificationEnvironment, now time.Time) error {
	if action.Identity.Scope != runtime.ScopeCatalogEntry || action.Identity.State != runtime.StateVerifying || environment.Validate(action.Snapshot.Runtime) != nil || environment.WorkflowID != action.Identity.OwnerID || now.IsZero() {
		return errors.New("catalog verification environment is invalid")
	}
	encoded, err := marshalJSON(environment)
	if err != nil {
		return err
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE catalog_release_entries entry SET verify_environment = ?::jsonb, updated_at = ?
		FROM catalog_releases release WHERE entry.id = ? AND entry.release_id = release.id AND release.state = ? AND entry.state = ?
		AND entry.state_version = ? AND entry.lease_owner = ? AND entry.lease_expires_at > ?`,
		encoded, now.UTC(), action.Identity.OwnerID, catalogdomain.ReleaseInstalling, catalogdomain.EntryVerifying,
		action.Identity.StateVersion, action.LeaseOwner, now.UTC())
	if err != nil {
		return fmt.Errorf("record catalog verification environment: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return runtime.ErrLeaseLost
	}
	return nil
}

func (d *CatalogRepository) CompleteCatalogVerification(ctx context.Context, action runtime.Context, report execution.VerificationReport, now time.Time) error {
	if action.Identity.Scope != runtime.ScopeCatalogEntry || action.Identity.State != runtime.StateVerifying || !report.Passed || report.Validate(action.Snapshot) != nil || now.IsZero() {
		return errors.New("catalog verification completion is invalid")
	}
	encoded, err := marshalJSON(report)
	if err != nil {
		return err
	}
	return d.transitionCatalogEntry(ctx, action, catalogdomain.EntryVerifying, catalogdomain.EntryReadyToCommit, now,
		`verification_report = ?::jsonb`, encoded)
}

func (d *CatalogRepository) CompleteCatalogChallengePublication(ctx context.Context, action runtime.Context, artifact execution.ArtifactReference, now time.Time) error {
	if action.Identity.Scope != runtime.ScopeCatalogCommit || action.Identity.State != runtime.StateChallengePublishing || artifact.Validate(action.Snapshot.Runtime) != nil || now.IsZero() {
		return errors.New("catalog challenge publication is invalid")
	}
	encoded, err := marshalJSON(artifact)
	if err != nil {
		return err
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE catalog_release_entry_commits commit SET state = ?, state_version = state_version + 1,
		runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL, artifact_reference = ?::jsonb, last_error = '', updated_at = ?
		FROM catalog_releases release WHERE commit.id = ? AND commit.release_id = release.id AND release.state = ? AND commit.state = ?
		AND commit.state_version = ? AND commit.lease_owner = ? AND commit.lease_expires_at > ?`,
		catalogdomain.CommitArtifactPublished, encoded, now.UTC(), action.Identity.OwnerID, catalogdomain.ReleaseCommitting,
		catalogdomain.CommitPrepared, action.Identity.StateVersion, action.LeaseOwner, now.UTC())
	if err != nil {
		return fmt.Errorf("complete catalog challenge publication: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return runtime.ErrLeaseLost
	}
	return nil
}

func (d *CatalogRepository) ReportCatalogRuntimeInfrastructureFailure(ctx context.Context, action runtime.Context, failure runtime.Failure, now time.Time) error {
	if failure.Class != runtime.FailureInfrastructure || failure.Validate() != nil || now.IsZero() {
		return errors.New("catalog runtime infrastructure failure is invalid")
	}
	return d.reportCatalogRuntimeFailure(ctx, action, failure.Summary, nil, false, now)
}

func (d *CatalogRepository) ReportCatalogRuntimeArtifactFailure(ctx context.Context, action runtime.Context, failure runtime.Failure, report *execution.VerificationReport, now time.Time) error {
	if failure.Class != runtime.FailureArtifact || failure.Validate() != nil || now.IsZero() {
		return errors.New("catalog runtime artifact failure is invalid")
	}
	if report != nil && report.Validate(action.Snapshot) != nil {
		return errors.New("catalog artifact failure report is invalid")
	}
	return d.reportCatalogRuntimeFailure(ctx, action, failure.Summary, report, true, now)
}

func (d *CatalogRepository) reportCatalogRuntimeFailure(ctx context.Context, action runtime.Context, summary string, report *execution.VerificationReport, deterministic bool, now time.Time) error {
	if err := action.Valid(); err != nil || strings.TrimSpace(summary) == "" {
		return errors.New("catalog runtime failure action is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	switch action.Identity.Scope {
	case runtime.ScopeCatalogEntry:
		entry, err := scanCatalogEntry(tx.QueryRowContext(ctx, catalogEntrySelect+` WHERE id = ? FOR UPDATE`, action.Identity.OwnerID))
		if errors.Is(err, sql.ErrNoRows) {
			return runtime.ErrActionNotFound
		}
		if err != nil {
			return err
		}
		release, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, entry.ReleaseID))
		if err != nil {
			return err
		}
		if release.State != catalogdomain.ReleaseInstalling || !entryActionMatches(*entry, action, now) {
			return runtime.ErrLeaseLost
		}
		if deterministic || entry.RuntimeAttempt >= 5 {
			var encoded any
			if report != nil {
				encoded, err = marshalJSON(report)
				if err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, `UPDATE catalog_release_entries SET state = ?, state_version = state_version + 1,
				runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL, verification_report = COALESCE(?::jsonb, verification_report), last_error = ?, updated_at = ? WHERE id = ?`,
				catalogdomain.EntryFailed, encoded, strings.TrimSpace(summary), now.UTC(), entry.ID); err != nil {
				return fmt.Errorf("fail catalog entry: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE catalog_releases SET state = ?, last_error = ?, updated_at = ? WHERE id = ?`,
				catalogdomain.ReleaseFailed, strings.TrimSpace(summary), now.UTC(), release.ID); err != nil {
				return fmt.Errorf("fail catalog release: %w", err)
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE catalog_release_entries SET runtime_attempt = runtime_attempt + 1,
			next_run_at = ?, lease_owner = '', lease_expires_at = NULL, last_error = ?, updated_at = ? WHERE id = ?`,
			catalogRetryAt(entry.RuntimeAttempt+1, now.UTC()), strings.TrimSpace(summary), now.UTC(), entry.ID); err != nil {
			return fmt.Errorf("retry catalog entry: %w", err)
		}

	case runtime.ScopeCatalogCommit:
		commit, err := scanCatalogCommit(tx.QueryRowContext(ctx, catalogCommitSelect+` WHERE id = ? FOR UPDATE`, action.Identity.OwnerID))
		if errors.Is(err, sql.ErrNoRows) {
			return runtime.ErrActionNotFound
		}
		if err != nil {
			return err
		}
		release, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, commit.ReleaseID))
		if err != nil {
			return err
		}
		if release.State != catalogdomain.ReleaseCommitting || !commitActionMatches(*commit, action, now) {
			return runtime.ErrLeaseLost
		}
		if deterministic || commit.RuntimeAttempt >= 5 {
			if _, err := tx.ExecContext(ctx, `UPDATE catalog_release_entry_commits SET state = ?, state_version = state_version + 1,
				runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL, last_error = ?, updated_at = ? WHERE id = ?`,
				catalogdomain.CommitFailed, strings.TrimSpace(summary), now.UTC(), commit.ID); err != nil {
				return fmt.Errorf("fail catalog commit: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE catalog_releases SET state = ?, last_error = ?, updated_at = ? WHERE id = ?`,
				catalogdomain.ReleaseFailed, strings.TrimSpace(summary), now.UTC(), release.ID); err != nil {
				return fmt.Errorf("fail catalog release: %w", err)
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE catalog_release_entry_commits SET runtime_attempt = runtime_attempt + 1,
			next_run_at = ?, lease_owner = '', lease_expires_at = NULL, last_error = ?, updated_at = ? WHERE id = ?`,
			catalogRetryAt(commit.RuntimeAttempt+1, now.UTC()), strings.TrimSpace(summary), now.UTC(), commit.ID); err != nil {
			return fmt.Errorf("retry catalog commit: %w", err)
		}
	default:
		return runtime.ErrActionNotFound
	}
	return tx.Commit()
}

// PrepareReleaseCommit reserves stable challenge identities only after all
// Entry runtime actions have verified. Each commit then becomes a separately
// claimable ChallengePublishing action for Runtime Worker.
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
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM catalog_release_entries WHERE release_id = ? AND state <> ?)`, release.ID, catalogdomain.EntryReadyToCommit).Scan(&unfinished); err != nil {
		return nil, nil, fmt.Errorf("check catalog entry readiness: %w", err)
	}
	if unfinished {
		return nil, nil, errors.New("catalog release has unverified entries")
	}
	seen := make(map[string]struct{}, len(intents))
	for _, commit := range intents {
		if commit.ID != catalogdomain.EntryCommitIDFor(release.ID, commit.EntryID) || commit.ReleaseID != release.ID || commit.State != catalogdomain.CommitPrepared || commit.StateVersion != 1 || commit.RuntimeAttempt != 1 || !commit.Valid() {
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
			(id, release_id, entry_id, challenge_id, challenge_revision_id, source_slug, state, state_version, runtime_attempt, lease_owner, lease_expires_at, next_run_at, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', NULL, ?, '', ?, ?)`,
			commit.ID, commit.ReleaseID, commit.EntryID, commit.ChallengeID, commit.ChallengeRevisionID, commit.SourceSlug, commit.State, commit.StateVersion,
			commit.RuntimeAttempt, commit.NextRunAt.UTC(), commit.CreatedAt.UTC(), commit.UpdatedAt.UTC()); err != nil {
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
	release, err = scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases SET state = ?, commit_id = ?, last_error = '',
		finalizer_error_category = '', finalizer_last_error = '', finalizer_last_attempted_at = NULL, finalizer_next_retry_at = NULL, updated_at = ?
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
	return scanCatalogCommits(rows)
}

// MarkCommitMaterialized is the Server-owned, idempotent finalizer boundary.
// It is deliberately separate from final artifact promotion so a Server crash
// can resume source materialization without invoking Runtime Worker again.
func (d *CatalogRepository) MarkCommitMaterialized(ctx context.Context, releaseID, commitID string, now time.Time) (*catalogdomain.Commit, error) {
	if strings.TrimSpace(releaseID) == "" || strings.TrimSpace(commitID) == "" || now.IsZero() {
		return nil, errors.New("catalog materialization completion is invalid")
	}
	updated, err := scanCatalogCommit(d.conn.QueryRowContext(ctx, `UPDATE catalog_release_entry_commits commit SET state = ?, state_version = state_version + 1,
		runtime_attempt = 0, materialized_at = ?, updated_at = ? FROM catalog_releases release
		WHERE commit.id = ? AND commit.release_id = ? AND commit.release_id = release.id AND release.state = ? AND commit.state = ?
		RETURNING `+catalogCommitReturningColumns, catalogdomain.CommitMaterialized, now.UTC(), now.UTC(), strings.TrimSpace(commitID), strings.TrimSpace(releaseID),
		catalogdomain.ReleaseCommitting, catalogdomain.CommitArtifactPublished))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, runtime.ErrLeaseLost
	}
	if err != nil {
		return nil, fmt.Errorf("mark catalog commit materialized: %w", err)
	}
	return updated, nil
}

// CompleteReleaseCommit is the sole visibility transition. It atomically
// publishes the Roadmap revision and commits every already-materialized entry.
func (d *CatalogRepository) CompleteReleaseCommit(ctx context.Context, releaseID string, value roadmap.Revision, now time.Time) (*catalogdomain.Release, error) {
	if strings.TrimSpace(releaseID) == "" || now.IsZero() {
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
	release, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, strings.TrimSpace(releaseID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, err
	}
	if release.State != catalogdomain.ReleaseCommitting {
		return nil, runtime.ErrLeaseLost
	}
	if _, err := tx.ExecContext(ctx, `SELECT revision_id FROM roadmap_current WHERE singleton = TRUE FOR UPDATE`); err != nil {
		return nil, fmt.Errorf("lock roadmap current revision: %w", err)
	}
	if err := ensureRoadmapPublicationAllowedTx(ctx, tx, now); err != nil {
		return nil, err
	}
	var publishedChallenges int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM challenges`).Scan(&publishedChallenges); err != nil {
		return nil, fmt.Errorf("count challenges before catalog baseline commit: %w", err)
	}
	if publishedChallenges != 0 {
		return nil, catalogdomain.ErrBaselineEstablished
	}
	var otherReadyReleases int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_releases WHERE state = ? AND id <> ?`,
		catalogdomain.ReleaseReady, release.ID).Scan(&otherReadyReleases); err != nil {
		return nil, fmt.Errorf("count existing catalog baselines: %w", err)
	}
	if otherReadyReleases != 0 {
		return nil, catalogdomain.ErrBaselineEstablished
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
		if !exists || binding.Challenge.RevisionID != commit.ChallengeRevisionID || binding.Challenge.SourceRef != entry.SourceRef ||
			binding.Challenge.Title != entry.Title || binding.Challenge.ContentRevision != string(entry.ContentRevision) ||
			binding.Challenge.SourceSlug != commit.SourceSlug || binding.Challenge.MaterializedRevision == "" ||
			commit.Artifact == nil || commit.Artifact.Validate(entry.Snapshot.Runtime) != nil {
			return nil, fmt.Errorf("catalog commit %q does not match its roadmap binding", commit.EntryID)
		}
	}
	for _, commit := range commits {
		entry := entriesByID[commit.EntryID]
		binding := bindingsByChallengeID[commit.ChallengeID]
		stable := challengedomain.Challenge{
			ID: commit.ChallengeID, SourceKind: challengedomain.SourceRelease, SourceRef: entry.SourceRef,
			State: challengedomain.StateActive, ActiveRevisionID: commit.ChallengeRevisionID, SourceSlug: commit.SourceSlug,
			CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
		}
		published := challengeRevisionFromPublication(entry.Title, entry.Snapshot.Runtime, entry.Type, entry.Tags, string(entry.ContentRevision), binding.Challenge.MaterializedRevision,
			*commit.Artifact, commit.ChallengeRevisionID, commit.ChallengeID, entry.SourceRef, string(entry.ContentRevision), "",
			commit.SourceSlug, contentchallenge.MaterializedPath(commit.SourceSlug, commit.ChallengeRevisionID), challengedomain.SourceRelease, now.UTC())
		if err := insertPersistedChallengeTx(ctx, tx, stable); err != nil {
			return nil, fmt.Errorf("create catalog challenge %q: %w", commit.ChallengeID, err)
		}
		if err := insertPersistedChallengeRevisionTx(ctx, tx, published); err != nil {
			return nil, fmt.Errorf("create catalog challenge revision %q: %w", commit.ChallengeRevisionID, err)
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
	updated, err := scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases SET state = ?, last_error = '',
		finalizer_error_category = '', finalizer_last_error = '', finalizer_last_attempted_at = NULL, finalizer_next_retry_at = NULL, updated_at = ?
		WHERE id = ? RETURNING `+catalogReleaseColumns, catalogdomain.ReleaseReady, now.UTC(), release.ID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	canonical.Revision = revisionID
	return updated, nil
}

func (d *CatalogRepository) transitionCatalogEntry(ctx context.Context, action runtime.Context, expected, next catalogdomain.EntryState, now time.Time, assignment string, args ...any) error {
	values := make([]any, 0, len(args)+11)
	values = append(values, next, next, catalogdomain.EntryReadyToCommit, now.UTC())
	values = append(values, args...)
	values = append(values, now.UTC(), action.Identity.OwnerID, catalogdomain.ReleaseInstalling, expected, action.Identity.StateVersion, action.LeaseOwner, now.UTC())
	result, err := d.conn.ExecContext(ctx, `UPDATE catalog_release_entries entry SET state = ?, state_version = state_version + 1,
		runtime_attempt = CASE WHEN ? = ? THEN 0 ELSE 1 END, lease_owner = '', lease_expires_at = NULL, next_run_at = ?, `+assignment+`, last_error = '', updated_at = ?
		FROM catalog_releases release WHERE entry.id = ? AND entry.release_id = release.id AND release.state = ? AND entry.state = ?
		AND entry.state_version = ? AND entry.lease_owner = ? AND entry.lease_expires_at > ?`,
		values...)
	if err != nil {
		return fmt.Errorf("transition catalog entry: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return runtime.ErrLeaseLost
	}
	return nil
}

const runtimeLeaseExpiredSummary = "runtime worker lease expired"

// recoverExpiredCatalogEntryRuntimeLeaseTx charges the same state-scoped
// infrastructure budget as an explicit Worker failure. StateVersion remains
// unchanged on retry so the next Worker must create-or-get the same external
// resource identity.
func recoverExpiredCatalogEntryRuntimeLeaseTx(ctx context.Context, tx *Tx, release catalogdomain.Release, entry catalogdomain.Entry, now time.Time) error {
	if release.State != catalogdomain.ReleaseInstalling || !entry.State.Leaseable() || entry.LeaseExpires == nil || entry.LeaseExpires.After(now) {
		return runtime.ErrLeaseLost
	}
	if entry.RuntimeAttempt >= runtime.MaxAttempts {
		result, err := tx.ExecContext(ctx, `UPDATE catalog_release_entries SET state = ?, state_version = state_version + 1,
			runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL, last_error = ?, updated_at = ?
			WHERE id = ? AND release_id = ? AND state = ? AND state_version = ? AND lease_owner = ? AND lease_expires_at <= ?`,
			catalogdomain.EntryFailed, runtimeLeaseExpiredSummary, now.UTC(), entry.ID, release.ID, entry.State,
			entry.StateVersion, entry.LeaseOwner, now.UTC())
		if err != nil {
			return fmt.Errorf("fail exhausted catalog entry runtime lease: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return runtime.ErrLeaseLost
		}
		result, err = tx.ExecContext(ctx, `UPDATE catalog_releases SET state = ?, last_error = ?, updated_at = ?
			WHERE id = ? AND state = ?`, catalogdomain.ReleaseFailed, runtimeLeaseExpiredSummary, now.UTC(), release.ID, catalogdomain.ReleaseInstalling)
		if err != nil {
			return fmt.Errorf("fail catalog release after exhausted entry lease: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return runtime.ErrLeaseLost
		}
		return nil
	}
	nextAttempt := entry.RuntimeAttempt + 1
	result, err := tx.ExecContext(ctx, `UPDATE catalog_release_entries SET runtime_attempt = ?, lease_owner = '', lease_expires_at = NULL,
		next_run_at = ?, last_error = ?, updated_at = ?
		WHERE id = ? AND release_id = ? AND state = ? AND state_version = ? AND lease_owner = ? AND lease_expires_at <= ?`,
		nextAttempt, catalogRetryAt(nextAttempt, now.UTC()), runtimeLeaseExpiredSummary, now.UTC(), entry.ID, release.ID,
		entry.State, entry.StateVersion, entry.LeaseOwner, now.UTC())
	if err != nil {
		return fmt.Errorf("recover expired catalog entry runtime lease: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return runtime.ErrLeaseLost
	}
	return nil
}

// recoverExpiredCatalogCommitRuntimeLeaseTx applies the same bounded retry
// rule to the final artifact promotion action. The commit's persisted state is
// Prepared while the shared Runtime Action state is ChallengePublishing.
func recoverExpiredCatalogCommitRuntimeLeaseTx(ctx context.Context, tx *Tx, release catalogdomain.Release, commit catalogdomain.Commit, now time.Time) error {
	if release.State != catalogdomain.ReleaseCommitting || commit.State != catalogdomain.CommitPrepared || commit.LeaseExpires == nil || commit.LeaseExpires.After(now) {
		return runtime.ErrLeaseLost
	}
	if commit.RuntimeAttempt >= runtime.MaxAttempts {
		result, err := tx.ExecContext(ctx, `UPDATE catalog_release_entry_commits SET state = ?, state_version = state_version + 1,
			runtime_attempt = 0, lease_owner = '', lease_expires_at = NULL, last_error = ?, updated_at = ?
			WHERE id = ? AND release_id = ? AND state = ? AND state_version = ? AND lease_owner = ? AND lease_expires_at <= ?`,
			catalogdomain.CommitFailed, runtimeLeaseExpiredSummary, now.UTC(), commit.ID, release.ID, catalogdomain.CommitPrepared,
			commit.StateVersion, commit.LeaseOwner, now.UTC())
		if err != nil {
			return fmt.Errorf("fail exhausted catalog commit runtime lease: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return runtime.ErrLeaseLost
		}
		result, err = tx.ExecContext(ctx, `UPDATE catalog_releases SET state = ?, last_error = ?, updated_at = ?
			WHERE id = ? AND state = ?`, catalogdomain.ReleaseFailed, runtimeLeaseExpiredSummary, now.UTC(), release.ID, catalogdomain.ReleaseCommitting)
		if err != nil {
			return fmt.Errorf("fail catalog release after exhausted commit lease: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return runtime.ErrLeaseLost
		}
		return nil
	}
	nextAttempt := commit.RuntimeAttempt + 1
	result, err := tx.ExecContext(ctx, `UPDATE catalog_release_entry_commits SET runtime_attempt = ?, lease_owner = '', lease_expires_at = NULL,
		next_run_at = ?, last_error = ?, updated_at = ?
		WHERE id = ? AND release_id = ? AND state = ? AND state_version = ? AND lease_owner = ? AND lease_expires_at <= ?`,
		nextAttempt, catalogRetryAt(nextAttempt, now.UTC()), runtimeLeaseExpiredSummary, now.UTC(), commit.ID, release.ID,
		catalogdomain.CommitPrepared, commit.StateVersion, commit.LeaseOwner, now.UTC())
	if err != nil {
		return fmt.Errorf("recover expired catalog commit runtime lease: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return runtime.ErrLeaseLost
	}
	return nil
}

func entryActionMatches(entry catalogdomain.Entry, action runtime.Context, now time.Time) bool {
	return entry.LeaseExpires != nil && entry.LeaseExpires.After(now.UTC()) && entry.LeaseOwner == action.LeaseOwner && entry.StateVersion == action.Identity.StateVersion && string(entry.State) == string(action.Identity.State)
}

func commitActionMatches(commit catalogdomain.Commit, action runtime.Context, now time.Time) bool {
	return commit.LeaseExpires != nil && commit.LeaseExpires.After(now.UTC()) && commit.LeaseOwner == action.LeaseOwner && commit.StateVersion == action.Identity.StateVersion && commit.State == catalogdomain.CommitPrepared && action.Identity.State == runtime.StateChallengePublishing
}

func catalogEntryAction(release catalogdomain.Release, entry catalogdomain.Entry) (*runtime.Context, error) {
	if release.State != catalogdomain.ReleaseInstalling || !entry.State.Leaseable() || strings.TrimSpace(entry.LeaseOwner) == "" {
		return nil, runtime.ErrLeaseLost
	}
	action := runtime.Context{
		Identity:   runtime.Identity{Scope: runtime.ScopeCatalogEntry, OwnerID: entry.ID, ParentID: release.ID, CandidateID: entry.ID, State: runtime.State(entry.State), StateVersion: entry.StateVersion},
		LeaseOwner: entry.LeaseOwner, ArchiveSHA256: entry.ArchiveSHA256, Snapshot: entry.Snapshot,
		Build: entry.Build, Artifact: entry.Artifact, VerificationEnvironment: entry.VerifyEnvironment,
	}
	if err := action.Valid(); err != nil {
		return nil, fmt.Errorf("catalog entry runtime action is invalid: %w", err)
	}
	return &action, nil
}

func catalogCommitAction(release catalogdomain.Release, entry catalogdomain.Entry, commit catalogdomain.Commit) (*runtime.Context, error) {
	if release.State != catalogdomain.ReleaseCommitting || commit.State != catalogdomain.CommitPrepared || strings.TrimSpace(commit.LeaseOwner) == "" {
		return nil, runtime.ErrLeaseLost
	}
	action := runtime.Context{
		Identity:   runtime.Identity{Scope: runtime.ScopeCatalogCommit, OwnerID: commit.ID, ParentID: release.ID, CandidateID: entry.ID, State: runtime.StateChallengePublishing, StateVersion: commit.StateVersion},
		LeaseOwner: commit.LeaseOwner, ArchiveSHA256: entry.ArchiveSHA256, Snapshot: entry.Snapshot, Build: entry.Build,
		Artifact: entry.Artifact, ChallengeID: commit.ChallengeID, ChallengeRevisionID: commit.ChallengeRevisionID,
	}
	if err := action.Valid(); err != nil {
		return nil, fmt.Errorf("catalog commit runtime action is invalid: %w", err)
	}
	return &action, nil
}

// ClaimCatalogResourceReap shares Runtime Worker's cleanup lane with authoring
// candidates. It does not create new business state and may be retried until
// the provider accepts deletion.
func (d *CatalogRepository) ClaimCatalogResourceReap(ctx context.Context, owner string, leaseTTL time.Duration, now time.Time) (*runtime.ReapClaim, error) {
	if strings.TrimSpace(owner) == "" || leaseTTL <= 0 || now.IsZero() {
		return nil, errors.New("catalog resource reap claim is invalid")
	}
	if err := d.EnsureCatalogResourceReaps(ctx, now.UTC()); err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin catalog resource reap claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var entryID string
	var kind runtime.ReapKind
	err = tx.QueryRowContext(ctx, `SELECT entry_id, kind FROM catalog_runtime_resource_reaps
		WHERE state IN (?, ?) AND next_run_at <= ? AND (lease_expires_at IS NULL OR lease_expires_at <= ?)
		ORDER BY next_run_at, CASE kind WHEN 'verification-environment' THEN 0 WHEN 'node-build-image' THEN 1 ELSE 2 END, created_at, entry_id
		FOR UPDATE SKIP LOCKED LIMIT 1`, resourceReapPending, resourceReapRunning, now.UTC(), now.UTC()).Scan(&entryID, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select catalog resource reap: %w", err)
	}
	leaseOwner := strings.TrimSpace(owner) + "-" + catalogLeaseOwner("reap")
	var attempt int
	var deleteFinal bool
	if err := tx.QueryRowContext(ctx, `UPDATE catalog_runtime_resource_reaps SET state = ?, lease_owner = ?, lease_expires_at = ?, updated_at = ?
		WHERE entry_id = ? AND kind = ? RETURNING attempt, delete_final_artifact`, resourceReapRunning, leaseOwner, now.UTC().Add(leaseTTL), now.UTC(), entryID, kind).Scan(&attempt, &deleteFinal); err != nil {
		return nil, fmt.Errorf("claim catalog resource reap: %w", err)
	}
	entry, err := scanCatalogEntry(tx.QueryRowContext(ctx, catalogEntrySelect+` WHERE id = ?`, entryID))
	if err != nil {
		return nil, err
	}
	var final *execution.ArtifactReference
	var challengeID, challengeRevisionID string
	commit, commitErr := scanCatalogCommit(tx.QueryRowContext(ctx, catalogCommitSelect+` WHERE entry_id = ?`, entryID))
	if commitErr == nil {
		final = commit.Artifact
		challengeID = commit.ChallengeID
		challengeRevisionID = commit.ChallengeRevisionID
	} else if !errors.Is(commitErr, sql.ErrNoRows) {
		return nil, commitErr
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit catalog resource reap claim: %w", err)
	}
	reap := runtime.Reap{Scope: runtime.ScopeCatalogEntry, ResourceID: entry.ID, Kind: kind, DeleteFinalArtifact: deleteFinal,
		Snapshot: entry.Snapshot, Build: entry.Build, Artifact: entry.Artifact, FinalArtifact: final,
		VerificationEnvironment: entry.VerifyEnvironment, ChallengeID: challengeID, ChallengeRevisionID: challengeRevisionID}
	if err := reap.Valid(); err != nil {
		return nil, fmt.Errorf("load catalog resource reap: %w", err)
	}
	return &runtime.ReapClaim{Reap: reap, ReapCredential: runtime.ReapCredential{Attempt: attempt, LeaseOwner: leaseOwner}}, nil
}

func (d *CatalogRepository) CompleteCatalogResourceReap(ctx context.Context, claim runtime.ReapClaim, failure string, now time.Time) error {
	if claim.Valid() != nil || claim.Scope != runtime.ScopeCatalogEntry || now.IsZero() {
		return errors.New("catalog resource reap completion is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if strings.TrimSpace(failure) == "" {
		result, err := tx.ExecContext(ctx, `UPDATE catalog_runtime_resource_reaps SET state = ?, lease_owner = '', lease_expires_at = NULL,
			last_error = '', completed_at = ?, updated_at = ? WHERE entry_id = ? AND kind = ? AND state = ? AND attempt = ? AND lease_owner = ?`,
			resourceReapCompleted, now.UTC(), now.UTC(), claim.ResourceID, claim.Kind, resourceReapRunning, claim.Attempt, claim.LeaseOwner)
		if err != nil {
			return fmt.Errorf("complete catalog resource reap: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return runtime.ErrLeaseLost
		}
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE catalog_runtime_resource_reaps SET state = ?, attempt = attempt + 1, lease_owner = '', lease_expires_at = NULL,
			next_run_at = ?, last_error = ?, updated_at = ? WHERE entry_id = ? AND kind = ? AND state = ? AND attempt = ? AND lease_owner = ?`,
			resourceReapPending, catalogRetryAt(claim.Attempt+1, now.UTC()), strings.TrimSpace(failure), now.UTC(), claim.ResourceID, claim.Kind,
			resourceReapRunning, claim.Attempt, claim.LeaseOwner)
		if err != nil {
			return fmt.Errorf("retry catalog resource reap: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return runtime.ErrLeaseLost
		}
	}
	return tx.Commit()
}

func (d *CatalogRepository) EnsureCatalogResourceReaps(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		return errors.New("catalog resource reap discovery requires current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	inactive := `release.state IN ('Ready', 'Failed')`
	queries := []struct {
		kind  runtime.ReapKind
		where string
	}{
		{runtime.ReapVerificationEnvironment, `entry.verify_environment IS NOT NULL AND (entry.verification_report IS NOT NULL OR ` + inactive + `)`},
		{runtime.ReapNodeBuildImage, `entry.build_output IS NOT NULL AND entry.execution_snapshot->>'runtime' = 'node' AND ` + inactive},
		{runtime.ReapCandidateArtifact, `(entry.artifact_reference IS NOT NULL OR entry.build_output IS NOT NULL) AND ` + inactive},
	}
	for _, query := range queries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_runtime_resource_reaps
			(release_id, entry_id, kind, delete_final_artifact, state, attempt, lease_owner, next_run_at, last_error, created_at, updated_at)
			SELECT entry.release_id, entry.id, ?,
				release.state = 'Failed' AND EXISTS (SELECT 1 FROM catalog_release_entry_commits commit WHERE commit.entry_id = entry.id AND commit.artifact_reference IS NOT NULL),
				?, 0, '', ?, '', ?, ?
			FROM catalog_release_entries entry JOIN catalog_releases release ON release.id = entry.release_id WHERE `+query.where+`
			ON CONFLICT (entry_id, kind) DO UPDATE SET delete_final_artifact = catalog_runtime_resource_reaps.delete_final_artifact OR EXCLUDED.delete_final_artifact`,
			query.kind, resourceReapPending, now.UTC(), now.UTC(), now.UTC()); err != nil {
			return fmt.Errorf("discover catalog %s resource reaps: %w", query.kind, err)
		}
	}
	return tx.Commit()
}

func listCatalogEntries(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, releaseID string) ([]catalogdomain.Entry, error) {
	rows, err := queryer.QueryContext(ctx, catalogEntrySelect+` WHERE release_id = ? ORDER BY source_path, id`, strings.TrimSpace(releaseID))
	if err != nil {
		return nil, fmt.Errorf("list catalog release entries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanCatalogEntries(rows)
}

func listCatalogEntriesTx(ctx context.Context, tx *Tx, releaseID string) ([]catalogdomain.Entry, error) {
	return listCatalogEntries(ctx, tx, releaseID)
}

func listCatalogCommitsTx(ctx context.Context, tx *Tx, releaseID string) ([]catalogdomain.Commit, error) {
	rows, err := tx.QueryContext(ctx, catalogCommitSelect+` WHERE release_id = ? ORDER BY entry_id`, releaseID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanCatalogCommits(rows)
}

func scanCatalogEntries(rows *sql.Rows) ([]catalogdomain.Entry, error) {
	entries := make([]catalogdomain.Entry, 0)
	for rows.Next() {
		entry, err := scanCatalogEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, *entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalog release entries: %w", err)
	}
	return entries, nil
}

func scanCatalogCommits(rows *sql.Rows) ([]catalogdomain.Commit, error) {
	commits := make([]catalogdomain.Commit, 0)
	for rows.Next() {
		commit, err := scanCatalogCommit(rows)
		if err != nil {
			return nil, err
		}
		commits = append(commits, *commit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalog release commits: %w", err)
	}
	return commits, nil
}

func scanCatalogRelease(row scanner) (*catalogdomain.Release, error) {
	var value catalogdomain.Release
	var digest, sourceDigest string
	var finalizerLastAttemptedAt, finalizerNextRetryAt sql.NullTime
	if err := row.Scan(&value.ID, &value.Name, &value.Version, &digest, &sourceDigest, &value.State, &value.SourceAttempt, &value.NextRunAt,
		&value.CommitID, &value.LastError, &value.FinalizerErrorCategory, &value.FinalizerLastError, &finalizerLastAttemptedAt, &finalizerNextRetryAt,
		&value.CreatedAt, &value.UpdatedAt); err != nil {
		return nil, err
	}
	value.BundleDigest = catalogdomain.BundleDigest(digest)
	value.SourceDigest = catalogdomain.ContentRevision(sourceDigest)
	if finalizerLastAttemptedAt.Valid {
		at := finalizerLastAttemptedAt.Time.UTC()
		value.FinalizerLastAttemptedAt = &at
	}
	if finalizerNextRetryAt.Valid {
		at := finalizerNextRetryAt.Time.UTC()
		value.FinalizerNextRetryAt = &at
	}
	value.NextRunAt = value.NextRunAt.UTC()
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
	var tags, snapshot, build, artifact, environment, report []byte
	var expires sql.NullTime
	if err := row.Scan(&value.ID, &value.ReleaseID, &value.SourcePath, &value.SourceRef, &value.Title, &value.Type, &tags, &revision, &value.ArchiveSHA256,
		&snapshot, &value.State, &value.StateVersion, &value.RuntimeAttempt, &value.LeaseOwner, &expires, &value.NextRunAt, &build, &artifact,
		&environment, &report, &value.LastError, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return nil, err
	}
	value.ContentRevision = catalogdomain.ContentRevision(revision)
	if err := json.Unmarshal(tags, &value.Tags); err != nil {
		return nil, fmt.Errorf("decode catalog entry tags: %w", err)
	}
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
	var expires, materialized, committed sql.NullTime
	if err := row.Scan(&value.ID, &value.ReleaseID, &value.EntryID, &value.ChallengeID, &value.ChallengeRevisionID, &value.SourceSlug, &value.State, &value.StateVersion,
		&value.RuntimeAttempt, &value.LeaseOwner, &expires, &value.NextRunAt, &value.LastError, &artifact, &materialized, &committed,
		&value.CreatedAt, &value.UpdatedAt); err != nil {
		return nil, err
	}
	if err := decodeOptionalJSON(artifact, &value.Artifact); err != nil {
		return nil, fmt.Errorf("decode catalog commit artifact: %w", err)
	}
	if expires.Valid {
		at := expires.Time.UTC()
		value.LeaseExpires = &at
	}
	if materialized.Valid {
		at := materialized.Time.UTC()
		value.MaterializedAt = &at
	}
	if committed.Valid {
		at := committed.Time.UTC()
		value.CommittedAt = &at
	}
	value.NextRunAt = value.NextRunAt.UTC()
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

func catalogLeaseOwner(prefix string) string {
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		return strings.TrimSpace(prefix) + "-" + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return strings.TrimSpace(prefix) + "-" + hex.EncodeToString(data[:])
}

func catalogRetryAt(attempt int, now time.Time) time.Time {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second * time.Duration(1<<minCatalog(attempt-1, 6))
	if delay > time.Minute {
		delay = time.Minute
	}
	return now.UTC().Add(delay)
}

func minCatalog(left, right int) int {
	if left < right {
		return left
	}
	return right
}
