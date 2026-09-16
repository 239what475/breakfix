package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	contentscenario "github.com/breakfix/breakfix/internal/content/scenario"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/publication"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	scenariodomain "github.com/breakfix/breakfix/internal/domain/scenario"
)

var (
	ErrCatalogReleaseNotFound = catalogdomain.ErrReleaseNotFound
	ErrCatalogEntryNotFound   = errors.New("catalog release entry not found")
	ErrCatalogCommitNotFound  = errors.New("catalog release entry commit not found")
	ErrCatalogTransitionLost  = errors.New("catalog transition lost")
)

const catalogReleaseColumns = `id, name, version, bundle_digest, source_digest, state, source_attempt, next_run_at, commit_id,
	last_error, finalizer_error_category, finalizer_last_error, finalizer_last_attempted_at, finalizer_next_retry_at, created_at, updated_at`
const catalogReleaseSelect = `SELECT ` + catalogReleaseColumns + ` FROM catalog_releases`

const catalogEntryColumns = `id, release_id, source_path, source_ref, title, scenario_type, tags, content_revision, source_archive,
	state, state_version, runnable_revision_id, runnable_revision_digest, verification_report_id, verification_report_digest,
	last_error, created_at, updated_at`
const catalogEntrySelect = `SELECT ` + catalogEntryColumns + ` FROM catalog_release_entries`

const catalogCommitColumns = `id, release_id, entry_id, scenario_id, scenario_revision_id, source_slug, state, last_error,
	materialized_revision, materialized_at, committed_at, created_at, updated_at`
const catalogCommitSelect = `SELECT ` + catalogCommitColumns + ` FROM catalog_release_entry_commits`

type scanner interface{ Scan(...any) error }

