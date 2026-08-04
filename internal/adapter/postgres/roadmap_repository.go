package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/roadmap"
)

var ErrRoadmapRevisionNotFound = errors.New("roadmap revision not found")

// PublishRoadmap stores a complete immutable projection and atomically makes
// it current. The revision ID is the canonical JSON digest, so retries always
// identify exactly the same durable projection.
func (d *RoadmapRepository) PublishRoadmap(ctx context.Context, value roadmap.Revision, now time.Time) (*roadmap.Revision, error) {
	if now.IsZero() {
		return nil, errors.New("roadmap publication requires current time")
	}
	canonical, encoded, revisionID, err := canonicalRoadmap(value)
	if err != nil {
		return nil, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin roadmap publication: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT revision_id FROM roadmap_current WHERE singleton = TRUE FOR UPDATE`); err != nil {
		return nil, fmt.Errorf("lock roadmap current revision: %w", err)
	}
	if err := ensureRoadmapPublicationAllowedTx(ctx, tx, now); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO roadmap_revisions (id, content_json, created_at)
		VALUES (?, ?::jsonb, ?)
		ON CONFLICT (id) DO NOTHING`, revisionID, encoded, now.UTC()); err != nil {
		return nil, fmt.Errorf("store roadmap revision: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE roadmap_current SET revision_id = ?, updated_at = ? WHERE singleton = TRUE`, revisionID, now.UTC()); err != nil {
		return nil, fmt.Errorf("set current roadmap revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit roadmap publication: %w", err)
	}
	canonical.Revision = revisionID
	return &canonical, nil
}

func (d *RoadmapRepository) CurrentRoadmap(ctx context.Context) (*roadmap.Revision, error) {
	var revisionID sql.NullString
	if err := d.conn.QueryRowContext(ctx, `SELECT revision_id FROM roadmap_current WHERE singleton = TRUE`).Scan(&revisionID); err != nil {
		return nil, fmt.Errorf("read current roadmap revision: %w", err)
	}
	if !revisionID.Valid || strings.TrimSpace(revisionID.String) == "" {
		return nil, roadmap.ErrNoCurrentRevision
	}
	return d.RoadmapRevision(ctx, revisionID.String)
}

func (d *RoadmapRepository) CurrentRoadmapID(ctx context.Context) (string, error) {
	var revisionID sql.NullString
	if err := d.conn.QueryRowContext(ctx, `SELECT revision_id FROM roadmap_current WHERE singleton = TRUE`).Scan(&revisionID); err != nil {
		return "", fmt.Errorf("read current roadmap revision: %w", err)
	}
	if !revisionID.Valid || strings.TrimSpace(revisionID.String) == "" {
		return "", roadmap.ErrNoCurrentRevision
	}
	return revisionID.String, nil
}

func (d *RoadmapRepository) HasCurrentRoadmap(ctx context.Context) (bool, error) {
	_, err := d.CurrentRoadmapID(ctx)
	if errors.Is(err, roadmap.ErrNoCurrentRevision) {
		return false, nil
	}
	return err == nil, err
}

func (d *RoadmapRepository) RoadmapRevision(ctx context.Context, revisionID string) (*roadmap.Revision, error) {
	if !roadmap.ValidRevision(strings.TrimSpace(revisionID)) {
		return nil, fmt.Errorf("invalid roadmap revision %q", revisionID)
	}
	var encoded []byte
	if err := d.conn.QueryRowContext(ctx, `SELECT content_json FROM roadmap_revisions WHERE id = ?`, revisionID).Scan(&encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRoadmapRevisionNotFound
		}
		return nil, fmt.Errorf("read roadmap revision: %w", err)
	}
	var value roadmap.Revision
	if err := json.Unmarshal(encoded, &value); err != nil {
		return nil, fmt.Errorf("decode roadmap revision: %w", err)
	}
	value.Revision = revisionID
	if err := value.Validate(); err != nil {
		return nil, fmt.Errorf("validate roadmap revision: %w", err)
	}
	return &value, nil
}

func canonicalRoadmap(value roadmap.Revision) (roadmap.Revision, []byte, string, error) {
	value = value.Sorted()
	value.Revision = ""
	if err := value.Validate(); err != nil {
		return roadmap.Revision{}, nil, "", fmt.Errorf("validate roadmap revision: %w", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return roadmap.Revision{}, nil, "", fmt.Errorf("encode roadmap revision: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return value, encoded, "sha256:" + hex.EncodeToString(sum[:]), nil
}
