package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

var (
	ErrCatalogReleaseNotFound = errors.New("catalog release not found")
	ErrCatalogEntryNotFound   = errors.New("catalog release entry not found")
	ErrCatalogCommitNotFound  = errors.New("catalog release entry commit not found")
)

const catalogReleaseColumns = `id, name, version, bundle_digest, taxonomy_content_revision, state,
	deadline_at, last_error, created_at, updated_at`
const catalogReleaseSelect = `SELECT ` + catalogReleaseColumns + ` FROM catalog_releases`

const catalogEntryColumns = `id, release_id, source_path, content_revision, COALESCE(candidate_revision_id, ''),
	state, last_error, created_at, updated_at`
const catalogEntrySelect = `SELECT ` + catalogEntryColumns + ` FROM catalog_release_entries`

const catalogCommitColumns = `entry_id, content_revision, state, challenge_id, slug, materialized_at,
	committed_at, created_at, updated_at`
const catalogCommitSelect = `SELECT ` + catalogCommitColumns + ` FROM catalog_release_entry_commits`

// CreateInstallation records an already-staged portable source and every
// release-owned candidate/workflow in one transaction. The catalog aggregate
// owns this cross-domain write because an entry without its matching immutable
// candidate can never be recovered safely.
func (r *CatalogRepository) CreateInstallation(ctx context.Context, installation catalog.Installation) (*catalog.Release, error) {
	if err := installation.Validate(); err != nil {
		return nil, err
	}
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin catalog installation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(487550140918)`); err != nil {
		return nil, fmt.Errorf("lock catalog installation: %w", err)
	}
	var occupied bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM catalog_releases WHERE state <> ?
	)`, catalog.ReleaseFailed).Scan(&occupied); err != nil {
		return nil, fmt.Errorf("check catalog installation state: %w", err)
	}
	if occupied {
		return nil, errors.New("catalog release installation requires an uninitialized platform")
	}
	release := installation.Release
	if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_releases
		(id, name, version, bundle_digest, taxonomy_content_revision, state, deadline_at, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?)`,
		release.ID, release.Name, release.Version, release.BundleDigest, release.TaxonomyContentRevision,
		release.State, release.DeadlineAt, release.CreatedAt.UTC(), release.UpdatedAt.UTC()); err != nil {
		return nil, fmt.Errorf("insert catalog installation release: %w", err)
	}
	for _, value := range installation.Entries {
		candidate := value.Candidate
		if err := insertCandidateRevisionTx(ctx, tx, candidate); err != nil {
			return nil, err
		}
		entry := value.Entry
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_release_entries
			(id, release_id, source_path, content_revision, candidate_revision_id, state, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)`,
			entry.ID, entry.ReleaseID, entry.SourcePath, entry.ContentRevision, value.Candidate.ID, entry.State, entry.CreatedAt.UTC(), entry.UpdatedAt.UTC()); err != nil {
			return nil, fmt.Errorf("insert catalog installation entry %q: %w", entry.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_release_entry_commits
			(entry_id, content_revision, state, challenge_id, slug, created_at, updated_at)
			VALUES (?, ?, ?, NULL, NULL, ?, ?)`, entry.ID, entry.ContentRevision, catalog.CommitPending, entry.CreatedAt.UTC(), entry.UpdatedAt.UTC()); err != nil {
			return nil, fmt.Errorf("insert catalog installation commit %q: %w", entry.ID, err)
		}
		workflow := value.Workflow
		if _, err := tx.ExecContext(ctx, `INSERT INTO generation_workflows
			(id, source_kind, source_ref, source_revision, state, cleanup_intent, candidate_revision_id, active_agent_run_id,
			state_attempt, lease_owner, next_run_at, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, '', ?, NULL, 0, '', ?, '', ?, ?)`,
			workflow.ID, workflow.Source.Kind, workflow.Source.Ref, workflow.SourceRevision, workflow.State,
			workflow.CandidateRevisionID, workflow.NextRunAt.UTC(), workflow.CreatedAt.UTC(), workflow.UpdatedAt.UTC()); err != nil {
			return nil, fmt.Errorf("insert catalog installation workflow %q: %w", workflow.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit catalog installation: %w", err)
	}
	return &release, nil
}

// HasReadyRelease gates catalog visibility. Challenge and taxonomy files may
// be materialized while a release is Committing, but are not publicly visible
// until this durable state transition completes.
func (r *CatalogRepository) HasReadyRelease(ctx context.Context) (bool, error) {
	var ready bool
	if err := r.conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM catalog_releases WHERE state = ?)`, catalog.ReleaseReady).Scan(&ready); err != nil {
		return false, fmt.Errorf("check ready catalog release: %w", err)
	}
	return ready, nil
}

