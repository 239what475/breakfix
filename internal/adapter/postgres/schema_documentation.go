package postgres

// Documentation links are a small admin-maintained list read by every
// visitor of the aggregation page. The key is a server-generated random id,
// so a rename never breaks ?doc=<key> deep links.
var schemaDocumentationStatements = []string{
	`CREATE TABLE documentation_links (
		key TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		url TEXT NOT NULL,
		embed BOOLEAN NOT NULL DEFAULT TRUE,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX documentation_links_created ON documentation_links (created_at)`,
}
