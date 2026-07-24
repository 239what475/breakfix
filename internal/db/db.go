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
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)

	db := &DB{conn: conn}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func (d *DB) Close() error { return d.conn.Close() }

var migrations = []string{
	// v1: initial schema
	`
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
		image       TEXT NOT NULL,
		dir_path    TEXT NOT NULL DEFAULT '',
		created_at  TEXT NOT NULL DEFAULT (datetime('now'))
	);
	`,
	// v2: drop legacy tables (instances + submissions moved to CRDs)
	`
	DROP TABLE IF EXISTS instances;
	DROP TABLE IF EXISTS submissions;
	`,
	// v3: human-reviewed challenge authoring sessions
	`
	CREATE TABLE IF NOT EXISTS authoring_sessions (
		id                TEXT PRIMARY KEY,
		user_id           TEXT NOT NULL,
		agent_session_id  TEXT NOT NULL,
		agent_started     INTEGER NOT NULL DEFAULT 0,
		state             TEXT NOT NULL,
		current_revision  INTEGER NOT NULL DEFAULT 0,
		generation_id     TEXT NOT NULL DEFAULT '',
		verify_task_id    TEXT NOT NULL DEFAULT '',
		last_error        TEXT NOT NULL DEFAULT '',
		created_at        TEXT NOT NULL,
		updated_at        TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS authoring_revisions (
		session_id              TEXT NOT NULL,
		revision                INTEGER NOT NULL,
		plan_json               TEXT NOT NULL,
		candidate_submission_id TEXT NOT NULL DEFAULT '',
		candidate_dir           TEXT NOT NULL DEFAULT '',
		candidate_generation_id TEXT NOT NULL DEFAULT '',
		verification_json       TEXT NOT NULL DEFAULT '',
		created_at              TEXT NOT NULL,
		PRIMARY KEY (session_id, revision)
	);

	CREATE TABLE IF NOT EXISTS authoring_messages (
		id           TEXT PRIMARY KEY,
		session_id   TEXT NOT NULL,
		role         TEXT NOT NULL,
		content      TEXT NOT NULL,
		changes_json TEXT NOT NULL DEFAULT '[]',
		created_at   TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS authoring_messages_session_created
		ON authoring_messages(session_id, created_at);
	`,
	// v4: verified artifacts are distinct from the old unverified candidate
	// workflow, and sessions retain the last author-visible verified revision.
	`
	ALTER TABLE authoring_sessions ADD COLUMN visible_revision INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE authoring_revisions RENAME COLUMN candidate_submission_id TO artifact_submission_id;
	ALTER TABLE authoring_revisions RENAME COLUMN candidate_dir TO artifact_dir;
	ALTER TABLE authoring_revisions RENAME COLUMN candidate_generation_id TO artifact_generation_id;
	`,
	// v5: retain an allocated opaque id across the external filesystem promote.
	`
	ALTER TABLE authoring_sessions ADD COLUMN publish_challenge_id TEXT NOT NULL DEFAULT '';
	`,
	// v6: generation jobs need their own resumable Claude Code session. It is
	// deliberately distinct from the author-facing planning conversation.
	`
	ALTER TABLE authoring_sessions ADD COLUMN workflow_session_id TEXT NOT NULL DEFAULT '';
	ALTER TABLE authoring_sessions ADD COLUMN workflow_started INTEGER NOT NULL DEFAULT 0;
	`,
	// v7: a failed VerifyTask must survive transient Generation job creation
	// failures so the same workflow session receives the diagnostic on retry.
	`
	ALTER TABLE authoring_sessions ADD COLUMN pending_feedback TEXT NOT NULL DEFAULT '';
	`,
	// v8: environment-scoped challenge assistant conversations.
	`
	CREATE TABLE IF NOT EXISTS assistant_sessions (
		id                TEXT PRIMARY KEY,
		user_id           TEXT NOT NULL,
		environment_uid   TEXT NOT NULL,
		environment_name  TEXT NOT NULL,
		runtime           TEXT NOT NULL,
		challenge_id      TEXT NOT NULL,
		agent_session_id  TEXT NOT NULL,
		agent_started     INTEGER NOT NULL DEFAULT 0,
		created_at        TEXT NOT NULL,
		updated_at        TEXT NOT NULL,
		UNIQUE(user_id, environment_uid, challenge_id)
	);

	CREATE TABLE IF NOT EXISTS assistant_messages (
		id             TEXT PRIMARY KEY,
		session_id     TEXT NOT NULL,
		role           TEXT NOT NULL,
		content        TEXT NOT NULL,
		evidence_json  TEXT NOT NULL DEFAULT '[]',
		created_at     TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS assistant_messages_session_created
		ON assistant_messages(session_id, created_at);
	CREATE INDEX IF NOT EXISTS assistant_sessions_environment_uid
		ON assistant_sessions(environment_uid);
	`,
	// v9: completion history outlives the ephemeral Environment CRD.
	`
	CREATE TABLE IF NOT EXISTS user_challenge_progress (
		user_id         TEXT NOT NULL,
		challenge_id    TEXT NOT NULL,
		completed_at    TEXT NOT NULL,
		environment_uid TEXT NOT NULL,
		PRIMARY KEY (user_id, challenge_id)
	);
	CREATE INDEX IF NOT EXISTS user_challenge_progress_user
		ON user_challenge_progress(user_id);
	`,
	// v10: user learning activity is durable, while Environment CRDs and
	// terminal WebSockets remain ephemeral runtime resources.
	`
	CREATE TABLE IF NOT EXISTS user_challenge_attempts (
		environment_uid  TEXT PRIMARY KEY,
		user_id          TEXT NOT NULL,
		challenge_id     TEXT NOT NULL,
		runtime          TEXT NOT NULL DEFAULT '',
		ready_at         TEXT NOT NULL,
		ended_at         TEXT NOT NULL DEFAULT '',
		outcome          TEXT NOT NULL DEFAULT 'active',
		learning_seconds INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS user_challenge_attempts_user_recent
		ON user_challenge_attempts(user_id, ready_at DESC);
	CREATE INDEX IF NOT EXISTS user_challenge_attempts_challenge_user
		ON user_challenge_attempts(challenge_id, user_id);

	-- Preserve the invariant that historical completions always count as an
	-- attempt, without inventing a runtime or a duration that was never stored.
	INSERT INTO user_challenge_attempts
		(environment_uid, user_id, challenge_id, runtime, ready_at, ended_at, outcome, learning_seconds)
	SELECT environment_uid, user_id, challenge_id, '', completed_at, completed_at, 'completed', 0
	FROM user_challenge_progress
	WHERE true
	ON CONFLICT(environment_uid) DO NOTHING;

	CREATE TABLE IF NOT EXISTS terminal_connections (
		id                  TEXT PRIMARY KEY,
		environment_uid     TEXT NOT NULL,
		user_id             TEXT NOT NULL,
		challenge_id        TEXT NOT NULL,
		gateway_instance_id TEXT NOT NULL,
		connected_at        TEXT NOT NULL,
		heartbeat_at        TEXT NOT NULL,
		disconnected_at     TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS terminal_connections_environment_active
		ON terminal_connections(environment_uid, disconnected_at, heartbeat_at);

	CREATE TABLE IF NOT EXISTS environment_usage_sessions (
		id              TEXT PRIMARY KEY,
		environment_uid TEXT NOT NULL,
		user_id         TEXT NOT NULL,
		challenge_id    TEXT NOT NULL,
		started_at      TEXT NOT NULL,
		heartbeat_at    TEXT NOT NULL,
		ended_at        TEXT NOT NULL DEFAULT ''
	);
	CREATE UNIQUE INDEX IF NOT EXISTS environment_usage_sessions_one_active
		ON environment_usage_sessions(environment_uid) WHERE ended_at = '';
	CREATE INDEX IF NOT EXISTS environment_usage_sessions_user
		ON environment_usage_sessions(user_id, started_at DESC);
	`,
	// v11: duration is derived solely from usage sessions. Rebuild the table
	// rather than dropping the duplicate field directly: an earlier local v10
	// development schema already omitted it, while the committed v10 schema
	// included it. Selecting the durable columns works for either history.
	`
	BEGIN;
	CREATE TABLE user_challenge_attempts_v11 (
		environment_uid TEXT PRIMARY KEY,
		user_id         TEXT NOT NULL,
		challenge_id    TEXT NOT NULL,
		runtime         TEXT NOT NULL DEFAULT '',
		ready_at        TEXT NOT NULL,
		ended_at        TEXT NOT NULL DEFAULT '',
		outcome         TEXT NOT NULL DEFAULT 'active'
	);
	INSERT INTO user_challenge_attempts_v11
		(environment_uid, user_id, challenge_id, runtime, ready_at, ended_at, outcome)
	SELECT environment_uid, user_id, challenge_id, runtime, ready_at, ended_at, outcome
	FROM user_challenge_attempts;
	DROP TABLE user_challenge_attempts;
	ALTER TABLE user_challenge_attempts_v11 RENAME TO user_challenge_attempts;
	CREATE INDEX user_challenge_attempts_user_recent
		ON user_challenge_attempts(user_id, ready_at DESC);
	CREATE INDEX user_challenge_attempts_challenge_user
		ON user_challenge_attempts(challenge_id, user_id);
	COMMIT;
	`,
	// v12: learning history uses a compound cursor so attempts created in the
	// same timestamp bucket cannot be skipped between pages.
	`
	DROP INDEX IF EXISTS user_challenge_attempts_user_recent;
	CREATE INDEX user_challenge_attempts_user_recent
		ON user_challenge_attempts(user_id, ready_at DESC, environment_uid DESC);
	`,
	// v13: challenge directories are the sole catalog authority. The v1 table
	// was never part of the filesystem-backed catalog and must not remain as a
	// misleading, stale copy of challenge metadata.
	`
	DROP TABLE IF EXISTS challenges;
	`,
	// v14: terminal connection ownership belongs to the Server process.
	`
	ALTER TABLE terminal_connections RENAME COLUMN gateway_instance_id TO server_instance_id;
	`,
}

func (d *DB) migrate() error {
	var version int
	if err := d.conn.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	for i := version; i < len(migrations); i++ {
		if _, err := d.conn.Exec(migrations[i]); err != nil {
			return fmt.Errorf("migration v%d: %w", i+1, err)
		}
		version = i + 1
		if _, err := d.conn.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
			return fmt.Errorf("set schema version: %w", err)
		}
	}
	return nil
}