// ListRecoverableReleases includes Ready releases so Server can retry removal
// of non-visible staging residue after its durable visibility transition.
func (r *CatalogRepository) ListRecoverableReleases(ctx context.Context) ([]catalog.Release, error) {
	rows, err := r.conn.QueryContext(ctx, catalogReleaseSelect+` WHERE state IN (?, ?, ?, ?) ORDER BY created_at, id`,
		catalog.ReleaseInstalling, catalog.ReleaseCommitting, catalog.ReleaseCleaningUp, catalog.ReleaseReady)
	if err != nil {
		return nil, fmt.Errorf("list recoverable catalog releases: %w", err)
	}
	defer func() { _ = rows.Close() }()
	releases := make([]catalog.Release, 0)
	for rows.Next() {
		release, err := scanCatalogRelease(rows)
		if err != nil {
			return nil, err
		}
		releases = append(releases, *release)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recoverable catalog releases: %w", err)
	}
	return releases, nil
}

// BeginReleaseCommit changes a fully verified installation into Committing.
// It is intentionally idempotent so a Server restart can resume the same
// release without creating a second target-platform identity set.
func (r *CatalogRepository) BeginReleaseCommit(ctx context.Context, releaseID string, now time.Time) (*catalog.Release, bool, error) {
	if strings.TrimSpace(releaseID) == "" || now.IsZero() {
		return nil, false, errors.New("catalog release commit requires release identity and current time")
	}
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin catalog release commit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	release, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, releaseID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, false, err
	}
	if release.State == catalog.ReleaseCommitting {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return release, true, nil
	}
	if release.State != catalog.ReleaseInstalling {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return release, false, nil
	}
	var unfinished bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM catalog_release_entries WHERE release_id = ? AND state <> ?)`,
		releaseID, catalog.EntryReadyToCommit).Scan(&unfinished); err != nil {
		return nil, false, fmt.Errorf("check catalog release entries: %w", err)
	}
	if unfinished {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return release, false, nil
	}
	release, err = scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases SET state = ?, last_error = '', updated_at = ?
		WHERE id = ? RETURNING `+catalogReleaseColumns, catalog.ReleaseCommitting, now.UTC(), releaseID))
	if err != nil {
		return nil, false, fmt.Errorf("begin catalog release commit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return release, true, nil
}

// CompleteReleaseCommit is the single durable visibility transition after all
// target directories and the compiled taxonomy snapshot have been prepared.
func (r *CatalogRepository) CompleteReleaseCommit(ctx context.Context, releaseID string, now time.Time) (*catalog.Release, error) {
	if strings.TrimSpace(releaseID) == "" || now.IsZero() {
		return nil, errors.New("catalog release completion requires release identity and current time")
	}
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin catalog release completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	release, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, releaseID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, err
	}
	if release.State == catalog.ReleaseReady {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return release, nil
	}
	if release.State != catalog.ReleaseCommitting {
		return nil, errors.New("catalog release is not committing")
	}
	var incomplete bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM catalog_release_entries entry
		JOIN catalog_release_entry_commits entry_commit ON entry_commit.entry_id = entry.id
		WHERE entry.release_id = ? AND (entry.state <> ? OR entry_commit.state NOT IN (?, ?))
	)`, releaseID, catalog.EntryReadyToCommit, catalog.CommitMaterialized, catalog.CommitCommitted).Scan(&incomplete); err != nil {
		return nil, fmt.Errorf("check catalog release commit state: %w", err)
	}
	if incomplete {
		return nil, errors.New("catalog release has entries that are not materialized")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_release_entry_commits SET state = ?, committed_at = COALESCE(committed_at, ?), updated_at = ?
		WHERE entry_id IN (SELECT id FROM catalog_release_entries WHERE release_id = ?)`, catalog.CommitCommitted, now.UTC(), now.UTC(), releaseID); err != nil {
		return nil, fmt.Errorf("complete catalog entry commits: %w", err)
	}
	release, err = scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases SET state = ?, last_error = '', updated_at = ?
		WHERE id = ? RETURNING `+catalogReleaseColumns, catalog.ReleaseReady, now.UTC(), releaseID))
	if err != nil {
		return nil, fmt.Errorf("mark catalog release ready: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return release, nil
}

// StartReleaseCleanup stops future release work and turns every unleased
// release workflow into a cleanup task. It intentionally leaves the release
// in CleaningUp until Server has removed its filesystem residue.
func (r *CatalogRepository) StartReleaseCleanup(ctx context.Context, releaseID, reason string, now time.Time) error {
	if strings.TrimSpace(releaseID) == "" || strings.TrimSpace(reason) == "" || now.IsZero() {
		return errors.New("catalog release cleanup requires release identity, reason, and current time")
	}
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin catalog release cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := setCatalogReleaseCleaningUpTx(ctx, tx, releaseID, reason, now); err != nil {
		return err
	}
	if err := scheduleCatalogReleaseCleanupTx(ctx, tx, releaseID, now); err != nil {
		return err
	}
	return tx.Commit()
}

// ReleaseCleanupReady reports whether every release-owned workflow is
// terminal. It does not mutate state so the coordinator can remove files
// before calling CompleteReleaseCleanup.
func (r *CatalogRepository) ReleaseCleanupReady(ctx context.Context, releaseID string) (bool, error) {
	if strings.TrimSpace(releaseID) == "" {
		return false, errors.New("catalog release cleanup readiness requires release identity")
	}
	var complete bool
	if err := r.conn.QueryRowContext(ctx, `SELECT NOT EXISTS (
		SELECT 1 FROM generation_workflows workflow
		JOIN catalog_release_entries entry ON entry.id = workflow.source_ref
		WHERE workflow.source_kind = ? AND entry.release_id = ?
		AND workflow.state NOT IN (?, ?, ?)
	)`, generation.SourceRelease, releaseID, generation.StateCompleted, generation.StateFailed, generation.StateCancelled).Scan(&complete); err != nil {
		return false, fmt.Errorf("check catalog release cleanup: %w", err)
	}
	return complete, nil
}

// CompleteReleaseCleanup transitions to Failed only after the coordinator has
// cleaned release-owned files. It rechecks workflow completion under a lock
// so a concurrent worker cannot race the terminal transition.
func (r *CatalogRepository) CompleteReleaseCleanup(ctx context.Context, releaseID string, now time.Time) (*catalog.Release, error) {
	if strings.TrimSpace(releaseID) == "" || now.IsZero() {
		return nil, errors.New("catalog release cleanup completion requires release identity and current time")
	}
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin catalog release cleanup completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	release, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, releaseID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, err
	}
	if release.State == catalog.ReleaseFailed {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return release, nil
	}
	if release.State != catalog.ReleaseCleaningUp {
		return nil, errors.New("catalog release is not cleaning up")
	}
	if err := finalizeCatalogReleaseCleanupTx(ctx, tx, releaseID, now); err != nil {
		return nil, err
	}
	release, err = scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ?`, releaseID))
	if err != nil {
		return nil, fmt.Errorf("read completed catalog release cleanup: %w", err)
	}
	if release.State != catalog.ReleaseFailed {
		return nil, errors.New("catalog release cleanup still has active workflows")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return release, nil
}

