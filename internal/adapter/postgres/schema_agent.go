package postgres

var schemaAgentStatements = []string{
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
}
