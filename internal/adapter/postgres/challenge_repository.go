package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	contentchallenge "github.com/breakfix/breakfix/internal/content/challenge"
	challengedomain "github.com/breakfix/breakfix/internal/domain/challenge"
	execution "github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
)

const persistedChallengeColumns = `id, source_kind, source_ref, owner_user_id, state, active_revision_id, source_slug, created_at, updated_at`
const persistedChallengeRevisionColumns = `id, challenge_id, source_kind, source_ref, source_revision_id, base_active_revision_id,
	title, runtime, content_revision, source_slug, materialized_path, materialized_revision, artifact_reference, state, published_at, created_at`

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

// DeprecateAuthoringChallenge removes one author-owned active Challenge from
// the current public Roadmap while retaining its stable identity, immutable
// revisions, and all Environment history. A later revision cannot race this
// operation because both paths lock the Challenge and current Roadmap.
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

	current, err := currentRoadmapForUpdateTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := ensureRoadmapPublicationAllowedTx(ctx, tx, now.UTC()); err != nil {
		return nil, err
	}
	next, err := removeActiveChallengeBinding(*current, *target)
	if err != nil {
		return nil, err
	}
	_, encodedRoadmap, revisionID, err := canonicalRoadmap(next)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO roadmap_revisions (id, content_json, created_at)
		VALUES (?, ?::jsonb, ?) ON CONFLICT (id) DO NOTHING`, revisionID, encodedRoadmap, now.UTC()); err != nil {
		return nil, fmt.Errorf("store deprecated challenge roadmap revision: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE roadmap_current SET revision_id = ?, updated_at = ? WHERE singleton = TRUE`, revisionID, now.UTC()); err != nil {
		return nil, fmt.Errorf("publish deprecated challenge roadmap revision: %w", err)
	}
	if err := removeRoadmapEntryTx(ctx, tx, target.ID); err != nil {
		return nil, err
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

func removeActiveChallengeBinding(current roadmap.Revision, target challengedomain.Challenge) (roadmap.Revision, error) {
	next := current.Clone()
	bindings := make([]roadmap.ChallengeBinding, 0, len(next.ChallengeBindings)-1)
	found := false
	for _, binding := range next.ChallengeBindings {
		if binding.Challenge.ID != target.ID {
			bindings = append(bindings, binding)
			continue
		}
		if binding.Challenge.RevisionID != target.ActiveRevisionID || binding.Challenge.SourceSlug != target.SourceSlug {
			return roadmap.Revision{}, challengedomain.ErrRevisionConflict
		}
		found = true
	}
	if !found {
		return roadmap.Revision{}, challengedomain.ErrRevisionConflict
	}
	next.ChallengeBindings = bindings
	edges := make([]roadmap.Edge, 0, len(next.ChallengeEdges))
	for _, edge := range next.ChallengeEdges {
		if edge.Source.ID != target.ID && edge.Target.ID != target.ID {
			edges = append(edges, edge)
		}
	}
	next.ChallengeEdges = edges
	if err := next.Validate(); err != nil {
		return roadmap.Revision{}, fmt.Errorf("validate deprecated challenge roadmap: %w", err)
	}
	return next, nil
}

func removeRoadmapEntryTx(ctx context.Context, tx *Tx, challengeID string) error {
	var topicProcessed, challengeProcessed bool
	err := tx.QueryRowContext(ctx, `SELECT topic_processed, challenge_processed FROM roadmap_entries
		WHERE challenge_id = ? FOR UPDATE`, challengeID).Scan(&topicProcessed, &challengeProcessed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock deprecated roadmap entry: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM roadmap_entries WHERE challenge_id = ?`, challengeID); err != nil {
		return fmt.Errorf("remove deprecated roadmap entry: %w", err)
	}
	if topicProcessed && challengeProcessed {
		return nil
	}
	control, err := lockRoadmapMaintenanceControlTx(ctx, tx)
	if err != nil {
		return err
	}
	if !control.Requested && control.UnrequestedChallengeCount > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE roadmap_maintenance_control
			SET unrequested_challenge_count = unrequested_challenge_count - 1 WHERE singleton = TRUE`); err != nil {
			return fmt.Errorf("adjust deprecated roadmap request count: %w", err)
		}
	}
	return nil
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
	var artifact []byte
	if err := row.Scan(&value.ID, &value.ChallengeID, &value.SourceKind, &value.SourceRef, &value.SourceRevisionID, &value.BaseActiveRevisionID,
		&value.Title, &value.Runtime, &value.ContentRevision, &value.SourceSlug, &value.MaterializedPath, &value.MaterializedRevision,
		&artifact, &value.State, &value.PublishedAt, &value.CreatedAt); err != nil {
		return nil, err
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO challenge_revisions
		(id, challenge_id, source_kind, source_ref, source_revision_id, base_active_revision_id, title, runtime,
		content_revision, source_slug, materialized_path, materialized_revision, artifact_reference, state, published_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?)`, value.ID, value.ChallengeID, value.SourceKind,
		value.SourceRef, value.SourceRevisionID, value.BaseActiveRevisionID, value.Title, value.Runtime, value.ContentRevision,
		value.SourceSlug, value.MaterializedPath, value.MaterializedRevision, artifact, value.State, value.PublishedAt.UTC(), value.CreatedAt.UTC()); err != nil {
		return fmt.Errorf("insert challenge revision: %w", err)
	}
	return nil
}

func challengeRevisionFromPublication(publicationTitle, runtime, contentRevision, materializedRevision string, publicationArtifact execution.ArtifactReference,
	id, challengeID, sourceRef, sourceRevisionID, baseActiveRevisionID, sourceSlug, materializedPath string, sourceKind challengedomain.SourceKind, publishedAt time.Time) challengedomain.Revision {
	return challengedomain.Revision{
		ID: id, ChallengeID: challengeID, SourceKind: sourceKind, SourceRef: sourceRef, SourceRevisionID: sourceRevisionID,
		BaseActiveRevisionID: baseActiveRevisionID, Title: publicationTitle, Runtime: runtime, ContentRevision: contentRevision,
		SourceSlug: sourceSlug, MaterializedPath: materializedPath, MaterializedRevision: materializedRevision,
		Artifact: publicationArtifact, State: challengedomain.RevisionActive, PublishedAt: publishedAt.UTC(), CreatedAt: publishedAt.UTC(),
	}
}

func challengeContentRevisionValid(value string) bool {
	return contentchallenge.ValidRevision(value)
}
