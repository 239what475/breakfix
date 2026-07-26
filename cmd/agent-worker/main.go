package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/breakfix/breakfix/internal/agentworker"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	configPath := flag.String("config", "config/breakfix.yaml", "Config file path")
	workerID := flag.String("worker-id", defaultWorkerID(), "Unique Worker identity")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}
	if strings.TrimSpace(cfg.AgentDatabaseURL) == "" {
		slog.Error("agent_database_url is required for agent worker")
		os.Exit(1)
	}
	database, err := db.OpenAgentRuntime(cfg.AgentDatabaseURL)
	if err != nil {
		slog.Error("failed to open agent runtime database", "err", err)
		os.Exit(1)
	}
	defer func() { _ = database.Close() }()

	worker, err := agentworker.New(database, map[string]agentworker.Executor{}, agentworker.NopDeltaSink{}, agentworker.Config{WorkerID: *workerID})
	if err != nil {
		slog.Error("failed to create agent worker", "err", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := worker.Run(ctx); err != nil {
		slog.Error("agent worker stopped", "err", err)
		os.Exit(1)
	}
}

func defaultWorkerID() string {
	if value := strings.TrimSpace(os.Getenv("POD_NAME")); value != "" {
		return value
	}
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
		return host
	}
	return "breakfix-agent-worker"
}
