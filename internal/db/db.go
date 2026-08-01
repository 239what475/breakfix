package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// DB owns Breakfix's PostgreSQL connection. The application deliberately has
// no SQLite mode: development starts from the same schema contract as a
// deployed Server.
type DB struct {
	conn *Conn
}

// New opens a PostgreSQL DSN and applies the current schema. Existing SQLite
// files are intentionally not accepted or migrated.
func New(dsn string) (*DB, error) {
	return open(dsn, true)
}

func open(dsn string, migrate bool) (*DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("postgres dsn is required")
	}

	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	raw.SetMaxOpenConns(20)
	raw.SetMaxIdleConns(5)

	ctx := context.Background()
	if err := raw.PingContext(ctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	db := &DB{conn: &Conn{raw: raw}}
	if migrate {
		if err := db.migrate(ctx); err != nil {
			_ = raw.Close()
			return nil, fmt.Errorf("migrate postgres: %w", err)
		}
	}
	return db, nil
}

func (d *DB) Close() error { return d.conn.Close() }

// Conn keeps PostgreSQL's placeholder syntax out of the domain repository
// code. It is not a SQL dialect abstraction: this package exclusively opens
// pgx/PostgreSQL, and all schema and SQL semantics are PostgreSQL-native.
type Conn struct {
	raw *sql.DB
}

func (c *Conn) Close() error { return c.raw.Close() }

func (c *Conn) Exec(query string, args ...any) (sql.Result, error) {
	return c.raw.Exec(bind(query), args...)
}

func (c *Conn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.raw.ExecContext(ctx, bind(query), args...)
}

func (c *Conn) Query(query string, args ...any) (*sql.Rows, error) {
	return c.raw.Query(bind(query), args...)
}

func (c *Conn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return c.raw.QueryContext(ctx, bind(query), args...)
}

func (c *Conn) QueryRow(query string, args ...any) *sql.Row {
	return c.raw.QueryRow(bind(query), args...)
}

func (c *Conn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return c.raw.QueryRowContext(ctx, bind(query), args...)
}

func (c *Conn) BeginTx(ctx context.Context, options *sql.TxOptions) (*Tx, error) {
	tx, err := c.raw.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &Tx{raw: tx}, nil
}

// Tx mirrors the small subset of database/sql.Tx used by the repositories and
// applies the same PostgreSQL parameter binding as Conn.
type Tx struct {
	raw *sql.Tx
}

func (t *Tx) Commit() error   { return t.raw.Commit() }
func (t *Tx) Rollback() error { return t.raw.Rollback() }

func (t *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.raw.ExecContext(ctx, bind(query), args...)
}

func (t *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.raw.QueryRowContext(ctx, bind(query), args...)
}

// bind rewrites positional parameters outside SQL literals and comments. The
// repository queries use '?' so their argument order stays readable while pgx
// receives PostgreSQL's required $1, $2, ... placeholders.
func bind(query string) string {
	var out strings.Builder
	out.Grow(len(query) + 16)
	argument := 0
	inSingleQuote := false
	inDoubleQuote := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(query); {
		if inLineComment {
			if query[i] == '\n' {
				inLineComment = false
			}
			out.WriteByte(query[i])
			i++
			continue
		}
		if inBlockComment {
			if i+1 < len(query) && query[i] == '*' && query[i+1] == '/' {
				out.WriteString("*/")
				i += 2
				inBlockComment = false
				continue
			}
			out.WriteByte(query[i])
			i++
			continue
		}
		if !inSingleQuote && !inDoubleQuote && i+1 < len(query) {
			if query[i] == '-' && query[i+1] == '-' {
				out.WriteString("--")
				i += 2
				inLineComment = true
				continue
			}
			if query[i] == '/' && query[i+1] == '*' {
				out.WriteString("/*")
				i += 2
				inBlockComment = true
				continue
			}
		}

		switch query[i] {
		case '\'':
			out.WriteByte(query[i])
			i++
			if inSingleQuote && i < len(query) && query[i] == '\'' {
				out.WriteByte(query[i])
				i++
				continue
			}
			if !inDoubleQuote {
				inSingleQuote = !inSingleQuote
			}
		case '"':
			out.WriteByte(query[i])
			i++
			if inDoubleQuote && i < len(query) && query[i] == '"' {
				out.WriteByte(query[i])
				i++
				continue
			}
			if !inSingleQuote {
				inDoubleQuote = !inDoubleQuote
			}
		case '?':
			if inSingleQuote || inDoubleQuote {
				out.WriteByte(query[i])
				i++
				continue
			}
			argument++
			out.WriteByte('$')
			_, _ = fmt.Fprintf(&out, "%d", argument)
			i++
		default:
			r, size := utf8.DecodeRuneInString(query[i:])
			out.WriteRune(r)
			i += size
		}
	}
	return out.String()
}

