package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func newTestDB(t *testing.T) *Store {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("BREAKFIX_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("BREAKFIX_TEST_DATABASE_URL is not set")
	}
	admin, err := sql.Open("pgx", baseURL)
	if err != nil {
		t.Fatalf("open postgres admin connection: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx := context.Background()
	if err := admin.PingContext(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}

	schema := fmt.Sprintf("breakfix_test_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	testURL, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse postgres url: %v", err)
	}
	query := testURL.Query()
	query.Set("search_path", schema)
	testURL.RawQuery = query.Encode()
	database, err := New(testURL.String())
	if err != nil {
		t.Fatalf("open migrated breakfix database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}