// CreateRelease persists a single immutable catalog source and every entry in
// one transaction. Source paths and content revisions are portable; target
// runtime identities are initialized separately as pending commit records.
func (r *CatalogRepository) CreateRelease(ctx context.Context, release catalog.Release, entries []catalog.Entry) (*catalog.Release, error) {
	if release.State == "" {
		release.State = catalog.ReleasePending
	}
	if !release.Valid() || release.State != catalog.ReleasePending {
		return nil, errors.New("catalog release creation requires a valid pending release")
	}
	if len(entries) == 0 {
		return nil, errors.New("catalog release requires at least one entry")
	}
	now := time.Now().UTC()
	if release.CreatedAt.IsZero() {
		release.CreatedAt = now
	}
	if release.UpdatedAt.IsZero() {
		release.UpdatedAt = release.CreatedAt
	}

	seenPaths := make(map[string]struct{}, len(entries))
	for index := range entries {
		entry := &entries[index]
		if entry.ReleaseID == "" {
			entry.ReleaseID = release.ID
		}
		if entry.State == "" {
			entry.State = catalog.EntryPending
		}
		if entry.CreatedAt.IsZero() {
			entry.CreatedAt = release.CreatedAt
		}
		if entry.UpdatedAt.IsZero() {
			entry.UpdatedAt = entry.CreatedAt
		}
		if entry.ReleaseID != release.ID || !entry.Valid() || entry.State != catalog.EntryPending {
			return nil, fmt.Errorf("catalog release entry %d is invalid", index+1)
		}
		if _, duplicate := seenPaths[entry.SourcePath]; duplicate {
			return nil, fmt.Errorf("catalog release has duplicate source path %q", entry.SourcePath)
		}
		seenPaths[entry.SourcePath] = struct{}{}
	}

	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin catalog release creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_releases
		(id, name, version, bundle_digest, taxonomy_content_revision, state, deadline_at, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?)`,
		release.ID, release.Name, release.Version, release.BundleDigest, release.TaxonomyContentRevision,
		release.State, release.DeadlineAt, release.CreatedAt.UTC(), release.UpdatedAt.UTC()); err != nil {
		return nil, fmt.Errorf("insert catalog release: %w", err)
	}
	for _, entry := range entries {
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_release_entries
			(id, release_id, source_path, content_revision, candidate_revision_id, state, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, NULL, ?, '', ?, ?)`,
			entry.ID, entry.ReleaseID, entry.SourcePath, entry.ContentRevision, entry.State, entry.CreatedAt.UTC(), entry.UpdatedAt.UTC()); err != nil {
			return nil, fmt.Errorf("insert catalog release entry %q: %w", entry.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_release_entry_commits
			(entry_id, content_revision, state, challenge_id, slug, created_at, updated_at)
			VALUES (?, ?, ?, NULL, NULL, ?, ?)`, entry.ID, entry.ContentRevision, catalog.CommitPending, entry.CreatedAt.UTC(), entry.UpdatedAt.UTC()); err != nil {
			return nil, fmt.Errorf("initialize catalog entry commit %q: %w", entry.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit catalog release creation: %w", err)
	}
	return &release, nil
}

func (r *CatalogRepository) GetRelease(ctx context.Context, id string) (*catalog.Release, error) {
	release, err := scanCatalogRelease(r.conn.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get catalog release: %w", err)
	}
	return release, nil
}

func (r *CatalogRepository) ListEntries(ctx context.Context, releaseID string) ([]catalog.Entry, error) {
	if strings.TrimSpace(releaseID) == "" {
		return nil, errors.New("catalog release id is required")
	}
	rows, err := r.conn.QueryContext(ctx, catalogEntrySelect+` WHERE release_id = ? ORDER BY source_path, id`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("list catalog release entries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	entries := make([]catalog.Entry, 0)
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

func (r *CatalogRepository) GetEntry(ctx context.Context, id string) (*catalog.Entry, error) {
	entry, err := scanCatalogEntry(r.conn.QueryRowContext(ctx, catalogEntrySelect+` WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogEntryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get catalog release entry: %w", err)
	}
	return entry, nil
}

