package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Store owns the shared PostgreSQL connection and exposes only domain-scoped
// repositories. It deliberately has no SQLite mode: development starts from
// the same schema contract as a deployed Server.
type Store struct {
	conn *Conn

	Agent       *AgentRepository
	Authoring   *AuthoringRepository
	Catalog     *CatalogRepository
	Environment *EnvironmentRepository
	Generation  *GenerationRepository
	Identity    *IdentityRepository
	Roadmap     *RoadmapRepository
	Reporting   *ReportingRepository
}

// New opens a PostgreSQL DSN and applies the current schema. Existing SQLite
// files are intentionally not accepted or migrated.
func New(dsn string) (*Store, error) {
	return open(dsn, true)
}

func open(dsn string, migrate bool) (*Store, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("postgres dsn is required")
	}

	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	raw.SetMaxOpenConns(20)
	raw.SetMaxIdleConns(5)

	ctx := context.Background()
	if err := raw.PingContext(ctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	store := newStore(&Conn{raw: raw})
	if migrate {
		if err := store.migrate(ctx); err != nil {
			_ = raw.Close()
			return nil, fmt.Errorf("migrate postgres: %w", err)
		}
	}
	return store, nil
}

func newStore(conn *Conn) *Store {
	return &Store{
		conn:        conn,
		Agent:       &AgentRepository{conn: conn},
		Authoring:   &AuthoringRepository{conn: conn},
		Catalog:     &CatalogRepository{conn: conn},
		Environment: &EnvironmentRepository{conn: conn},
		Generation:  &GenerationRepository{conn: conn},
		Identity:    &IdentityRepository{conn: conn},
		Roadmap:     &RoadmapRepository{conn: conn},
		Reporting:   &ReportingRepository{conn: conn},
	}
}

func (s *Store) Close() error { return s.conn.Close() }

// Conn keeps PostgreSQL's placeholder syntax out of the domain repository
// code. It is not a SQL dialect abstraction: this package exclusively opens
// pgx/PostgreSQL, and all schema and SQL semantics are PostgreSQL-native.
type Conn struct {
	raw *sql.DB
}

func (c *Conn) Close() error { return c.raw.Close() }

func (c *Conn) Exec(query string, args ...any) (sql.Result, error) {
	return c.raw.Exec(bind(query), args...)
}

func (c *Conn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.raw.ExecContext(ctx, bind(query), args...)
}

func (c *Conn) Query(query string, args ...any) (*sql.Rows, error) {
	return c.raw.Query(bind(query), args...)
}

func (c *Conn) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return c.raw.QueryContext(ctx, bind(query), args...)
}

func (c *Conn) QueryRow(query string, args ...any) *sql.Row {
	return c.raw.QueryRow(bind(query), args...)
}

func (c *Conn) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return c.raw.QueryRowContext(ctx, bind(query), args...)
}

func (c *Conn) BeginTx(ctx context.Context, options *sql.TxOptions) (*Tx, error) {
	tx, err := c.raw.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &Tx{raw: tx}, nil
}

// Tx mirrors the small subset of database/sql.Tx used by the repositories and
// applies the same PostgreSQL parameter binding as Conn.
type Tx struct {
	raw *sql.Tx
}

func (t *Tx) Commit() error   { return t.raw.Commit() }
func (t *Tx) Rollback() error { return t.raw.Rollback() }

func (t *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.raw.ExecContext(ctx, bind(query), args...)
}

func (t *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.raw.QueryRowContext(ctx, bind(query), args...)
}

func (t *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.raw.QueryContext(ctx, bind(query), args...)
}

