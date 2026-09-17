package postgres

// Human action audits are append-only by design: no update or delete path
// exists for this table anywhere in production code. The ledger answers
// "who performed an administrative verb" and is never rewritten.
var schemaAuditStatements = []string{
	`CREATE TABLE human_action_audits (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		action TEXT NOT NULL,
		target_type TEXT NOT NULL,
		target_id TEXT NOT NULL,
		detail JSONB NOT NULL,
		created_at TIMESTAMPTZ NOT NULL
	)`,
	`CREATE INDEX human_action_audits_time ON human_action_audits (created_at DESC, id)`,
	`CREATE INDEX human_action_audits_action ON human_action_audits (action, created_at DESC)`,
}
