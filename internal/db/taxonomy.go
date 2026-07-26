package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/taxonomy"
)

var ErrTaxonomyWorkNotFound = errors.New("taxonomy work item not found")

func (d *DB) EnqueueTaxonomyWork(ctx context.Context, item taxonomy.WorkItem) (*taxonomy.WorkItem, error) {
	if item.Kind != taxonomy.WorkKindMapping || strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.ChallengeID) == "" || strings.TrimSpace(item.ChallengeRevision) == "" {
		return nil, fmt.Errorf("invalid taxonomy work item")
	}
	if item.TechnicalFailures < 0 || item.ExecutionFailures < 0 {
		return nil, fmt.Errorf("taxonomy work failure counts cannot be negative")
	}
	if item.State == "" {
		item.State = taxonomy.WorkPending
	}
	if item.State != taxonomy.WorkPending || item.ActiveStage != "" || item.ActiveRunID != "" {
		return nil, fmt.Errorf("new taxonomy work item must be an inactive Pending item")
	}
	now := time.Now().UTC()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	_, err := d.conn.ExecContext(ctx, `INSERT INTO taxonomy_work_items
		(id, kind, challenge_id, challenge_revision, base_revision, technical_failures, execution_failures, next_run_at, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, '0001-01-01T00:00:00Z')::timestamptz, ?, ?, ?)
		ON CONFLICT(kind, challenge_id, challenge_revision) DO NOTHING`,
		item.ID, item.Kind, item.ChallengeID, item.ChallengeRevision, item.BaseRevision, item.TechnicalFailures, item.ExecutionFailures, taxonomyTime(item.NextRunAt), item.State, item.CreatedAt, item.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("enqueue taxonomy work: %w", err)
	}
	return d.GetTaxonomyWorkByChallenge(ctx, item.Kind, item.ChallengeID, item.ChallengeRevision)
}

func (d *DB) GetTaxonomyWork(ctx context.Context, id string) (*taxonomy.WorkItem, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("taxonomy work id is required")
	}
	return d.readTaxonomyWork(ctx, `SELECT `+taxonomyWorkColumns+` FROM taxonomy_work_items WHERE id = ?`, id)
}

func (d *DB) GetTaxonomyWorkByChallenge(ctx context.Context, kind taxonomy.WorkKind, challengeID, revision string) (*taxonomy.WorkItem, error) {
	return d.readTaxonomyWork(ctx, `SELECT `+taxonomyWorkColumns+` FROM taxonomy_work_items WHERE kind = ? AND challenge_id = ? AND challenge_revision = ?`, kind, challengeID, revision)
}

func (d *DB) ListTaxonomyWork(ctx context.Context) ([]taxonomy.WorkItem, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT `+taxonomyWorkColumns+` FROM taxonomy_work_items ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list taxonomy work: %w", err)
	}
	defer rows.Close()
	items := make([]taxonomy.WorkItem, 0)
	for rows.Next() {
		item, err := scanTaxonomyWork(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate taxonomy work: %w", err)
	}
	return items, nil
}

