package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/breakfix/breakfix/internal/domain/audit"
)

type HumanActionFilter struct {
	Action string
	UserID string
	Cursor *audit.HumanActionCursor
	Limit  int
}

// RecordHumanAction appends one administrative action to the ledger. It is
// the standalone form; state-changing repositories call insertHumanAction
// inside their own transaction so an audit row survives only together with
// the state change it describes.
func (d *HumanActionRepository) RecordHumanAction(ctx context.Context, action audit.HumanAction) error {
	if err := action.Validate(); err != nil {
		return err
	}
	return insertHumanAction(ctx, d.conn, action)
}

// insertHumanAction is the transactional primitive shared with the state-
// changing repositories. Both Conn and Tx satisfy the parameter interface.
func insertHumanAction(ctx context.Context, db interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}, action audit.HumanAction) error {
	_, err := db.ExecContext(ctx, `INSERT INTO human_action_audits (id, user_id, action, target_type, target_id, detail, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		action.ID, action.UserID, action.Action, action.TargetType, action.TargetID, []byte(action.Detail), action.CreatedAt.UTC())
	return err
}

// ListHumanActions pages the ledger newest-first. The keyset cursor keeps
// pagination stable while new rows arrive.
func (d *HumanActionRepository) ListHumanActions(ctx context.Context, filter HumanActionFilter) ([]audit.HumanAction, error) {
	if filter.Limit < 1 {
		return nil, errors.New("human action audit limit must be positive")
	}
	var query strings.Builder
	query.WriteString(`SELECT id, user_id, action, target_type, target_id, detail, created_at FROM human_action_audits`)
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 5)
	if filter.Action != "" {
		conditions = append(conditions, `action = ?`)
		args = append(args, filter.Action)
	}
	if filter.UserID != "" {
		conditions = append(conditions, `user_id = ?`)
		args = append(args, filter.UserID)
	}
	if filter.Cursor != nil {
		if filter.Cursor.CreatedAt.IsZero() || strings.TrimSpace(filter.Cursor.ID) == "" {
			return nil, errors.New("human action audit cursor is invalid")
		}
		conditions = append(conditions, `(created_at, id) < (?, ?)`)
		args = append(args, filter.Cursor.CreatedAt.UTC(), filter.Cursor.ID)
	}
	if len(conditions) > 0 {
		query.WriteString(` WHERE ` + strings.Join(conditions, ` AND `))
	}
	query.WriteString(` ORDER BY created_at DESC, id DESC LIMIT ?`)
	args = append(args, filter.Limit)
	rows, err := d.conn.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	actions := make([]audit.HumanAction, 0)
	for rows.Next() {
		var action audit.HumanAction
		var detail []byte
		if err := rows.Scan(&action.ID, &action.UserID, &action.Action, &action.TargetType, &action.TargetID, &detail, &action.CreatedAt); err != nil {
			return nil, err
		}
		action.Detail = detail
		actions = append(actions, action)
	}
	return actions, rows.Err()
}
