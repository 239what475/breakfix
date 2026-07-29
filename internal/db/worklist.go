package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/worklist"
)

func (d *DB) EnqueueWorkItem(ctx context.Context, input worklist.CreateItem) (*worklist.Item, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	row := d.conn.QueryRowContext(ctx, `INSERT INTO work_items
		(id, kind, subject_type, subject_id, state, next_run_at, deadline_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING `+workItemColumns,
		input.ID, input.Kind, input.SubjectType, input.SubjectID, worklist.StatePending,
		input.NextRunAt.UTC(), nullableTime(input.DeadlineAt), now, now)
	item, err := scanWorkItem(row)
	if err != nil {
		return nil, fmt.Errorf("enqueue work item: %w", err)
	}
	return item, nil
}

func (d *DB) GetWorkItem(ctx context.Context, id string) (*worklist.Item, error) {
	item, err := scanWorkItem(d.conn.QueryRowContext(ctx, `SELECT `+workItemColumns+` FROM work_items WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, worklist.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get work item: %w", err)
	}
	return item, nil
}

func (d *DB) GetWorkItemForSubject(ctx context.Context, kind worklist.Kind, subjectType worklist.SubjectType, subjectID string) (*worklist.Item, error) {
	item, err := scanWorkItem(d.conn.QueryRowContext(ctx, `SELECT `+workItemColumns+`
		FROM work_items WHERE kind = ? AND subject_type = ? AND subject_id = ?`, kind, subjectType, subjectID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, worklist.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get subject work item: %w", err)
	}
	return item, nil
}

func (d *DB) ClaimWorkItem(ctx context.Context, kind worklist.Kind, workerID string, leaseTTL time.Duration, now time.Time) (*worklist.Claim, error) {
	if !worklist.ValidKind(kind) || strings.TrimSpace(workerID) == "" || leaseTTL <= 0 || now.IsZero() {
		return nil, errors.New("claim requires a valid kind, worker, lease ttl, and current time")
	}
	now = now.UTC()
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin work item claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `UPDATE work_items SET
		state = ?, lease_owner = '', lease_expires_at = NULL, error_code = 'deadline_exceeded',
		error_summary = 'work item deadline exceeded', updated_at = ?
		WHERE kind = ? AND state IN (?, ?) AND deadline_at IS NOT NULL AND deadline_at <= ?`,
		worklist.StateFailed, now, kind, worklist.StatePending, worklist.StateRunning, now); err != nil {
		return nil, fmt.Errorf("expire work items: %w", err)
	}

	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM work_items
		WHERE kind = ? AND (
			(state = ? AND next_run_at <= ?)
			OR (state = ? AND lease_expires_at <= ?)
		) AND (deadline_at IS NULL OR deadline_at > ?)
		ORDER BY next_run_at, created_at, id
		FOR UPDATE SKIP LOCKED LIMIT 1`,
		kind, worklist.StatePending, now, worklist.StateRunning, now, now).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit work item expiry: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select claimable work item: %w", err)
	}

	leaseOwner := strings.TrimSpace(workerID) + "-" + worklist.NewID("lease")
	item, err := scanWorkItem(tx.QueryRowContext(ctx, `UPDATE work_items SET
		state = ?, attempt = attempt + 1, lease_owner = ?, lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND state IN (?, ?)
		RETURNING `+workItemColumns,
		worklist.StateRunning, leaseOwner, now.Add(leaseTTL), now, id, worklist.StatePending, worklist.StateRunning))
	if err != nil {
		return nil, fmt.Errorf("claim work item: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit work item claim: %w", err)
	}
	return &worklist.Claim{Item: *item, LeaseOwner: leaseOwner}, nil
}

func (d *DB) RenewWorkItem(ctx context.Context, claim worklist.Claim, leaseTTL time.Duration, now time.Time) error {
	if !claim.Valid() || leaseTTL <= 0 || now.IsZero() {
		return errors.New("renew requires a valid claim, lease ttl, and current time")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE work_items SET lease_expires_at = ?, updated_at = ?
		WHERE id = ? AND state = ? AND attempt = ? AND lease_owner = ?
		AND lease_expires_at > ? AND (deadline_at IS NULL OR deadline_at > ?)`,
		now.UTC().Add(leaseTTL), now.UTC(), claim.Item.ID, worklist.StateRunning,
		claim.Item.Attempt, claim.LeaseOwner, now.UTC(), now.UTC())
	return workItemMutationResult(result, err, "renew work item")
}

func (d *DB) ValidateWorkItemClaim(ctx context.Context, claim worklist.Claim, now time.Time) error {
	if !claim.Valid() || now.IsZero() {
		return errors.New("validation requires a valid claim and current time")
	}
	var valid bool
	err := d.conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_items
		WHERE id = ? AND state = ? AND attempt = ? AND lease_owner = ?
		AND lease_expires_at > ? AND (deadline_at IS NULL OR deadline_at > ?))`,
		claim.Item.ID, worklist.StateRunning, claim.Item.Attempt, claim.LeaseOwner, now.UTC(), now.UTC()).Scan(&valid)
	if err != nil {
		return fmt.Errorf("validate work item claim: %w", err)
	}
	if !valid {
		return worklist.ErrLeaseLost
	}
	return nil
}

