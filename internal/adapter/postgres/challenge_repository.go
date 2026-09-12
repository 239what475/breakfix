package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	content "github.com/breakfix/breakfix/internal/content/challenge"
	challengedomain "github.com/breakfix/breakfix/internal/domain/challenge"
	execution "github.com/breakfix/breakfix/internal/domain/execution"
)

const persistedChallengeColumns = `id, source_kind, source_ref, owner_user_id, state, active_revision_id, source_slug, created_at, updated_at`
const persistedChallengeRevisionColumns = `id, challenge_id, source_kind, source_ref, source_revision_id, base_active_revision_id,
	title, runtime, scenario_type, tags, content_revision, source_slug, materialized_path, materialized_revision, artifact_reference, state, published_at, created_at`
const activeChallengeRevisionColumns = `
	c.id, c.source_kind, c.source_ref, c.owner_user_id, c.state, c.active_revision_id, c.source_slug, c.created_at, c.updated_at,
	r.id, r.challenge_id, r.source_kind, r.source_ref, r.source_revision_id, r.base_active_revision_id,
	r.title, r.runtime, r.scenario_type, r.tags, r.content_revision, r.source_slug, r.materialized_path, r.materialized_revision,
	r.artifact_reference, r.state, r.published_at, r.created_at`

// GetChallenge returns the stable identity and its active pointer. Callers that
// need content must resolve the returned revision explicitly; this method never
// reads a mutable materialized directory.
func (d *ChallengeRepository) GetChallenge(ctx context.Context, id string) (*challengedomain.Challenge, error) {
	value, err := scanPersistedChallenge(d.conn.QueryRowContext(ctx, `SELECT `+persistedChallengeColumns+` FROM challenges WHERE id = ?`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, challengedomain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get challenge: %w", err)
	}
	return value, nil
}

// GetChallengeRevision returns one exact immutable published revision.
func (d *ChallengeRepository) GetChallengeRevision(ctx context.Context, challengeID, revisionID string) (*challengedomain.Revision, error) {
	value, err := scanPersistedChallengeRevision(d.conn.QueryRowContext(ctx, `SELECT `+persistedChallengeRevisionColumns+`
		FROM challenge_revisions WHERE challenge_id = ? AND id = ?`, strings.TrimSpace(challengeID), strings.TrimSpace(revisionID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, challengedomain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get challenge revision: %w", err)
	}
	return value, nil
}

// ListActiveChallengeRevisions reads the Catalog directly from stable
// identities and their active immutable revision pointers.
func (d *ChallengeRepository) ListActiveChallengeRevisions(ctx context.Context) ([]challengedomain.ActiveRevision, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT `+activeChallengeRevisionColumns+`
		FROM challenges c
		JOIN challenge_revisions r ON r.challenge_id = c.id AND r.id = c.active_revision_id
		WHERE c.state = ? AND r.state = ?
		ORDER BY r.published_at DESC, c.id`, challengedomain.StateActive, challengedomain.RevisionActive)
	if err != nil {
		return nil, fmt.Errorf("list active challenge revisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]challengedomain.ActiveRevision, 0)
	for rows.Next() {
		value, scanErr := scanActiveChallengeRevision(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan active challenge revision: %w", scanErr)
		}
		result = append(result, *value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active challenge revisions: %w", err)
	}
	return result, nil
}

// ListAuthoringChallenges returns the stable identities visible in one
// author's space. Revision details stay separate so callers cannot confuse a
// stable challenge with one particular immutable version.
func (d *ChallengeRepository) ListAuthoringChallenges(ctx context.Context, userID string) ([]challengedomain.Challenge, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("authoring challenge list requires a user")
	}
	rows, err := d.conn.QueryContext(ctx, `SELECT `+persistedChallengeColumns+`
		FROM challenges WHERE source_kind = ? AND owner_user_id = ?
		ORDER BY updated_at DESC, id DESC`, challengedomain.SourceAuthoring, strings.TrimSpace(userID))
	if err != nil {
		return nil, fmt.Errorf("list authoring challenges: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]challengedomain.Challenge, 0)
	for rows.Next() {
		value, scanErr := scanPersistedChallenge(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan authoring challenge: %w", scanErr)
		}
		result = append(result, *value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate authoring challenges: %w", err)
	}
	return result, nil
}

// DeprecateAuthoringChallenge hides one author-owned active Challenge from
// the public Catalog while retaining its stable identity, immutable revisions,
// and all Environment history.
func (d *ChallengeRepository) DeprecateAuthoringChallenge(ctx context.Context, userID, challengeID string, now time.Time) (*challengedomain.Challenge, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(challengeID) == "" || now.IsZero() {
		return nil, errors.New("challenge deprecation requires user, challenge, and current time")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin challenge deprecation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	target, err := lockPersistedChallengeTx(ctx, tx, challengeID)
	if err != nil {
		return nil, err
	}
	if target.SourceKind != challengedomain.SourceAuthoring {
		return nil, challengedomain.ErrNotMutable
	}
	if target.OwnerUserID != strings.TrimSpace(userID) {
		return nil, challengedomain.ErrNotFound
	}
	if target.State != challengedomain.StateActive {
		return nil, challengedomain.ErrNotMutable
	}
	active, err := lockPersistedChallengeRevisionTx(ctx, tx, target.ActiveRevisionID)
	if err != nil {
		return nil, err
	}
	if active.ChallengeID != target.ID || active.State != challengedomain.RevisionActive {
		return nil, challengedomain.ErrRevisionConflict
	}

	updated, err := scanPersistedChallenge(tx.QueryRowContext(ctx, `UPDATE challenges SET state = ?, updated_at = ?
		WHERE id = ? AND state = ? RETURNING `+persistedChallengeColumns,
		challengedomain.StateDeprecated, now.UTC(), target.ID, challengedomain.StateActive))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, challengedomain.ErrRevisionConflict
	}
	if err != nil {
		return nil, fmt.Errorf("deprecate challenge: %w", err)
	}
	if updated.ActiveRevisionID != target.ActiveRevisionID || updated.SourceSlug != target.SourceSlug {
		return nil, challengedomain.ErrRevisionConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit challenge deprecation: %w", err)
	}
	return updated, nil
}

func lockPersistedChallengeTx(ctx context.Context, tx *Tx, id string) (*challengedomain.Challenge, error) {
	value, err := scanPersistedChallenge(tx.QueryRowContext(ctx, `SELECT `+persistedChallengeColumns+` FROM challenges WHERE id = ? FOR UPDATE`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, challengedomain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock challenge: %w", err)
	}
	return value, nil
}

func lockPersistedChallengeRevisionTx(ctx context.Context, tx *Tx, id string) (*challengedomain.Revision, error) {
	value, err := scanPersistedChallengeRevision(tx.QueryRowContext(ctx, `SELECT `+persistedChallengeRevisionColumns+` FROM challenge_revisions WHERE id = ? FOR UPDATE`, strings.TrimSpace(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, challengedomain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock challenge revision: %w", err)
	}
	return value, nil
}

func scanPersistedChallenge(row scanner) (*challengedomain.Challenge, error) {
	var value challengedomain.Challenge
	if err := row.Scan(&value.ID, &value.SourceKind, &value.SourceRef, &value.OwnerUserID, &value.State, &value.ActiveRevisionID,
		&value.SourceSlug, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return nil, err
	}
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	if !value.Valid() {
		return nil, errors.New("stored challenge is invalid")
	}
	return &value, nil
}

func scanPersistedChallengeRevision(row scanner) (*challengedomain.Revision, error) {
	var value challengedomain.Revision
	var tags, artifact []byte
	if err := row.Scan(&value.ID, &value.ChallengeID, &value.SourceKind, &value.SourceRef, &value.SourceRevisionID, &value.BaseActiveRevisionID,
		&value.Title, &value.Runtime, &value.Type, &tags, &value.ContentRevision, &value.SourceSlug, &value.MaterializedPath, &value.MaterializedRevision,
		&artifact, &value.State, &value.PublishedAt, &value.CreatedAt); err != nil {
		return nil, err
	}
	if err := decodeOptionalJSON(tags, &value.Tags); err != nil {
		return nil, fmt.Errorf("decode challenge revision tags: %w", err)
	}
	if err := decodeOptionalJSON(artifact, &value.Artifact); err != nil {
		return nil, fmt.Errorf("decode challenge revision artifact: %w", err)
	}
	value.PublishedAt = value.PublishedAt.UTC()
	value.CreatedAt = value.CreatedAt.UTC()
	if !value.Valid() {
		return nil, errors.New("stored challenge revision is invalid")
	}
	return &value, nil
}

func scanActiveChallengeRevision(row scanner) (*challengedomain.ActiveRevision, error) {
	var value challengedomain.ActiveRevision
	var tags, artifact []byte
	if err := row.Scan(
		&value.Challenge.ID, &value.Challenge.SourceKind, &value.Challenge.SourceRef, &value.Challenge.OwnerUserID,
		&value.Challenge.State, &value.Challenge.ActiveRevisionID, &value.Challenge.SourceSlug, &value.Challenge.CreatedAt, &value.Challenge.UpdatedAt,
		&value.Revision.ID, &value.Revision.ChallengeID, &value.Revision.SourceKind, &value.Revision.SourceRef,
		&value.Revision.SourceRevisionID, &value.Revision.BaseActiveRevisionID, &value.Revision.Title, &value.Revision.Runtime,
		&value.Revision.Type, &tags, &value.Revision.ContentRevision, &value.Revision.SourceSlug, &value.Revision.MaterializedPath,
		&value.Revision.MaterializedRevision, &artifact, &value.Revision.State, &value.Revision.PublishedAt, &value.Revision.CreatedAt,
	); err != nil {
		return nil, err
	}
	if err := decodeOptionalJSON(tags, &value.Revision.Tags); err != nil {
		return nil, fmt.Errorf("decode active challenge revision tags: %w", err)
	}
	if err := decodeOptionalJSON(artifact, &value.Revision.Artifact); err != nil {
		return nil, fmt.Errorf("decode active challenge revision artifact: %w", err)
	}
	value.Challenge.CreatedAt = value.Challenge.CreatedAt.UTC()
	value.Challenge.UpdatedAt = value.Challenge.UpdatedAt.UTC()
	value.Revision.PublishedAt = value.Revision.PublishedAt.UTC()
	value.Revision.CreatedAt = value.Revision.CreatedAt.UTC()
	if !value.Valid() {
		return nil, errors.New("stored active challenge revision is invalid")
	}
	return &value, nil
}

func insertPersistedChallengeTx(ctx context.Context, tx *Tx, value challengedomain.Challenge) error {
	if !value.Valid() {
		return errors.New("challenge is invalid")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO challenges
		(id, source_kind, source_ref, owner_user_id, state, active_revision_id, source_slug, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.SourceKind, value.SourceRef, value.OwnerUserID, value.State,
		value.ActiveRevisionID, value.SourceSlug, value.CreatedAt.UTC(), value.UpdatedAt.UTC()); err != nil {
		return fmt.Errorf("insert challenge: %w", err)
	}
	return nil
}

func insertPersistedChallengeRevisionTx(ctx context.Context, tx *Tx, value challengedomain.Revision) error {
	if !value.Valid() {
		return errors.New("challenge revision is invalid")
	}
	artifact, err := marshalJSON(value.Artifact)
	if err != nil {
		return fmt.Errorf("encode challenge revision artifact: %w", err)
	}
	tags, err := marshalJSON(value.Tags)
	if err != nil {
		return fmt.Errorf("encode challenge revision tags: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO challenge_revisions
		(id, challenge_id, source_kind, source_ref, source_revision_id, base_active_revision_id, title, runtime,
		scenario_type, tags, content_revision, source_slug, materialized_path, materialized_revision, artifact_reference, state, published_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?::jsonb, ?, ?, ?)`, value.ID, value.ChallengeID, value.SourceKind,
		value.SourceRef, value.SourceRevisionID, value.BaseActiveRevisionID, value.Title, value.Runtime, value.Type, tags, value.ContentRevision,
		value.SourceSlug, value.MaterializedPath, value.MaterializedRevision, artifact, value.State, value.PublishedAt.UTC(), value.CreatedAt.UTC()); err != nil {
		return fmt.Errorf("insert challenge revision: %w", err)
	}
	return nil
}

func challengeRevisionFromPublication(publicationTitle, runtime string, scenarioType content.ScenarioType, tags []string, contentRevision, materializedRevision string, publicationArtifact execution.ArtifactReference,
	id, challengeID, sourceRef, sourceRevisionID, baseActiveRevisionID, sourceSlug, materializedPath string, sourceKind challengedomain.SourceKind, publishedAt time.Time) challengedomain.Revision {
	return challengedomain.Revision{
		ID: id, ChallengeID: challengeID, SourceKind: sourceKind, SourceRef: sourceRef, SourceRevisionID: sourceRevisionID,
		BaseActiveRevisionID: baseActiveRevisionID, Title: publicationTitle, Runtime: runtime, Type: scenarioType, Tags: append([]string(nil), tags...), ContentRevision: contentRevision,
		SourceSlug: sourceSlug, MaterializedPath: materializedPath, MaterializedRevision: materializedRevision,
		Artifact: publicationArtifact, State: challengedomain.RevisionActive, PublishedAt: publishedAt.UTC(), CreatedAt: publishedAt.UTC(),
	}
}
