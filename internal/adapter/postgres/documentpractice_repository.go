package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
)

func (d *DocumentPracticeRepository) SaveArtifact(ctx context.Context, workflowID string, artifact domain.ArtifactRecord) error {
	if d == nil || d.conn == nil || strings.TrimSpace(workflowID) == "" {
		return errors.New("document artifact repository requires workflow and connection")
	}
	if err := artifact.Validate(); err != nil {
		return err
	}
	payload := artifact.Payload
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}
	if !json.Valid(payload) {
		return errors.New("document artifact payload must be JSON")
	}
	_, err := d.conn.ExecContext(ctx, `INSERT INTO document_artifact_ledger (id, workflow_id, kind, parent_id, content_revision, digest, schema_version, owner_role, policy_version, payload, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (workflow_id, kind, digest) DO NOTHING`, artifact.ID, workflowID, artifact.Kind, artifact.ParentID, artifact.ContentRevision, artifact.Digest, artifact.SchemaVersion, artifact.OwnerRole, artifact.PolicyVersion, payload, artifact.CreatedAt.UTC())
	return err
}

func (d *DocumentPracticeRepository) ListArtifacts(ctx context.Context, workflowID string) ([]domain.ArtifactRecord, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT id, kind, parent_id, content_revision, digest, schema_version, owner_role, policy_version, payload, created_at FROM document_artifact_ledger WHERE workflow_id = ? ORDER BY created_at, id`, workflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []domain.ArtifactRecord{}
	for rows.Next() {
		var a domain.ArtifactRecord
		var payload []byte
		if err := rows.Scan(&a.ID, &a.Kind, &a.ParentID, &a.ContentRevision, &a.Digest, &a.SchemaVersion, &a.OwnerRole, &a.PolicyVersion, &payload, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Payload = append([]byte(nil), payload...)
		result = append(result, a)
	}
	return result, rows.Err()
}

func (d *DocumentPracticeRepository) CreateWorkflow(ctx context.Context, workflow domain.Workflow) error {
	if err := workflow.Validate(); err != nil {
		return err
	}
	_, err := d.conn.ExecContext(ctx, `INSERT INTO document_workflows (id, state, state_version, revision, max_revisions, lease_owner, lease_expires_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, workflow.ID, workflow.State, workflow.StateVersion, workflow.Revision, workflow.MaxRevisions, workflow.LeaseOwner, workflow.LeaseExpiresAt, workflow.UpdatedAt.UTC())
	return err
}

func (d *DocumentPracticeRepository) GetWorkflow(ctx context.Context, id string) (domain.Workflow, error) {
	var w domain.Workflow
	err := d.conn.QueryRowContext(ctx, `SELECT id, state, state_version, revision, max_revisions, lease_owner, lease_expires_at, updated_at FROM document_workflows WHERE id = ?`, id).Scan(&w.ID, &w.State, &w.StateVersion, &w.Revision, &w.MaxRevisions, &w.LeaseOwner, &w.LeaseExpiresAt, &w.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Workflow{}, errors.New("document workflow not found")
	}
	if err != nil {
		return domain.Workflow{}, fmt.Errorf("get document workflow: %w", err)
	}
	return w, nil
}

func (d *DocumentPracticeRepository) SaveManifest(ctx context.Context, workflowID string, manifest domain.PublicationManifest) error {
	if err := manifest.Context.Validate(); err != nil {
		return err
	}
	bytes, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	digest, err := jsonDigest(bytes)
	if err != nil {
		return err
	}
	_, err = d.conn.ExecContext(ctx, `INSERT INTO document_publication_manifests (id, workflow_id, manifest, manifest_digest, created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT (manifest_digest) DO NOTHING`, manifest.ID, workflowID, bytes, digest, manifest.CreatedAt.UTC())
	return err
}

func jsonDigest(value []byte) (string, error) {
	if len(value) == 0 {
		return "", errors.New("empty JSON")
	}
	return domainDigest(value), nil
}
func domainDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + fmt.Sprintf("%x", sum[:])
}
