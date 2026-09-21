package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	app "github.com/breakfix/breakfix/internal/application/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/audit"
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
	_, err := d.conn.ExecContext(ctx, `INSERT INTO document_artifact_ledger (id, workflow_id, kind, parent_id, content_revision, digest, schema_version, owner_role, policy_version, payload, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`, artifact.ID, workflowID, artifact.Kind, artifact.ParentID, artifact.ContentRevision, artifact.Digest, artifact.SchemaVersion, artifact.OwnerRole, artifact.PolicyVersion, payload, artifact.CreatedAt.UTC())
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
	defer func() { _ = rows.Close() }()
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

// CreateWorkflow durably creates the workflow with its page identity and,
// when the caller supplies the administrative ignition action, its human audit
// row in the same transaction: an audit row exists only if the workflow does.
func (d *DocumentPracticeRepository) CreateWorkflow(ctx context.Context, workflow domain.Workflow, identity domain.WorkflowPageIdentity, action *audit.HumanAction) error {
	if err := workflow.Validate(); err != nil {
		return err
	}
	if err := identity.Validate(); err != nil {
		return err
	}
	if action == nil {
		_, err := d.conn.ExecContext(ctx, `INSERT INTO document_workflows (id, state, state_version, revision, max_revisions, source_id, commit, language, page_path, anchor, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, workflow.ID, workflow.State, workflow.StateVersion, workflow.Revision, workflow.MaxRevisions, identity.SourceID, identity.Commit, identity.Language, identity.PagePath, identity.Anchor, workflow.UpdatedAt.UTC())
		return err
	}
	if err := action.Validate(); err != nil {
		return err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO document_workflows (id, state, state_version, revision, max_revisions, source_id, commit, language, page_path, anchor, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, workflow.ID, workflow.State, workflow.StateVersion, workflow.Revision, workflow.MaxRevisions, identity.SourceID, identity.Commit, identity.Language, identity.PagePath, identity.Anchor, workflow.UpdatedAt.UTC()); err != nil {
		return err
	}
	if err := insertHumanAction(ctx, tx, *action); err != nil {
		return err
	}
	return tx.Commit()
}

// BackfillWorkflowPageIdentity derives the page identity columns of workflows
// created before the columns existed from their durable document-context
// ledger artifact. It is idempotent and reports how many rows it enriched.
func (d *DocumentPracticeRepository) BackfillWorkflowPageIdentity(ctx context.Context) (int64, error) {
	result, err := d.conn.ExecContext(ctx, `UPDATE document_workflows w SET
		source_id = ledger.payload->>'source_id',
		commit = ledger.payload->>'commit',
		language = ledger.payload->>'language',
		page_path = ledger.payload->>'page_path',
		anchor = COALESCE(ledger.payload->>'anchor', '')
		FROM (
			SELECT DISTINCT ON (workflow_id) workflow_id, payload
			FROM document_artifact_ledger
			WHERE kind = 'document-context'
			ORDER BY workflow_id, created_at DESC, id DESC
		) AS ledger
		WHERE ledger.workflow_id = w.id AND w.page_path = ''`)
	if err != nil {
		return 0, fmt.Errorf("backfill document workflow page identity: %w", err)
	}
	return result.RowsAffected()
}

// RecordHumanAction appends a standalone administrative action row for paths
// that changed no documentation state.
func (d *DocumentPracticeRepository) RecordHumanAction(ctx context.Context, action audit.HumanAction) error {
	if err := action.Validate(); err != nil {
		return err
	}
	return insertHumanAction(ctx, d.conn, action)
}

func (d *DocumentPracticeRepository) GetWorkflow(ctx context.Context, id string) (domain.Workflow, error) {
	var w domain.Workflow
	err := d.conn.QueryRowContext(ctx, `SELECT id, state, state_version, revision, max_revisions, updated_at FROM document_workflows WHERE id = ?`, id).Scan(&w.ID, &w.State, &w.StateVersion, &w.Revision, &w.MaxRevisions, &w.UpdatedAt)
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

// ForceFailWorkflow drives any non-terminal workflow to Failed as an
// administrative decision and records the twin audit trail — an artifact
// ledger entry plus a human action row — in the same transaction. It deletes
// nothing and leaves in-flight runtime work to its existing TTLs.
func (d *DocumentPracticeRepository) ForceFailWorkflow(ctx context.Context, workflowID, reason string, action *audit.HumanAction, now time.Time) (domain.Workflow, error) {
	return d.adminWorkflowTransition(ctx, workflowID, reason, action, now, func(w *domain.Workflow) error {
		return w.AdvanceAt(domain.Failed, now)
	}, "admin.force_fail")
}

// RestartWorkflow resets a failed or rejected workflow to Planning. The
// revision counter advances without the MaxRevisions cap because a restart is
// a human judgment, not an automatic loop iteration. It invokes no Agent role.
func (d *DocumentPracticeRepository) RestartWorkflow(ctx context.Context, workflowID, reason string, action *audit.HumanAction, now time.Time) (domain.Workflow, error) {
	return d.adminWorkflowTransition(ctx, workflowID, reason, action, now, func(w *domain.Workflow) error {
		return w.RestartAt(now)
	}, "admin.restart")
}

// AutoRestartWorkflow re-drives a gate-rejected workflow to Planning as a
// pipeline decision: the same RestartAt transition and revision advance an
// administrator's restart performs, fenced by the state version the rejection
// produced. The system ledger entry carries the gate and its rejection
// reasons; no human action audit row is written because no human operated.
func (d *DocumentPracticeRepository) AutoRestartWorkflow(ctx context.Context, workflowID, gateKind string, reasons []string, expectedStateVersion int64, now time.Time) (domain.Workflow, error) {
	if strings.TrimSpace(workflowID) == "" || strings.TrimSpace(gateKind) == "" || len(reasons) == 0 || expectedStateVersion < 1 || now.IsZero() {
		return domain.Workflow{}, errors.New("pipeline auto restart is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return domain.Workflow{}, fmt.Errorf("begin pipeline auto restart: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := scanDocumentWorkflow(tx.QueryRowContext(ctx, `SELECT id, state, state_version, revision, max_revisions, updated_at FROM document_workflows WHERE id = ? FOR UPDATE`, workflowID))
	if err != nil {
		return domain.Workflow{}, err
	}
	if workflow.StateVersion != expectedStateVersion {
		return domain.Workflow{}, fmt.Errorf("%w (workflow moved to %s v%d)", domain.ErrWorkflowConflict, workflow.State, workflow.StateVersion)
	}
	fromState := workflow.State
	if err := workflow.RestartAt(now); err != nil {
		return domain.Workflow{}, fmt.Errorf("%w (%s)", domain.ErrWorkflowConflict, err.Error())
	}
	payload, err := json.Marshal(struct {
		System    string    `json:"system"`
		FromState string    `json:"from_state"`
		Gate      string    `json:"gate"`
		Reasons   []string  `json:"reasons"`
		At        time.Time `json:"at"`
	}{System: "documentation-pipeline", FromState: string(fromState), Gate: gateKind, Reasons: reasons, At: now.UTC()})
	if err != nil {
		return domain.Workflow{}, err
	}
	digest, err := domain.DigestAgentInput(struct {
		System    string    `json:"system"`
		FromState string    `json:"from_state"`
		Gate      string    `json:"gate"`
		Reasons   []string  `json:"reasons"`
		At        time.Time `json:"at"`
	}{System: "documentation-pipeline", FromState: string(fromState), Gate: gateKind, Reasons: reasons, At: now.UTC()})
	if err != nil {
		return domain.Workflow{}, err
	}
	artifact := domain.ArtifactRecord{
		ID:              "pipeline.auto_restart-" + workflowID + "-v" + strconv.FormatInt(workflow.StateVersion, 10),
		Kind:            "pipeline.auto_restart",
		ContentRevision: strconv.FormatInt(workflow.Revision, 10),
		Digest:          digest,
		SchemaVersion:   domain.FormatVersion,
		OwnerRole:       "system",
		CreatedAt:       now.UTC(),
		Payload:         payload,
	}
	if err := insertImmutableDocumentArtifact(ctx, tx, workflowID, artifact); err != nil {
		return domain.Workflow{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE document_workflows SET state = ?, state_version = ?, revision = ?, updated_at = ? WHERE id = ? AND state_version = ?`, workflow.State, workflow.StateVersion, workflow.Revision, workflow.UpdatedAt, workflowID, expectedStateVersion)
	if err != nil {
		return domain.Workflow{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return domain.Workflow{}, errors.New("pipeline auto restart lost its fence")
	}
	if err := tx.Commit(); err != nil {
		return domain.Workflow{}, err
	}
	return workflow, nil
}

// adminWorkflowTransition is the shared force-fail/restart implementation. The
// state fence, the ledger entry, and the human audit commit together: a lost
// race or rejected transition leaves no trace in either record.
func (d *DocumentPracticeRepository) adminWorkflowTransition(ctx context.Context, workflowID, reason string, action *audit.HumanAction, now time.Time, transition func(*domain.Workflow) error, ledgerKind string) (domain.Workflow, error) {
	if strings.TrimSpace(workflowID) == "" || now.IsZero() {
		return domain.Workflow{}, errors.New("administrative workflow transition is invalid")
	}
	if action == nil {
		return domain.Workflow{}, errors.New("administrative workflow transition requires a human action audit")
	}
	if err := action.Validate(); err != nil {
		return domain.Workflow{}, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return domain.Workflow{}, fmt.Errorf("begin administrative workflow transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := scanDocumentWorkflow(tx.QueryRowContext(ctx, `SELECT id, state, state_version, revision, max_revisions, updated_at FROM document_workflows WHERE id = ? FOR UPDATE`, workflowID))
	if err != nil {
		return domain.Workflow{}, err
	}
	fromState := workflow.State
	if err := transition(&workflow); err != nil {
		return domain.Workflow{}, fmt.Errorf("%w (%s)", domain.ErrWorkflowConflict, err.Error())
	}
	payload, err := json.Marshal(struct {
		Actor     string    `json:"actor"`
		FromState string    `json:"from_state"`
		Reason    string    `json:"reason"`
		At        time.Time `json:"at"`
	}{Actor: action.UserID, FromState: string(fromState), Reason: reason, At: now.UTC()})
	if err != nil {
		return domain.Workflow{}, err
	}
	digest, err := domain.DigestAgentInput(struct {
		Actor     string    `json:"actor"`
		FromState string    `json:"from_state"`
		Reason    string    `json:"reason"`
		At        time.Time `json:"at"`
	}{Actor: action.UserID, FromState: string(fromState), Reason: reason, At: now.UTC()})
	if err != nil {
		return domain.Workflow{}, err
	}
	artifact := domain.ArtifactRecord{
		ID:              ledgerKind + "-" + workflowID + "-v" + strconv.FormatInt(workflow.StateVersion, 10),
		Kind:            ledgerKind,
		ContentRevision: strconv.FormatInt(workflow.Revision, 10),
		Digest:          digest,
		SchemaVersion:   domain.FormatVersion,
		OwnerRole:       "admin",
		CreatedAt:       now.UTC(),
		Payload:         payload,
	}
	if err := insertImmutableDocumentArtifact(ctx, tx, workflowID, artifact); err != nil {
		return domain.Workflow{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE document_workflows SET state = ?, state_version = ?, revision = ?, updated_at = ? WHERE id = ? AND state_version = ?`, workflow.State, workflow.StateVersion, workflow.Revision, workflow.UpdatedAt, workflowID, workflow.StateVersion-1)
	if err != nil {
		return domain.Workflow{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return domain.Workflow{}, errors.New("administrative workflow transition lost its fence")
	}
	enriched := *action
	detail, err := json.Marshal(map[string]string{"reason": reason, "from_state": string(fromState), "to_state": string(workflow.State)})
	if err != nil {
		return domain.Workflow{}, err
	}
	enriched.Detail = detail
	if err := insertHumanAction(ctx, tx, enriched); err != nil {
		return domain.Workflow{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Workflow{}, err
	}
	return workflow, nil
}

// ListWorkflowWatchdogCandidates returns every non-terminal workflow whose
// bound public action (at the workflow's current state version) already failed
// or exhausted its attempts. Reading the candidates is intentionally read-only;
// the mapping decision belongs to the application layer.
func (d *DocumentPracticeRepository) ListWorkflowWatchdogCandidates(ctx context.Context, now time.Time) ([]app.WatchdogCandidate, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT w.id, w.state, w.state_version, b.phase, a.state, a.attempt,
			COALESCE(a.failure_class, ''), COALESCE(a.failure_code, ''), COALESCE(a.failure_summary, ''),
			a.updated_at, (a.state = 'running' AND a.lease_expires_at IS NOT NULL AND a.lease_expires_at <= ?)
			FROM document_workflows w
			JOIN document_runnable_actions b ON b.workflow_id = w.id AND b.state_version = w.state_version
			JOIN runnable_actions a ON a.action_key = b.action_key
			WHERE w.state NOT IN ('Published','NoPractice','Rejected','Failed')
			  AND (a.state = 'failed' OR (a.attempt >= 5 AND a.state IN ('queued','running')))
			ORDER BY w.id`, now.UTC())
	if err != nil {
		return nil, fmt.Errorf("list document workflow watchdog candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := []app.WatchdogCandidate{}
	for rows.Next() {
		var candidate app.WatchdogCandidate
		if err := rows.Scan(&candidate.WorkflowID, &candidate.WorkflowState, &candidate.WorkflowStateVersion,
			&candidate.Action.Phase, &candidate.Action.State, &candidate.Action.Attempt,
			&candidate.Action.FailureClass, &candidate.Action.FailureCode, &candidate.Action.FailureSummary,
			&candidate.Action.UpdatedAt, &candidate.Action.LeaseExpired); err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	return result, rows.Err()
}

// WatchdogFailWorkflow maps a stranded workflow onto Failed as a system
// decision. The state fence and the watchdog ledger entry commit together; no
// human action audit row is written because no human operated.
func (d *DocumentPracticeRepository) WatchdogFailWorkflow(ctx context.Context, workflowID, reason string, expectedStateVersion int64, now time.Time) (domain.Workflow, error) {
	if strings.TrimSpace(workflowID) == "" || strings.TrimSpace(reason) == "" || expectedStateVersion < 1 || now.IsZero() {
		return domain.Workflow{}, errors.New("watchdog workflow transition is invalid")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return domain.Workflow{}, fmt.Errorf("begin watchdog workflow transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	workflow, err := scanDocumentWorkflow(tx.QueryRowContext(ctx, `SELECT id, state, state_version, revision, max_revisions, updated_at FROM document_workflows WHERE id = ? FOR UPDATE`, workflowID))
	if err != nil {
		return domain.Workflow{}, err
	}
	if workflow.StateVersion != expectedStateVersion {
		return domain.Workflow{}, fmt.Errorf("%w (workflow moved to %s v%d)", domain.ErrWorkflowConflict, workflow.State, workflow.StateVersion)
	}
	fromState := workflow.State
	if err := workflow.AdvanceAt(domain.Failed, now); err != nil {
		return domain.Workflow{}, fmt.Errorf("%w (%s)", domain.ErrWorkflowConflict, err.Error())
	}
	payload, err := json.Marshal(struct {
		System    string    `json:"system"`
		FromState string    `json:"from_state"`
		Reason    string    `json:"reason"`
		At        time.Time `json:"at"`
	}{System: "watchdog", FromState: string(fromState), Reason: reason, At: now.UTC()})
	if err != nil {
		return domain.Workflow{}, err
	}
	digest, err := domain.DigestAgentInput(struct {
		System    string    `json:"system"`
		FromState string    `json:"from_state"`
		Reason    string    `json:"reason"`
		At        time.Time `json:"at"`
	}{System: "watchdog", FromState: string(fromState), Reason: reason, At: now.UTC()})
	if err != nil {
		return domain.Workflow{}, err
	}
	artifact := domain.ArtifactRecord{
		ID:              "watchdog.force_fail-" + workflowID + "-v" + strconv.FormatInt(workflow.StateVersion, 10),
		Kind:            "watchdog.force_fail",
		ContentRevision: strconv.FormatInt(workflow.Revision, 10),
		Digest:          digest,
		SchemaVersion:   domain.FormatVersion,
		OwnerRole:       "system",
		CreatedAt:       now.UTC(),
		Payload:         payload,
	}
	if err := insertImmutableDocumentArtifact(ctx, tx, workflowID, artifact); err != nil {
		return domain.Workflow{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE document_workflows SET state = ?, state_version = ?, updated_at = ? WHERE id = ? AND state_version = ?`, workflow.State, workflow.StateVersion, workflow.UpdatedAt, workflowID, expectedStateVersion)
	if err != nil {
		return domain.Workflow{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return domain.Workflow{}, errors.New("watchdog workflow transition lost its fence")
	}
	if err := tx.Commit(); err != nil {
		return domain.Workflow{}, err
	}
	return workflow, nil
}

// DocumentBoundActionStatus mirrors the public runnable action joined through
// the workflow's current state-version binding. It feeds stuck derivation and
// never mutates anything.
type DocumentBoundActionStatus struct {
	Phase          runnable.ActionPhase
	State          string
	Attempt        int
	FailureClass   string
	FailureCode    string
	FailureSummary string
}

// DocumentWorkflowObservation is the read model for the admin workflow list
// and detail. The ledger and audits are assembled separately for the detail.
type DocumentWorkflowObservation struct {
	Workflow domain.Workflow
	Identity domain.WorkflowPageIdentity
	Action   *DocumentBoundActionStatus
}

// DocumentWorkflowCursor is the keyset continuation of one workflow list
// page. Rows are ordered by (updated_at DESC, id DESC) so the cursor carries
// both.
type DocumentWorkflowCursor struct {
	UpdatedAt time.Time `json:"u"`
	ID        string    `json:"i"`
}

// DocumentWorkflowListFilter narrows the admin workflow list. An empty state
// or page path means unfiltered.
type DocumentWorkflowListFilter struct {
	State    string
	PagePath string
	Cursor   *DocumentWorkflowCursor
	Limit    int
}

const documentWorkflowObservationColumns = `w.id, w.state, w.state_version, w.revision, w.max_revisions, w.updated_at,
		w.source_id, w.commit, w.language, w.page_path, w.anchor,
		b.phase, a.state, a.attempt, a.failure_class, a.failure_code, a.failure_summary`

const documentWorkflowObservationJoins = `FROM document_workflows w
		LEFT JOIN document_runnable_actions b ON b.workflow_id = w.id AND b.state_version = w.state_version
		LEFT JOIN runnable_actions a ON a.action_key = b.action_key`

// ListWorkflowObservations lists one page of documentation workflows with the
// public action status bound to its current state version, newest first. The
// binding join is intentionally read-only; stuck flags are derived by callers.
// The returned cursor is nil when the page is the last one.
func (d *DocumentPracticeRepository) ListWorkflowObservations(ctx context.Context, filter DocumentWorkflowListFilter) ([]DocumentWorkflowObservation, *DocumentWorkflowCursor, error) {
	if filter.Limit < 1 {
		return nil, nil, errors.New("document workflow list limit must be positive")
	}
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if filter.State != "" {
		conditions = append(conditions, `w.state = ?`)
		args = append(args, filter.State)
	}
	if filter.PagePath != "" {
		conditions = append(conditions, `w.page_path = ?`)
		args = append(args, filter.PagePath)
	}
	if filter.Cursor != nil {
		if filter.Cursor.UpdatedAt.IsZero() || strings.TrimSpace(filter.Cursor.ID) == "" {
			return nil, nil, errors.New("document workflow list cursor is invalid")
		}
		conditions = append(conditions, `(w.updated_at, w.id) < (?, ?)`)
		args = append(args, filter.Cursor.UpdatedAt.UTC(), filter.Cursor.ID)
	}
	query := `SELECT ` + documentWorkflowObservationColumns + ` ` + documentWorkflowObservationJoins
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, ` AND `)
	}
	query += ` ORDER BY w.updated_at DESC, w.id DESC LIMIT ?`
	args = append(args, filter.Limit+1)
	rows, err := d.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list document workflow observations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := []DocumentWorkflowObservation{}
	for rows.Next() {
		observation, err := scanWorkflowObservation(rows)
		if err != nil {
			return nil, nil, err
		}
		result = append(result, observation)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *DocumentWorkflowCursor
	if len(result) > filter.Limit {
		last := result[filter.Limit-1]
		next = &DocumentWorkflowCursor{UpdatedAt: last.Workflow.UpdatedAt, ID: last.Workflow.ID}
		result = result[:filter.Limit]
	}
	return result, next, nil
}

// GetWorkflowObservation resolves one workflow's observation read model.
func (d *DocumentPracticeRepository) GetWorkflowObservation(ctx context.Context, workflowID string) (DocumentWorkflowObservation, error) {
	row := d.conn.QueryRowContext(ctx, `SELECT `+documentWorkflowObservationColumns+` `+documentWorkflowObservationJoins+`
		WHERE w.id = ?`, workflowID)
	observation, err := scanWorkflowObservation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return DocumentWorkflowObservation{}, fmt.Errorf("get document workflow observation: %w", domain.ErrWorkflowNotFound)
	}
	return observation, err
}

func scanWorkflowObservation(row interface{ Scan(...any) error }) (DocumentWorkflowObservation, error) {
	var observation DocumentWorkflowObservation
	var phase sql.NullString
	var actionState sql.NullString
	var attempt sql.NullInt64
	var failureClass, failureCode, failureSummary sql.NullString
	if err := row.Scan(&observation.Workflow.ID, &observation.Workflow.State, &observation.Workflow.StateVersion, &observation.Workflow.Revision, &observation.Workflow.MaxRevisions, &observation.Workflow.UpdatedAt,
		&observation.Identity.SourceID, &observation.Identity.Commit, &observation.Identity.Language, &observation.Identity.PagePath, &observation.Identity.Anchor,
		&phase, &actionState, &attempt, &failureClass, &failureCode, &failureSummary); err != nil {
		return DocumentWorkflowObservation{}, err
	}
	if phase.Valid && actionState.Valid {
		observation.Action = &DocumentBoundActionStatus{
			Phase:          runnable.ActionPhase(phase.String),
			State:          actionState.String,
			Attempt:        int(attempt.Int64),
			FailureClass:   failureClass.String,
			FailureCode:    failureCode.String,
			FailureSummary: failureSummary.String,
		}
	}
	return observation, nil
}

// ListAgentAudits returns one workflow's machine-side AgentRun audits in
// ledger order. The volume is bounded by the fixed pipeline's audit count.
func (d *DocumentPracticeRepository) ListAgentAudits(ctx context.Context, workflowID string) ([]domain.AgentAudit, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT run_id, role, model, prompt_version, tool_version, policy_version, input_digest, output_digest, created_at FROM document_agent_audits WHERE workflow_id = ? ORDER BY created_at, run_id`, workflowID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	audits := []domain.AgentAudit{}
	for rows.Next() {
		var auditRow domain.AgentAudit
		if err := rows.Scan(&auditRow.RunID, &auditRow.Role, &auditRow.Model, &auditRow.PromptVersion, &auditRow.ToolVersion, &auditRow.PolicyVersion, &auditRow.InputDigest, &auditRow.OutputDigest, &auditRow.CreatedAt); err != nil {
			return nil, err
		}
		audits = append(audits, auditRow)
	}
	return audits, rows.Err()
}

// DocumentPublicationRecord exposes the published manifest of a workflow.
type DocumentPublicationRecord struct {
	ID        string
	Manifest  domain.PublicationManifest
	Digest    string
	CreatedAt time.Time
}

// GetWorkflowPublication returns the published manifest, or nil when the
// workflow has not published.
func (d *DocumentPracticeRepository) GetWorkflowPublication(ctx context.Context, workflowID string) (*DocumentPublicationRecord, error) {
	var record DocumentPublicationRecord
	var manifest []byte
	err := d.conn.QueryRowContext(ctx, `SELECT id, manifest, manifest_digest, created_at FROM document_publication_manifests WHERE workflow_id = ?`, workflowID).Scan(&record.ID, &manifest, &record.Digest, &record.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(manifest, &record.Manifest); err != nil {
		return nil, fmt.Errorf("decode document publication manifest: %w", err)
	}
	return &record, nil
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
	w, err := scanDocumentWorkflow(tx.QueryRowContext(ctx, `SELECT id, state, state_version, revision, max_revisions, updated_at FROM document_workflows WHERE id = ? FOR UPDATE`, workflowID))
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

// BindRunnableAction reserves the exact public runtime identity before it can
// be claimed by a Worker. The binding is immutable and is intentionally a
// documentation-product projection, not a public runnable action field.
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
	defer func() { _ = rows.Close() }()
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
	workflow, err := scanDocumentWorkflow(tx.QueryRowContext(ctx, `SELECT id, state, state_version, revision, max_revisions, updated_at FROM document_workflows WHERE id = ? FOR UPDATE`, workflowID))
	if err != nil {
		return domain.Workflow{}, err
	}
	if workflow.State == domain.Published {
		var stored []byte
		if err := tx.QueryRowContext(ctx, `SELECT revision FROM document_practice_revisions WHERE workflow_id = ? FOR UPDATE`, workflowID).Scan(&stored); err != nil {
			return domain.Workflow{}, err
		}
		if !sameJSON(stored, revision) {
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
		if !sameJSON(stored, revision) {
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
	planID := fmt.Sprintf("plan-%s-r%d-a%d", revision.PlanID, revision.PlanRevision, revision.WorkflowRevision)
	planArtifact, ok := byID[planID]
	if !ok || planArtifact.Kind != "learning-unit-plan" {
		return errors.New("practice publication is missing its learning plan")
	}
	contextID := "document-context-" + domain.ContentID(revision.Context)
	contextArtifact, ok := byID[contextID]
	if !ok || contextArtifact.Kind != "document-context" || !sameJSON(contextArtifact.Payload, revision.Context) {
		return errors.New("practice publication document context binding is invalid")
	}
	// The evidence triple (upstream commit, parser version, page digest) lives
	// inside DocumentContext, so context equality across the practice revision
	// and the publication manifest is also triple consistency.
	if manifest.Context != revision.Context {
		return errors.New("practice publication manifest context does not match the practice revision")
	}
	if planArtifact.ParentID != contextArtifact.ID {
		return errors.New("practice publication learning plan is not bound to its document context")
	}
	var plan domain.LearningUnitPlan
	if err := json.Unmarshal(planArtifact.Payload, &plan); err != nil || plan.Validate() != nil || plan.ID != revision.PlanID || plan.Revision != revision.PlanRevision || plan.Context != revision.Context {
		return errors.New("practice publication learning plan binding is invalid")
	}
	if revision.ReaderProjection == nil || !reflect.DeepEqual(*revision.ReaderProjection, *domain.ReaderProjectionFromPlan(plan)) {
		return errors.New("practice publication reader projection does not bind the learning plan")
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
	candidateID := fmt.Sprintf("candidate-%s-r%d-a%d", candidate.ID, candidate.Revision, revision.WorkflowRevision)
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
	err := row.Scan(&w.ID, &w.State, &w.StateVersion, &w.Revision, &w.MaxRevisions, &w.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Workflow{}, fmt.Errorf("scan document workflow: %w", domain.ErrWorkflowNotFound)
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
	defer func() { _ = rows.Close() }()
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

// CountDocumentWorkflowsByState returns one durable count per state. The
// metrics endpoint fills absent states with zeros to keep the series stable.
func (d *DocumentPracticeRepository) CountDocumentWorkflowsByState(ctx context.Context) (map[string]int64, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT state, COUNT(*) FROM document_workflows GROUP BY state`)
	if err != nil {
		return nil, fmt.Errorf("count document workflows by state: %w", err)
	}
	defer func() { _ = rows.Close() }()
	counts := map[string]int64{}
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		counts[state] = count
	}
	return counts, rows.Err()
}

// ErrPublishedPracticeNotFound reports that a practice is unknown or not
// reader-visible. The reader treats both identically.
var ErrPublishedPracticeNotFound = errors.New("published practice is not available")

// PublishedPracticeSummary is the reader-visible practice entry on a page:
// the anchor it hangs from, its stable identifier, and its frozen title.
type PublishedPracticeSummary struct {
	Anchor     string
	PracticeID string
	Title      string
}

// ListPublishedPracticesForPage returns the practices a reader sees on one
// page, scoped to the pinned library identity. Revisions without a reader
// projection are invisible by design: the record published before
// projections existed is not backfilled.
func (d *DocumentPracticeRepository) ListPublishedPracticesForPage(ctx context.Context, sourceID, commit, language, pagePath string) ([]PublishedPracticeSummary, error) {
	if strings.TrimSpace(sourceID) == "" || strings.TrimSpace(commit) == "" || strings.TrimSpace(language) == "" || strings.TrimSpace(pagePath) == "" {
		return nil, errors.New("documentation page identity is required")
	}
	rows, err := d.conn.QueryContext(ctx, `
		SELECT index.anchor, revisions.id, revisions.revision->'reader_projection'->>'title'
		FROM document_practice_index AS index
		JOIN document_practice_revisions AS revisions ON revisions.id = index.practice_revision_id
		WHERE index.source_id = ? AND index.commit = ? AND index.language = ? AND index.page_path = ?
		  AND revisions.revision->'reader_projection' IS NOT NULL
		ORDER BY index.anchor`, sourceID, commit, language, pagePath)
	if err != nil {
		return nil, fmt.Errorf("list published practices: %w", err)
	}
	defer func() { _ = rows.Close() }()
	summaries := []PublishedPracticeSummary{}
	for rows.Next() {
		var summary PublishedPracticeSummary
		if err := rows.Scan(&summary.Anchor, &summary.PracticeID, &summary.Title); err != nil {
			return nil, fmt.Errorf("scan published practice: %w", err)
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// GetPublishedPractice returns one reader-visible practice revision. Unknown
// IDs and revisions without a reader projection are indistinguishable to the
// reader: both report not found.
func (d *DocumentPracticeRepository) GetPublishedPractice(ctx context.Context, practiceID string) (domain.PracticeRevision, error) {
	if strings.TrimSpace(practiceID) == "" {
		return domain.PracticeRevision{}, ErrPublishedPracticeNotFound
	}
	var encoded []byte
	err := d.conn.QueryRowContext(ctx, `SELECT revision FROM document_practice_revisions WHERE id = ?`, practiceID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PracticeRevision{}, ErrPublishedPracticeNotFound
	}
	if err != nil {
		return domain.PracticeRevision{}, fmt.Errorf("read published practice: %w", err)
	}
	var revision domain.PracticeRevision
	if err := json.Unmarshal(encoded, &revision); err != nil {
		return domain.PracticeRevision{}, fmt.Errorf("decode published practice: %w", err)
	}
	if revision.ReaderProjection == nil {
		return domain.PracticeRevision{}, ErrPublishedPracticeNotFound
	}
	return revision, nil
}

// CreateBatch durably creates the batch, its resolved items, and the creating
// administrator's human action audit in one transaction: the audit row exists
// only if the batch does.
func (d *DocumentPracticeRepository) CreateBatch(ctx context.Context, batch domain.DocumentBatch, items []domain.BatchItem, action *audit.HumanAction) error {
	if err := batch.Validate(); err != nil {
		return err
	}
	if action == nil {
		return errors.New("document batch creation requires a human action audit")
	}
	if err := action.Validate(); err != nil {
		return err
	}
	scopeJSON, err := json.Marshal(batch.Scope)
	if err != nil {
		return err
	}
	resolutionJSON, err := json.Marshal(batch.Resolution)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := item.Validate(); err != nil {
			return err
		}
		if item.BatchID != batch.ID {
			return errors.New("document batch item belongs to another batch")
		}
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create document batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO document_batches (id, state, scope, concurrency, resolution, total_items, created_by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		batch.ID, batch.State, scopeJSON, batch.Concurrency, resolutionJSON, batch.TotalItems, batch.CreatedBy, batch.CreatedAt.UTC(), batch.UpdatedAt.UTC()); err != nil {
		return fmt.Errorf("insert document batch: %w", err)
	}
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO document_batch_items (id, batch_id, ordinal, page_path, anchor, title, workflow_id, state, detail, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.ID, item.BatchID, item.Ordinal, item.PagePath, item.Anchor, item.Title, item.WorkflowID, item.State, item.Detail, item.CreatedAt.UTC(), item.UpdatedAt.UTC()); err != nil {
			return fmt.Errorf("insert document batch item: %w", err)
		}
	}
	if err := insertHumanAction(ctx, tx, *action); err != nil {
		return err
	}
	return tx.Commit()
}

const documentBatchColumns = `id, state, scope, concurrency, resolution, total_items, created_by, created_at, updated_at`

func scanDocumentBatch(row interface{ Scan(...any) error }) (domain.DocumentBatch, error) {
	var batch domain.DocumentBatch
	var scopeJSON, resolutionJSON []byte
	if err := row.Scan(&batch.ID, &batch.State, &scopeJSON, &batch.Concurrency, &resolutionJSON, &batch.TotalItems, &batch.CreatedBy, &batch.CreatedAt, &batch.UpdatedAt); err != nil {
		return domain.DocumentBatch{}, err
	}
	if err := json.Unmarshal(scopeJSON, &batch.Scope); err != nil {
		return domain.DocumentBatch{}, fmt.Errorf("decode document batch scope: %w", err)
	}
	if err := json.Unmarshal(resolutionJSON, &batch.Resolution); err != nil {
		return domain.DocumentBatch{}, fmt.Errorf("decode document batch resolution: %w", err)
	}
	if err := batch.Validate(); err != nil {
		return domain.DocumentBatch{}, err
	}
	return batch, nil
}

// GetBatch returns one batch with per-state item counts.
func (d *DocumentPracticeRepository) GetBatch(ctx context.Context, batchID string) (domain.DocumentBatch, domain.BatchItemCounts, error) {
	row := d.conn.QueryRowContext(ctx, `SELECT `+documentBatchColumns+` FROM document_batches WHERE id = ?`, batchID)
	batch, err := scanDocumentBatch(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DocumentBatch{}, nil, fmt.Errorf("get document batch: %w", domain.ErrWorkflowNotFound)
	}
	if err != nil {
		return domain.DocumentBatch{}, nil, err
	}
	counts, err := d.countBatchItems(ctx, batchID)
	if err != nil {
		return domain.DocumentBatch{}, nil, err
	}
	return batch, counts, nil
}

// ListBatches returns the newest batches with per-state item counts.
func (d *DocumentPracticeRepository) ListBatches(ctx context.Context, limit int) ([]domain.DocumentBatch, []domain.BatchItemCounts, error) {
	if limit < 1 {
		return nil, nil, errors.New("document batch list limit must be positive")
	}
	rows, err := d.conn.QueryContext(ctx, `SELECT `+documentBatchColumns+` FROM document_batches ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("list document batches: %w", err)
	}
	defer func() { _ = rows.Close() }()
	batches := []domain.DocumentBatch{}
	ids := []string{}
	for rows.Next() {
		batch, err := scanDocumentBatch(rows)
		if err != nil {
			return nil, nil, err
		}
		batches = append(batches, batch)
		ids = append(ids, batch.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	countsByBatch := map[string]domain.BatchItemCounts{}
	if len(ids) > 0 {
		countRows, err := d.conn.QueryContext(ctx, `SELECT batch_id, state, COUNT(*) FROM document_batch_items WHERE batch_id IN (`+placeholders(len(ids))+`) GROUP BY batch_id, state`, toAny(ids)...)
		if err != nil {
			return nil, nil, fmt.Errorf("count document batch items: %w", err)
		}
		defer func() { _ = countRows.Close() }()
		for countRows.Next() {
			var batchID, state string
			var count int
			if err := countRows.Scan(&batchID, &state, &count); err != nil {
				return nil, nil, err
			}
			if countsByBatch[batchID] == nil {
				countsByBatch[batchID] = domain.BatchItemCounts{}
			}
			countsByBatch[batchID][domain.BatchItemState(state)] = count
		}
		if err := countRows.Err(); err != nil {
			return nil, nil, err
		}
	}
	counts := make([]domain.BatchItemCounts, 0, len(batches))
	for _, batch := range batches {
		if countsByBatch[batch.ID] == nil {
			countsByBatch[batch.ID] = domain.BatchItemCounts{}
		}
		counts = append(counts, countsByBatch[batch.ID])
	}
	return batches, counts, nil
}

func (d *DocumentPracticeRepository) countBatchItems(ctx context.Context, batchID string) (domain.BatchItemCounts, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT state, COUNT(*) FROM document_batch_items WHERE batch_id = ? GROUP BY state`, batchID)
	if err != nil {
		return nil, fmt.Errorf("count document batch items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	counts := domain.BatchItemCounts{}
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		counts[domain.BatchItemState(state)] = count
	}
	return counts, rows.Err()
}

// ListBatchItems pages one batch's items in corpus order. The returned cursor
// is nil when the page is the last one.
func (d *DocumentPracticeRepository) ListBatchItems(ctx context.Context, filter domain.BatchItemFilter) ([]domain.BatchItem, *domain.BatchItemCursor, error) {
	if filter.Limit < 1 {
		return nil, nil, errors.New("document batch item limit must be positive")
	}
	if strings.TrimSpace(filter.BatchID) == "" {
		return nil, nil, errors.New("document batch item filter requires a batch")
	}
	conditions := []string{`batch_id = ?`}
	args := []any{filter.BatchID}
	if filter.State != "" {
		if !filter.State.Valid() {
			return nil, nil, errors.New("document batch item state filter is invalid")
		}
		conditions = append(conditions, `state = ?`)
		args = append(args, filter.State)
	}
	if filter.Cursor != nil {
		conditions = append(conditions, `ordinal > ?`)
		args = append(args, filter.Cursor.Ordinal)
	}
	query := `SELECT id, batch_id, ordinal, page_path, anchor, title, workflow_id, state, detail, created_at, updated_at
		FROM document_batch_items WHERE ` + strings.Join(conditions, ` AND `) + ` ORDER BY ordinal ASC LIMIT ?`
	args = append(args, filter.Limit+1)
	rows, err := d.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list document batch items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := []domain.BatchItem{}
	for rows.Next() {
		var item domain.BatchItem
		if err := rows.Scan(&item.ID, &item.BatchID, &item.Ordinal, &item.PagePath, &item.Anchor, &item.Title, &item.WorkflowID, &item.State, &item.Detail, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *domain.BatchItemCursor
	if len(items) > filter.Limit {
		last := items[filter.Limit-1]
		next = &domain.BatchItemCursor{Ordinal: last.Ordinal}
		items = items[:filter.Limit]
	}
	return items, next, nil
}

// ListPublishedWorkflowAnchors keys the pinned identity's published practices
// by "page\x00anchor" so batch creation can skip them in one query.
func (d *DocumentPracticeRepository) ListPublishedWorkflowAnchors(ctx context.Context, identity domain.DocumentContext) (map[string]bool, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT page_path, anchor FROM document_workflows
		WHERE source_id = ? AND commit = ? AND language = ? AND state = 'Published'`, identity.SourceID, identity.Commit, identity.Language)
	if err != nil {
		return nil, fmt.Errorf("list published document workflow anchors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	published := map[string]bool{}
	for rows.Next() {
		var pagePath, anchor string
		if err := rows.Scan(&pagePath, &anchor); err != nil {
			return nil, err
		}
		published[pagePath+"\x00"+anchor] = true
	}
	return published, rows.Err()
}

func placeholders(count int) string {
	result := make([]string, count)
	for index := range result {
		result[index] = "?"
	}
	return strings.Join(result, ", ")
}

func toAny(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

// ListSchedulerBatches returns every batch in the given states, oldest first.
func (d *DocumentPracticeRepository) ListSchedulerBatches(ctx context.Context, states []domain.BatchState) ([]domain.DocumentBatch, error) {
	if len(states) == 0 {
		return []domain.DocumentBatch{}, nil
	}
	stateStrings := make([]any, 0, len(states))
	for _, state := range states {
		stateStrings = append(stateStrings, string(state))
	}
	query := `SELECT ` + documentBatchColumns + ` FROM document_batches WHERE state IN (` + placeholders(len(states)) + `) ORDER BY created_at, id`
	rows, err := d.conn.QueryContext(ctx, query, stateStrings...)
	if err != nil {
		return nil, fmt.Errorf("list scheduler document batches: %w", err)
	}
	defer func() { _ = rows.Close() }()
	batches := []domain.DocumentBatch{}
	for rows.Next() {
		batch, err := scanDocumentBatch(rows)
		if err != nil {
			return nil, err
		}
		batches = append(batches, batch)
	}
	return batches, rows.Err()
}

// ListBatchItemsByStates returns one batch's items in the given states,
// corpus order.
func (d *DocumentPracticeRepository) ListBatchItemsByStates(ctx context.Context, batchID string, states []domain.BatchItemState) ([]domain.BatchItem, error) {
	if len(states) == 0 {
		return []domain.BatchItem{}, nil
	}
	stateStrings := make([]any, 0, len(states))
	for _, state := range states {
		stateStrings = append(stateStrings, string(state))
	}
	query := `SELECT id, batch_id, ordinal, page_path, anchor, title, workflow_id, state, detail, created_at, updated_at
		FROM document_batch_items WHERE batch_id = ? AND state IN (` + placeholders(len(states)) + `) ORDER BY ordinal`
	args := append([]any{batchID}, stateStrings...)
	rows, err := d.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list document batch items by states: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := []domain.BatchItem{}
	for rows.Next() {
		var item domain.BatchItem
		if err := rows.Scan(&item.ID, &item.BatchID, &item.Ordinal, &item.PagePath, &item.Anchor, &item.Title, &item.WorkflowID, &item.State, &item.Detail, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ActiveBatchItemWorkflowStates joins every in-flight item of a batch with its
// workflow's current state.
func (d *DocumentPracticeRepository) ActiveBatchItemWorkflowStates(ctx context.Context, batchID string) ([]app.ActiveBatchItem, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT i.id, i.batch_id, i.ordinal, i.page_path, i.anchor, i.title, i.workflow_id, i.state, i.detail, i.created_at, i.updated_at, w.state
		FROM document_batch_items i
		JOIN document_workflows w ON w.id = i.workflow_id
		WHERE i.batch_id = ? AND i.state IN ('Scheduled','Running') ORDER BY i.ordinal`, batchID)
	if err != nil {
		return nil, fmt.Errorf("list active document batch items: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := []app.ActiveBatchItem{}
	for rows.Next() {
		var item domain.BatchItem
		var workflowState domain.WorkflowState
		if err := rows.Scan(&item.ID, &item.BatchID, &item.Ordinal, &item.PagePath, &item.Anchor, &item.Title, &item.WorkflowID, &item.State, &item.Detail, &item.CreatedAt, &item.UpdatedAt, &workflowState); err != nil {
			return nil, err
		}
		result = append(result, app.ActiveBatchItem{Item: item, WorkflowState: workflowState})
	}
	return result, rows.Err()
}

// TransitionBatchItem moves one item under a state fence and reports whether
// this caller won it.
func (d *DocumentPracticeRepository) TransitionBatchItem(ctx context.Context, itemID string, from, to domain.BatchItemState, detail string, now time.Time) (bool, error) {
	if !from.Valid() || !to.Valid() || now.IsZero() {
		return false, errors.New("document batch item transition is invalid")
	}
	query := `UPDATE document_batch_items SET state = ?, detail = ?, updated_at = ? WHERE id = ? AND state = ?`
	args := []any{to, detail, now.UTC(), itemID, from}
	if detail == "" {
		query = `UPDATE document_batch_items SET state = ?, updated_at = ? WHERE id = ? AND state = ?`
		args = []any{to, now.UTC(), itemID, from}
	}
	result, err := d.conn.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("transition document batch item: %w", err)
	}
	changed, _ := result.RowsAffected()
	return changed == 1, nil
}

// TransitionBatchState moves the batch under a state fence and, when a human
// action is supplied, records its audit in the same transaction.
func (d *DocumentPracticeRepository) TransitionBatchState(ctx context.Context, batchID string, from, to domain.BatchState, action *audit.HumanAction, now time.Time) (bool, error) {
	if !from.Valid() || !to.Valid() || now.IsZero() {
		return false, errors.New("document batch transition is invalid")
	}
	if action == nil {
		result, err := d.conn.ExecContext(ctx, `UPDATE document_batches SET state = ?, updated_at = ? WHERE id = ? AND state = ?`, to, now.UTC(), batchID, from)
		if err != nil {
			return false, fmt.Errorf("transition document batch: %w", err)
		}
		changed, _ := result.RowsAffected()
		return changed == 1, nil
	}
	if err := action.Validate(); err != nil {
		return false, err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin document batch transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE document_batches SET state = ?, updated_at = ? WHERE id = ? AND state = ?`, to, now.UTC(), batchID, from)
	if err != nil {
		return false, fmt.Errorf("transition document batch: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return false, nil
	}
	if err := insertHumanAction(ctx, tx, *action); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// CancelBatch fences the batch to Cancelled and cancels its not-yet-started
// items in one transaction; in-flight items keep running to their terminal
// states.
func (d *DocumentPracticeRepository) CancelBatch(ctx context.Context, batchID string, from domain.BatchState, action *audit.HumanAction, now time.Time) (int64, error) {
	if !from.Valid() || now.IsZero() {
		return 0, errors.New("document batch cancel is invalid")
	}
	if action == nil || action.Validate() != nil {
		return 0, errors.New("document batch cancel requires a human action audit")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin document batch cancel: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE document_batches SET state = ?, updated_at = ? WHERE id = ? AND state = ?`, domain.BatchCancelled, now.UTC(), batchID, from)
	if err != nil {
		return 0, fmt.Errorf("cancel document batch: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return 0, nil
	}
	items, err := tx.ExecContext(ctx, `UPDATE document_batch_items SET state = ?, detail = ?, updated_at = ? WHERE batch_id = ? AND state = ?`, domain.ItemCancelled, "batch-cancelled", now.UTC(), batchID, domain.ItemPending)
	if err != nil {
		return 0, fmt.Errorf("cancel document batch items: %w", err)
	}
	cancelled, _ := items.RowsAffected()
	if err := insertHumanAction(ctx, tx, *action); err != nil {
		return 0, err
	}
	return cancelled, tx.Commit()
}

// CorpusPageStateCounts is one page's workflow-state rollup for the admin
// corpus projection. Stuck counts workflows that are either Failed or past
// their state class's dwell budget.
type CorpusPageStateCounts struct {
	Total      int
	Published  int
	Failed     int
	NoPractice int
	InProgress int
	Stuck      int
}

// SummarizeWorkflowStatesByPages rolls the pinned identity's workflows up per
// page path. Unknown pages simply have no rows. The dwell cutoffs split
// stuck detection between Agent phases and public runtime phases; terminal
// states never count as stuck - Failed is the explicit attention signal and
// Published/NoPractice/Rejected rows simply age.
func (d *DocumentPracticeRepository) SummarizeWorkflowStatesByPages(ctx context.Context, identity domain.DocumentContext, pages []string, agentCutoff, runtimeCutoff time.Time) (map[string]CorpusPageStateCounts, error) {
	result := map[string]CorpusPageStateCounts{}
	if len(pages) == 0 {
		return result, nil
	}
	const chunk = 100
	for start := 0; start < len(pages); start += chunk {
		end := start + chunk
		if end > len(pages) {
			end = len(pages)
		}
		batch := pages[start:end]
		query := `SELECT page_path, state, COUNT(*),
				COUNT(*) FILTER (WHERE state = 'Failed' OR (state NOT IN ('Published', 'NoPractice', 'Rejected') AND updated_at <= (CASE WHEN state IN ('MaterializingArtifact','Verifying','VerificationReviewing','Publishing') THEN ?::timestamptz ELSE ?::timestamptz END)))
				FROM document_workflows
				WHERE source_id = ? AND commit = ? AND language = ? AND page_path IN (` + placeholders(len(batch)) + `)
				GROUP BY page_path, state`
		args := []any{runtimeCutoff.UTC(), agentCutoff.UTC(), identity.SourceID, identity.Commit, identity.Language}
		for _, page := range batch {
			args = append(args, page)
		}
		rows, err := d.conn.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("summarize document workflow states: %w", err)
		}
		for rows.Next() {
			var pagePath string
			var state domain.WorkflowState
			var count, stuck int
			if err := rows.Scan(&pagePath, &state, &count, &stuck); err != nil {
				_ = rows.Close()
				return nil, err
			}
			counts := result[pagePath]
			counts.Total += count
			counts.Stuck += stuck
			switch state {
			case domain.Published:
				counts.Published += count
			case domain.Failed:
				counts.Failed += count
			case domain.NoPractice:
				counts.NoPractice += count
			default:
				counts.InProgress += count
			}
			result[pagePath] = counts
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return result, nil
}
