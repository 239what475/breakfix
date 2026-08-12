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
		published_at TIMESTAMPTZ
	)`,
	`CREATE INDEX candidate_revisions_source ON candidate_revisions(source_kind, source_ref, source_revision, created_at)`,
	`CREATE TABLE challenges (
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
	`CREATE TABLE challenge_revisions (
		id TEXT PRIMARY KEY,
		challenge_id TEXT NOT NULL REFERENCES challenges(id) ON DELETE RESTRICT,
		source_kind TEXT NOT NULL CHECK (source_kind IN ('authoring', 'release')),
		source_ref TEXT NOT NULL,
		source_revision_id TEXT NOT NULL,
		base_active_revision_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL,
		runtime TEXT NOT NULL CHECK (runtime IN ('node', 'k8s')),
		content_revision TEXT NOT NULL,
		source_slug TEXT NOT NULL,
		materialized_path TEXT NOT NULL UNIQUE,
		materialized_revision TEXT NOT NULL,
		artifact_reference JSONB NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('active', 'superseded')),
		published_at TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		UNIQUE(challenge_id, content_revision, materialized_revision)
	)`,
	`CREATE INDEX challenge_revisions_challenge ON challenge_revisions(challenge_id, published_at DESC)`,
	`CREATE UNIQUE INDEX challenge_revisions_one_active ON challenge_revisions(challenge_id) WHERE state = 'active'`,
	`ALTER TABLE challenge_revisions ADD CONSTRAINT challenge_revisions_id_challenge_key UNIQUE (id, challenge_id)`,
	`ALTER TABLE challenges ADD CONSTRAINT challenges_active_revision_fk FOREIGN KEY (active_revision_id, id)
		REFERENCES challenge_revisions (id, challenge_id) DEFERRABLE INITIALLY DEFERRED`,
	`CREATE TABLE generation_workflows (
		id TEXT PRIMARY KEY,
		 source_kind TEXT NOT NULL CHECK (source_kind IN ('authoring')),
		source_ref TEXT NOT NULL,
		source_revision TEXT NOT NULL,
		 state TEXT NOT NULL CHECK (state IN ('Generating', 'Judging', 'Building', 'ArtifactPublishing', 'Verifying', 'NeedsAuthorReview', 'Classifying', 'NeedsClassificationReview', 'ChallengePublishing', 'Published', 'Failed', 'Cancelled')),
		classification_roadmap_revision TEXT NOT NULL DEFAULT '',
		classification_feedback TEXT NOT NULL DEFAULT '',
		candidate_revision_id TEXT REFERENCES candidate_revisions(id) ON DELETE RESTRICT,
		active_agent_run_id TEXT REFERENCES agent_runs(id) ON DELETE RESTRICT,
		state_version BIGINT NOT NULL DEFAULT 1 CHECK (state_version >= 1),
		runtime_attempt INTEGER NOT NULL DEFAULT 0 CHECK (runtime_attempt >= 0 AND runtime_attempt <= 5),
		lease_owner TEXT NOT NULL DEFAULT '',
		lease_expires_at TIMESTAMPTZ,
		next_run_at TIMESTAMPTZ NOT NULL,
		last_error TEXT NOT NULL DEFAULT '',
		finalizer_error_category TEXT NOT NULL DEFAULT '' CHECK (finalizer_error_category IN ('', 'deterministic', 'transient')),
		finalizer_last_error TEXT NOT NULL DEFAULT '',
		finalizer_last_attempted_at TIMESTAMPTZ,
		finalizer_next_retry_at TIMESTAMPTZ,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		CHECK (
			(state IN ('Building', 'ArtifactPublishing', 'Verifying', 'ChallengePublishing') AND runtime_attempt BETWEEN 1 AND 5) OR
			(state NOT IN ('Building', 'ArtifactPublishing', 'Verifying', 'ChallengePublishing') AND runtime_attempt = 0)
		),
		CHECK ((lease_owner = '') = (lease_expires_at IS NULL)),
		CHECK (
			(finalizer_error_category = '' AND finalizer_last_error = '' AND finalizer_last_attempted_at IS NULL AND finalizer_next_retry_at IS NULL) OR
			(finalizer_error_category = 'deterministic' AND finalizer_last_error <> '' AND finalizer_last_attempted_at IS NOT NULL AND finalizer_next_retry_at IS NULL) OR
			(finalizer_error_category = 'transient' AND finalizer_last_error <> '' AND finalizer_last_attempted_at IS NOT NULL AND finalizer_next_retry_at IS NOT NULL)
		)
	)`,
	`CREATE UNIQUE INDEX generation_workflows_source_revision ON generation_workflows(source_kind, source_ref, source_revision)`,
	`CREATE INDEX generation_workflows_claim ON generation_workflows(state, next_run_at, lease_expires_at, created_at, id)`,
	`CREATE TABLE generation_action_receipts (
		session_id TEXT NOT NULL REFERENCES authoring_sessions(id) ON DELETE RESTRICT,
		action TEXT NOT NULL CHECK (action IN ('confirm-generation', 'submit-candidate', 'confirm-content', 'request-classification-changes', 'confirm-classification-and-publish', 'request-content-changes', 'cancel-generation')),
		idempotency_key TEXT NOT NULL,
		workflow_id TEXT NOT NULL REFERENCES generation_workflows(id) ON DELETE RESTRICT,
		plan_revision BIGINT,
		candidate_revision_id TEXT REFERENCES candidate_revisions(id) ON DELETE RESTRICT,
		proposal_revision INTEGER,
		created_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (session_id, action, idempotency_key),
		CHECK (
			(action = 'confirm-generation' AND plan_revision IS NOT NULL AND candidate_revision_id IS NULL AND proposal_revision IS NULL) OR
			(action IN ('submit-candidate', 'confirm-content', 'request-content-changes') AND plan_revision IS NULL AND candidate_revision_id IS NOT NULL AND proposal_revision IS NULL) OR
			(action = 'cancel-generation' AND plan_revision IS NULL AND candidate_revision_id IS NULL AND proposal_revision IS NULL) OR
			(action IN ('request-classification-changes', 'confirm-classification-and-publish') AND plan_revision IS NULL AND candidate_revision_id IS NOT NULL AND proposal_revision IS NOT NULL)
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
	`CREATE TABLE generator_workspaces (
		workspace_id TEXT PRIMARY KEY,
		workflow_id TEXT NOT NULL REFERENCES generation_workflows(id) ON DELETE RESTRICT,
		namespace TEXT NOT NULL,
		pvc_name TEXT NOT NULL UNIQUE,
		sandbox_id TEXT NOT NULL DEFAULT '',
		active_turn_id TEXT NOT NULL DEFAULT '',
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
