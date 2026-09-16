package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	content "github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	scenariodomain "github.com/breakfix/breakfix/internal/domain/scenario"
)

const persistedScenarioColumns = `id, source_kind, source_ref, owner_user_id, state, active_revision_id, source_slug, created_at, updated_at`
const persistedScenarioRevisionColumns = `id, scenario_id, source_kind, source_ref, source_revision_id, base_active_revision_id,
	title, scenario_type, tags, content_revision, source_slug, materialized_path, materialized_revision,
	runnable_revision_id, runnable_revision_digest, verification_report_id, verification_report_digest, state, published_at, created_at`
const activeScenarioRevisionColumns = `
	c.id, c.source_kind, c.source_ref, c.owner_user_id, c.state, c.active_revision_id, c.source_slug, c.created_at, c.updated_at,
	r.id, r.scenario_id, r.source_kind, r.source_ref, r.source_revision_id, r.base_active_revision_id,
	r.title, r.scenario_type, r.tags, r.content_revision, r.source_slug, r.materialized_path, r.materialized_revision,
	r.runnable_revision_id, r.runnable_revision_digest, r.verification_report_id, r.verification_report_digest, r.state, r.published_at, r.created_at`

// GetScenario returns the stable identity and its active pointer. Callers that
// need content must resolve the returned revision explicitly; this method never
// reads a mutable materialized directory.
func (d *ScenarioRepository) GetScenario(ctx context.Context, id string) (*scenariodomain.Scenario, error) {
	value, err := scanPersistedScenario(d.conn.QueryRowContext(ctx, `SELECT `+persistedScenarioColumns+` FROM scenarios WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, scenariodomain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get scenario: %w", err)
	}
	return value, nil
}

// GetScenarioRevision returns one exact immutable published revision.
func (d *ScenarioRepository) GetScenarioRevision(ctx context.Context, scenarioID, revisionID string) (*scenariodomain.Revision, error) {
	value, err := scanPersistedScenarioRevision(d.conn.QueryRowContext(ctx, `SELECT `+persistedScenarioRevisionColumns+`
		FROM scenario_revisions WHERE scenario_id = ? AND id = ?`, strings.TrimSpace(scenarioID), strings.TrimSpace(revisionID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, scenariodomain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get scenario revision: %w", err)
	}
	return value, nil
}

// ListActiveScenarioRevisions reads the Catalog directly from stable
// identities and their active immutable revision pointers.
func (d *ScenarioRepository) ListActiveScenarioRevisions(ctx context.Context) ([]scenariodomain.ActiveRevision, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT `+activeScenarioRevisionColumns+`
		FROM scenarios c
		JOIN scenario_revisions r ON r.scenario_id = c.id AND r.id = c.active_revision_id
		WHERE c.state = ? AND r.state = ?
		ORDER BY r.published_at DESC, c.id`, scenariodomain.StateActive, scenariodomain.RevisionActive)
	if err != nil {
		return nil, fmt.Errorf("list active scenario revisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]scenariodomain.ActiveRevision, 0)
	for rows.Next() {
		value, scanErr := scanActiveScenarioRevision(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan active scenario revision: %w", scanErr)
		}
		result = append(result, *value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active scenario revisions: %w", err)
	}
	return result, nil
}

// ListAuthoringScenarios returns the stable identities visible in one
// author's space. Revision details stay separate so callers cannot confuse a
// stable scenario with one particular immutable version.
func (d *ScenarioRepository) ListAuthoringScenarios(ctx context.Context, userID string) ([]scenariodomain.Scenario, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("authoring scenario list requires a user")
	}
	rows, err := d.conn.QueryContext(ctx, `SELECT `+persistedScenarioColumns+`
		FROM scenarios WHERE source_kind = ? AND owner_user_id = ?
		ORDER BY updated_at DESC, id DESC`, scenariodomain.SourceAuthoring, strings.TrimSpace(userID))
	if err != nil {
		return nil, fmt.Errorf("list authoring scenarios: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]scenariodomain.Scenario, 0)
	for rows.Next() {
		value, scanErr := scanPersistedScenario(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan authoring scenario: %w", scanErr)
		}
		result = append(result, *value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate authoring scenarios: %w", err)
	}
	return result, nil
}

// DeprecateAuthoringScenario hides one author-owned active Scenario from
// the public Catalog while retaining its stable identity, immutable revisions,
// and all Environment history.
func (d *ScenarioRepository) DeprecateAuthoringScenario(ctx context.Context, userID, scenarioID string, now time.Time) (*scenariodomain.Scenario, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(scenarioID) == "" || now.IsZero() {
		return nil, errors.New("scenario deprecation requires user, scenario, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin scenario deprecation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	target, err := lockPersistedScenarioTx(ctx, tx, scenarioID)
	if err != nil {
		return nil, err
	}
	if target.SourceKind != scenariodomain.SourceAuthoring {
		return nil, scenariodomain.ErrNotMutable
	}
	if target.OwnerUserID != strings.TrimSpace(userID) {
		return nil, scenariodomain.ErrNotFound
	}
	if target.State != scenariodomain.StateActive {
		return nil, scenariodomain.ErrNotMutable
	}
	active, err := lockPersistedScenarioRevisionTx(ctx, tx, target.ActiveRevisionID)
	if err != nil {
		return nil, err
	}
	if active.ScenarioID != target.ID || active.State != scenariodomain.RevisionActive {
		return nil, scenariodomain.ErrRevisionConflict
	}

	updated, err := scanPersistedScenario(tx.QueryRowContext(ctx, `UPDATE scenarios SET state = ?, updated_at = ?
		WHERE id = ? AND state = ? RETURNING `+persistedScenarioColumns,
		scenariodomain.StateDeprecated, now.UTC(), target.ID, scenariodomain.StateActive))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, scenariodomain.ErrRevisionConflict
	}
	if err != nil {
		return nil, fmt.Errorf("deprecate scenario: %w", err)
	}
	if updated.ActiveRevisionID != target.ActiveRevisionID || updated.SourceSlug != target.SourceSlug {
		return nil, scenariodomain.ErrRevisionConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit scenario deprecation: %w", err)
	}
	return updated, nil
}

func lockPersistedScenarioTx(ctx context.Context, tx *Tx, id string) (*scenariodomain.Scenario, error) {
	value, err := scanPersistedScenario(tx.QueryRowContext(ctx, `SELECT `+persistedScenarioColumns+` FROM scenarios WHERE id = ? FOR UPDATE`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, scenariodomain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock scenario: %w", err)
	}
	return value, nil
}

func lockPersistedScenarioRevisionTx(ctx context.Context, tx *Tx, id string) (*scenariodomain.Revision, error) {
	value, err := scanPersistedScenarioRevision(tx.QueryRowContext(ctx, `SELECT `+persistedScenarioRevisionColumns+` FROM scenario_revisions WHERE id = ? FOR UPDATE`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, scenariodomain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock scenario revision: %w", err)
	}
	return value, nil
}

func scanPersistedScenario(row scanner) (*scenariodomain.Scenario, error) {
	var value scenariodomain.Scenario
	if err := row.Scan(&value.ID, &value.SourceKind, &value.SourceRef, &value.OwnerUserID, &value.State, &value.ActiveRevisionID,
		&value.SourceSlug, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return nil, err
	}
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	if !value.Valid() {
		return nil, errors.New("stored scenario is invalid")
	}
	return &value, nil
}

func scanPersistedScenarioRevision(row scanner) (*scenariodomain.Revision, error) {
	var value scenariodomain.Revision
	var tags []byte
	if err := row.Scan(&value.ID, &value.ScenarioID, &value.SourceKind, &value.SourceRef, &value.SourceRevisionID, &value.BaseActiveRevisionID,
		&value.Title, &value.Type, &tags, &value.ContentRevision, &value.SourceSlug, &value.MaterializedPath, &value.MaterializedRevision,
		&value.RunnableRevisionRef.ID, &value.RunnableRevisionRef.Digest, &value.VerificationReportRef.ID, &value.VerificationReportRef.Digest,
		&value.State, &value.PublishedAt, &value.CreatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(tags, &value.Tags); err != nil {
		return nil, fmt.Errorf("decode scenario revision tags: %w", err)
	}
	value.PublishedAt = value.PublishedAt.UTC()
	value.CreatedAt = value.CreatedAt.UTC()
	if !value.Valid() {
		return nil, errors.New("stored scenario revision is invalid")
	}
	return &value, nil
}

func scanActiveScenarioRevision(row scanner) (*scenariodomain.ActiveRevision, error) {
	var value scenariodomain.ActiveRevision
	var tags []byte
	if err := row.Scan(
		&value.Scenario.ID, &value.Scenario.SourceKind, &value.Scenario.SourceRef, &value.Scenario.OwnerUserID,
		&value.Scenario.State, &value.Scenario.ActiveRevisionID, &value.Scenario.SourceSlug, &value.Scenario.CreatedAt, &value.Scenario.UpdatedAt,
		&value.Revision.ID, &value.Revision.ScenarioID, &value.Revision.SourceKind, &value.Revision.SourceRef,
		&value.Revision.SourceRevisionID, &value.Revision.BaseActiveRevisionID, &value.Revision.Title,
		&value.Revision.Type, &tags, &value.Revision.ContentRevision, &value.Revision.SourceSlug, &value.Revision.MaterializedPath,
		&value.Revision.MaterializedRevision, &value.Revision.RunnableRevisionRef.ID, &value.Revision.RunnableRevisionRef.Digest,
		&value.Revision.VerificationReportRef.ID, &value.Revision.VerificationReportRef.Digest, &value.Revision.State, &value.Revision.PublishedAt, &value.Revision.CreatedAt,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(tags, &value.Revision.Tags); err != nil {
		return nil, fmt.Errorf("decode active scenario revision tags: %w", err)
	}
	value.Scenario.CreatedAt = value.Scenario.CreatedAt.UTC()
	value.Scenario.UpdatedAt = value.Scenario.UpdatedAt.UTC()
	value.Revision.PublishedAt = value.Revision.PublishedAt.UTC()
	value.Revision.CreatedAt = value.Revision.CreatedAt.UTC()
	if !value.Valid() {
		return nil, errors.New("stored active scenario revision is invalid")
	}
	return &value, nil
}

func insertPersistedScenarioTx(ctx context.Context, tx *Tx, value scenariodomain.Scenario) error {
	if !value.Valid() {
		return errors.New("scenario is invalid")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO scenarios
		(id, source_kind, source_ref, owner_user_id, state, active_revision_id, source_slug, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.SourceKind, value.SourceRef, value.OwnerUserID, value.State,
		value.ActiveRevisionID, value.SourceSlug, value.CreatedAt.UTC(), value.UpdatedAt.UTC()); err != nil {
		return fmt.Errorf("insert scenario: %w", err)
	}
	return nil
}

func insertPersistedScenarioRevisionTx(ctx context.Context, tx *Tx, value scenariodomain.Revision) error {
	if !value.Valid() {
		return errors.New("scenario revision is invalid")
	}
	tags, err := marshalJSON(value.Tags)
	if err != nil {
		return fmt.Errorf("encode scenario revision tags: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO scenario_revisions
		(id, scenario_id, source_kind, source_ref, source_revision_id, base_active_revision_id, title,
		scenario_type, tags, content_revision, source_slug, materialized_path, materialized_revision,
		runnable_revision_id, runnable_revision_digest, verification_report_id, verification_report_digest, state, published_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.ScenarioID, value.SourceKind,
		value.SourceRef, value.SourceRevisionID, value.BaseActiveRevisionID, value.Title, value.Type, tags, value.ContentRevision,
		value.SourceSlug, value.MaterializedPath, value.MaterializedRevision, value.RunnableRevisionRef.ID, value.RunnableRevisionRef.Digest,
		value.VerificationReportRef.ID, value.VerificationReportRef.Digest, value.State, value.PublishedAt.UTC(), value.CreatedAt.UTC()); err != nil {
		return fmt.Errorf("insert scenario revision: %w", err)
	}
	return nil
}

func scenarioRevisionFromPublication(publicationTitle string, scenarioType content.ScenarioType, tags []string, contentRevision, materializedRevision string, runnableRevision runnable.RevisionReference, verificationReport runnable.VerificationReportReference,
	id, scenarioID, sourceRef, sourceRevisionID, baseActiveRevisionID, sourceSlug, materializedPath string, sourceKind scenariodomain.SourceKind, publishedAt time.Time) scenariodomain.Revision {
	return scenariodomain.Revision{
		ID: id, ScenarioID: scenarioID, SourceKind: sourceKind, SourceRef: sourceRef, SourceRevisionID: sourceRevisionID,
		BaseActiveRevisionID: baseActiveRevisionID, Title: publicationTitle, Type: scenarioType, Tags: append([]string(nil), tags...), ContentRevision: contentRevision,
		SourceSlug: sourceSlug, MaterializedPath: materializedPath, MaterializedRevision: materializedRevision,
		RunnableRevisionRef: runnableRevision, VerificationReportRef: verificationReport, State: scenariodomain.RevisionActive, PublishedAt: publishedAt.UTC(), CreatedAt: publishedAt.UTC(),
	}
}
