package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type DB struct {
	conn *sql.DB
}

func New(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	conn, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_synchronous=NORMAL")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// Connection pool config (SQLite serializes writes)
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func (d *DB) Close() error { return d.conn.Close() }

func (d *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id          TEXT PRIMARY KEY,
		subject     TEXT NOT NULL UNIQUE,
		name        TEXT NOT NULL,
		password_hash TEXT NOT NULL DEFAULT '',
		totp_secret TEXT NOT NULL DEFAULT '',
		created_at  TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS challenges (
		id          TEXT PRIMARY KEY,
		title       TEXT NOT NULL,
		type        TEXT NOT NULL,
		difficulty  TEXT NOT NULL,
		tags        TEXT NOT NULL DEFAULT '[]',
		description TEXT NOT NULL,
		timeout     INTEGER NOT NULL DEFAULT 600,
		image       TEXT NOT NULL,
		dir_path    TEXT NOT NULL,
		created_at  TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS instances (
		id           TEXT PRIMARY KEY,
		user_id      TEXT NOT NULL REFERENCES users(id),
		challenge_id TEXT NOT NULL REFERENCES challenges(id),
		status       TEXT NOT NULL DEFAULT 'running',
		namespace    TEXT NOT NULL,
		pod_name     TEXT NOT NULL,
		created_at   TEXT NOT NULL DEFAULT (datetime('now')),
		destroyed_at TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_instances_user ON instances(user_id, status);

	CREATE TABLE IF NOT EXISTS submissions (
		id           TEXT PRIMARY KEY,
		instance_id  TEXT NOT NULL REFERENCES instances(id),
		user_id      TEXT NOT NULL REFERENCES users(id),
		challenge_id TEXT NOT NULL REFERENCES challenges(id),
		passed       INTEGER NOT NULL,
		exit_code    INTEGER NOT NULL,
		output       TEXT NOT NULL DEFAULT '',
		created_at   TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE INDEX IF NOT EXISTS idx_submissions_user ON submissions(user_id, created_at);
	`
	_, err := d.conn.Exec(schema)
	return err
}