func (d *DB) migrate(ctx context.Context) error {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(487550139917)`); err != nil {
		return fmt.Errorf("lock database schema initialization: %w", err)
	}

	var initialized bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1
		FROM pg_catalog.pg_class relation
		JOIN pg_catalog.pg_namespace namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = current_schema()
			AND relation.relname = 'breakfix_schema'
			AND relation.relkind = 'r'
	)`).Scan(&initialized); err != nil {
		return fmt.Errorf("inspect database schema marker: %w", err)
	}
	if initialized {
		var count, version int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MIN(version), 0) FROM breakfix_schema`).Scan(&count, &version); err != nil {
			return fmt.Errorf("read database schema marker: %w", err)
		}
		if count != 1 || version != currentSchemaVersion {
			return fmt.Errorf("database schema marker is invalid; recreate the development database for schema version %d", currentSchemaVersion)
		}
		return tx.Commit()
	}

	var hasExistingTables bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_catalog.pg_tables WHERE schemaname = current_schema()
	)`).Scan(&hasExistingTables); err != nil {
		return fmt.Errorf("inspect existing database tables: %w", err)
	}
	if hasExistingTables {
		return fmt.Errorf("database contains an incompatible pre-baseline schema; recreate the development database for schema version %d", currentSchemaVersion)
	}

	if _, err := tx.ExecContext(ctx, `CREATE TABLE breakfix_schema (
		version INTEGER PRIMARY KEY CHECK (version = `+fmt.Sprintf("%d", currentSchemaVersion)+`)
	)`); err != nil {
		return fmt.Errorf("create database schema marker: %w", err)
	}
	for index, statement := range currentSchemaStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize schema statement %d: %w", index+1, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO breakfix_schema (version) VALUES (?)`, currentSchemaVersion); err != nil {
		return fmt.Errorf("record database schema version: %w", err)
	}
	return tx.Commit()
}

const currentSchemaVersion = 5

