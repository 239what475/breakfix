// Command taxonomy-e2e starts one disposable Server and Agent Worker for the
// opt-in taxonomy Playwright suite. It never connects to product data.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/breakfix/breakfix/internal/agentworker"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/server"
	"github.com/breakfix/breakfix/internal/taxonomy"
	"github.com/breakfix/breakfix/internal/worklist"
	"github.com/gin-gonic/gin"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	defaultAgentBaseURL = "https://api.deepseek.com"
	defaultAgentModel   = "deepseek-v4-pro"
)

type options struct {
	databaseURL string
	challenge   string
	frontendDir string
	baseURL     string
	model       string
	timeout     time.Duration
}

type runView struct {
	ID        string `json:"id"`
	Purpose   string `json:"purpose"`
	Status    string `json:"status"`
	Attempt   int    `json:"attempt"`
	LastError string `json:"last_error,omitempty"`
}

type mappingView struct {
	ID                string `json:"id"`
	ChallengeID       string `json:"challenge_id"`
	ChallengeRevision string `json:"challenge_revision"`
	State             string `json:"state"`
	Stage             string `json:"stage,omitempty"`
	ActiveRunID       string `json:"active_run_id,omitempty"`
	Round             int    `json:"round"`
	LastError         string `json:"last_error,omitempty"`
}

type statusView struct {
	InitialSnapshotAbsent bool          `json:"initial_snapshot_absent"`
	CatalogMapped         bool          `json:"catalog_mapped"`
	CurrentRevision       string        `json:"current_revision,omitempty"`
	Mappings              []mappingView `json:"mappings"`
	Runs                  []runView     `json:"runs"`
	SnapshotError         string        `json:"snapshot_error,omitempty"`
}

type statusReporter struct {
	database              *db.DB
	store                 *taxonomy.Store
	challenge             challenge.Entry
	initialSnapshotAbsent bool
}

func main() {
	var value options
	flag.StringVar(&value.databaseURL, "database-url", strings.TrimSpace(os.Getenv("BREAKFIX_TAXONOMY_E2E_DATABASE_URL")), "PostgreSQL DSN used only to create a temporary schema")
	flag.StringVar(&value.challenge, "challenge-dir", "data/challenges/cleanup-logs", "verified cleanup-logs source directory")
	flag.StringVar(&value.frontendDir, "frontend-dir", "cmd/server/frontend/dist", "built frontend directory")
	flag.StringVar(&value.baseURL, "agent-base-url", firstNonEmpty(os.Getenv("BREAKFIX_TAXONOMY_E2E_AGENT_BASE_URL"), os.Getenv("DEEPSEEK_BASE_URL"), defaultAgentBaseURL), "model API base URL")
	flag.StringVar(&value.model, "agent-model", firstNonEmpty(os.Getenv("BREAKFIX_TAXONOMY_E2E_AGENT_MODEL"), os.Getenv("DEEPSEEK_MODEL"), defaultAgentModel), "model name")
	flag.DurationVar(&value.timeout, "timeout", 10*time.Minute, "one total deadline for the taxonomy workflow")
	flag.Parse()

	if err := value.validate(); err != nil {
		slog.Error("taxonomy E2E configuration is invalid", "err", err)
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, value.timeout)
	defer cancel()
	if err := run(ctx, value); err != nil {
		slog.Error("taxonomy E2E failed", "err", err)
		os.Exit(1)
	}
}

func (o options) validate() error {
	if strings.TrimSpace(o.databaseURL) == "" {
		return errors.New("BREAKFIX_TAXONOMY_E2E_DATABASE_URL or -database-url is required")
	}
	if strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")) == "" {
		return errors.New("DEEPSEEK_API_KEY is required")
	}
	if o.timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	for _, path := range []string{o.challenge, o.frontendDir} {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("required path %q: %w", path, err)
		}
	}
	return nil
}

