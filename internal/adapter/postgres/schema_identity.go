package postgres

var schemaIdentityStatements = []string{
	`CREATE TABLE users (
		id TEXT PRIMARY KEY,
		subject TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		password_hash TEXT NOT NULL DEFAULT '',
		totp_secret TEXT NOT NULL DEFAULT '',
		role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('user','admin')),
		created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
}
