package db

import (
	"fmt"
	"time"
)

type User struct {
	ID           string
	Subject      string
	Name         string
	PasswordHash string
	TOTPSecret   string
	CreatedAt    string
}

func (d *DB) GetOrCreateUser(subject, name string) (*User, bool, error) {
	u, err := d.GetUserBySubject(subject)
	if err == nil {
		return u, false, nil
	}
	id := fmt.Sprintf("u-%d", time.Now().UnixNano())
	_, err = d.conn.Exec(
		"INSERT INTO users (id, subject, name) VALUES (?, ?, ?)",
		id, subject, name,
	)
	if err != nil {
		return nil, false, err
	}
	return &User{ID: id, Subject: subject, Name: name}, true, nil
}

func (d *DB) CreateUserWithAuth(id, username, passwordHash, totpSecret string) (string, error) {
	_, err := d.conn.Exec(
		"INSERT INTO users (id, subject, name, password_hash, totp_secret) VALUES (?, ?, ?, ?, ?)",
		id, username, username, passwordHash, totpSecret,
	)
	return id, err
}

func (d *DB) GetUserBySubject(subject string) (*User, error) {
	u := &User{}
	err := d.conn.QueryRow(
		"SELECT id, subject, name, password_hash, totp_secret, created_at FROM users WHERE subject = ?",
		subject,
	).Scan(&u.ID, &u.Subject, &u.Name, &u.PasswordHash, &u.TOTPSecret, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}