// bind rewrites positional parameters outside SQL literals and comments. The
// repository queries use '?' so their argument order stays readable while pgx
// receives PostgreSQL's required $1, $2, ... placeholders.
func bind(query string) string {
	var out strings.Builder
	out.Grow(len(query) + 16)
	argument := 0
	inSingleQuote := false
	inDoubleQuote := false
	inLineComment := false
	inBlockComment := false

	for i := 0; i < len(query); {
		if inLineComment {
			if query[i] == '\n' {
				inLineComment = false
			}
			out.WriteByte(query[i])
			i++
			continue
		}
		if inBlockComment {
			if i+1 < len(query) && query[i] == '*' && query[i+1] == '/' {
				out.WriteString("*/")
				i += 2
				inBlockComment = false
				continue
			}
			out.WriteByte(query[i])
			i++
			continue
		}
		if !inSingleQuote && !inDoubleQuote && i+1 < len(query) {
			if query[i] == '-' && query[i+1] == '-' {
				out.WriteString("--")
				i += 2
				inLineComment = true
				continue
			}
			if query[i] == '/' && query[i+1] == '*' {
				out.WriteString("/*")
				i += 2
				inBlockComment = true
				continue
			}
		}

		switch query[i] {
		case '\'':
			out.WriteByte(query[i])
			i++
			if inSingleQuote && i < len(query) && query[i] == '\'' {
				out.WriteByte(query[i])
				i++
				continue
			}
			if !inDoubleQuote {
				inSingleQuote = !inSingleQuote
			}
		case '"':
			out.WriteByte(query[i])
			i++
			if inDoubleQuote && i < len(query) && query[i] == '"' {
				out.WriteByte(query[i])
				i++
				continue
			}
			if !inSingleQuote {
				inDoubleQuote = !inDoubleQuote
			}
		case '?':
			if inSingleQuote || inDoubleQuote {
				out.WriteByte(query[i])
				i++
				continue
			}
			argument++
			out.WriteByte('$')
			_, _ = fmt.Fprintf(&out, "%d", argument)
			i++
		default:
			r, size := utf8.DecodeRuneInString(query[i:])
			out.WriteRune(r)
			i += size
		}
	}
	return out.String()
}

func (d *Store) migrate(ctx context.Context) error {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(487550139917)`); err != nil {
		return fmt.Errorf("lock database schema initialization: %w", err)
	}

	var initialized bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1
		FROM pg_catalog.pg_class relation
		JOIN pg_catalog.pg_namespace namespace ON namespace.oid = relation.relnamespace
		WHERE namespace.nspname = current_schema()
			AND relation.relname = 'breakfix_schema'
			AND relation.relkind = 'r'
	)`).Scan(&initialized); err != nil {
		return fmt.Errorf("inspect database schema marker: %w", err)
	}
	if initialized {
		var count, version int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MIN(version), 0) FROM breakfix_schema`).Scan(&count, &version); err != nil {
			return fmt.Errorf("read database schema marker: %w", err)
		}
		if count != 1 || version != currentSchemaVersion {
			return fmt.Errorf("database schema marker is invalid; recreate the development database for schema version %d", currentSchemaVersion)
		}
		return tx.Commit()
	}

	var hasExistingTables bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_catalog.pg_tables WHERE schemaname = current_schema()
	)`).Scan(&hasExistingTables); err != nil {
		return fmt.Errorf("inspect existing database tables: %w", err)
	}
	if hasExistingTables {
		return fmt.Errorf("database contains an incompatible pre-baseline schema; recreate the development database for schema version %d", currentSchemaVersion)
	}

	if _, err := tx.ExecContext(ctx, `CREATE TABLE breakfix_schema (
		version INTEGER PRIMARY KEY CHECK (version = `+fmt.Sprintf("%d", currentSchemaVersion)+`)
	)`); err != nil {
		return fmt.Errorf("create database schema marker: %w", err)
	}
	for index, statement := range currentSchemaStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize schema statement %d: %w", index+1, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO breakfix_schema (version) VALUES (?)`, currentSchemaVersion); err != nil {
		return fmt.Errorf("record database schema version: %w", err)
	}
	return tx.Commit()
}
