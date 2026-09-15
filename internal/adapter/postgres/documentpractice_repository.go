package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
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

// AppendArtifact serializes ledger writes with the owning workflow. It never
// updates an existing record: a retry may replay identical bytes only.
func (d *DocumentPracticeRepository) AppendArtifact(ctx context.Context, workflowID string, artifact domain.ArtifactRecord) error {
	if err := artifact.Validate(); err != nil || strings.TrimSpace(workflowID) == "" {
		return errors.New("append document artifact is invalid")
	}
	payload := artifact.Payload
	if len(payload) == 0 {
		payload = []byte(`{}`)
	}
	if !json.Valid(payload) {
		return errors.New("document artifact payload must be JSON")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin append document artifact: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM document_workflows WHERE id = ? FOR UPDATE)`, workflowID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("document workflow not found")
	}
	var previousDigest string
	err = tx.QueryRowContext(ctx, `SELECT digest FROM document_artifact_ledger WHERE id = ? FOR UPDATE`, artifact.ID).Scan(&previousDigest)
	if err == nil {
		if previousDigest != artifact.Digest {
			return errors.New("document artifact id already has another digest")
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO document_artifact_ledger (id, workflow_id, kind, parent_id, content_revision, digest, schema_version, owner_role, policy_version, payload, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, artifact.ID, workflowID, artifact.Kind, artifact.ParentID, artifact.ContentRevision, artifact.Digest, artifact.SchemaVersion, artifact.OwnerRole, artifact.PolicyVersion, payload, artifact.CreatedAt.UTC()); err != nil {
		return fmt.Errorf("insert document artifact: %w", err)
	}
	return tx.Commit()
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
	artifacts, err := d.ListArtifacts(ctx, id)
	if err != nil {
		return domain.Workflow{}, err
	}
	w.Artifacts = artifacts
	return w, nil
}

// AdvanceWorkflow atomically checks the expected state version and ledger
// prerequisites before changing the durable state. It rejects any skipped
// phase even when a caller has database access to this repository.
func (d *DocumentPracticeRepository) AdvanceWorkflow(ctx context.Context, workflowID string, expectedStateVersion int64, next domain.WorkflowState, now time.Time, requiredKinds ...string) (domain.Workflow, error) {
	if strings.TrimSpace(workflowID) == "" || expectedStateVersion < 1 || now.IsZero() {
		return domain.Workflow{}, errors.New("advance document workflow is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return domain.Workflow{}, fmt.Errorf("begin advance document workflow: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	w, err := scanDocumentWorkflow(tx.QueryRowContext(ctx, `SELECT id, state, state_version, revision, max_revisions, lease_owner, lease_expires_at, updated_at FROM document_workflows WHERE id = ? FOR UPDATE`, workflowID))
	if err != nil {
		return domain.Workflow{}, err
	}
	if w.StateVersion != expectedStateVersion {
		return domain.Workflow{}, errors.New("document workflow state version is stale")
	}
	artifacts, err := listDocumentArtifacts(ctx, tx, workflowID)
	if err != nil {
		return domain.Workflow{}, err
	}
	w.Artifacts = artifacts
	if err := w.AdvanceAt(next, now, requiredKinds...); err != nil {
		return domain.Workflow{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE document_workflows SET state = ?, state_version = ?, updated_at = ? WHERE id = ? AND state_version = ?`, w.State, w.StateVersion, w.UpdatedAt, workflowID, expectedStateVersion)
	if err != nil {
		return domain.Workflow{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return domain.Workflow{}, errors.New("document workflow transition lost its fence")
	}
	if err := tx.Commit(); err != nil {
		return domain.Workflow{}, err
	}
	return w, nil
}

