package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/breakfix/breakfix/internal/domain/audit"
)

type User struct {
	ID           string
	Subject      string
	Name         string
	PasswordHash string
	TOTPSecret   string
	Role         string
	CreatedAt    time.Time
}

type UserSummary struct {
	ID        string    `json:"id"`
	Subject   string    `json:"subject"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// firstUserAdvisoryKey serializes first-registration detection across
// concurrent registers. The value is an arbitrary but fixed constant.
const firstUserAdvisoryKey = 487550139918

// CreateUserWithAuth assigns the bootstrap admin role to the very first user
// inside the registration transaction: the advisory lock closes the
// concurrent-first-registration race, then the empty-table check decides.
func (d *IdentityRepository) CreateUserWithAuth(ctx context.Context, id, username, passwordHash, totpSecret string) (string, error) {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(?)`, firstUserAdvisoryKey); err != nil {
		return "", err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return "", err
	}
	role := "user"
	if count == 0 {
		role = "admin"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, subject, name, password_hash, totp_secret, role) VALUES (?, ?, ?, ?, ?, ?)`, id, username, username, passwordHash, totpSecret, role); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

func (d *IdentityRepository) GetUserBySubject(subject string) (*User, error) {
	u := &User{}
	err := d.conn.QueryRow(
		"SELECT id, subject, name, password_hash, totp_secret, role, created_at FROM users WHERE subject = ?",
		subject,
	).Scan(&u.ID, &u.Subject, &u.Name, &u.PasswordHash, &u.TOTPSecret, &u.Role, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (d *IdentityRepository) GetUserByID(id string) (*User, error) {
	u := &User{}
	err := d.conn.QueryRow(
		"SELECT id, subject, name, password_hash, totp_secret, role, created_at FROM users WHERE id = ?",
		id,
	).Scan(&u.ID, &u.Subject, &u.Name, &u.PasswordHash, &u.TOTPSecret, &u.Role, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (d *IdentityRepository) ListUsers(ctx context.Context) ([]UserSummary, error) {
	rows, err := d.conn.QueryContext(ctx, `SELECT id, subject, name, role, created_at FROM users ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]UserSummary, 0)
	for rows.Next() {
		var user UserSummary
		if err := rows.Scan(&user.ID, &user.Subject, &user.Name, &user.Role, &user.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

// ResetUserTOTPSecret replaces the target's second factor and records the
// administrator action in the same transaction: a rolled-back reset never
// leaves an audit row behind, and a recorded reset always took effect.
func (d *IdentityRepository) ResetUserTOTPSecret(ctx context.Context, userID, secret string, action *audit.HumanAction) error {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE users SET totp_secret = ? WHERE id = ?`, secret, userID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return fmt.Errorf("user not found")
	}
	if action != nil {
		if err := action.Validate(); err != nil {
			return err
		}
		if err := insertHumanAction(ctx, tx, *action); err != nil {
			return err
		}
	}
	return tx.Commit()
}
