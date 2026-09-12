package postgres

var schemaEnvironmentLearningStatements = []string{
	`CREATE TABLE user_scenario_progress (
		user_id TEXT NOT NULL,
		scenario_id TEXT NOT NULL,
		completed_at TEXT NOT NULL,
		environment_uid TEXT NOT NULL,
		PRIMARY KEY (user_id, scenario_id)
	)`,
	`CREATE INDEX user_scenario_progress_user ON user_scenario_progress(user_id)`,
	`CREATE TABLE user_scenario_attempts (
		environment_uid TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		scenario_id TEXT NOT NULL,
		scenario_revision TEXT NOT NULL,
		runtime TEXT NOT NULL DEFAULT '',
		ready_at TEXT NOT NULL,
		ended_at TEXT NOT NULL DEFAULT '',
		outcome TEXT NOT NULL DEFAULT 'active'
	)`,
	`CREATE INDEX user_scenario_attempts_user_recent ON user_scenario_attempts(user_id, ready_at DESC, environment_uid DESC)`,
	`CREATE INDEX user_scenario_attempts_scenario_user ON user_scenario_attempts(scenario_id, user_id)`,
	`CREATE TABLE terminal_connections (
		id TEXT PRIMARY KEY,
		environment_uid TEXT NOT NULL,
		user_id TEXT NOT NULL,
		scenario_id TEXT NOT NULL,
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
		scenario_id TEXT NOT NULL,
		started_at TEXT NOT NULL,
		heartbeat_at TEXT NOT NULL,
		ended_at TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE UNIQUE INDEX environment_usage_sessions_one_active ON environment_usage_sessions(environment_uid) WHERE ended_at = ''`,
	`CREATE INDEX environment_usage_sessions_user ON environment_usage_sessions(user_id, started_at DESC)`,
}

var schemaEnvironmentTerminalStatements = []string{
	`CREATE TABLE terminal_tickets (
		token_hash TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		environment_uid TEXT NOT NULL,
		scenario_id TEXT NOT NULL,
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
		scenario_id TEXT NOT NULL,
		scenario_revision TEXT NOT NULL DEFAULT '',
		first_passed_at TIMESTAMPTZ NOT NULL,
		summary TEXT NOT NULL,
		PRIMARY KEY (environment_uid, checkpoint_id)
	)`,
	`CREATE INDEX checkpoint_pass_events_user_scenario ON checkpoint_pass_events(user_id, scenario_id, first_passed_at)`,
}
