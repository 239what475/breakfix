package db

import ()

type User struct {
	ID           string
	Subject      string
	Name         string
	PasswordHash string
	TOTPSecret   string
	CreatedAt    string
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

func (d *DB) GetUserByID(id string) (*User, error) {
	u := &User{}
	err := d.conn.QueryRow(
		"SELECT id, subject, name, password_hash, totp_secret, created_at FROM users WHERE id = ?",
		id,
	).Scan(&u.ID, &u.Subject, &u.Name, &u.PasswordHash, &u.TOTPSecret, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}