// currentSchemaStatements is the only database schema accepted by this
// development-only, intentionally destructive runtime migration. Do not add
// ALTER/DROP compatibility statements here: a previous schema must be reset.
var currentSchemaStatements = []string{
	`CREATE TABLE users (
		id TEXT PRIMARY KEY,
		subject TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		password_hash TEXT NOT NULL DEFAULT '',
		totp_secret TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE authoring_sessions (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		runtime_session_id TEXT NOT NULL DEFAULT '',
		generator_session_id TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL,
		current_revision BIGINT NOT NULL DEFAULT 0,
		visible_revision BIGINT NOT NULL DEFAULT 0,
		publish_challenge_id TEXT NOT NULL DEFAULT '',
		last_error TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
	`CREATE TABLE authoring_revisions (
		session_id TEXT NOT NULL,
		revision BIGINT NOT NULL,
		plan_json TEXT NOT NULL,
		candidate_revision_id TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		PRIMARY KEY (session_id, revision)
	)`,
	`CREATE TABLE user_challenge_progress (
		user_id TEXT NOT NULL,
		challenge_id TEXT NOT NULL,
		completed_at TEXT NOT NULL,
		environment_uid TEXT NOT NULL,
		PRIMARY KEY (user_id, challenge_id)
	)`,
	`CREATE INDEX user_challenge_progress_user ON user_challenge_progress(user_id)`,
	`CREATE TABLE user_challenge_attempts (
		environment_uid TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		challenge_id TEXT NOT NULL,
		runtime TEXT NOT NULL DEFAULT '',
		ready_at TEXT NOT NULL,
		ended_at TEXT NOT NULL DEFAULT '',
		outcome TEXT NOT NULL DEFAULT 'active'
	)`,
	`CREATE INDEX user_challenge_attempts_user_recent ON user_challenge_attempts(user_id, ready_at DESC, environment_uid DESC)`,
	`CREATE INDEX user_challenge_attempts_challenge_user ON user_challenge_attempts(challenge_id, user_id)`,
	`CREATE TABLE terminal_connections (
		id TEXT PRIMARY KEY,
		environment_uid TEXT NOT NULL,
		user_id TEXT NOT NULL,
		challenge_id TEXT NOT NULL,
		server_instance_id TEXT NOT NULL,
		connected_at TEXT NOT NULL,
		heartbeat_at TEXT NOT NULL,
		disconnected_at TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX terminal_connections_environment_active ON terminal_connections(environment_uid, disconnected_at, heartbeat_at)`,
	`CREATE TABLE environment_usage_sessions (
		id TEXT PRIMARY KEY,
		environment_uid TEXT NOT NULL,
		user_id TEXT NOT NULL,
		challenge_id TEXT NOT NULL,
		started_at TEXT NOT NULL,
		heartbeat_at TEXT NOT NULL,
		ended_at TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE UNIQUE INDEX environment_usage_sessions_one_active ON environment_usage_sessions(environment_uid) WHERE ended_at = ''`,
	`CREATE INDEX environment_usage_sessions_user ON environment_usage_sessions(user_id, started_at DESC)`,
	`CREATE TABLE agent_sessions (
		id TEXT PRIMARY KEY,
		purpose TEXT NOT NULL,
		owner_kind TEXT NOT NULL,
		owner_ref TEXT NOT NULL,
		user_ref TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX agent_sessions_owner ON agent_sessions(owner_kind, owner_ref)`,
	`CREATE UNIQUE INDEX agent_sessions_owner_purpose ON agent_sessions(purpose, owner_kind, owner_ref, user_ref)`,
	`CREATE TABLE agent_messages (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
		sequence BIGINT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb,
		created_at TIMESTAMPTZ NOT NULL,
		UNIQUE(session_id, sequence)
	)`,
	`CREATE INDEX agent_messages_session_sequence ON agent_messages(session_id, sequence)`,
	`CREATE TABLE agent_runs (
		id TEXT PRIMARY KEY,
		session_id TEXT REFERENCES agent_sessions(id) ON DELETE SET NULL,
		purpose TEXT NOT NULL,
		owner_kind TEXT NOT NULL,
		owner_ref TEXT NOT NULL,
		input_revision TEXT NOT NULL DEFAULT '',
		input_json JSONB NOT NULL DEFAULT '{}'::jsonb,
		status TEXT NOT NULL,
		model TEXT NOT NULL,
		prompt_version TEXT NOT NULL,
		last_error TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		completed_at TIMESTAMPTZ
	)`,
	`CREATE UNIQUE INDEX agent_runs_session_active ON agent_runs(session_id) WHERE session_id IS NOT NULL AND status = 'running'`,
	`CREATE TABLE authoring_stages (
		run_id TEXT PRIMARY KEY REFERENCES agent_runs(id) ON DELETE CASCADE,
		session_id TEXT NOT NULL REFERENCES authoring_sessions(id) ON DELETE CASCADE,
		base_revision BIGINT NOT NULL,
		stage_revision BIGINT NOT NULL,
		plan_json JSONB NOT NULL,
		changes_json JSONB NOT NULL DEFAULT '[]'::jsonb,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX authoring_stages_session ON authoring_stages(session_id)`,
	`CREATE TABLE generator_workspaces (
		generator_run_id TEXT PRIMARY KEY REFERENCES agent_runs(id) ON DELETE RESTRICT,
		namespace TEXT NOT NULL,
		pvc_name TEXT NOT NULL UNIQUE,
		sandbox_id TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL,
		provision_deadline TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		deleted_at TIMESTAMPTZ
	)`,
	`CREATE INDEX generator_workspaces_pending ON generator_workspaces(state, provision_deadline) WHERE state = 'pending'`,
	`CREATE INDEX generator_workspaces_deleting ON generator_workspaces(updated_at) WHERE state = 'deleting'`,
	`CREATE TABLE terminal_tickets (
		token_hash TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		environment_uid TEXT NOT NULL,
		challenge_id TEXT NOT NULL,
		node_name TEXT NOT NULL DEFAULT '',
		window_name TEXT NOT NULL,
		expires_at TIMESTAMPTZ NOT NULL,
		used_at TIMESTAMPTZ
	)`,
	`CREATE INDEX terminal_tickets_expiry ON terminal_tickets(expires_at)`,
	`CREATE TABLE checkpoint_pass_events (
		environment_uid TEXT NOT NULL,
		checkpoint_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		challenge_id TEXT NOT NULL,
		challenge_revision TEXT NOT NULL DEFAULT '',
		first_passed_at TIMESTAMPTZ NOT NULL,
		summary TEXT NOT NULL,
		PRIMARY KEY (environment_uid, checkpoint_id)
	)`,
	`CREATE INDEX checkpoint_pass_events_user_challenge ON checkpoint_pass_events(user_id, challenge_id, first_passed_at)`,
	`CREATE TABLE candidate_revisions (
		id TEXT PRIMARY KEY,
		authoring_session_id TEXT NOT NULL REFERENCES authoring_sessions(id) ON DELETE RESTRICT,
		authoring_revision BIGINT NOT NULL,
		generator_session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE RESTRICT,
		generator_run_id TEXT NOT NULL REFERENCES agent_runs(id) ON DELETE RESTRICT,
		judge_run_id TEXT NOT NULL DEFAULT '',
		archive_path TEXT NOT NULL,
		archive_sha256 TEXT NOT NULL,
		execution_snapshot JSONB NOT NULL,
		build_output JSONB,
		artifact_reference JSONB,
		verification_report JSONB,
		verify_environment JSONB,
		failure JSONB,
		publication JSONB,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		verified_at TIMESTAMPTZ,
		published_at TIMESTAMPTZ,
		UNIQUE(generator_run_id)
	)`,
	`CREATE INDEX candidate_revisions_authoring ON candidate_revisions(authoring_session_id, authoring_revision, created_at)`,
	`CREATE TABLE generation_workflows (
		id TEXT PRIMARY KEY,
		authoring_session_id TEXT NOT NULL REFERENCES authoring_sessions(id) ON DELETE RESTRICT,
		authoring_revision BIGINT NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('Queued', 'Generating', 'Judging', 'Building', 'ArtifactPublishing', 'Verifying', 'NeedsAuthorReview', 'ChallengePublishing', 'CleaningUp', 'Completed', 'Failed', 'Cancelled')),
		cleanup_intent TEXT NOT NULL DEFAULT '' CHECK (cleanup_intent IN ('', 'completed', 'failed', 'cancelled')),
		candidate_revision_id TEXT REFERENCES candidate_revisions(id) ON DELETE RESTRICT,
		active_agent_run_id TEXT REFERENCES agent_runs(id) ON DELETE RESTRICT,
		state_attempt INTEGER NOT NULL DEFAULT 0 CHECK (state_attempt >= 0),
		lease_owner TEXT NOT NULL DEFAULT '',
		lease_expires_at TIMESTAMPTZ,
		next_run_at TIMESTAMPTZ NOT NULL,
		deadline_at TIMESTAMPTZ,
		deadline_paused_at TIMESTAMPTZ,
		last_error TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		CHECK ((state = 'CleaningUp') = (cleanup_intent <> '')),
		CHECK ((state = 'NeedsAuthorReview') = (deadline_paused_at IS NOT NULL)),
		CHECK ((lease_owner = '') = (lease_expires_at IS NULL))
	)`,
	`CREATE UNIQUE INDEX generation_workflows_active_authoring_session ON generation_workflows(authoring_session_id) WHERE state NOT IN ('Completed', 'Failed', 'Cancelled')`,
	`CREATE INDEX generation_workflows_claim ON generation_workflows(state, next_run_at, lease_expires_at, created_at, id)`,
	`CREATE INDEX generation_workflows_deadline ON generation_workflows(deadline_at) WHERE deadline_at IS NOT NULL`,
	`CREATE TABLE taxonomy_workflows (
		id TEXT PRIMARY KEY,
		challenge_id TEXT NOT NULL,
		challenge_revision TEXT NOT NULL,
		base_revision TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL CHECK (state IN ('Queued', 'Mapping', 'Reviewing', 'Publishing', 'Completed', 'Failed', 'Cancelled')),
		round INTEGER NOT NULL DEFAULT 0 CHECK (round >= 0),
		state_attempt INTEGER NOT NULL DEFAULT 0 CHECK (state_attempt >= 0),
		candidate_changeset JSONB,
		curriculum_review_json JSONB,
		sre_review_json JSONB,
		expected_snapshot_revision TEXT NOT NULL DEFAULT '',
		published_revision TEXT NOT NULL DEFAULT '',
		lease_owner TEXT NOT NULL DEFAULT '',
		lease_expires_at TIMESTAMPTZ,
		next_run_at TIMESTAMPTZ NOT NULL,
		last_error TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		UNIQUE(challenge_id, challenge_revision),
		CHECK ((lease_owner = '') = (lease_expires_at IS NULL))
	)`,
	`CREATE INDEX taxonomy_workflows_claim ON taxonomy_workflows(state, next_run_at, lease_expires_at, created_at, id)`,
}