// ClaimTaxonomyWork leases one fair Server-side reconciliation item. It never
// executes a model call; model work is represented by agent_runs instead.
func (d *DB) ClaimTaxonomyWork(ctx context.Context, owner string, ttl time.Duration) (*taxonomy.WorkItem, error) {
	if strings.TrimSpace(owner) == "" || ttl <= 0 {
		return nil, fmt.Errorf("taxonomy work lease owner and ttl are required")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	row := tx.QueryRowContext(ctx, `SELECT `+taxonomyWorkColumns+` FROM taxonomy_work_items
		WHERE state IN (?, ?) AND (next_run_at IS NULL OR next_run_at <= ?) AND (lease_expires_at IS NULL OR lease_expires_at <= ?)
		ORDER BY next_run_at NULLS FIRST, updated_at, created_at, id
		FOR UPDATE SKIP LOCKED
		LIMIT 1`, taxonomy.WorkPending, taxonomy.WorkReadyPublish, now, now)
	item, err := scanTaxonomyWork(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	expiresAt := now.Add(ttl)
	result, err := tx.ExecContext(ctx, `UPDATE taxonomy_work_items SET lease_owner = ?, lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND (lease_expires_at IS NULL OR lease_expires_at <= ?)`, owner, expiresAt, now, item.ID, now)
	if err != nil {
		return nil, fmt.Errorf("claim taxonomy work: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, tx.Commit()
	}
	item.LeaseOwner = owner
	item.LeaseExpiresAt = expiresAt
	item.UpdatedAt = now
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return item, nil
}

// SaveClaimedTaxonomyWork persists Server-owned work state and releases its
// reconciliation lease. The Worker never invokes this method.
func (d *DB) SaveClaimedTaxonomyWork(ctx context.Context, item taxonomy.WorkItem) error {
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.LeaseOwner) == "" {
		return fmt.Errorf("taxonomy work id and lease owner are required")
	}
	if !validTaxonomyWorkState(item.State) {
		return fmt.Errorf("invalid persisted taxonomy work state %q", item.State)
	}
	if item.TechnicalFailures < 0 || item.ExecutionFailures < 0 {
		return fmt.Errorf("taxonomy work failure counts cannot be negative")
	}
	if (item.ActiveStage == "") != (item.ActiveRunID == "") {
		return fmt.Errorf("taxonomy active stage and run id must be set together")
	}
	if item.ActiveStage != "" {
		if _, err := taxonomy.PurposeForStage(item.ActiveStage); err != nil {
			return err
		}
	}
	candidate, err := marshalOptional(item.Candidate)
	if err != nil {
		return err
	}
	curriculum, err := marshalOptional(item.CurriculumReview)
	if err != nil {
		return err
	}
	sre, err := marshalOptional(item.SREReview)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	result, err := d.conn.ExecContext(ctx, `UPDATE taxonomy_work_items SET
		base_revision = ?, active_stage = ?, active_run_id = ?, candidate_json = ?::jsonb, curriculum_review_json = ?::jsonb, sre_review_json = ?::jsonb,
		round = ?, technical_failures = ?, execution_failures = ?, next_run_at = NULLIF(?, '0001-01-01T00:00:00Z')::timestamptz,
		state = ?, published_revision = ?, last_error = ?, lease_owner = '', lease_expires_at = NULL, updated_at = ?
		WHERE id = ? AND lease_owner = ?`,
		item.BaseRevision, item.ActiveStage, item.ActiveRunID, candidate, curriculum, sre, item.Round, item.TechnicalFailures, item.ExecutionFailures,
		taxonomyTime(item.NextRunAt), item.State, item.PublishedRevision, item.LastError, now, item.ID, item.LeaseOwner)
	if err != nil {
		return fmt.Errorf("save taxonomy work: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrTaxonomyWorkNotFound
	}
	return nil
}

func (d *DB) ExtendTaxonomyWorkLease(ctx context.Context, id, owner string, ttl time.Duration) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(owner) == "" || ttl <= 0 {
		return fmt.Errorf("taxonomy work lease id, owner, and ttl are required")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE taxonomy_work_items SET lease_expires_at = ? WHERE id = ? AND lease_owner = ?`, time.Now().UTC().Add(ttl), id, owner)
	if err != nil {
		return fmt.Errorf("extend taxonomy work lease: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrTaxonomyWorkNotFound
	}
	return nil
}

// CancelClaimedTaxonomyWork terminates a stale WorkItem and its active generic
// Run together. It is used only when the immutable challenge artifact no
// longer matches the WorkItem; ordinary model failures remain retryable.
func (d *DB) CancelClaimedTaxonomyWork(ctx context.Context, item taxonomy.WorkItem, reason string) error {
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.LeaseOwner) == "" || strings.TrimSpace(reason) == "" {
		return fmt.Errorf("claimed taxonomy work and cancellation reason are required")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin cancel taxonomy work: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var activeRunID string
	err = tx.QueryRowContext(ctx, `SELECT active_run_id FROM taxonomy_work_items WHERE id = ? AND lease_owner = ? FOR UPDATE`, item.ID, item.LeaseOwner).Scan(&activeRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTaxonomyWorkNotFound
	}
	if err != nil {
		return fmt.Errorf("lock taxonomy work for cancellation: %w", err)
	}
	now := time.Now().UTC()
	if activeRunID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_runs
			SET status = ?, lease_owner = '', lease_expires_at = NULL, completed_at = ?, updated_at = ?
			WHERE id = ? AND status IN (?, ?)`, agentruntime.RunCancelled, now, now, activeRunID, agentruntime.RunPending, agentruntime.RunRunning); err != nil {
			return fmt.Errorf("cancel active taxonomy run: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE taxonomy_work_items SET
		state = ?, active_stage = '', active_run_id = '', last_error = ?, next_run_at = NULL,
		lease_owner = '', lease_expires_at = NULL, updated_at = ?
		WHERE id = ? AND lease_owner = ?`, taxonomy.WorkCancelled, reason, now, item.ID, item.LeaseOwner)
	if err != nil {
		return fmt.Errorf("cancel taxonomy work: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrTaxonomyWorkNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit taxonomy work cancellation: %w", err)
	}
	return nil
}

// ScheduleTaxonomyRun atomically binds an inactive WorkItem stage to the
// generic runtime queue, then releases the short Server reconciliation lease.
func (d *DB) ScheduleTaxonomyRun(ctx context.Context, item taxonomy.WorkItem, run agentruntime.CreateRun) (*agentruntime.Run, error) {
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.LeaseOwner) == "" || item.ActiveStage != "" || item.ActiveRunID != "" {
		return nil, fmt.Errorf("inactive claimed taxonomy work is required to schedule a run")
	}
	if err := agentruntime.ValidateCreateRun(run); err != nil {
		return nil, err
	}
	input, err := taxonomy.DecodeRunInput(run.Input)
	if err != nil {
		return nil, fmt.Errorf("decode taxonomy run input: %w", err)
	}
	purpose, err := taxonomy.PurposeForStage(input.Stage)
	if err != nil {
		return nil, err
	}
	if input.WorkID != item.ID || input.Round != item.Round || run.SessionID != "" || run.Purpose != purpose || run.OwnerKind != "taxonomy-work" || run.OwnerRef != item.ID {
		return nil, fmt.Errorf("taxonomy agent run does not match its work item")
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin schedule taxonomy run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	created, err := createRunTx(ctx, tx, run, now)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE taxonomy_work_items
		SET base_revision = ?, active_stage = ?, active_run_id = ?, lease_owner = '', lease_expires_at = NULL, updated_at = ?
		WHERE id = ? AND lease_owner = ? AND active_stage = '' AND active_run_id = ''`, item.BaseRevision, input.Stage, created.ID, now, item.ID, item.LeaseOwner)
	if err != nil {
		return nil, fmt.Errorf("bind taxonomy work to agent run: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return nil, ErrTaxonomyWorkNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit taxonomy run scheduling: %w", err)
	}
	return created, nil
}

// FinalizeTaxonomyMapperRun commits a valid candidate and completes its
// attempt in one transaction. Validation of the candidate against Server-owned
// artifact and snapshot data happens before this method is called.
func (d *DB) FinalizeTaxonomyMapperRun(ctx context.Context, claim agentruntime.Claim, workID, baseRevision string, candidate taxonomy.ChangeSet) error {
	encoded, err := marshalOptional(&candidate)
	if err != nil {
		return err
	}
	if strings.TrimSpace(baseRevision) == "" && candidate.Empty() {
		return errors.New("taxonomy mapper candidate is empty")
	}
	return d.finalizeTaxonomyRun(ctx, claim, workID, taxonomy.WorkStageMapper, func(tx *Tx, now time.Time) error {
		result, err := tx.ExecContext(ctx, `UPDATE taxonomy_work_items SET
			base_revision = ?, candidate_json = ?::jsonb, curriculum_review_json = NULL, sre_review_json = NULL,
			active_stage = '', active_run_id = '', technical_failures = 0, execution_failures = 0, next_run_at = NULL,
			state = ?, last_error = '', lease_owner = '', lease_expires_at = NULL, updated_at = ?
			WHERE id = ? AND active_stage = ? AND active_run_id = ?`,
			baseRevision, encoded, taxonomy.WorkPending, now, workID, taxonomy.WorkStageMapper, claim.Run.ID)
		if err != nil {
			return fmt.Errorf("persist taxonomy mapper candidate: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return ErrTaxonomyWorkNotFound
		}
		return nil
	})
}

// FinalizeTaxonomyReviewRun promotes a review pair as one durable committee
// result. No individual reviewer conclusion is ever written to the WorkItem.
func (d *DB) FinalizeTaxonomyReviewRun(ctx context.Context, claim agentruntime.Claim, workID string, curriculum, sre taxonomy.Review) error {
	curriculumJSON, err := marshalOptional(&curriculum)
	if err != nil {
		return err
	}
	sreJSON, err := marshalOptional(&sre)
	if err != nil {
		return err
	}
	state := taxonomy.WorkPending
	if curriculum.Decision == taxonomy.ReviewApprove && sre.Decision == taxonomy.ReviewApprove {
		state = taxonomy.WorkReadyPublish
	}
	return d.finalizeTaxonomyRun(ctx, claim, workID, taxonomy.WorkStageReview, func(tx *Tx, now time.Time) error {
		result, err := tx.ExecContext(ctx, `UPDATE taxonomy_work_items SET
			curriculum_review_json = ?::jsonb, sre_review_json = ?::jsonb, round = round + 1,
			active_stage = '', active_run_id = '', technical_failures = 0, execution_failures = 0, next_run_at = NULL,
			state = ?, last_error = '', lease_owner = '', lease_expires_at = NULL, updated_at = ?
			WHERE id = ? AND active_stage = ? AND active_run_id = ?`,
			curriculumJSON, sreJSON, state, now, workID, taxonomy.WorkStageReview, claim.Run.ID)
		if err != nil {
			return fmt.Errorf("persist taxonomy reviewer pair: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return ErrTaxonomyWorkNotFound
		}
		return nil
	})
}

func (d *DB) finalizeTaxonomyRun(ctx context.Context, claim agentruntime.Claim, workID string, stage taxonomy.WorkStage, apply func(*Tx, time.Time) error) error {
	if !claim.Valid() || strings.TrimSpace(workID) == "" {
		return fmt.Errorf("valid taxonomy claim and work id are required")
	}
	purpose, err := taxonomy.PurposeForStage(stage)
	if err != nil {
		return err
	}
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin finalize taxonomy run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC()
	var matched bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs
		WHERE id = ? AND purpose = ? AND owner_kind = 'taxonomy-work' AND owner_ref = ? AND status = ? AND attempt = ? AND lease_owner = ? AND deadline_at > ?
		FOR UPDATE)`, claim.Run.ID, purpose, workID, agentruntime.RunRunning, claim.Run.Attempt, claim.LeaseOwner, now).Scan(&matched)
	if err != nil {
		return fmt.Errorf("lock taxonomy agent run: %w", err)
	}
	if !matched {
		return agentruntime.ErrLeaseLost
	}
	if err := apply(tx, now); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_runs
		SET status = ?, lease_owner = '', lease_expires_at = NULL, completed_at = ?, updated_at = ?
		WHERE id = ? AND status = ? AND attempt = ? AND lease_owner = ?`,
		agentruntime.RunSucceeded, now, now, claim.Run.ID, agentruntime.RunRunning, claim.Run.Attempt, claim.LeaseOwner)
	if err != nil {
		return fmt.Errorf("complete taxonomy agent run: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return agentruntime.ErrLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit taxonomy agent finalization: %w", err)
	}
	return nil
}

// AcquireTaxonomyLease serializes the model-free filesystem Publisher across
// Server processes sharing the same database and data volume.
func (d *DB) AcquireTaxonomyLease(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(owner) == "" || ttl <= 0 {
		return false, fmt.Errorf("taxonomy lease name, owner, and ttl are required")
	}
	now := time.Now().UTC()
	nowValue := nowText(now)
	result, err := d.conn.ExecContext(ctx, `INSERT INTO taxonomy_leases (name, owner, expires_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET owner = excluded.owner, expires_at = excluded.expires_at, updated_at = excluded.updated_at
		WHERE taxonomy_leases.expires_at <= ? OR taxonomy_leases.owner = excluded.owner`,
		name, owner, nowText(now.Add(ttl)), nowValue, nowValue)
	if err != nil {
		return false, fmt.Errorf("acquire taxonomy lease: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return count == 1, nil
}

func (d *DB) ReleaseTaxonomyLease(ctx context.Context, name, owner string) error {
	_, err := d.conn.ExecContext(ctx, `DELETE FROM taxonomy_leases WHERE name = ? AND owner = ?`, name, owner)
	if err != nil {
		return fmt.Errorf("release taxonomy lease: %w", err)
	}
	return nil
}

const taxonomyWorkColumns = `id, kind, challenge_id, challenge_revision, base_revision, active_stage, active_run_id,
	COALESCE(candidate_json::text, ''), COALESCE(curriculum_review_json::text, ''), COALESCE(sre_review_json::text, ''),
	round, technical_failures, execution_failures, next_run_at, state, published_revision, last_error, lease_owner, lease_expires_at, created_at, updated_at`

type taxonomyWorkScanner interface {
	Scan(dest ...any) error
}

func (d *DB) readTaxonomyWork(ctx context.Context, query string, args ...any) (*taxonomy.WorkItem, error) {
	item, err := scanTaxonomyWork(d.conn.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaxonomyWorkNotFound
	}
	return item, err
}

func scanTaxonomyWork(scanner taxonomyWorkScanner) (*taxonomy.WorkItem, error) {
	var item taxonomy.WorkItem
	var candidate, curriculum, sre string
	var nextRunAt, leaseExpires sql.NullTime
	if err := scanner.Scan(&item.ID, &item.Kind, &item.ChallengeID, &item.ChallengeRevision, &item.BaseRevision, &item.ActiveStage, &item.ActiveRunID,
		&candidate, &curriculum, &sre, &item.Round, &item.TechnicalFailures, &item.ExecutionFailures, &nextRunAt, &item.State, &item.PublishedRevision,
		&item.LastError, &item.LeaseOwner, &leaseExpires, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalOptional(candidate, &item.Candidate); err != nil {
		return nil, fmt.Errorf("decode taxonomy candidate for %q: %w", item.ID, err)
	}
	if err := unmarshalOptional(curriculum, &item.CurriculumReview); err != nil {
		return nil, fmt.Errorf("decode curriculum review for %q: %w", item.ID, err)
	}
	if err := unmarshalOptional(sre, &item.SREReview); err != nil {
		return nil, fmt.Errorf("decode SRE review for %q: %w", item.ID, err)
	}
	if nextRunAt.Valid {
		item.NextRunAt = nextRunAt.Time.UTC()
	}
	if leaseExpires.Valid {
		item.LeaseExpiresAt = leaseExpires.Time.UTC()
	}
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	return &item, nil
}

func validTaxonomyWorkState(value taxonomy.WorkState) bool {
	switch value {
	case taxonomy.WorkPending, taxonomy.WorkReadyPublish, taxonomy.WorkPublished, taxonomy.WorkFailed, taxonomy.WorkCancelled:
		return true
	default:
		return false
	}
}

func taxonomyTime(value time.Time) string {
	if value.IsZero() {
		return "0001-01-01T00:00:00Z"
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func marshalOptional(value any) (string, error) {
	if value == nil {
		return "null", nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalOptional(data string, target any) error {
	if strings.TrimSpace(data) == "" || strings.TrimSpace(data) == "null" {
		return nil
	}
	return json.Unmarshal([]byte(data), target)
}
