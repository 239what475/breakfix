package postgres

var schemaRunnableStatements = []string{
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
	`CREATE TABLE runnable_reaps (
		reap_key TEXT PRIMARY KEY,
		runnable_revision_digest TEXT NOT NULL REFERENCES runnable_revisions(runnable_revision_digest) ON DELETE RESTRICT,
		reap_request JSONB NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('queued', 'claimed', 'succeeded')),
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
