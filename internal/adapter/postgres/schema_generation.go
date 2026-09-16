package postgres

var schemaGenerationWorkspaceStatements = []string{}

var schemaGenerationStatements = []string{
	`CREATE TABLE candidate_revisions (
		id TEXT PRIMARY KEY,
		 source_kind TEXT NOT NULL CHECK (source_kind IN ('authoring')),
		source_ref TEXT NOT NULL,
		source_revision TEXT NOT NULL,
		judge_run_id TEXT NOT NULL DEFAULT '',
		parent_candidate_revision_id TEXT REFERENCES candidate_revisions(id) ON DELETE RESTRICT,
		repair_reason TEXT NOT NULL DEFAULT '',
		archive_path TEXT NOT NULL,
		archive_digest TEXT NOT NULL,
		content_revision TEXT NOT NULL,
		source_archive JSONB NOT NULL,
		runnable_revision_id TEXT REFERENCES runnable_revisions(id) ON DELETE RESTRICT,
		runnable_revision_digest TEXT REFERENCES runnable_revisions(runnable_revision_digest) ON DELETE RESTRICT,
		verification_report_id TEXT REFERENCES runnable_verification_reports(id) ON DELETE RESTRICT,
		verification_report_digest TEXT REFERENCES runnable_verification_reports(verification_report_digest) ON DELETE RESTRICT,
		failure JSONB,
		publication JSONB,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		verified_at TIMESTAMPTZ,
		published_at TIMESTAMPTZ
	)`,
	`CREATE INDEX candidate_revisions_source ON candidate_revisions(source_kind, source_ref, source_revision, created_at)`,
	`CREATE TABLE scenarios (
		id TEXT PRIMARY KEY,
		source_kind TEXT NOT NULL CHECK (source_kind IN ('authoring', 'release')),
		source_ref TEXT NOT NULL,
		owner_user_id TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL CHECK (state IN ('active', 'deprecated')),
		active_revision_id TEXT NOT NULL,
		source_slug TEXT NOT NULL UNIQUE,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		UNIQUE(source_kind, source_ref),
		CHECK ((source_kind = 'authoring' AND owner_user_id <> '') OR (source_kind = 'release' AND owner_user_id = ''))
	)`,
	`CREATE TABLE scenario_revisions (
		id TEXT PRIMARY KEY,
		scenario_id TEXT NOT NULL REFERENCES scenarios(id) ON DELETE RESTRICT,
		source_kind TEXT NOT NULL CHECK (source_kind IN ('authoring', 'release')),
		source_ref TEXT NOT NULL,
		source_revision_id TEXT NOT NULL,
		base_active_revision_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL,
		scenario_type TEXT NOT NULL CHECK (scenario_type IN ('documentation-example', 'operations-scenario')),
		tags JSONB NOT NULL DEFAULT '[]'::jsonb,
		content_revision TEXT NOT NULL,
		source_slug TEXT NOT NULL,
		materialized_path TEXT NOT NULL UNIQUE,
		materialized_revision TEXT NOT NULL,
		runnable_revision_id TEXT NOT NULL REFERENCES runnable_revisions(id) ON DELETE RESTRICT,
		runnable_revision_digest TEXT NOT NULL REFERENCES runnable_revisions(runnable_revision_digest) ON DELETE RESTRICT,
		verification_report_id TEXT NOT NULL REFERENCES runnable_verification_reports(id) ON DELETE RESTRICT,
		verification_report_digest TEXT NOT NULL REFERENCES runnable_verification_reports(verification_report_digest) ON DELETE RESTRICT,
		state TEXT NOT NULL CHECK (state IN ('active', 'superseded')),
		published_at TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		UNIQUE(scenario_id, content_revision, materialized_revision)
	)`,
	`CREATE INDEX scenario_revisions_scenario ON scenario_revisions(scenario_id, published_at DESC)`,
	`CREATE UNIQUE INDEX scenario_revisions_one_active ON scenario_revisions(scenario_id) WHERE state = 'active'`,
	`ALTER TABLE scenario_revisions ADD CONSTRAINT scenario_revisions_id_scenario_key UNIQUE (id, scenario_id)`,
	`ALTER TABLE scenarios ADD CONSTRAINT scenarios_active_revision_fk FOREIGN KEY (active_revision_id, id)
		REFERENCES scenario_revisions (id, scenario_id) DEFERRABLE INITIALLY DEFERRED`,
	`CREATE TABLE generation_workflows (
		id TEXT PRIMARY KEY,
		 source_kind TEXT NOT NULL CHECK (source_kind IN ('authoring')),
		source_ref TEXT NOT NULL,
		source_revision TEXT NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('Generating', 'Judging', 'MaterializingArtifact', 'Verifying', 'NeedsAuthorReview', 'Publishing', 'Published', 'Failed', 'Cancelled')),
		candidate_revision_id TEXT REFERENCES candidate_revisions(id) ON DELETE RESTRICT,
		workspace_snapshot_digest TEXT NOT NULL DEFAULT '',
		active_agent_run_id TEXT REFERENCES agent_runs(id) ON DELETE RESTRICT,
		state_version BIGINT NOT NULL DEFAULT 1 CHECK (state_version >= 1),
		agent_lease_owner TEXT NOT NULL DEFAULT '',
		agent_lease_expires_at TIMESTAMPTZ,
		next_run_at TIMESTAMPTZ NOT NULL,
		last_error TEXT NOT NULL DEFAULT '',
		finalizer_error_category TEXT NOT NULL DEFAULT '' CHECK (finalizer_error_category IN ('', 'deterministic', 'transient')),
		finalizer_last_error TEXT NOT NULL DEFAULT '',
		finalizer_last_attempted_at TIMESTAMPTZ,
		finalizer_next_retry_at TIMESTAMPTZ,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		CHECK (
			(finalizer_error_category = '' AND finalizer_last_error = '' AND finalizer_last_attempted_at IS NULL AND finalizer_next_retry_at IS NULL) OR
			(finalizer_error_category = 'deterministic' AND finalizer_last_error <> '' AND finalizer_last_attempted_at IS NOT NULL AND finalizer_next_retry_at IS NULL) OR
			(finalizer_error_category = 'transient' AND finalizer_last_error <> '' AND finalizer_last_attempted_at IS NOT NULL AND finalizer_next_retry_at IS NOT NULL)
		),
		CHECK ((agent_lease_owner = '') = (agent_lease_expires_at IS NULL))
	)`,
	`CREATE UNIQUE INDEX generation_workflows_source_revision ON generation_workflows(source_kind, source_ref, source_revision)`,
	`CREATE INDEX generation_workflows_recovery ON generation_workflows(state, next_run_at, created_at, id)`,
	`CREATE TABLE generation_action_receipts (
		session_id TEXT NOT NULL REFERENCES authoring_sessions(id) ON DELETE RESTRICT,
		action TEXT NOT NULL CHECK (action IN ('confirm-generation', 'submit-candidate', 'confirm-content', 'request-content-changes', 'cancel-generation')),
		idempotency_key TEXT NOT NULL,
		request_digest TEXT NOT NULL,
		workflow_id TEXT NOT NULL REFERENCES generation_workflows(id) ON DELETE RESTRICT,
		plan_revision BIGINT,
		candidate_revision_id TEXT REFERENCES candidate_revisions(id) ON DELETE RESTRICT,
		proposal_revision INTEGER,
		created_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (session_id, action, idempotency_key),
		CHECK (
			(action = 'confirm-generation' AND plan_revision IS NOT NULL AND candidate_revision_id IS NULL AND proposal_revision IS NULL) OR
			(action IN ('submit-candidate', 'confirm-content', 'request-content-changes') AND plan_revision IS NULL AND candidate_revision_id IS NOT NULL AND proposal_revision IS NULL) OR
			(action = 'cancel-generation' AND plan_revision IS NULL AND candidate_revision_id IS NULL AND proposal_revision IS NULL)
		)
	)`,
	`CREATE TABLE generation_plan_receipts (
		user_id TEXT NOT NULL,
		idempotency_key TEXT NOT NULL,
		session_id TEXT NOT NULL REFERENCES authoring_sessions(id) ON DELETE RESTRICT,
		expected_revision BIGINT NOT NULL CHECK (expected_revision >= 0),
		plan_revision BIGINT NOT NULL CHECK (plan_revision >= 1),
		plan_sha256 TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (user_id, idempotency_key)
	)`,
	`CREATE TABLE generator_workspaces (
		workspace_id TEXT PRIMARY KEY,
		workflow_id TEXT NOT NULL REFERENCES generation_workflows(id) ON DELETE RESTRICT,
		namespace TEXT NOT NULL,
		pvc_name TEXT NOT NULL UNIQUE,
		sandbox_id TEXT NOT NULL DEFAULT '',
		active_turn_id TEXT NOT NULL DEFAULT '',
		idle_since TIMESTAMPTZ,
		state TEXT NOT NULL CHECK (state IN ('pending', 'active', 'deleting', 'deleted')),
		provision_deadline TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		deleted_at TIMESTAMPTZ
	)`,
	`CREATE UNIQUE INDEX generator_workspaces_current ON generator_workspaces(workflow_id) WHERE state IN ('pending', 'active')`,
	`CREATE INDEX generator_workspaces_pending ON generator_workspaces(state, provision_deadline) WHERE state = 'pending'`,
	`CREATE INDEX generator_workspaces_deleting ON generator_workspaces(updated_at) WHERE state = 'deleting'`,
}