func run(ctx context.Context, options options) error {
	dataDir, err := os.MkdirTemp("", "breakfix-taxonomy-e2e-")
	if err != nil {
		return fmt.Errorf("create temporary data directory: %w", err)
	}
	defer os.RemoveAll(dataDir) //nolint:errcheck

	testDSN, dropSchema, err := isolatedSchema(options.databaseURL)
	if err != nil {
		return err
	}
	defer dropSchema()
	database, err := db.New(testDSN)
	if err != nil {
		return fmt.Errorf("open temporary taxonomy database: %w", err)
	}
	defer database.Close() //nolint:errcheck

	if err := seedChallenge(options.challenge, filepath.Join(dataDir, "challenges", "cleanup-logs")); err != nil {
		return err
	}
	entry, err := challenge.Get(filepath.Join(dataDir, "challenges"), "chal-r7m4x2q9v6kp")
	if err != nil {
		return fmt.Errorf("load isolated cleanup-logs challenge: %w", err)
	}
	store := taxonomy.NewStore(dataDir)
	if _, err := store.LoadCurrent(); !errors.Is(err, taxonomy.ErrNoCurrentRevision) {
		return fmt.Errorf("isolated data unexpectedly has taxonomy snapshot: %w", err)
	}

	frontend, err := frontendFS(options.frontendDir)
	if err != nil {
		return err
	}
	//nolint:gosec // Disposable local E2E identities, never production credentials.
	cfg := config.Config{
		DataDir:         dataDir,
		JWTSecret:       "taxonomy-e2e-jwt-secret",
		InternalWorkers: config.InternalWorkerKeys{Agent: "taxonomy-e2e-agent-key"},
		CooldownMinutes: 5,
		Agent: config.AgentConfig{
			BaseURL:        options.baseURL,
			APIKeyEnv:      "DEEPSEEK_API_KEY",
			APIKey:         os.Getenv("DEEPSEEK_API_KEY"),
			Model:          options.model,
			RequestTimeout: "90s",
		},
	}
	router, err := server.SetupRouter(ctx, database, nil, cfg, frontend, server.Dependencies{})
	if err != nil {
		return fmt.Errorf("setup taxonomy e2e server: %w", err)
	}
	reporter := statusReporter{database: database, store: store, challenge: *entry, initialSnapshotAbsent: true}
	router.GET("/__taxonomy-e2e/status", func(c *gin.Context) { c.JSON(http.StatusOK, reporter.snapshot(c.Request.Context())) })
	httpServer := httptest.NewServer(router)
	defer httpServer.Close()

	cfg.Worker.ServerURL = httpServer.URL
	internalClient, err := taxonomy.NewInternalClient(cfg.Worker.ServerURL, cfg.InternalWorkers.Agent)
	if err != nil {
		return err
	}
	executor, err := taxonomy.NewWorkerExecutor(cfg.Agent, internalClient)
	if err != nil {
		return err
	}
	worker, err := agentworker.New(database, map[string]agentworker.Executor{
		taxonomy.RuntimePurposeMapper: executor,
		taxonomy.RuntimePurposeReview: executor,
	}, agentworker.NopDeltaSink{}, agentworker.Config{WorkerID: "taxonomy-e2e-worker", PollEvery: 100 * time.Millisecond})
	if err != nil {
		return err
	}
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(ctx) }()

	fmt.Printf("TAXONOMY_E2E_URL=%s\n", httpServer.URL)
	select {
	case err := <-workerDone:
		if err != nil {
			return fmt.Errorf("taxonomy E2E agent worker stopped: %w", err)
		}
		return errors.New("taxonomy E2E agent worker stopped before the suite completed")
	case <-ctx.Done():
		<-workerDone
		_, _ = fmt.Fprintln(os.Stderr, "TAXONOMY_E2E_DIAGNOSTICS="+marshalStatus(reporter.snapshot(context.Background())))
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("taxonomy E2E total deadline exceeded after %s", options.timeout)
		}
		return nil
	}
}

func seedChallenge(source, destination string) error {
	if err := challenge.CopyRegularFiles(source, destination); err != nil {
		return fmt.Errorf("copy verified cleanup-logs artifact: %w", err)
	}
	return nil
}

func frontendFS(dir string) (fs.FS, error) {
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		return nil, fmt.Errorf("read built frontend index: %w", err)
	}
	return os.DirFS(dir), nil
}

func (s statusReporter) snapshot(ctx context.Context) statusView {
	status := statusView{InitialSnapshotAbsent: s.initialSnapshotAbsent, Mappings: make([]mappingView, 0), Runs: make([]runView, 0)}
	items, err := s.database.ListTaxonomyMappings(ctx)
	if err != nil {
		status.SnapshotError = err.Error()
		return status
	}
	for _, item := range items {
		status.Mappings = append(status.Mappings, mappingView{
			ID: item.ID, ChallengeID: item.ChallengeID, ChallengeRevision: item.ChallengeRevision,
			State: string(item.State), Stage: string(item.ActiveStage), ActiveRunID: item.ActiveRunID,
			Round: item.Round, LastError: item.LastError,
		})
		runs, err := s.database.ListRunsForOwner(ctx, "taxonomy-mapping", item.ID)
		if err != nil {
			status.SnapshotError = err.Error()
			return status
		}
		for _, run := range runs {
			attempt := 0
			if work, workErr := s.database.GetWorkItemForSubject(ctx, worklist.KindAgent, worklist.SubjectAgentRun, run.ID); workErr == nil {
				attempt = work.Attempt
			}
			status.Runs = append(status.Runs, runView{ID: run.ID, Purpose: run.Purpose, Status: string(run.Status), Attempt: attempt, LastError: run.LastError})
		}
	}
	current, err := s.store.LoadCurrent()
	if errors.Is(err, taxonomy.ErrNoCurrentRevision) {
		return status
	}
	if err != nil {
		status.SnapshotError = err.Error()
		return status
	}
	status.CurrentRevision = current.Revision
	index, err := taxonomy.NewCatalogIndex(*current, []challenge.Entry{s.challenge})
	if err != nil {
		status.SnapshotError = err.Error()
		return status
	}
	_, status.CatalogMapped = index.Mapping(s.challenge.ID)
	return status
}

func isolatedSchema(databaseURL string) (string, func(), error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return "", nil, fmt.Errorf("parse taxonomy E2E database URL: %w", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", nil, errors.New("taxonomy E2E database URL must use postgres")
	}
	schema, err := randomSchemaName()
	if err != nil {
		return "", nil, err
	}
	query := parsed.Query()
	query.Del("search_path")
	parsed.RawQuery = query.Encode()
	admin, err := sql.Open("pgx", parsed.String())
	if err != nil {
		return "", nil, fmt.Errorf("open taxonomy E2E database admin connection: %w", err)
	}
	if err := admin.Ping(); err != nil {
		_ = admin.Close()
		return "", nil, fmt.Errorf("ping taxonomy E2E database: %w", err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		_ = admin.Close()
		return "", nil, fmt.Errorf("create taxonomy E2E schema: %w", err)
	}
	query = parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	cleanup := func() {
		_, _ = admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		_ = admin.Close()
	}
	return parsed.String(), cleanup, nil
}

func randomSchemaName() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate taxonomy E2E schema name: %w", err)
	}
	return "breakfix_taxonomy_e2e_" + hex.EncodeToString(raw[:]), nil
}

func marshalStatus(value statusView) string {
	data, err := json.Marshal(value)
	if err != nil {
		return `{"snapshot_error":"marshal diagnostics"}`
	}
	return string(data)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
