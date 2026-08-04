package postgres

var schemaRoadmapStatements = []string{
	`CREATE TABLE roadmap_revisions (
		id TEXT PRIMARY KEY,
		content_json JSONB NOT NULL,
		created_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE TABLE roadmap_current (
		singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
		revision_id TEXT REFERENCES roadmap_revisions(id) ON DELETE RESTRICT,
		updated_at TIMESTAMPTZ NOT NULL
	)`,
	`INSERT INTO roadmap_current (singleton, revision_id, updated_at)
		VALUES (TRUE, NULL, '1970-01-01T00:00:00Z'::timestamptz)`,
}
