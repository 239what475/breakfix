package postgres

var schemaCatalogStatements = []string{
	`CREATE TABLE catalog_releases (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		version TEXT NOT NULL,
		bundle_digest TEXT NOT NULL,
		taxonomy_content_revision TEXT NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('Pending', 'Installing', 'Committing', 'Ready', 'CleaningUp', 'Failed')),
		deadline_at TIMESTAMPTZ,
		last_error TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX catalog_releases_state ON catalog_releases(state, created_at, id)`,
	`CREATE TABLE catalog_release_entries (
		id TEXT PRIMARY KEY,
		release_id TEXT NOT NULL REFERENCES catalog_releases(id) ON DELETE RESTRICT,
		source_path TEXT NOT NULL,
		content_revision TEXT NOT NULL,
		candidate_revision_id TEXT REFERENCES candidate_revisions(id) ON DELETE RESTRICT,
		state TEXT NOT NULL CHECK (state IN ('Pending', 'Building', 'Verifying', 'ReadyToCommit', 'CleaningUp', 'Cleaned', 'Failed')),
		last_error TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		UNIQUE(release_id, source_path)
	)`,
	`CREATE INDEX catalog_release_entries_claim ON catalog_release_entries(release_id, state, created_at, id)`,
	`CREATE TABLE catalog_release_entry_commits (
		entry_id TEXT PRIMARY KEY REFERENCES catalog_release_entries(id) ON DELETE RESTRICT,
		content_revision TEXT NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('Pending', 'Prepared', 'Materialized', 'Committed')),
		challenge_id TEXT,
		slug TEXT,
		materialized_at TIMESTAMPTZ,
		committed_at TIMESTAMPTZ,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		CHECK ((challenge_id IS NULL) = (slug IS NULL)),
		CHECK (
			(state = 'Pending' AND challenge_id IS NULL AND materialized_at IS NULL AND committed_at IS NULL) OR
			(state = 'Prepared' AND challenge_id IS NOT NULL AND materialized_at IS NULL AND committed_at IS NULL) OR
			(state = 'Materialized' AND challenge_id IS NOT NULL AND materialized_at IS NOT NULL AND committed_at IS NULL) OR
			(state = 'Committed' AND challenge_id IS NOT NULL AND materialized_at IS NOT NULL AND committed_at IS NOT NULL)
		)
	)`,
	`CREATE UNIQUE INDEX catalog_release_entry_commits_challenge_id ON catalog_release_entry_commits(challenge_id) WHERE challenge_id IS NOT NULL`,
	`CREATE UNIQUE INDEX catalog_release_entry_commits_slug ON catalog_release_entry_commits(slug) WHERE slug IS NOT NULL`,
}
