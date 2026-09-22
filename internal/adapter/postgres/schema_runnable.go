package postgres

var schemaRunnableStatements = []string{
	`CREATE TABLE runnable_sources (
		source_digest TEXT PRIMARY KEY,
		archive BYTEA NOT NULL,
		created_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE TABLE runnable_specs (
		spec_digest TEXT PRIMARY KEY,
		content_kind TEXT NOT NULL,
		content_id TEXT NOT NULL,
		content_revision TEXT NOT NULL,
		source_digest TEXT NOT NULL,
		spec JSONB NOT NULL,
		created_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX runnable_specs_content ON runnable_specs (content_kind, content_id, content_revision, created_at DESC)`,
	`CREATE TABLE runnable_revisions (
		id TEXT PRIMARY KEY,
		runnable_revision_digest TEXT NOT NULL UNIQUE,
		spec_digest TEXT NOT NULL REFERENCES runnable_specs(spec_digest) ON DELETE RESTRICT,
		revision JSONB NOT NULL,
		created_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX runnable_revisions_spec ON runnable_revisions (spec_digest, created_at DESC)`,
	`CREATE TABLE runnable_verification_reports (
		id TEXT PRIMARY KEY,
		verification_report_digest TEXT NOT NULL UNIQUE,
		runnable_revision_digest TEXT NOT NULL REFERENCES runnable_revisions(runnable_revision_digest) ON DELETE RESTRICT,
		report JSONB NOT NULL,
		created_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX runnable_verification_reports_revision ON runnable_verification_reports (runnable_revision_digest, created_at DESC)`,
	`CREATE TABLE runnable_execution_outputs (
		output_digest TEXT PRIMARY KEY,
		capture BYTEA NOT NULL,
		created_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE TABLE runnable_actions (
		action_key TEXT PRIMARY KEY,
		content_kind TEXT NOT NULL,
		content_id TEXT NOT NULL,
		content_revision TEXT NOT NULL,
		spec_digest TEXT NOT NULL REFERENCES runnable_specs(spec_digest) ON DELETE RESTRICT,
		phase TEXT NOT NULL CHECK (phase IN ('materialize-artifact', 'verify')),
		state_version BIGINT NOT NULL CHECK (state_version >= 1),
		runnable_revision_digest TEXT REFERENCES runnable_revisions(runnable_revision_digest) ON DELETE RESTRICT,
		state TEXT NOT NULL CHECK (state IN ('queued', 'running', 'completed', 'failed')),
		attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0 AND attempt <= 5),
		lease_owner TEXT NOT NULL DEFAULT '',
		lease_expires_at TIMESTAMPTZ,
		next_run_at TIMESTAMPTZ NOT NULL,
		failure_class TEXT NOT NULL DEFAULT '' CHECK (failure_class IN ('', 'artifact', 'infrastructure')),
		failure_code TEXT NOT NULL DEFAULT '',
		failure_summary TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		completed_at TIMESTAMPTZ,
		UNIQUE(content_kind, content_id, content_revision, spec_digest, phase, state_version),
		CHECK (
			(phase = 'materialize-artifact' AND (
				(state IN ('queued', 'running', 'failed') AND runnable_revision_digest IS NULL) OR
				(state = 'completed' AND runnable_revision_digest IS NOT NULL)
			)) OR
			(phase = 'verify' AND runnable_revision_digest IS NOT NULL)
		),
		CHECK ((lease_owner = '') = (lease_expires_at IS NULL))
	)`,
	`CREATE INDEX runnable_actions_claim ON runnable_actions (state, next_run_at, lease_expires_at, created_at, action_key)`,
	`CREATE TABLE runnable_reaps (
		reap_key TEXT PRIMARY KEY,
		-- The digest fences one concrete resource set: a runnable revision
		-- digest for content environments, the blank plan digest for blank
		-- ones. No foreign key: blank digests have no runnable_revisions row.
		runnable_revision_digest TEXT NOT NULL,
		reap_request JSONB NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('queued', 'claimed', 'succeeded', 'dead')),
		attempt BIGINT NOT NULL DEFAULT 0 CHECK (attempt >= 0),
		lease_owner TEXT NOT NULL DEFAULT '',
		lease_expires_at TIMESTAMPTZ,
		next_attempt_at TIMESTAMPTZ NOT NULL,
		last_error TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		completed_at TIMESTAMPTZ,
		CHECK ((lease_owner = '') = (lease_expires_at IS NULL))
	)`,
	`CREATE INDEX runnable_reaps_claim ON runnable_reaps (state, next_attempt_at, lease_expires_at, created_at, reap_key)`,
}
