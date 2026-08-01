package postgres

var schemaTaxonomyStatements = []string{
	`CREATE TABLE taxonomy_workflows (
		id TEXT PRIMARY KEY,
		challenge_id TEXT NOT NULL,
		challenge_content_revision TEXT NOT NULL,
		base_revision TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL CHECK (state IN ('Queued', 'Mapping', 'Reviewing', 'Publishing', 'Completed', 'Failed', 'Cancelled')),
		round INTEGER NOT NULL DEFAULT 0 CHECK (round >= 0),
		state_attempt INTEGER NOT NULL DEFAULT 0 CHECK (state_attempt >= 0),
		candidate_changeset JSONB,
		curriculum_review_json JSONB,
		sre_review_json JSONB,
		expected_snapshot_revision TEXT NOT NULL DEFAULT '',
		published_revision TEXT NOT NULL DEFAULT '',
		lease_owner TEXT NOT NULL DEFAULT '',
		lease_expires_at TIMESTAMPTZ,
		next_run_at TIMESTAMPTZ NOT NULL,
		last_error TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		UNIQUE(challenge_id, challenge_content_revision),
		CHECK ((lease_owner = '') = (lease_expires_at IS NULL))
	)`,
	`CREATE INDEX taxonomy_workflows_claim ON taxonomy_workflows(state, next_run_at, lease_expires_at, created_at, id)`,
}