func (d *DB) RequeueWorkItem(ctx context.Context, claim worklist.Claim, nextRunAt time.Time, code, summary string, now time.Time) error {
	if !claim.Valid() || nextRunAt.IsZero() || now.IsZero() || strings.TrimSpace(code) == "" || strings.TrimSpace(summary) == "" {
		return errors.New("requeue requires a valid claim, next run time, code, summary, and current time")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE work_items SET state = ?, next_run_at = ?, lease_owner = '',
		lease_expires_at = NULL, error_code = ?, error_summary = ?, updated_at = ?
		WHERE id = ? AND state = ? AND attempt = ? AND lease_owner = ?
		AND lease_expires_at > ? AND (deadline_at IS NULL OR deadline_at > ?)`,
		worklist.StatePending, nextRunAt.UTC(), code, summary, now.UTC(), claim.Item.ID,
		worklist.StateRunning, claim.Item.Attempt, claim.LeaseOwner, now.UTC(), now.UTC())
	return workItemMutationResult(result, err, "requeue work item")
}

func (d *DB) CompleteWorkItem(ctx context.Context, claim worklist.Claim, now time.Time) error {
	return d.finishWorkItem(ctx, claim, worklist.StateSucceeded, "", "", now)
}

func (d *DB) FailWorkItem(ctx context.Context, claim worklist.Claim, code, summary string, now time.Time) error {
	if strings.TrimSpace(code) == "" || strings.TrimSpace(summary) == "" {
		return errors.New("failed work item requires an error code and summary")
	}
	return d.finishWorkItem(ctx, claim, worklist.StateFailed, code, summary, now)
}

func (d *DB) finishWorkItem(ctx context.Context, claim worklist.Claim, state worklist.State, code, summary string, now time.Time) error {
	if !claim.Valid() || now.IsZero() || (state != worklist.StateSucceeded && state != worklist.StateFailed) {
		return errors.New("finish requires a valid claim, terminal state, and current time")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE work_items SET state = ?, lease_owner = '', lease_expires_at = NULL,
		error_code = ?, error_summary = ?, updated_at = ?
		WHERE id = ? AND state = ? AND attempt = ? AND lease_owner = ? AND lease_expires_at > ?`,
		state, code, summary, now.UTC(), claim.Item.ID, worklist.StateRunning,
		claim.Item.Attempt, claim.LeaseOwner, now.UTC())
	return workItemMutationResult(result, err, "finish work item")
}

func (d *DB) CancelWorkItemsForSubject(ctx context.Context, subjectType worklist.SubjectType, subjectID, code, summary string, now time.Time) (int64, error) {
	if strings.TrimSpace(subjectID) == "" || strings.TrimSpace(code) == "" || strings.TrimSpace(summary) == "" || now.IsZero() {
		return 0, errors.New("cancel requires subject, code, summary, and current time")
	}
	result, err := d.conn.ExecContext(ctx, `UPDATE work_items SET state = ?, lease_owner = '', lease_expires_at = NULL,
		error_code = ?, error_summary = ?, updated_at = ?
		WHERE subject_type = ? AND subject_id = ? AND state IN (?, ?)`,
		worklist.StateCancelled, code, summary, now.UTC(), subjectType, subjectID,
		worklist.StatePending, worklist.StateRunning)
	if err != nil {
		return 0, fmt.Errorf("cancel subject work items: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count cancelled work items: %w", err)
	}
	return count, nil
}

func createWorkItemTx(ctx context.Context, tx *Tx, input worklist.CreateItem, now time.Time) (*worklist.Item, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	item, err := scanWorkItem(tx.QueryRowContext(ctx, `INSERT INTO work_items
		(id, kind, subject_type, subject_id, state, next_run_at, deadline_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING `+workItemColumns,
		input.ID, input.Kind, input.SubjectType, input.SubjectID, worklist.StatePending,
		input.NextRunAt.UTC(), nullableTime(input.DeadlineAt), now.UTC(), now.UTC()))
	if err != nil {
		return nil, fmt.Errorf("insert work item: %w", err)
	}
	return item, nil
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func workItemMutationResult(result sql.Result, err error, operation string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return worklist.ErrLeaseLost
	}
	return nil
}

const workItemColumns = `id, kind, subject_type, subject_id, state, attempt, lease_owner, lease_expires_at,
	next_run_at, deadline_at, error_code, error_summary, created_at, updated_at`

type workItemScanner interface {
	Scan(...any) error
}

func scanWorkItem(row workItemScanner) (*worklist.Item, error) {
	var item worklist.Item
	var leaseExpiresAt, deadlineAt sql.NullTime
	if err := row.Scan(&item.ID, &item.Kind, &item.SubjectType, &item.SubjectID, &item.State,
		&item.Attempt, &item.LeaseOwner, &leaseExpiresAt, &item.NextRunAt, &deadlineAt,
		&item.ErrorCode, &item.ErrorSummary, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return nil, err
	}
	if leaseExpiresAt.Valid {
		value := leaseExpiresAt.Time.UTC()
		item.LeaseExpiresAt = &value
	}
	if deadlineAt.Valid {
		value := deadlineAt.Time.UTC()
		item.DeadlineAt = &value
	}
	item.NextRunAt = item.NextRunAt.UTC()
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	return &item, nil
}
