package postgres

var schemaGenerationWorkspaceStatements = []string{
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
}

var schemaGenerationStatements = []string{
	`CREATE TABLE candidate_revisions (
		id TEXT PRIMARY KEY,
		 source_kind TEXT NOT NULL CHECK (source_kind IN ('authoring')),
		source_ref TEXT NOT NULL,
		source_revision TEXT NOT NULL,
		generator_session_id TEXT REFERENCES agent_sessions(id) ON DELETE RESTRICT,
		generator_run_id TEXT REFERENCES agent_runs(id) ON DELETE RESTRICT,
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
	`CREATE INDEX candidate_revisions_source ON candidate_revisions(source_kind, source_ref, source_revision, created_at)`,
	`CREATE TABLE generation_workflows (
		id TEXT PRIMARY KEY,
		 source_kind TEXT NOT NULL CHECK (source_kind IN ('authoring')),
		source_ref TEXT NOT NULL,
		source_revision TEXT NOT NULL,
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
	`CREATE UNIQUE INDEX generation_workflows_active_source ON generation_workflows(source_kind, source_ref) WHERE state NOT IN ('Completed', 'Failed', 'Cancelled')`,
	`CREATE INDEX generation_workflows_claim ON generation_workflows(state, next_run_at, lease_expires_at, created_at, id)`,
	`CREATE INDEX generation_workflows_deadline ON generation_workflows(deadline_at) WHERE deadline_at IS NOT NULL`,
}
