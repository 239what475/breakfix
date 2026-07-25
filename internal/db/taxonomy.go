package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/taxonomy"
)

var ErrTaxonomyWorkNotFound = errors.New("taxonomy work item not found")

func (d *DB) EnqueueTaxonomyWork(ctx context.Context, item taxonomy.WorkItem) (*taxonomy.WorkItem, error) {
	if item.Kind != taxonomy.WorkKindMapping || strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.ChallengeID) == "" || strings.TrimSpace(item.ChallengeRevision) == "" || strings.TrimSpace(item.MapperSessionID) == "" || strings.TrimSpace(item.CurriculumSession) == "" || strings.TrimSpace(item.SRESession) == "" {
		return nil, fmt.Errorf("invalid taxonomy work item")
	}
	if item.TechnicalFailures < 0 || item.ExecutionFailures < 0 {
		return nil, fmt.Errorf("taxonomy work failure counts cannot be negative")
	}
	if item.State == "" {
		item.State = taxonomy.WorkPending
	}
	if item.State != taxonomy.WorkPending {
		return nil, fmt.Errorf("new taxonomy work item must be Pending")
	}
	now := time.Now().UTC()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	_, err := d.conn.ExecContext(ctx, `INSERT INTO taxonomy_work_items
		(id, kind, challenge_id, challenge_revision, base_revision, mapper_session_id, mapper_started, curriculum_session, curriculum_started, sre_session, sre_started, technical_failures, execution_failures, next_run_at, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(kind, challenge_id, challenge_revision) DO NOTHING`,
		item.ID, item.Kind, item.ChallengeID, item.ChallengeRevision, item.BaseRevision, item.MapperSessionID, item.MapperStarted, item.CurriculumSession, item.CurriculumStarted, item.SRESession, item.SREStarted, item.TechnicalFailures, item.ExecutionFailures, taxonomyTimeText(item.NextRunAt), item.State, nowText(item.CreatedAt), nowText(item.UpdatedAt))
	if err != nil {
		return nil, fmt.Errorf("enqueue taxonomy work: %w", err)
	}
	return d.GetTaxonomyWorkByChallenge(ctx, item.Kind, item.ChallengeID, item.ChallengeRevision)
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