func (d *DocumentPracticeRepository) AcquireWorkflowLease(ctx context.Context, workflowID, owner string, ttl time.Duration, now time.Time) (domain.Workflow, error) {
	if strings.TrimSpace(workflowID) == "" || strings.TrimSpace(owner) == "" || ttl <= 0 || now.IsZero() {
		return domain.Workflow{}, errors.New("document workflow lease is invalid")
	}
	expires := now.UTC().Add(ttl)
	result, err := d.conn.ExecContext(ctx, `UPDATE document_workflows SET lease_owner = ?, lease_expires_at = ?, updated_at = ? WHERE id = ? AND (lease_owner = '' OR lease_owner = ? OR lease_expires_at <= ?)`, owner, expires, now.UTC(), workflowID, owner, now.UTC())
	if err != nil {
		return domain.Workflow{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return domain.Workflow{}, errors.New("document workflow lease is held")
	}
	return d.GetWorkflow(ctx, workflowID)
}

// BindRunnableAction reserves the exact public runtime identity before it can
// be claimed by a Worker. The binding is immutable and is intentionally a
// documentation-product projection, not a runtime action field.
func (d *DocumentPracticeRepository) BindRunnableAction(ctx context.Context, workflowID string, action runnable.ActionIdentity, now time.Time) error {
	if strings.TrimSpace(workflowID) == "" || action.Validate() != nil || now.IsZero() {
		return errors.New("document runnable action binding is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin bind document runnable action: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM document_workflows WHERE id = ? FOR UPDATE)`, workflowID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("document workflow not found")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO document_runnable_actions (action_key, workflow_id, content_kind, content_id, content_revision, spec_digest, phase, state_version, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (action_key) DO NOTHING`,
		action.Key(), workflowID, action.Content.Kind, action.Content.ID, action.Content.Revision, action.SpecDigest, action.Phase, action.StateVersion, now.UTC()); err != nil {
		return fmt.Errorf("insert document runnable action binding: %w", err)
	}
	var storedWorkflow, contentKind, contentID, contentRevision, specDigest string
	var phase runnable.ActionPhase
	var stateVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT workflow_id, content_kind, content_id, content_revision, spec_digest, phase, state_version
		FROM document_runnable_actions WHERE action_key = ? FOR UPDATE`, action.Key()).Scan(&storedWorkflow, &contentKind, &contentID, &contentRevision, &specDigest, &phase, &stateVersion); err != nil {
		return fmt.Errorf("read document runnable action binding: %w", err)
	}
	stored := runnable.ActionIdentity{Content: runnable.ContentIdentity{Kind: contentKind, ID: contentID, Revision: contentRevision}, SpecDigest: specDigest, Phase: phase, StateVersion: stateVersion}
	if storedWorkflow != workflowID || stored != action {
		return errors.New("runnable action is already bound to another document workflow")
	}
	return tx.Commit()
}

func (d *DocumentPracticeRepository) WorkflowForRunnableAction(ctx context.Context, action runnable.ActionIdentity) (string, bool, error) {
	if err := action.Validate(); err != nil {
		return "", false, errors.New("document runnable action lookup is invalid")
	}
	var workflowID string
	err := d.conn.QueryRowContext(ctx, `SELECT workflow_id FROM document_runnable_actions
		WHERE action_key = ? AND content_kind = ? AND content_id = ? AND content_revision = ? AND spec_digest = ? AND phase = ? AND state_version = ?`,
		action.Key(), action.Content.Kind, action.Content.ID, action.Content.Revision, action.SpecDigest, action.Phase, action.StateVersion).Scan(&workflowID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("lookup document runnable action: %w", err)
	}
	return workflowID, true, nil
}

// ListCompletedUnreconciledRunnableActions is the Server restart outbox. It
// joins the public action result before exposing it to the product pipeline.
func (d *DocumentPracticeRepository) ListCompletedUnreconciledRunnableActions(ctx context.Context) ([]runnable.ActionIdentity, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT bindings.content_kind, bindings.content_id, bindings.content_revision, bindings.spec_digest, bindings.phase, bindings.state_version
		FROM document_runnable_actions bindings
		JOIN runnable_actions actions ON actions.action_key = bindings.action_key
		WHERE bindings.reconciled_at IS NULL AND actions.state = 'completed'
		ORDER BY bindings.created_at, bindings.action_key`)
	if err != nil {
		return nil, fmt.Errorf("list completed document runnable actions: %w", err)
	}
	defer rows.Close()
	result := []runnable.ActionIdentity{}
	for rows.Next() {
		var action runnable.ActionIdentity
		if err := rows.Scan(&action.Content.Kind, &action.Content.ID, &action.Content.Revision, &action.SpecDigest, &action.Phase, &action.StateVersion); err != nil {
			return nil, err
		}
		if err := action.Validate(); err != nil {
			return nil, errors.New("stored document runnable action is invalid")
		}
		result = append(result, action)
	}
	return result, rows.Err()
}

func (d *DocumentPracticeRepository) MarkRunnableActionReconciled(ctx context.Context, action runnable.ActionIdentity, now time.Time) error {
	if err := action.Validate(); err != nil || now.IsZero() {
		return errors.New("mark document runnable action reconciled is invalid")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE document_runnable_actions SET reconciled_at = COALESCE(reconciled_at, ?)
		WHERE action_key = ? AND content_kind = ? AND content_id = ? AND content_revision = ? AND spec_digest = ? AND phase = ? AND state_version = ?`,
		now.UTC(), action.Key(), action.Content.Kind, action.Content.ID, action.Content.Revision, action.SpecDigest, action.Phase, action.StateVersion)
	if err != nil {
		return fmt.Errorf("mark document runnable action reconciled: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("document runnable action binding not found")
	}
	return nil
}

func (d *DocumentPracticeRepository) SaveAgentAudit(ctx context.Context, workflowID string, audit domain.AgentAudit) error {
	if strings.TrimSpace(workflowID) == "" || audit.Validate() != nil {
		return errors.New("document agent audit is invalid")
	}
	result, err := d.conn.ExecContext(ctx, `INSERT INTO document_agent_audits (run_id, workflow_id, role, model, prompt_version, tool_version, policy_version, input_digest, output_digest, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (run_id) DO NOTHING`, audit.RunID, workflowID, audit.Role, audit.Model, audit.PromptVersion, audit.ToolVersion, audit.PolicyVersion, audit.InputDigest, audit.OutputDigest, audit.CreatedAt.UTC())
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 1 {
		return nil
	}
	var previous domain.AgentAudit
	err = d.conn.QueryRowContext(ctx, `SELECT run_id, role, model, prompt_version, tool_version, policy_version, input_digest, output_digest, created_at FROM document_agent_audits WHERE run_id = ?`, audit.RunID).Scan(&previous.RunID, &previous.Role, &previous.Model, &previous.PromptVersion, &previous.ToolVersion, &previous.PolicyVersion, &previous.InputDigest, &previous.OutputDigest, &previous.CreatedAt)
	if err != nil {
		return err
	}
	if previous != audit {
		return errors.New("document AgentRun id already has another audit record")
	}
	return nil
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

// PublishPracticeRevision is the documentation finalizer's only public write.
// It verifies that the runtime revision/report are already immutable and then
// writes the manifest, revision, and page index in one transaction.
func (d *DocumentPracticeRepository) PublishPracticeRevision(ctx context.Context, workflowID string, expectedStateVersion int64, revision domain.PracticeRevision, manifest domain.PublicationManifest, now time.Time) (domain.Workflow, error) {
	if strings.TrimSpace(workflowID) == "" || expectedStateVersion < 1 || now.IsZero() || revision.WorkflowID != workflowID {
		return domain.Workflow{}, errors.New("practice publication workflow fence is invalid")
	}
	if err := revision.Validate(); err != nil {
		return domain.Workflow{}, err
	}
	if manifest.ID != revision.PublicationManifestID || manifest.Context != revision.Context || manifest.RunnableRevisionDigest != revision.RunnableRevisionRef.Digest || manifest.VerificationReportDigest != revision.VerificationReportRef.Digest {
		return domain.Workflow{}, errors.New("practice revision does not match its publication manifest")
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return domain.Workflow{}, err
	}
	manifestDigest, err := jsonDigest(manifestJSON)
	if err != nil {
		return domain.Workflow{}, err
	}
	revisionJSON, err := json.Marshal(revision)
	if err != nil {
		return domain.Workflow{}, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return domain.Workflow{}, err
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := scanDocumentWorkflow(tx.QueryRowContext(ctx, `SELECT id, state, state_version, revision, max_revisions, lease_owner, lease_expires_at, updated_at FROM document_workflows WHERE id = ? FOR UPDATE`, workflowID))
	if err != nil {
		return domain.Workflow{}, err
	}
	if workflow.State == domain.Published {
		var stored []byte
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM document_practice_revisions WHERE workflow_id = ? FOR UPDATE`, workflowID).Scan(&stored); err != nil {
			return domain.Workflow{}, err
		}
		if !bytes.Equal(stored, revisionJSON) {
			return domain.Workflow{}, errors.New("published workflow has another practice revision")
		}
		if err := tx.Commit(); err != nil {
			return domain.Workflow{}, err
		}
		return workflow, nil
	}
	if workflow.State != domain.Publishing || workflow.StateVersion != expectedStateVersion {
		return domain.Workflow{}, errors.New("practice publication state version is stale")
	}
	artifacts, err := listDocumentArtifacts(ctx, tx, workflowID)
	if err != nil {
		return domain.Workflow{}, err
	}
	if err := validatePublicationLedger(artifacts, revision, manifest); err != nil {
		return domain.Workflow{}, err
	}
	var reportRevisionDigest string
	var reportJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT runnable_revision_digest FROM runnable_verification_reports WHERE id = ? AND verification_report_digest = ? FOR UPDATE`, revision.VerificationReportRef.ID, revision.VerificationReportRef.Digest).Scan(&reportRevisionDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Workflow{}, errors.New("practice publication references an unknown verification report")
	}
	if err != nil {
		return domain.Workflow{}, err
	}
	if reportRevisionDigest != revision.RunnableRevisionRef.Digest {
		return domain.Workflow{}, errors.New("verification report belongs to another runnable revision")
	}
	if err := tx.QueryRowContext(ctx, `SELECT report FROM runnable_verification_reports WHERE id = ? AND verification_report_digest = ?`, revision.VerificationReportRef.ID, revision.VerificationReportRef.Digest).Scan(&reportJSON); err != nil {
		return domain.Workflow{}, err
	}
	var report runnable.VerificationReport
	if err := json.Unmarshal(reportJSON, &report); err != nil {
		return domain.Workflow{}, fmt.Errorf("decode stored verification report: %w", err)
	}
	if err := manifest.Validate(report); err != nil {
		return domain.Workflow{}, err
	}
	if report.RunnableRevisionDigest != revision.RunnableRevisionRef.Digest {
		return domain.Workflow{}, errors.New("stored verification report digest binding is invalid")
	}
	var revisionExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM runnable_revisions WHERE id = ? AND runnable_revision_digest = ?)`, revision.RunnableRevisionRef.ID, revision.RunnableRevisionRef.Digest).Scan(&revisionExists); err != nil {
		return domain.Workflow{}, err
	}
	if !revisionExists {
		return domain.Workflow{}, errors.New("practice publication references an unknown runnable revision")
	}
	publicationArtifact := domain.ArtifactRecord{
		ID:              "publication-manifest-" + manifest.ID,
		ParentID:        "verification-review-" + revision.VerificationReportRef.ID,
		Kind:            "publication-manifest",
		ContentRevision: revision.CandidateID,
		Digest:          manifestDigest,
		SchemaVersion:   domain.FormatVersion,
		OwnerRole:       "server",
		PolicyVersion:   manifest.PlanGate.PolicyVersion,
		CreatedAt:       manifest.CreatedAt.UTC(),
		Payload:         manifestJSON,
	}
	if err := insertImmutableDocumentArtifact(ctx, tx, workflowID, publicationArtifact); err != nil {
		return domain.Workflow{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO document_publication_manifests (id, workflow_id, manifest, manifest_digest, created_at) VALUES (?, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`, manifest.ID, revision.WorkflowID, manifestJSON, manifestDigest, manifest.CreatedAt.UTC()); err != nil {
		return domain.Workflow{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO document_practice_revisions (id, workflow_id, source_id, commit, language, page_path, anchor, candidate_id, runnable_revision_id, runnable_revision_digest, verification_report_id, verification_report_digest, manifest_id, revision, published_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`, revision.ID, revision.WorkflowID, revision.Context.SourceID, revision.Context.Commit, revision.Context.Language, revision.Context.PagePath, revision.Context.Anchor, revision.CandidateID, revision.RunnableRevisionRef.ID, revision.RunnableRevisionRef.Digest, revision.VerificationReportRef.ID, revision.VerificationReportRef.Digest, manifest.ID, revisionJSON, revision.PublishedAt.UTC())
	if err != nil {
		return domain.Workflow{}, err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		var stored []byte
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM document_practice_revisions WHERE id = ? FOR UPDATE`, revision.ID).Scan(&stored); err != nil {
			return domain.Workflow{}, err
		}
		if !bytes.Equal(stored, revisionJSON) {
			return domain.Workflow{}, errors.New("practice revision id already has another value")
		}
	}
	indexResult, err := tx.ExecContext(ctx, `INSERT INTO document_practice_index (source_id, commit, language, page_path, anchor, practice_revision_id) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (source_id, commit, language, page_path, anchor) DO UPDATE SET practice_revision_id = EXCLUDED.practice_revision_id WHERE document_practice_index.practice_revision_id = EXCLUDED.practice_revision_id`, revision.Context.SourceID, revision.Context.Commit, revision.Context.Language, revision.Context.PagePath, revision.Context.Anchor, revision.ID)
	if err != nil {
		return domain.Workflow{}, err
	}
	if changed, _ := indexResult.RowsAffected(); changed != 1 {
		return domain.Workflow{}, errors.New("documentation page anchor already indexes another practice revision")
	}
	workflow.State = domain.Published
	workflow.StateVersion++
	workflow.UpdatedAt = now.UTC()
	result, err = tx.ExecContext(ctx, `UPDATE document_workflows SET state = ?, state_version = ?, updated_at = ? WHERE id = ? AND state = ? AND state_version = ?`, workflow.State, workflow.StateVersion, workflow.UpdatedAt, workflow.ID, domain.Publishing, expectedStateVersion)
	if err != nil {
		return domain.Workflow{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return domain.Workflow{}, errors.New("practice publication lost its workflow fence")
	}
	if err := tx.Commit(); err != nil {
		return domain.Workflow{}, err
	}
	return workflow, nil
}

// validatePublicationLedger binds the product finalizer to the exact gate and
// runtime artifacts that reached Publishing. It deliberately decodes only
// structured records; document bytes never enter the publication transaction.
func validatePublicationLedger(artifacts []domain.ArtifactRecord, revision domain.PracticeRevision, manifest domain.PublicationManifest) error {
	byID := make(map[string]domain.ArtifactRecord, len(artifacts))
	for _, artifact := range artifacts {
		byID[artifact.ID] = artifact
	}
	planID := "plan-" + revision.PlanID + fmt.Sprintf("-r%d", revision.PlanRevision)
	planArtifact, ok := byID[planID]
	if !ok || planArtifact.Kind != "learning-unit-plan" {
		return errors.New("practice publication is missing its learning plan")
	}
	contextID := "document-context-" + domain.ContentID(revision.Context)
	contextArtifact, ok := byID[contextID]
	if !ok || contextArtifact.Kind != "document-context" || !sameJSON(contextArtifact.Payload, revision.Context) {
		return errors.New("practice publication document context binding is invalid")
	}
	if planArtifact.ParentID != contextArtifact.ID {
		return errors.New("practice publication learning plan is not bound to its document context")
	}
	var plan domain.LearningUnitPlan
	if err := json.Unmarshal(planArtifact.Payload, &plan); err != nil || plan.Validate() != nil || plan.ID != revision.PlanID || plan.Revision != revision.PlanRevision || plan.Context != revision.Context {
		return errors.New("practice publication learning plan binding is invalid")
	}
	planGate, ok := byID["plan-gate-"+planID]
	if !ok || planGate.Kind != "plan-gate" || planGate.ParentID != planID || !sameJSON(planGate.Payload, manifest.PlanGate) {
		return errors.New("practice publication plan gate binding is invalid")
	}
	var candidate domain.PracticeCandidate
	foundCandidate := false
	for _, artifact := range artifacts {
		if artifact.Kind != "practice-candidate" || artifact.ParentID != planID {
			continue
		}
		var value domain.PracticeCandidate
		if err := json.Unmarshal(artifact.Payload, &value); err == nil && value.Validate() == nil && value.ID == revision.CandidateID {
			candidate = value
			foundCandidate = true
			break
		}
	}
	if !foundCandidate || candidate.Context != revision.Context || candidate.PlanID != plan.ID || candidate.PlanRevision != plan.Revision || candidate.ID != manifest.PracticeCandidateID {
		return errors.New("practice publication candidate binding is invalid")
	}
	candidateID := "candidate-" + candidate.ID + fmt.Sprintf("-r%d", candidate.Revision)
	artifactGate, ok := byID["artifact-gate-"+candidateID]
	if !ok || artifactGate.Kind != "artifact-gate" || artifactGate.ParentID != candidateID || !sameJSON(artifactGate.Payload, manifest.ArtifactGate) {
		return errors.New("practice publication artifact gate binding is invalid")
	}
	if artifact, ok := byID["runnable-revision-"+revision.RunnableRevisionRef.ID]; !ok || artifact.Kind != "runnable-revision" || !sameJSON(artifact.Payload, revision.RunnableRevisionRef) {
		return errors.New("practice publication runnable revision ledger binding is invalid")
	}
	if artifact, ok := byID["verification-report-"+revision.VerificationReportRef.ID]; !ok || artifact.Kind != "verification-report" || !sameJSON(artifact.Payload, revision.VerificationReportRef) {
		return errors.New("practice publication verification report ledger binding is invalid")
	}
	if artifact, ok := byID["verification-review-"+revision.VerificationReportRef.ID]; !ok || artifact.Kind != "verification-review" || !sameJSON(artifact.Payload, manifest.VerificationReview) {
		return errors.New("practice publication verification review ledger binding is invalid")
	}
	return nil
}

func sameJSON(payload []byte, value any) bool {
	expected, err := json.Marshal(value)
	if err != nil {
		return false
	}
	var actualValue any
	var expectedValue any
	return json.Unmarshal(payload, &actualValue) == nil &&
		json.Unmarshal(expected, &expectedValue) == nil &&
		reflect.DeepEqual(actualValue, expectedValue)
}

func insertImmutableDocumentArtifact(ctx context.Context, tx *Tx, workflowID string, artifact domain.ArtifactRecord) error {
	if err := artifact.Validate(); err != nil {
		return err
	}
	var previousDigest string
	err := tx.QueryRowContext(ctx, `SELECT digest FROM document_artifact_ledger WHERE id = ? FOR UPDATE`, artifact.ID).Scan(&previousDigest)
	if err == nil {
		if previousDigest != artifact.Digest {
			return errors.New("document artifact id already has another digest")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO document_artifact_ledger (id, workflow_id, kind, parent_id, content_revision, digest, schema_version, owner_role, policy_version, payload, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, artifact.ID, workflowID, artifact.Kind, artifact.ParentID, artifact.ContentRevision, artifact.Digest, artifact.SchemaVersion, artifact.OwnerRole, artifact.PolicyVersion, artifact.Payload, artifact.CreatedAt.UTC())
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

func scanDocumentWorkflow(row interface{ Scan(...any) error }) (domain.Workflow, error) {
	var w domain.Workflow
	err := row.Scan(&w.ID, &w.State, &w.StateVersion, &w.Revision, &w.MaxRevisions, &w.LeaseOwner, &w.LeaseExpiresAt, &w.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Workflow{}, errors.New("document workflow not found")
	}
	return w, err
}

func listDocumentArtifacts(ctx context.Context, query interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, workflowID string) ([]domain.ArtifactRecord, error) {
	rows, err := query.QueryContext(ctx, `SELECT id, kind, parent_id, content_revision, digest, schema_version, owner_role, policy_version, payload, created_at FROM document_artifact_ledger WHERE workflow_id = ? ORDER BY created_at, id`, workflowID)
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
