package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrTerminalTicketInvalid = errors.New("terminal ticket is invalid, expired, or already used")

// TerminalTicket is the durable, single-use authorization record behind a
// browser WebSocket ticket. The raw token is never stored in PostgreSQL.
type TerminalTicket struct {
	TokenHash      string
	UserID         string
	EnvironmentUID string
	ChallengeID    string
	WindowName     string
	ExpiresAt      time.Time
}

func (d *DB) CreateTerminalTicket(ctx context.Context, ticket TerminalTicket, now time.Time) error {
	for name, value := range map[string]string{
		"token hash": ticket.TokenHash, "user id": ticket.UserID, "environment uid": ticket.EnvironmentUID,
		"challenge id": ticket.ChallengeID, "window name": ticket.WindowName,
	} {
		if err := requiredLearningValue(name, value); err != nil {
			return err
		}
	}
	if now.IsZero() || ticket.ExpiresAt.IsZero() || !ticket.ExpiresAt.After(now) {
		return fmt.Errorf("terminal ticket expiry must be in the future")
	}
	if _, err := d.conn.ExecContext(ctx, `DELETE FROM terminal_tickets WHERE expires_at <= ?`, now.UTC()); err != nil {
		return fmt.Errorf("remove expired terminal tickets: %w", err)
	}
	if _, err := d.conn.ExecContext(ctx, `
		INSERT INTO terminal_tickets
			(token_hash, user_id, environment_uid, challenge_id, window_name, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, ticket.TokenHash, ticket.UserID, ticket.EnvironmentUID, ticket.ChallengeID, ticket.WindowName, ticket.ExpiresAt.UTC()); err != nil {
		return fmt.Errorf("create terminal ticket: %w", err)
	}
	return nil
}

// ClaimTerminalTicket atomically consumes one ticket. Caller-supplied route
// values are part of the predicate, so a ticket cannot be replayed for a
// different challenge or tmux window.
func (d *DB) ClaimTerminalTicket(ctx context.Context, tokenHash, challengeID, windowName string, now time.Time) (TerminalTicket, error) {
	if strings.TrimSpace(tokenHash) == "" || strings.TrimSpace(challengeID) == "" || strings.TrimSpace(windowName) == "" || now.IsZero() {
		return TerminalTicket{}, ErrTerminalTicketInvalid
	}
	var ticket TerminalTicket
	err := d.conn.QueryRowContext(ctx, `
		UPDATE terminal_tickets
		SET used_at = ?
		WHERE token_hash = ?
		  AND challenge_id = ?
		  AND window_name = ?
		  AND used_at IS NULL
		  AND expires_at > ?
		RETURNING token_hash, user_id, environment_uid, challenge_id, window_name, expires_at
	`, now.UTC(), tokenHash, challengeID, windowName, now.UTC()).Scan(
		&ticket.TokenHash, &ticket.UserID, &ticket.EnvironmentUID, &ticket.ChallengeID, &ticket.WindowName, &ticket.ExpiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return TerminalTicket{}, ErrTerminalTicketInvalid
	}
	if err != nil {
		return TerminalTicket{}, fmt.Errorf("claim terminal ticket: %w", err)
	}
	return ticket, nil
}