func (r *CatalogRepository) SetReleaseState(ctx context.Context, id string, state catalog.ReleaseState, lastError string, now time.Time) (*catalog.Release, error) {
	if strings.TrimSpace(id) == "" || !state.Valid() || now.IsZero() {
		return nil, errors.New("catalog release state update is invalid")
	}
	release, err := scanCatalogRelease(r.conn.QueryRowContext(ctx, `UPDATE catalog_releases
		SET state = ?, last_error = ?, updated_at = ? WHERE id = ? RETURNING `+catalogReleaseColumns,
		state, strings.TrimSpace(lastError), now.UTC(), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("set catalog release state: %w", err)
	}
	return release, nil
}

func (r *CatalogRepository) SetEntryState(ctx context.Context, id string, state catalog.EntryState, candidateRevisionID, lastError string, now time.Time) (*catalog.Entry, error) {
	if strings.TrimSpace(id) == "" || !state.Valid() || now.IsZero() {
		return nil, errors.New("catalog entry state update is invalid")
	}
	entry, err := scanCatalogEntry(r.conn.QueryRowContext(ctx, `UPDATE catalog_release_entries
		SET state = ?, candidate_revision_id = NULLIF(?, ''), last_error = ?, updated_at = ?
		WHERE id = ? RETURNING `+catalogEntryColumns,
		state, strings.TrimSpace(candidateRevisionID), strings.TrimSpace(lastError), now.UTC(), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogEntryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("set catalog entry state: %w", err)
	}
	return entry, nil
}

// PrepareCommit reserves one stable target-platform identity for an entry.
// A retry may only observe the same identity; it never silently replaces it.
func (r *CatalogRepository) PrepareCommit(ctx context.Context, entryID string, identity catalog.RuntimeIdentity, now time.Time) (*catalog.Commit, error) {
	if strings.TrimSpace(entryID) == "" || !identity.Valid() || now.IsZero() {
		return nil, errors.New("catalog commit preparation is invalid")
	}
	tx, err := r.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin catalog commit preparation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	entry, err := scanCatalogEntry(tx.QueryRowContext(ctx, catalogEntrySelect+` WHERE id = ? FOR UPDATE`, entryID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogEntryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock catalog commit entry: %w", err)
	}
	if entry.State != catalog.EntryReadyToCommit {
		return nil, fmt.Errorf("catalog entry %q is not ready to commit", entry.ID)
	}
	commit, err := scanCatalogCommit(tx.QueryRowContext(ctx, catalogCommitSelect+` WHERE entry_id = ? FOR UPDATE`, entryID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogCommitNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock catalog entry commit: %w", err)
	}
	if commit.State == catalog.CommitPending {
		commit, err = scanCatalogCommit(tx.QueryRowContext(ctx, `UPDATE catalog_release_entry_commits
			SET state = ?, challenge_id = ?, slug = ?, updated_at = ?
			WHERE entry_id = ? RETURNING `+catalogCommitColumns,
			catalog.CommitPrepared, identity.ChallengeID, identity.Slug, now.UTC(), entryID))
		if err != nil {
			return nil, fmt.Errorf("reserve catalog entry identity: %w", err)
		}
	} else if commit.RuntimeIdentity == nil || *commit.RuntimeIdentity != identity {
		return nil, errors.New("catalog entry already has a different runtime identity")
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit catalog identity reservation: %w", err)
	}
	return commit, nil
}

func (r *CatalogRepository) MarkCommitMaterialized(ctx context.Context, entryID string, now time.Time) (*catalog.Commit, error) {
	if strings.TrimSpace(entryID) == "" || now.IsZero() {
		return nil, errors.New("catalog materialization update is invalid")
	}
	commit, err := scanCatalogCommit(r.conn.QueryRowContext(ctx, `UPDATE catalog_release_entry_commits
		SET state = ?, materialized_at = COALESCE(materialized_at, ?), updated_at = ?
		WHERE entry_id = ? AND state IN (?, ?) RETURNING `+catalogCommitColumns,
		catalog.CommitMaterialized, now.UTC(), now.UTC(), entryID, catalog.CommitPrepared, catalog.CommitMaterialized))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogCommitNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("mark catalog commit materialized: %w", err)
	}
	return commit, nil
}

func (r *CatalogRepository) CompleteCommit(ctx context.Context, entryID string, now time.Time) (*catalog.Commit, error) {
	if strings.TrimSpace(entryID) == "" || now.IsZero() {
		return nil, errors.New("catalog commit completion is invalid")
	}
	commit, err := scanCatalogCommit(r.conn.QueryRowContext(ctx, `UPDATE catalog_release_entry_commits
		SET state = ?, committed_at = COALESCE(committed_at, ?), updated_at = ?
		WHERE entry_id = ? AND state IN (?, ?) RETURNING `+catalogCommitColumns,
		catalog.CommitCommitted, now.UTC(), now.UTC(), entryID, catalog.CommitMaterialized, catalog.CommitCommitted))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogCommitNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("complete catalog entry commit: %w", err)
	}
	return commit, nil
}

func (r *CatalogRepository) GetEntryCommit(ctx context.Context, entryID string) (*catalog.Commit, error) {
	commit, err := scanCatalogCommit(r.conn.QueryRowContext(ctx, catalogCommitSelect+` WHERE entry_id = ?`, strings.TrimSpace(entryID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogCommitNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get catalog entry commit: %w", err)
	}
	return commit, nil
}

type catalogRow interface{ Scan(...any) error }

func scanCatalogRelease(row catalogRow) (*catalog.Release, error) {
	var release catalog.Release
	var deadline sql.NullTime
	if err := row.Scan(&release.ID, &release.Name, &release.Version, &release.BundleDigest, &release.TaxonomyContentRevision,
		&release.State, &deadline, &release.LastError, &release.CreatedAt, &release.UpdatedAt); err != nil {
		return nil, err
	}
	if deadline.Valid {
		value := deadline.Time.UTC()
		release.DeadlineAt = &value
	}
	release.CreatedAt = release.CreatedAt.UTC()
	release.UpdatedAt = release.UpdatedAt.UTC()
	return &release, nil
}

func scanCatalogEntry(row catalogRow) (*catalog.Entry, error) {
	var entry catalog.Entry
	if err := row.Scan(&entry.ID, &entry.ReleaseID, &entry.SourcePath, &entry.ContentRevision, &entry.CandidateRevisionID,
		&entry.State, &entry.LastError, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
		return nil, err
	}
	entry.CreatedAt = entry.CreatedAt.UTC()
	entry.UpdatedAt = entry.UpdatedAt.UTC()
	return &entry, nil
}

func scanCatalogCommit(row catalogRow) (*catalog.Commit, error) {
	var commit catalog.Commit
	var challengeID, slug sql.NullString
	var materializedAt, committedAt sql.NullTime
	if err := row.Scan(&commit.EntryID, &commit.ContentRevision, &commit.State, &challengeID, &slug, &materializedAt,
		&committedAt, &commit.CreatedAt, &commit.UpdatedAt); err != nil {
		return nil, err
	}
	if challengeID.Valid || slug.Valid {
		if !challengeID.Valid || !slug.Valid {
			return nil, errors.New("catalog entry commit has partial runtime identity")
		}
		commit.RuntimeIdentity = &catalog.RuntimeIdentity{ChallengeID: challengeID.String, Slug: slug.String}
	}
	if materializedAt.Valid {
		value := materializedAt.Time.UTC()
		commit.MaterializedAt = &value
	}
	if committedAt.Valid {
		value := committedAt.Time.UTC()
		commit.CommittedAt = &value
	}
	commit.CreatedAt = commit.CreatedAt.UTC()
	commit.UpdatedAt = commit.UpdatedAt.UTC()
	return &commit, nil
}