// ClaimTaxonomyWork leases one fair queue item. All state transitions are
// persisted before the caller releases this lease again.
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
	nowValue := nowText(now)
	row := tx.QueryRowContext(ctx, `SELECT `+taxonomyWorkColumns+` FROM taxonomy_work_items
		WHERE state IN (?, ?) AND (next_run_at = '' OR next_run_at <= ?) AND (lease_expires_at = '' OR lease_expires_at <= ?)
		ORDER BY next_run_at, updated_at, created_at, id LIMIT 1`, taxonomy.WorkPending, taxonomy.WorkReadyPublish, nowValue, nowValue)
	item, err := scanTaxonomyWork(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	expiresAt := now.Add(ttl)
	result, err := tx.ExecContext(ctx, `UPDATE taxonomy_work_items SET lease_owner = ?, lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND (lease_expires_at = '' OR lease_expires_at <= ?)`, owner, nowText(expiresAt), nowValue, item.ID, nowValue)
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

// SaveClaimedTaxonomyWork atomically persists a committee-stage result and
// releases its worker lease. Technical execution failures remain resumable
// work with a future NextRunAt; only cancellation or explicit terminal states
// stop future claims.
func (d *DB) SaveClaimedTaxonomyWork(ctx context.Context, item taxonomy.WorkItem) error {
	if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.LeaseOwner) == "" {
		return fmt.Errorf("taxonomy work id and lease owner are required")
	}
	if item.State != taxonomy.WorkPending && item.State != taxonomy.WorkReadyPublish && item.State != taxonomy.WorkPublished && item.State != taxonomy.WorkFailed && item.State != taxonomy.WorkCancelled {
		return fmt.Errorf("invalid persisted taxonomy work state %q", item.State)
	}
	if item.TechnicalFailures < 0 || item.ExecutionFailures < 0 {
		return fmt.Errorf("taxonomy work failure counts cannot be negative")
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
		base_revision = ?, mapper_started = ?, curriculum_started = ?, sre_started = ?, candidate_json = ?, curriculum_review_json = ?, sre_review_json = ?, round = ?, technical_failures = ?, execution_failures = ?, next_run_at = ?, state = ?, published_revision = ?, last_error = ?,
		lease_owner = '', lease_expires_at = '', updated_at = ?
		WHERE id = ? AND lease_owner = ?`,
		item.BaseRevision, item.MapperStarted, item.CurriculumStarted, item.SREStarted, candidate, curriculum, sre, item.Round, item.TechnicalFailures, item.ExecutionFailures, taxonomyTimeText(item.NextRunAt), item.State, item.PublishedRevision, item.LastError, nowText(now), item.ID, item.LeaseOwner)
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
	result, err := d.conn.ExecContext(ctx, `UPDATE taxonomy_work_items SET lease_expires_at = ? WHERE id = ? AND lease_owner = ?`, nowText(time.Now().UTC().Add(ttl)), id, owner)
	if err != nil {
		return fmt.Errorf("extend taxonomy work lease: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrTaxonomyWorkNotFound
	}
	return nil
}

func (d *DB) MarkTaxonomyWorkAgentStarted(ctx context.Context, id, owner string, agent taxonomy.WorkAgent) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(owner) == "" {
		return fmt.Errorf("taxonomy work id and lease owner are required")
	}
	var column string
	switch agent {
	case taxonomy.WorkAgentMapper:
		column = "mapper_started"
	case taxonomy.WorkAgentCurriculum:
		column = "curriculum_started"
	case taxonomy.WorkAgentSRE:
		column = "sre_started"
	default:
		return fmt.Errorf("unknown taxonomy work agent %q", agent)
	}
	now := time.Now().UTC()
	result, err := d.conn.ExecContext(ctx, fmt.Sprintf(`UPDATE taxonomy_work_items SET %s = 1, updated_at = ? WHERE id = ? AND lease_owner = ?`, column), nowText(now), id, owner)
	if err != nil {
		return fmt.Errorf("mark taxonomy work agent started: %w", err)
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrTaxonomyWorkNotFound
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

const taxonomyWorkColumns = `id, kind, challenge_id, challenge_revision, base_revision, mapper_session_id, mapper_started, curriculum_session, curriculum_started, sre_session, sre_started,
	candidate_json, curriculum_review_json, sre_review_json, round, technical_failures, execution_failures, next_run_at, state, published_revision, last_error, lease_owner, lease_expires_at, created_at, updated_at`

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
	var candidate, curriculum, sre, nextRunAt, leaseExpires, createdAt, updatedAt string
	if err := scanner.Scan(&item.ID, &item.Kind, &item.ChallengeID, &item.ChallengeRevision, &item.BaseRevision, &item.MapperSessionID, &item.MapperStarted, &item.CurriculumSession, &item.CurriculumStarted, &item.SRESession, &item.SREStarted,
		&candidate, &curriculum, &sre, &item.Round, &item.TechnicalFailures, &item.ExecutionFailures, &nextRunAt, &item.State, &item.PublishedRevision, &item.LastError, &item.LeaseOwner, &leaseExpires, &createdAt, &updatedAt); err != nil {
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
	item.NextRunAt = parseAuthoringTime(nextRunAt)
	item.LeaseExpiresAt = parseAuthoringTime(leaseExpires)
	item.CreatedAt = parseAuthoringTime(createdAt)
	item.UpdatedAt = parseAuthoringTime(updatedAt)
	return &item, nil
}

func taxonomyTimeText(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return nowText(value)
}

func marshalOptional(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func unmarshalOptional(data string, target any) error {
	if strings.TrimSpace(data) == "" {
		return nil
	}
	return json.Unmarshal([]byte(data), target)
}