func (d *CatalogRepository) CreateOrGetRelease(ctx context.Context, release catalogdomain.Release) (*catalogdomain.Release, bool, error) {
	if release.State != catalogdomain.ReleasePending || !release.Valid() || release.SourceAttempt != 1 || release.ID != catalogdomain.ReleaseIDForBundle(release.BundleDigest) {
		return nil, false, errors.New("catalog release creation requires a valid pending source attempt")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("begin catalog release creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE bundle_digest = ? FOR UPDATE`, release.BundleDigest))
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("read catalog release by digest: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_releases
		(id, name, version, bundle_digest, source_digest, state, source_attempt, next_run_at, commit_id, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', '', ?, ?)`, release.ID, release.Name, release.Version, release.BundleDigest,
		release.SourceDigest, release.State, release.SourceAttempt, release.NextRunAt.UTC(), release.CreatedAt.UTC(), release.UpdatedAt.UTC()); err != nil {
		return nil, false, fmt.Errorf("insert catalog release: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("commit catalog release creation: %w", err)
	}
	return &release, true, nil
}

func (d *CatalogRepository) InitializeRelease(ctx context.Context, release catalogdomain.Release, entries []catalogdomain.Entry, now time.Time) (*catalogdomain.Release, error) {
	if release.State != catalogdomain.ReleasePending || !release.Valid() || strings.TrimSpace(release.Name) == "" || strings.TrimSpace(release.Version) == "" || !release.SourceDigest.Valid() || now.IsZero() {
		return nil, errors.New("catalog release initialization is invalid")
	}
	seenPath, seenRef := map[string]struct{}{}, map[string]struct{}{}
	for index, entry := range entries {
		if entry.ID != catalogdomain.EntryIDFor(release.ID, entry.SourcePath) || entry.ReleaseID != release.ID || entry.State != catalogdomain.EntryMaterializing || entry.StateVersion != 1 || !entry.Valid() {
			return nil, fmt.Errorf("catalog entry %d is invalid", index+1)
		}
		if _, exists := seenPath[entry.SourcePath]; exists {
			return nil, fmt.Errorf("duplicate catalog entry path %q", entry.SourcePath)
		}
		if _, exists := seenRef[entry.SourceRef]; exists {
			return nil, fmt.Errorf("duplicate catalog entry source reference %q", entry.SourceRef)
		}
		seenPath[entry.SourcePath], seenRef[entry.SourceRef] = struct{}{}, struct{}{}
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
		tags, err := marshalJSON(entry.Tags)
		if err != nil {
			return nil, fmt.Errorf("encode catalog entry %q tags: %w", entry.ID, err)
		}
		source, err := marshalJSON(entry.Source)
		if err != nil {
			return nil, fmt.Errorf("encode catalog entry %q source: %w", entry.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_release_entries
			(id, release_id, source_path, source_ref, title, scenario_type, tags, content_revision, source_archive, state, state_version, last_error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?::jsonb, ?, ?, '', ?, ?)`, entry.ID, entry.ReleaseID, entry.SourcePath, entry.SourceRef, entry.Title, entry.Type,
			tags, entry.ContentRevision, source, entry.State, entry.StateVersion, entry.CreatedAt.UTC(), entry.UpdatedAt.UTC()); err != nil {
			return nil, fmt.Errorf("insert catalog entry %q: %w", entry.ID, err)
		}
	}
	updated, err := scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases
		SET name = ?, version = ?, source_digest = ?, state = ?, last_error = '', updated_at = ?
		WHERE id = ? AND state = ? RETURNING `+catalogReleaseColumns, release.Name, release.Version, release.SourceDigest,
		catalogdomain.ReleaseInstalling, now.UTC(), release.ID, catalogdomain.ReleasePending))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogTransitionLost
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
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	release, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, releaseID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, err
	}
	if release.State != catalogdomain.ReleasePending {
		return nil, ErrCatalogTransitionLost
	}
	if release.SourceAttempt >= 5 {
		release, err = scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases SET state = ?, last_error = ?, updated_at = ? WHERE id = ? RETURNING `+catalogReleaseColumns,
			catalogdomain.ReleaseFailed, strings.TrimSpace(summary), now.UTC(), release.ID))
	} else {
		release, err = scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases SET source_attempt = source_attempt + 1, next_run_at = ?, last_error = ?, updated_at = ? WHERE id = ? RETURNING `+catalogReleaseColumns,
			catalogRetryAt(release.SourceAttempt+1, now), strings.TrimSpace(summary), now.UTC(), release.ID))
	}
	if err != nil {
		return nil, fmt.Errorf("record catalog source failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return release, nil
}

func (d *CatalogRepository) FailPendingRelease(ctx context.Context, releaseID, summary string, now time.Time) (*catalogdomain.Release, error) {
	return d.failReleaseState(ctx, releaseID, []catalogdomain.ReleaseState{catalogdomain.ReleasePending}, summary, now)
}

func (d *CatalogRepository) FailRelease(ctx context.Context, releaseID, summary string, now time.Time) (*catalogdomain.Release, error) {
	return d.failReleaseState(ctx, releaseID, []catalogdomain.ReleaseState{catalogdomain.ReleasePending, catalogdomain.ReleaseInstalling, catalogdomain.ReleaseCommitting}, summary, now)
}

func (d *CatalogRepository) failReleaseState(ctx context.Context, releaseID string, allowed []catalogdomain.ReleaseState, summary string, now time.Time) (*catalogdomain.Release, error) {
	if strings.TrimSpace(releaseID) == "" || strings.TrimSpace(summary) == "" || now.IsZero() || len(allowed) == 0 {
		return nil, errors.New("catalog release failure is invalid")
	}
	values := []any{catalogdomain.ReleaseFailed, strings.TrimSpace(summary), now.UTC(), strings.TrimSpace(releaseID)}
	for _, state := range allowed {
		values = append(values, state)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(allowed)), ",")
	updated, err := scanCatalogRelease(d.conn.QueryRowContext(ctx, `UPDATE catalog_releases SET state = ?, last_error = ?, updated_at = ? WHERE id = ? AND state IN (`+placeholders+`) RETURNING `+catalogReleaseColumns, values...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogTransitionLost
	}
	if err != nil {
		return nil, fmt.Errorf("fail catalog release: %w", err)
	}
	return updated, nil
}

func (d *CatalogRepository) RecordCatalogFinalizerFailure(ctx context.Context, releaseID string, diagnostic publication.Diagnostic) (*catalogdomain.Release, error) {
	if strings.TrimSpace(releaseID) == "" || diagnostic.Validate() != nil {
		return nil, errors.New("catalog finalizer diagnostic is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	release, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, releaseID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, err
	}
	if release.State != catalogdomain.ReleaseInstalling && release.State != catalogdomain.ReleaseCommitting {
		return nil, ErrCatalogTransitionLost
	}
	state, nextRunAt := release.State, diagnostic.LastAttemptedAt.UTC()
	if diagnostic.NextRetryAt != nil {
		nextRunAt = diagnostic.NextRetryAt.UTC()
	}
	if diagnostic.Category == publication.CategoryDeterministic {
		state = catalogdomain.ReleaseFailed
	}
	var retryAt any
	if diagnostic.NextRetryAt != nil {
		retryAt = diagnostic.NextRetryAt.UTC()
	}
	updated, err := scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases SET state = ?, next_run_at = ?, last_error = ?, finalizer_error_category = ?, finalizer_last_error = ?, finalizer_last_attempted_at = ?, finalizer_next_retry_at = ?, updated_at = ? WHERE id = ? AND state IN (?, ?) RETURNING `+catalogReleaseColumns,
		state, nextRunAt, diagnostic.LastError, diagnostic.Category, diagnostic.LastError, diagnostic.LastAttemptedAt.UTC(), retryAt, diagnostic.LastAttemptedAt.UTC(), release.ID, catalogdomain.ReleaseInstalling, catalogdomain.ReleaseCommitting))
	if err != nil {
		return nil, fmt.Errorf("persist catalog finalizer failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
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

func (d *CatalogRepository) CatalogBootstrapState(ctx context.Context) (catalogdomain.BootstrapState, error) {
	rows, err := d.conn.QueryContext(ctx, catalogReleaseSelect+` ORDER BY created_at, id`)
	if err != nil {
		return catalogdomain.BootstrapState{}, fmt.Errorf("list catalog bootstrap releases: %w", err)
	}
	defer rows.Close()
	state := catalogdomain.BootstrapState{}
	for rows.Next() {
		release, err := scanCatalogRelease(rows)
		if err != nil {
			return catalogdomain.BootstrapState{}, err
		}
		state.Releases = append(state.Releases, *release)
	}
	if err := rows.Err(); err != nil {
		return catalogdomain.BootstrapState{}, err
	}
	if err := d.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM scenarios`).Scan(&state.PublishedScenarioCount); err != nil {
		return catalogdomain.BootstrapState{}, fmt.Errorf("count published scenarios for catalog bootstrap: %w", err)
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

func (d *CatalogRepository) MarkCatalogEntryMaterialized(ctx context.Context, entryID string, reference runnable.RevisionReference, now time.Time) error {
	if strings.TrimSpace(entryID) == "" || reference.Validate() != nil || now.IsZero() {
		return errors.New("catalog entry materialization is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	entry, err := scanCatalogEntry(tx.QueryRowContext(ctx, catalogEntrySelect+` WHERE id = ? FOR UPDATE`, entryID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCatalogEntryNotFound
	}
	if err != nil {
		return err
	}
	if entry.State == catalogdomain.EntryVerifying && entry.RunnableRevisionRef != nil && *entry.RunnableRevisionRef == reference {
		return tx.Commit()
	}
	if entry.State != catalogdomain.EntryMaterializing {
		return ErrCatalogTransitionLost
	}
	if err := validateCatalogRunnableRevisionTx(ctx, tx, *entry, reference); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE catalog_release_entries entry SET state = ?, state_version = state_version + 1, runnable_revision_id = ?, runnable_revision_digest = ?, last_error = '', updated_at = ? FROM catalog_releases release WHERE entry.id = ? AND entry.release_id = release.id AND release.state = ? AND entry.state = ? AND entry.state_version = ?`,
		catalogdomain.EntryVerifying, reference.ID, reference.Digest, now.UTC(), entry.ID, catalogdomain.ReleaseInstalling, catalogdomain.EntryMaterializing, entry.StateVersion)
	if err != nil {
		return fmt.Errorf("mark catalog entry materialized: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrCatalogTransitionLost
	}
	return tx.Commit()
}

func (d *CatalogRepository) MarkCatalogEntryVerified(ctx context.Context, entryID string, reference runnable.VerificationReportReference, now time.Time) error {
	if strings.TrimSpace(entryID) == "" || reference.Validate() != nil || now.IsZero() {
		return errors.New("catalog entry verification is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	entry, err := scanCatalogEntry(tx.QueryRowContext(ctx, catalogEntrySelect+` WHERE id = ? FOR UPDATE`, entryID))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrCatalogEntryNotFound
	}
	if err != nil {
		return err
	}
	if entry.State == catalogdomain.EntryReadyToCommit && entry.VerificationReportRef != nil && *entry.VerificationReportRef == reference {
		return tx.Commit()
	}
	if entry.State != catalogdomain.EntryVerifying || entry.RunnableRevisionRef == nil {
		return ErrCatalogTransitionLost
	}
	if err := validateCatalogVerificationReportTx(ctx, tx, *entry.RunnableRevisionRef, reference); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE catalog_release_entries entry SET state = ?, state_version = state_version + 1, verification_report_id = ?, verification_report_digest = ?, last_error = '', updated_at = ? FROM catalog_releases release WHERE entry.id = ? AND entry.release_id = release.id AND release.state = ? AND entry.state = ? AND entry.state_version = ?`,
		catalogdomain.EntryReadyToCommit, reference.ID, reference.Digest, now.UTC(), entry.ID, catalogdomain.ReleaseInstalling, catalogdomain.EntryVerifying, entry.StateVersion)
	if err != nil {
		return fmt.Errorf("mark catalog entry verified: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrCatalogTransitionLost
	}
	return tx.Commit()
}

func validateCatalogRunnableRevisionTx(ctx context.Context, tx *Tx, entry catalogdomain.Entry, reference runnable.RevisionReference) error {
	var kind, contentID, contentRevision string
	err := tx.QueryRowContext(ctx, `SELECT specs.content_kind, specs.content_id, specs.content_revision FROM runnable_revisions revisions JOIN runnable_specs specs ON specs.spec_digest = revisions.spec_digest WHERE revisions.id = ? AND revisions.runnable_revision_digest = ? FOR UPDATE`, reference.ID, reference.Digest).Scan(&kind, &contentID, &contentRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrRunnableRevisionNotFound
	}
	if err != nil {
		return fmt.Errorf("load catalog runnable revision: %w", err)
	}
	if kind != "operations" || contentID != entry.ID || contentRevision != string(entry.ContentRevision) {
		return errors.New("catalog runnable revision does not match entry identity")
	}
	return nil
}

func validateCatalogVerificationReportTx(ctx context.Context, tx *Tx, revision runnable.RevisionReference, reference runnable.VerificationReportReference) error {
	var encoded []byte
	var reportRevisionDigest string
	err := tx.QueryRowContext(ctx, `SELECT report, runnable_revision_digest FROM runnable_verification_reports WHERE id = ? AND verification_report_digest = ? FOR UPDATE`, reference.ID, reference.Digest).Scan(&encoded, &reportRevisionDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrVerificationReportNotFound
	}
	if err != nil {
		return fmt.Errorf("load catalog verification report: %w", err)
	}
	if reportRevisionDigest != revision.Digest {
		return errors.New("catalog verification report does not match runnable revision")
	}
	var report runnable.VerificationReport
	if err := json.Unmarshal(encoded, &report); err != nil {
		return fmt.Errorf("decode catalog verification report: %w", err)
	}
	if !report.Passed {
		return errors.New("catalog verification report did not pass")
	}
	return nil
}

func (d *CatalogRepository) PrepareReleaseCommit(ctx context.Context, releaseID string, intents []catalogdomain.Commit, now time.Time) (*catalogdomain.Release, []catalogdomain.Commit, error) {
	if strings.TrimSpace(releaseID) == "" || now.IsZero() {
		return nil, nil, errors.New("catalog commit preparation is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	release, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, releaseID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if release.State == catalogdomain.ReleaseCommitting {
		commits, err := listCatalogCommitsTx(ctx, tx, release.ID)
		if err != nil {
			return nil, nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, nil, err
		}
		return release, commits, nil
	}
	if release.State != catalogdomain.ReleaseInstalling {
		return nil, nil, ErrCatalogTransitionLost
	}
	entries, err := listCatalogEntriesTx(ctx, tx, release.ID)
	if err != nil {
		return nil, nil, err
	}
	if len(entries) == 0 || len(entries) != len(intents) {
		return nil, nil, errors.New("catalog commit intents do not cover entries")
	}
	seen := map[string]struct{}{}
	for _, entry := range entries {
		if entry.State != catalogdomain.EntryReadyToCommit || entry.RunnableRevisionRef == nil || entry.VerificationReportRef == nil {
			return nil, nil, errors.New("catalog entry is not ready to commit")
		}
	}
	for _, intent := range intents {
		if intent.ID != catalogdomain.EntryCommitIDFor(release.ID, intent.EntryID) || intent.ReleaseID != release.ID || intent.State != catalogdomain.CommitPrepared || !intent.Valid() {
			return nil, nil, errors.New("catalog commit intent is invalid")
		}
		if _, found := seen[intent.EntryID]; found {
			return nil, nil, errors.New("catalog commit intent is duplicated")
		}
		seen[intent.EntryID] = struct{}{}
		if _, err := tx.ExecContext(ctx, `INSERT INTO catalog_release_entry_commits (id, release_id, entry_id, scenario_id, scenario_revision_id, source_slug, state, last_error, materialized_revision, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, '', '', ?, ?)`, intent.ID, intent.ReleaseID, intent.EntryID, intent.ScenarioID, intent.ScenarioRevisionID, intent.SourceSlug, intent.State, intent.CreatedAt.UTC(), intent.UpdatedAt.UTC()); err != nil {
			return nil, nil, fmt.Errorf("insert catalog commit %q: %w", intent.ID, err)
		}
	}
	for _, entry := range entries {
		if _, ok := seen[entry.ID]; !ok {
			return nil, nil, errors.New("catalog commit intent omitted an entry")
		}
	}
	updated, err := scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases SET state = ?, commit_id = ?, last_error = '', updated_at = ? WHERE id = ? AND state = ? RETURNING `+catalogReleaseColumns, catalogdomain.ReleaseCommitting, catalogdomain.CommitIDForRelease(release.ID), now.UTC(), release.ID, catalogdomain.ReleaseInstalling))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrCatalogTransitionLost
	}
	if err != nil {
		return nil, nil, err
	}
	commits, err := listCatalogCommitsTx(ctx, tx, release.ID)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return updated, commits, nil
}

func (d *CatalogRepository) Commits(ctx context.Context, releaseID string) ([]catalogdomain.Commit, error) {
	return listCatalogCommits(ctx, d.conn, releaseID)
}

func (d *CatalogRepository) MarkCommitMaterialized(ctx context.Context, releaseID, commitID, materializedRevision string, now time.Time) (*catalogdomain.Commit, error) {
	if strings.TrimSpace(releaseID) == "" || strings.TrimSpace(commitID) == "" || !contentscenario.ValidRevision(materializedRevision) || now.IsZero() {
		return nil, errors.New("catalog materialization completion is invalid")
	}
	updated, err := scanCatalogCommit(d.conn.QueryRowContext(ctx, `UPDATE catalog_release_entry_commits SET state = ?, materialized_revision = ?, materialized_at = ?, updated_at = ?
		WHERE id = ? AND release_id = ? AND state = ? AND EXISTS (
			SELECT 1 FROM catalog_releases WHERE catalog_releases.id = catalog_release_entry_commits.release_id AND catalog_releases.state = ?
		) RETURNING `+catalogCommitColumns,
		catalogdomain.CommitMaterialized, strings.TrimSpace(materializedRevision), now.UTC(), now.UTC(), strings.TrimSpace(commitID), strings.TrimSpace(releaseID), catalogdomain.CommitPrepared, catalogdomain.ReleaseCommitting))
	if errors.Is(err, sql.ErrNoRows) {
		existing, loadErr := scanCatalogCommit(d.conn.QueryRowContext(ctx, catalogCommitSelect+` WHERE id = ? AND release_id = ?`, commitID, releaseID))
		if loadErr == nil && existing.State == catalogdomain.CommitMaterialized && existing.MaterializedRevision == materializedRevision {
			return existing, nil
		}
		return nil, ErrCatalogTransitionLost
	}
	if err != nil {
		return nil, fmt.Errorf("mark catalog commit materialized: %w", err)
	}
	return updated, nil
}

func (d *CatalogRepository) CompleteReleaseCommit(ctx context.Context, releaseID string, now time.Time) (*catalogdomain.Release, error) {
	if strings.TrimSpace(releaseID) == "" || now.IsZero() {
		return nil, errors.New("catalog release completion is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	release, err := scanCatalogRelease(tx.QueryRowContext(ctx, catalogReleaseSelect+` WHERE id = ? FOR UPDATE`, releaseID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCatalogReleaseNotFound
	}
	if err != nil {
		return nil, err
	}
	if release.State == catalogdomain.ReleaseReady {
		return release, tx.Commit()
	}
	if release.State != catalogdomain.ReleaseCommitting {
		return nil, ErrCatalogTransitionLost
	}
	var published int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scenarios`).Scan(&published); err != nil {
		return nil, err
	}
	if published != 0 {
		return nil, catalogdomain.ErrBaselineEstablished
	}
	var ready int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM catalog_releases WHERE state = ? AND id <> ?`, catalogdomain.ReleaseReady, release.ID).Scan(&ready); err != nil {
		return nil, err
	}
	if ready != 0 {
		return nil, catalogdomain.ErrBaselineEstablished
	}
	entries, err := listCatalogEntriesTx(ctx, tx, release.ID)
	if err != nil {
		return nil, err
	}
	commits, err := listCatalogCommitsTx(ctx, tx, release.ID)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 || len(entries) != len(commits) {
		return nil, errors.New("catalog commits do not cover entries")
	}
	byEntry := map[string]catalogdomain.Entry{}
	for _, entry := range entries {
		byEntry[entry.ID] = entry
	}
	for _, commit := range commits {
		entry, found := byEntry[commit.EntryID]
		if !found || commit.State != catalogdomain.CommitMaterialized || commit.MaterializedAt == nil || !commit.Valid() || entry.State != catalogdomain.EntryReadyToCommit || entry.RunnableRevisionRef == nil || entry.VerificationReportRef == nil {
			return nil, errors.New("catalog release has incomplete publication state")
		}
		if err := validateCatalogRunnableRevisionTx(ctx, tx, entry, *entry.RunnableRevisionRef); err != nil {
			return nil, err
		}
		if err := validateCatalogVerificationReportTx(ctx, tx, *entry.RunnableRevisionRef, *entry.VerificationReportRef); err != nil {
			return nil, err
		}
		stable := scenariodomain.Scenario{ID: commit.ScenarioID, SourceKind: scenariodomain.SourceRelease, SourceRef: entry.SourceRef, State: scenariodomain.StateActive, ActiveRevisionID: commit.ScenarioRevisionID, SourceSlug: commit.SourceSlug, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
		published := scenariodomain.Revision{ID: commit.ScenarioRevisionID, ScenarioID: commit.ScenarioID, SourceKind: scenariodomain.SourceRelease, SourceRef: entry.SourceRef, SourceRevisionID: string(entry.ContentRevision), Title: entry.Title, Type: entry.Type, Tags: entry.Tags, ContentRevision: string(entry.ContentRevision), SourceSlug: commit.SourceSlug, MaterializedPath: contentscenario.MaterializedPath(commit.SourceSlug, commit.ScenarioRevisionID), MaterializedRevision: commit.MaterializedRevision, RunnableRevisionRef: *entry.RunnableRevisionRef, VerificationReportRef: *entry.VerificationReportRef, State: scenariodomain.RevisionActive, PublishedAt: commit.MaterializedAt.UTC(), CreatedAt: commit.MaterializedAt.UTC()}
		if err := insertPersistedScenarioTx(ctx, tx, stable); err != nil {
			return nil, fmt.Errorf("create catalog scenario %q: %w", commit.ScenarioID, err)
		}
		if err := insertPersistedScenarioRevisionTx(ctx, tx, published); err != nil {
			return nil, fmt.Errorf("create catalog scenario revision %q: %w", commit.ScenarioRevisionID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_release_entry_commits SET state = ?, committed_at = ?, updated_at = ? WHERE release_id = ? AND state = ?`, catalogdomain.CommitCommitted, now.UTC(), now.UTC(), release.ID, catalogdomain.CommitMaterialized); err != nil {
		return nil, err
	}
	updated, err := scanCatalogRelease(tx.QueryRowContext(ctx, `UPDATE catalog_releases SET state = ?, last_error = '', finalizer_error_category = '', finalizer_last_error = '', finalizer_last_attempted_at = NULL, finalizer_next_retry_at = NULL, updated_at = ? WHERE id = ? RETURNING `+catalogReleaseColumns, catalogdomain.ReleaseReady, now.UTC(), release.ID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return updated, nil
}

func listCatalogEntries(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, releaseID string) ([]catalogdomain.Entry, error) {
	rows, err := queryer.QueryContext(ctx, catalogEntrySelect+` WHERE release_id = ? ORDER BY source_path, id`, strings.TrimSpace(releaseID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCatalogEntries(rows)
}

func listCatalogEntriesTx(ctx context.Context, tx *Tx, releaseID string) ([]catalogdomain.Entry, error) {
	return listCatalogEntries(ctx, tx, releaseID)
}

func listCatalogCommits(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, releaseID string) ([]catalogdomain.Commit, error) {
	rows, err := queryer.QueryContext(ctx, catalogCommitSelect+` WHERE release_id = ? ORDER BY entry_id, id`, strings.TrimSpace(releaseID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCatalogCommits(rows)
}

func listCatalogCommitsTx(ctx context.Context, tx *Tx, releaseID string) ([]catalogdomain.Commit, error) {
	return listCatalogCommits(ctx, tx, releaseID)
}

func scanCatalogEntries(rows *sql.Rows) ([]catalogdomain.Entry, error) {
	values := []catalogdomain.Entry{}
	for rows.Next() {
		value, err := scanCatalogEntry(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, *value)
	}
	return values, rows.Err()
}

func scanCatalogCommits(rows *sql.Rows) ([]catalogdomain.Commit, error) {
	values := []catalogdomain.Commit{}
	for rows.Next() {
		value, err := scanCatalogCommit(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, *value)
	}
	return values, rows.Err()
}

func scanCatalogRelease(row scanner) (*catalogdomain.Release, error) {
	var value catalogdomain.Release
	var attempted, retry sql.NullTime
	if err := row.Scan(&value.ID, &value.Name, &value.Version, &value.BundleDigest, &value.SourceDigest, &value.State, &value.SourceAttempt, &value.NextRunAt, &value.CommitID, &value.LastError, &value.FinalizerErrorCategory, &value.FinalizerLastError, &attempted, &retry, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return nil, err
	}
	if attempted.Valid {
		at := attempted.Time.UTC()
		value.FinalizerLastAttemptedAt = &at
	}
	if retry.Valid {
		at := retry.Time.UTC()
		value.FinalizerNextRetryAt = &at
	}
	value.NextRunAt, value.CreatedAt, value.UpdatedAt = value.NextRunAt.UTC(), value.CreatedAt.UTC(), value.UpdatedAt.UTC()
	if !value.Valid() {
		return nil, errors.New("stored catalog release is invalid")
	}
	return &value, nil
}

func scanCatalogEntry(row scanner) (*catalogdomain.Entry, error) {
	var value catalogdomain.Entry
	var tags, source []byte
	var runnableID, runnableDigest, reportID, reportDigest sql.NullString
	if err := row.Scan(&value.ID, &value.ReleaseID, &value.SourcePath, &value.SourceRef, &value.Title, &value.Type, &tags, &value.ContentRevision, &source, &value.State, &value.StateVersion, &runnableID, &runnableDigest, &reportID, &reportDigest, &value.LastError, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(tags, &value.Tags); err != nil {
		return nil, fmt.Errorf("decode catalog entry tags: %w", err)
	}
	if err := json.Unmarshal(source, &value.Source); err != nil {
		return nil, fmt.Errorf("decode catalog entry source: %w", err)
	}
	if runnableID.Valid != runnableDigest.Valid || reportID.Valid != reportDigest.Valid {
		return nil, errors.New("stored catalog entry references are incomplete")
	}
	if runnableID.Valid {
		value.RunnableRevisionRef = &runnable.RevisionReference{ID: runnableID.String, Digest: runnableDigest.String}
	}
	if reportID.Valid {
		value.VerificationReportRef = &runnable.VerificationReportReference{ID: reportID.String, Digest: reportDigest.String}
	}
	value.CreatedAt, value.UpdatedAt = value.CreatedAt.UTC(), value.UpdatedAt.UTC()
	if !value.Valid() {
		return nil, errors.New("stored catalog entry is invalid")
	}
	return &value, nil
}

func scanCatalogCommit(row scanner) (*catalogdomain.Commit, error) {
	var value catalogdomain.Commit
	var materialized, committed sql.NullTime
	if err := row.Scan(&value.ID, &value.ReleaseID, &value.EntryID, &value.ScenarioID, &value.ScenarioRevisionID, &value.SourceSlug, &value.State, &value.LastError, &value.MaterializedRevision, &materialized, &committed, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return nil, err
	}
	if materialized.Valid {
		at := materialized.Time.UTC()
		value.MaterializedAt = &at
	}
	if committed.Valid {
		at := committed.Time.UTC()
		value.CommittedAt = &at
	}
	value.CreatedAt, value.UpdatedAt = value.CreatedAt.UTC(), value.UpdatedAt.UTC()
	if !value.Valid() {
		return nil, errors.New("stored catalog commit is invalid")
	}
	return &value, nil
}

func catalogRetryAt(attempt int, now time.Time) time.Time {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second * time.Duration(1<<min(attempt-1, 6))
	if delay > time.Minute {
		delay = time.Minute
	}
	return now.UTC().Add(delay)
}
