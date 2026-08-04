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
		parent_candidate_revision_id TEXT REFERENCES candidate_revisions(id) ON DELETE RESTRICT,
		repair_reason TEXT NOT NULL DEFAULT '',
		archive_path TEXT NOT NULL,
		archive_sha256 TEXT NOT NULL,
		execution_snapshot JSONB NOT NULL,
		build_output JSONB,
		artifact_reference JSONB,
		verification_report JSONB,
		verify_environment JSONB,
		failure JSONB,
		classification_proposal JSONB,
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
		state TEXT NOT NULL CHECK (state IN ('Generating', 'Judging', 'Building', 'ArtifactPublishing', 'Verifying', 'NeedsAuthorReview', 'Classifying', 'NeedsClassificationReview', 'ChallengePublishing', 'Published', 'Failed', 'Cancelled', 'Superseded')),
		classification_roadmap_revision TEXT NOT NULL DEFAULT '',
		superseded_by_workflow_id TEXT REFERENCES generation_workflows(id) ON DELETE RESTRICT,
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
		CHECK ((state IN ('NeedsAuthorReview', 'NeedsClassificationReview')) = (deadline_paused_at IS NOT NULL)),
		CHECK ((lease_owner = '') = (lease_expires_at IS NULL))
	)`,
	`CREATE UNIQUE INDEX generation_workflows_active_source ON generation_workflows(source_kind, source_ref) WHERE state NOT IN ('Published', 'Failed', 'Cancelled', 'Superseded')`,
	`CREATE INDEX generation_workflows_claim ON generation_workflows(state, next_run_at, lease_expires_at, created_at, id)`,
	`CREATE INDEX generation_workflows_deadline ON generation_workflows(deadline_at) WHERE deadline_at IS NOT NULL`,
	`CREATE TABLE generation_confirmation_receipts (
		session_id TEXT NOT NULL REFERENCES authoring_sessions(id) ON DELETE RESTRICT,
		action TEXT NOT NULL CHECK (action IN ('start', 'content', 'publication')),
		idempotency_key TEXT NOT NULL,
		workflow_id TEXT NOT NULL REFERENCES generation_workflows(id) ON DELETE RESTRICT,
		plan_revision BIGINT,
		candidate_revision_id TEXT REFERENCES candidate_revisions(id) ON DELETE RESTRICT,
		proposal_revision INTEGER,
		created_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (session_id, action, idempotency_key),
		CHECK (
			(action = 'start' AND plan_revision IS NOT NULL AND candidate_revision_id IS NULL AND proposal_revision IS NULL) OR
			(action = 'content' AND plan_revision IS NULL AND candidate_revision_id IS NOT NULL AND proposal_revision IS NULL) OR
			(action = 'publication' AND plan_revision IS NULL AND candidate_revision_id IS NOT NULL AND proposal_revision IS NOT NULL)
		)
	)`,
	`CREATE TABLE generation_resource_reaps (
		candidate_revision_id TEXT NOT NULL REFERENCES candidate_revisions(id) ON DELETE RESTRICT,
		kind TEXT NOT NULL CHECK (kind IN ('verification-environment', 'build-archive', 'node-build-image', 'candidate-artifact')),
		delete_final_artifact BOOLEAN NOT NULL DEFAULT FALSE,
		state TEXT NOT NULL CHECK (state IN ('pending', 'running', 'completed')),
		attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
		lease_owner TEXT NOT NULL DEFAULT '',
		lease_expires_at TIMESTAMPTZ,
		next_run_at TIMESTAMPTZ NOT NULL,
		last_error TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		completed_at TIMESTAMPTZ,
		PRIMARY KEY (candidate_revision_id, kind),
		CHECK ((lease_owner = '') = (lease_expires_at IS NULL))
	)`,
	`CREATE INDEX generation_resource_reaps_claim ON generation_resource_reaps(kind, state, next_run_at, lease_expires_at, created_at, candidate_revision_id)`,
}
