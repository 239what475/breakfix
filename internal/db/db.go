package db

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
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
	return open(dsn, true, "")
}

// NewWithAgentRole applies the schema and grants the limited agent runtime
// privileges needed by the Worker database role.
func NewWithAgentRole(dsn, agentRole string) (*DB, error) {
	return open(dsn, true, agentRole)
}

// OpenAgentRuntime opens the Worker connection without schema migration. The
// Worker database role is intentionally granted access only to agent_* tables,
// so schema ownership remains with the Server deployment.
func OpenAgentRuntime(dsn string) (*DB, error) {
	return open(dsn, false, "")
}

func open(dsn string, migrate bool, agentRole string) (*DB, error) {
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
		if err := db.migrate(ctx, agentRole); err != nil {
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
			out.WriteString(fmt.Sprintf("%d", argument))
			i++
		default:
			r, size := utf8.DecodeRuneInString(query[i:])
			out.WriteRune(r)
			i += size
		}
	}
	return out.String()
}

type schemaMigration struct {
	version    int
	statements []string
}

func (d *DB) migrate(ctx context.Context, agentRole string) error {
	if _, err := d.conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}

	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `LOCK TABLE schema_migrations IN ACCESS EXCLUSIVE MODE`); err != nil {
		return fmt.Errorf("lock schema migrations: %w", err)
	}
	for _, migration := range schemaMigrations {
		var installed bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)`, migration.version).Scan(&installed)
		if err != nil {
			return fmt.Errorf("read schema migration %d: %w", migration.version, err)
		}
		if installed {
			continue
		}
		for index, statement := range migration.statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("schema migration %d statement %d: %w", migration.version, index+1, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, migration.version); err != nil {
			return fmt.Errorf("record schema migration %d: %w", migration.version, err)
		}
	}
	if err := grantAgentRuntimePrivileges(ctx, tx, agentRole); err != nil {
		return err
	}
	return tx.Commit()
}

var postgresIdentifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func grantAgentRuntimePrivileges(ctx context.Context, tx *Tx, role string) error {
	role = strings.TrimSpace(role)
	if role == "" {
		return nil
	}
	if !postgresIdentifier.MatchString(role) {
		return fmt.Errorf("agent database role is not a valid postgres identifier")
	}
	quotedRole := `"` + role + `"`
	statements := []string{
		`GRANT USAGE ON SCHEMA public TO ` + quotedRole,
		`GRANT SELECT ON TABLE agent_sessions TO ` + quotedRole,
		`GRANT SELECT, INSERT ON TABLE agent_messages TO ` + quotedRole,
		`GRANT SELECT, UPDATE ON TABLE agent_runs TO ` + quotedRole,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("grant agent runtime privileges: %w", err)
		}
	}
	return nil
}

var schemaMigrations = []schemaMigration{
	{version: 1, statements: []string{
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
		agent_session_id TEXT NOT NULL,
		agent_started BOOLEAN NOT NULL DEFAULT FALSE,
		workflow_session_id TEXT NOT NULL DEFAULT '',
		workflow_started BOOLEAN NOT NULL DEFAULT FALSE,
		state TEXT NOT NULL,
		current_revision BIGINT NOT NULL DEFAULT 0,
		visible_revision BIGINT NOT NULL DEFAULT 0,
		generation_id TEXT NOT NULL DEFAULT '',
		verify_task_id TEXT NOT NULL DEFAULT '',
		pending_feedback TEXT NOT NULL DEFAULT '',
		publish_challenge_id TEXT NOT NULL DEFAULT '',
		last_error TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
		`CREATE TABLE authoring_revisions (
		session_id TEXT NOT NULL,
		revision BIGINT NOT NULL,
		plan_json TEXT NOT NULL,
		artifact_submission_id TEXT NOT NULL DEFAULT '',
		artifact_dir TEXT NOT NULL DEFAULT '',
		artifact_generation_id TEXT NOT NULL DEFAULT '',
		verification_json TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		PRIMARY KEY (session_id, revision)
	)`,
		`CREATE TABLE authoring_messages (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		changes_json TEXT NOT NULL DEFAULT '[]',
		created_at TEXT NOT NULL
	)`,
		`CREATE INDEX authoring_messages_session_created ON authoring_messages(session_id, created_at)`,
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
		`CREATE TABLE taxonomy_work_items (
		id TEXT PRIMARY KEY,
		kind TEXT NOT NULL,
		challenge_id TEXT NOT NULL,
		challenge_revision TEXT NOT NULL,
		base_revision TEXT NOT NULL DEFAULT '',
		mapper_session_id TEXT NOT NULL,
		mapper_started BOOLEAN NOT NULL DEFAULT FALSE,
		curriculum_session TEXT NOT NULL,
		curriculum_started BOOLEAN NOT NULL DEFAULT FALSE,
		sre_session TEXT NOT NULL,
		sre_started BOOLEAN NOT NULL DEFAULT FALSE,
		candidate_json TEXT NOT NULL DEFAULT '',
		curriculum_review_json TEXT NOT NULL DEFAULT '',
		sre_review_json TEXT NOT NULL DEFAULT '',
		round INTEGER NOT NULL DEFAULT 0,
		technical_failures INTEGER NOT NULL DEFAULT 0,
		execution_failures INTEGER NOT NULL DEFAULT 0,
		next_run_at TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL,
		published_revision TEXT NOT NULL DEFAULT '',
		last_error TEXT NOT NULL DEFAULT '',
		lease_owner TEXT NOT NULL DEFAULT '',
		lease_expires_at TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(kind, challenge_id, challenge_revision)
	)`,
		`CREATE INDEX taxonomy_work_items_ready ON taxonomy_work_items(state, next_run_at, lease_expires_at, updated_at, created_at)`,
		`CREATE TABLE taxonomy_leases (
		name TEXT PRIMARY KEY,
		owner TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
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
		status TEXT NOT NULL,
		model TEXT NOT NULL,
		prompt_version TEXT NOT NULL,
		attempt INTEGER NOT NULL DEFAULT 0,
		next_attempt_at TIMESTAMPTZ NOT NULL,
		lease_owner TEXT NOT NULL DEFAULT '',
		lease_expires_at TIMESTAMPTZ,
		deadline_at TIMESTAMPTZ NOT NULL,
		last_error TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		completed_at TIMESTAMPTZ
	)`,
		`CREATE INDEX agent_runs_claim ON agent_runs(status, next_attempt_at, deadline_at, created_at)`,
		`CREATE UNIQUE INDEX agent_runs_session_active ON agent_runs(session_id)
		WHERE session_id IS NOT NULL AND status IN ('pending', 'running')`,
	}},
	{version: 2, statements: []string{
		`ALTER TABLE agent_runs ADD COLUMN input_json JSONB NOT NULL DEFAULT '{}'::jsonb`,
	}},
	{version: 3, statements: []string{
		`ALTER TABLE agent_messages ADD COLUMN IF NOT EXISTS metadata_json JSONB NOT NULL DEFAULT '{}'::jsonb`,
	}},
	{version: 4, statements: []string{
		`ALTER TABLE authoring_sessions ADD COLUMN IF NOT EXISTS runtime_session_id TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE IF NOT EXISTS authoring_stages (
			run_id TEXT PRIMARY KEY REFERENCES agent_runs(id) ON DELETE CASCADE,
			session_id TEXT NOT NULL REFERENCES authoring_sessions(id) ON DELETE CASCADE,
			base_revision BIGINT NOT NULL,
			stage_revision BIGINT NOT NULL,
			plan_json JSONB NOT NULL,
			changes_json JSONB NOT NULL DEFAULT '[]'::jsonb,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS authoring_stages_session ON authoring_stages(session_id)`,
	}},
	{version: 5, statements: []string{
		// Taxonomy Agent execution is represented by generic agent_runs. Remove
		// provider-specific session bookkeeping from the domain WorkItem.
		`ALTER TABLE taxonomy_work_items
			DROP COLUMN IF EXISTS mapper_session_id,
			DROP COLUMN IF EXISTS mapper_started,
			DROP COLUMN IF EXISTS curriculum_session,
			DROP COLUMN IF EXISTS curriculum_started,
			DROP COLUMN IF EXISTS sre_session,
			DROP COLUMN IF EXISTS sre_started`,
		`ALTER TABLE taxonomy_work_items
			ADD COLUMN IF NOT EXISTS active_stage TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS active_run_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE taxonomy_work_items ALTER COLUMN candidate_json DROP DEFAULT`,
		`ALTER TABLE taxonomy_work_items ALTER COLUMN curriculum_review_json DROP DEFAULT`,
		`ALTER TABLE taxonomy_work_items ALTER COLUMN sre_review_json DROP DEFAULT`,
		`ALTER TABLE taxonomy_work_items ALTER COLUMN candidate_json TYPE JSONB USING NULLIF(candidate_json, '')::jsonb`,
		`ALTER TABLE taxonomy_work_items ALTER COLUMN curriculum_review_json TYPE JSONB USING NULLIF(curriculum_review_json, '')::jsonb`,
		`ALTER TABLE taxonomy_work_items ALTER COLUMN sre_review_json TYPE JSONB USING NULLIF(sre_review_json, '')::jsonb`,
		`ALTER TABLE taxonomy_work_items ALTER COLUMN next_run_at DROP DEFAULT`,
		`ALTER TABLE taxonomy_work_items ALTER COLUMN lease_expires_at DROP DEFAULT`,
		`UPDATE taxonomy_work_items SET next_run_at = NULL WHERE next_run_at = ''`,
		`UPDATE taxonomy_work_items SET lease_expires_at = NULL WHERE lease_expires_at = ''`,
		`ALTER TABLE taxonomy_work_items ALTER COLUMN next_run_at TYPE TIMESTAMPTZ USING next_run_at::timestamptz`,
		`ALTER TABLE taxonomy_work_items ALTER COLUMN lease_expires_at TYPE TIMESTAMPTZ USING lease_expires_at::timestamptz`,
		`ALTER TABLE taxonomy_work_items ALTER COLUMN created_at TYPE TIMESTAMPTZ USING created_at::timestamptz`,
		`ALTER TABLE taxonomy_work_items ALTER COLUMN updated_at TYPE TIMESTAMPTZ USING updated_at::timestamptz`,
		`ALTER TABLE taxonomy_work_items ALTER COLUMN next_run_at DROP NOT NULL`,
		`ALTER TABLE taxonomy_work_items ALTER COLUMN lease_expires_at DROP NOT NULL`,
		`DROP INDEX IF EXISTS taxonomy_work_items_ready`,
		`CREATE INDEX taxonomy_work_items_ready ON taxonomy_work_items(state, next_run_at, lease_expires_at, updated_at, created_at)`,
		`CREATE INDEX IF NOT EXISTS taxonomy_work_items_active_run ON taxonomy_work_items(active_run_id) WHERE active_run_id <> ''`,
	}},
	{version: 6, statements: []string{
		`CREATE TABLE generator_workspaces (
			generator_session_id TEXT PRIMARY KEY REFERENCES agent_sessions(id) ON DELETE RESTRICT,
			namespace TEXT NOT NULL,
			pvc_name TEXT NOT NULL UNIQUE,
			sandbox_id TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL,
			provision_deadline TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			deleted_at TIMESTAMPTZ
		)`,
		`CREATE INDEX generator_workspaces_pending ON generator_workspaces(state, provision_deadline)
			WHERE state = 'pending'`,
		`CREATE INDEX generator_workspaces_deleting ON generator_workspaces(updated_at)
			WHERE state = 'deleting'`,
	}},
	{version: 7, statements: []string{
		`ALTER TABLE authoring_sessions
			ADD COLUMN IF NOT EXISTS generator_session_id TEXT NOT NULL DEFAULT '',
			ADD COLUMN IF NOT EXISTS generator_run_id TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE generator_runs (
			run_id TEXT PRIMARY KEY REFERENCES agent_runs(id) ON DELETE CASCADE,
			generator_session_id TEXT NOT NULL REFERENCES agent_sessions(id) ON DELETE RESTRICT,
			authoring_session_id TEXT NOT NULL REFERENCES authoring_sessions(id) ON DELETE RESTRICT,
			authoring_revision BIGINT NOT NULL,
			seed_submission_id TEXT NOT NULL DEFAULT '',
			verify_task_id TEXT NOT NULL DEFAULT '',
			workspace_initialized_at TIMESTAMPTZ,
			submission_id TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			UNIQUE(generator_session_id, run_id)
		)`,
		`CREATE INDEX generator_runs_authoring_session ON generator_runs(authoring_session_id, authoring_revision, created_at)`,
		`CREATE UNIQUE INDEX generator_runs_submission ON generator_runs(submission_id) WHERE submission_id <> ''`,
		`CREATE UNIQUE INDEX generator_runs_verify_task ON generator_runs(verify_task_id) WHERE verify_task_id <> ''`,
	}},
	{version: 8, statements: []string{
		// Authoring now uses the provider-neutral runtime session and durable
		// Generator Run lineage exclusively. Remove the retired Claude/Generation
		// bookkeeping instead of keeping a second state machine alive.
		`ALTER TABLE authoring_sessions
			DROP COLUMN IF EXISTS agent_session_id,
			DROP COLUMN IF EXISTS agent_started,
			DROP COLUMN IF EXISTS workflow_session_id,
			DROP COLUMN IF EXISTS workflow_started,
			DROP COLUMN IF EXISTS generation_id,
			DROP COLUMN IF EXISTS pending_feedback`,
		`DROP TABLE IF EXISTS authoring_messages`,
		`ALTER TABLE authoring_revisions RENAME COLUMN artifact_generation_id TO artifact_generator_run_id`,
	}},
	{version: 9, statements: []string{
		// A mapping WorkItem has no candidate or reviewer conclusions before the
		// Mapper and Review stages complete. Migration 5 converted the legacy
		// text columns to JSONB but left their original NOT NULL constraints.
		`ALTER TABLE taxonomy_work_items ALTER COLUMN candidate_json DROP NOT NULL`,
		`ALTER TABLE taxonomy_work_items ALTER COLUMN curriculum_review_json DROP NOT NULL`,
		`ALTER TABLE taxonomy_work_items ALTER COLUMN sre_review_json DROP NOT NULL`,
	}},
	{version: 10, statements: []string{
		// Generator workspaces are ephemeral execution resources, not session
		// state. Development does not preserve the retired session-keyed rows.
		`DROP TABLE IF EXISTS generator_workspaces`,
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
		`CREATE INDEX generator_workspaces_pending ON generator_workspaces(state, provision_deadline)
			WHERE state = 'pending'`,
		`CREATE INDEX generator_workspaces_deleting ON generator_workspaces(updated_at)
			WHERE state = 'deleting'`,
	}},
	{version: 11, statements: []string{
		`CREATE TABLE terminal_tickets (
			token_hash TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			environment_uid TEXT NOT NULL,
			challenge_id TEXT NOT NULL,
			window_name TEXT NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL,
			used_at TIMESTAMPTZ
		)`,
		`CREATE INDEX terminal_tickets_expiry ON terminal_tickets(expires_at)`,
	}},
	{version: 12, statements: []string{
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
	}},
	{version: 13, statements: []string{
		`CREATE TABLE work_items (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL CHECK (kind IN ('agent', 'build', 'artifact_publish', 'verify', 'artifact_cleanup', 'challenge_publish')),
			subject_type TEXT NOT NULL CHECK (subject_type IN ('agent_run', 'candidate_revision')),
			subject_id TEXT NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')),
			attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
			lease_owner TEXT NOT NULL DEFAULT '',
			lease_expires_at TIMESTAMPTZ,
			next_run_at TIMESTAMPTZ NOT NULL,
			deadline_at TIMESTAMPTZ,
			error_code TEXT NOT NULL DEFAULT '',
			error_summary TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			UNIQUE(kind, subject_type, subject_id),
			CHECK ((kind = 'agent' AND subject_type = 'agent_run') OR (kind <> 'agent' AND subject_type = 'candidate_revision')),
			CHECK ((kind = 'artifact_cleanup' AND deadline_at IS NULL) OR (kind <> 'artifact_cleanup' AND deadline_at IS NOT NULL))
		)`,
		`CREATE INDEX work_items_claim ON work_items(kind, state, next_run_at, lease_expires_at, created_at, id)`,
		`CREATE INDEX work_items_subject ON work_items(subject_type, subject_id, created_at)`,
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
			state TEXT NOT NULL,
			build_output JSONB,
			artifact_reference JSONB,
			verification_report JSONB,
			failure JSONB,
			publication JSONB,
			superseded_by TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			verified_at TIMESTAMPTZ,
			published_at TIMESTAMPTZ,
			UNIQUE(generator_run_id)
		)`,
		`CREATE INDEX candidate_revisions_authoring ON candidate_revisions(authoring_session_id, authoring_revision, created_at)`,
		`CREATE INDEX candidate_revisions_state ON candidate_revisions(state, updated_at)`,
	}},
	{version: 14, statements: []string{
		// Development migration: scheduling authority moves from agent_runs to
		// the universal work_items table. Existing run identities and results
		// remain, but attempt and lease state has exactly one owner.
		`INSERT INTO work_items
			(id, kind, subject_type, subject_id, state, attempt, lease_owner, lease_expires_at,
			 next_run_at, deadline_at, error_code, error_summary, created_at, updated_at)
		SELECT 'work-agent-' || id, 'agent', 'agent_run', id,
			CASE status WHEN 'pending' THEN 'pending' WHEN 'running' THEN 'running'
				WHEN 'succeeded' THEN 'succeeded' WHEN 'failed' THEN 'failed' ELSE 'cancelled' END,
			attempt, lease_owner, lease_expires_at, next_attempt_at, deadline_at,
			CASE WHEN last_error = '' THEN '' ELSE 'agent_execution' END, last_error, created_at, updated_at
		FROM agent_runs
		ON CONFLICT (kind, subject_type, subject_id) DO NOTHING`,
		`DROP INDEX IF EXISTS agent_runs_claim`,
		`ALTER TABLE agent_runs
			DROP COLUMN attempt,
			DROP COLUMN next_attempt_at,
			DROP COLUMN lease_owner,
			DROP COLUMN lease_expires_at`,
	}},
}
