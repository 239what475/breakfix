package postgres

var schemaCatalogStatements = []string{
	`CREATE TABLE catalog_releases (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		version TEXT NOT NULL,
		bundle_digest TEXT NOT NULL UNIQUE,
		source_digest TEXT NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('Pending', 'Installing', 'Committing', 'Ready', 'Failed')),
		source_attempt INTEGER NOT NULL CHECK (source_attempt >= 0),
		next_run_at TIMESTAMPTZ NOT NULL,
		commit_id TEXT NOT NULL DEFAULT '',
		last_error TEXT NOT NULL DEFAULT '',
		finalizer_error_category TEXT NOT NULL DEFAULT '' CHECK (finalizer_error_category IN ('', 'deterministic', 'transient')),
		finalizer_last_error TEXT NOT NULL DEFAULT '',
		finalizer_last_attempted_at TIMESTAMPTZ,
		finalizer_next_retry_at TIMESTAMPTZ,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		CHECK ((state NOT IN ('Committing', 'Ready')) OR commit_id <> ''),
		CHECK (
			(finalizer_error_category = '' AND finalizer_last_error = '' AND finalizer_last_attempted_at IS NULL AND finalizer_next_retry_at IS NULL) OR
			(finalizer_error_category = 'deterministic' AND finalizer_last_error <> '' AND finalizer_last_attempted_at IS NOT NULL AND finalizer_next_retry_at IS NULL) OR
			(finalizer_error_category = 'transient' AND finalizer_last_error <> '' AND finalizer_last_attempted_at IS NOT NULL AND finalizer_next_retry_at IS NOT NULL)
		)
	)`,
	`CREATE INDEX catalog_releases_recovery ON catalog_releases(state, next_run_at, created_at, id)`,
	`CREATE TABLE catalog_release_entries (
		id TEXT PRIMARY KEY,
		release_id TEXT NOT NULL REFERENCES catalog_releases(id) ON DELETE RESTRICT,
		source_path TEXT NOT NULL,
		source_ref TEXT NOT NULL,
		title TEXT NOT NULL,
		scenario_type TEXT NOT NULL CHECK (scenario_type IN ('documentation-example', 'operations-scenario')),
		tags JSONB NOT NULL DEFAULT '[]'::jsonb,
		content_revision TEXT NOT NULL,
		source_archive JSONB NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('MaterializingArtifact', 'Verifying', 'ReadyToCommit', 'Failed')),
		state_version BIGINT NOT NULL CHECK (state_version >= 1),
		runnable_revision_id TEXT REFERENCES runnable_revisions(id) ON DELETE RESTRICT,
		runnable_revision_digest TEXT REFERENCES runnable_revisions(runnable_revision_digest) ON DELETE RESTRICT,
		verification_report_id TEXT REFERENCES runnable_verification_reports(id) ON DELETE RESTRICT,
		verification_report_digest TEXT REFERENCES runnable_verification_reports(verification_report_digest) ON DELETE RESTRICT,
		last_error TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		UNIQUE(release_id, source_path),
		UNIQUE(release_id, source_ref),
		CHECK ((runnable_revision_id IS NULL) = (runnable_revision_digest IS NULL)),
		CHECK ((verification_report_id IS NULL) = (verification_report_digest IS NULL))
	)`,
	`CREATE INDEX catalog_release_entries_source_ref ON catalog_release_entries(source_ref, content_revision)`,
	`CREATE TABLE catalog_release_entry_commits (
		id TEXT PRIMARY KEY,
		release_id TEXT NOT NULL REFERENCES catalog_releases(id) ON DELETE RESTRICT,
		entry_id TEXT NOT NULL UNIQUE REFERENCES catalog_release_entries(id) ON DELETE RESTRICT,
		scenario_id TEXT NOT NULL UNIQUE,
		scenario_revision_id TEXT NOT NULL UNIQUE,
		source_slug TEXT NOT NULL UNIQUE,
		state TEXT NOT NULL CHECK (state IN ('Prepared', 'Materialized', 'Committed', 'Failed')),
		last_error TEXT NOT NULL DEFAULT '',
		materialized_revision TEXT NOT NULL DEFAULT '',
		materialized_at TIMESTAMPTZ,
		committed_at TIMESTAMPTZ,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX catalog_release_entry_commits_release ON catalog_release_entry_commits(release_id, state, entry_id)`,
}
