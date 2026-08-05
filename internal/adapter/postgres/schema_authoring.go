package postgres

var schemaAuthoringCoreStatements = []string{
	`CREATE TABLE authoring_sessions (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		runtime_session_id TEXT NOT NULL DEFAULT '',
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
}

var schemaAuthoringRuntimeStatements = []string{
	`CREATE TABLE authoring_stages (
		run_id TEXT PRIMARY KEY REFERENCES agent_runs(id) ON DELETE CASCADE,
		session_id TEXT NOT NULL REFERENCES authoring_sessions(id) ON DELETE CASCADE,
		base_revision BIGINT NOT NULL,
		stage_revision BIGINT NOT NULL,
		run_attempt INTEGER NOT NULL CHECK (run_attempt >= 1 AND run_attempt <= 5),
		plan_json JSONB NOT NULL,
		changes_json JSONB NOT NULL DEFAULT '[]'::jsonb,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX authoring_stages_session ON authoring_stages(session_id)`,
}
