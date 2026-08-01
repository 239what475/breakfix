package postgres

import (
	"testing"
)

// TestPostgresSchemaAndBoundParameters is intentionally an integration test.
// It never falls back to SQLite; local and CI callers provide a disposable
// PostgreSQL endpoint through BREAKFIX_TEST_DATABASE_URL.
func TestPostgresSchemaAndBoundParameters(t *testing.T) {
	database := newTestDB(t)
	if _, err := database.conn.Exec(`CREATE TABLE boolean_binding_test (id TEXT PRIMARY KEY, value BOOLEAN NOT NULL)`); err != nil {
		t.Fatalf("create boolean binding test table: %v", err)
	}
	if _, err := database.conn.Exec(`INSERT INTO boolean_binding_test (id, value) VALUES (?, ?)`, "one", false); err != nil {
		t.Fatalf("insert boolean bound parameter: %v", err)
	}

	if _, err := database.Identity.CreateUserWithAuth("user-one", "alice", "hash", "totp"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	user, err := database.Identity.GetUserBySubject("alice")
	if err != nil {
		t.Fatalf("get user by bound parameter: %v", err)
	}
	if user.ID != "user-one" || user.PasswordHash != "hash" || user.TOTPSecret != "totp" {
		t.Fatalf("user = %#v, want persisted credentials", user)
	}

	var runsTable string
	if err := database.conn.QueryRow(`SELECT to_regclass(current_schema() || '.agent_runs')`).Scan(&runsTable); err != nil {
		t.Fatalf("read agent_runs relation: %v", err)
	}
	if runsTable == "" {
		t.Fatal("agent_runs relation does not exist")
	}
}
