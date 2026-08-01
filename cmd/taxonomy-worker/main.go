package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/breakfix/breakfix/internal/adapter/internalapi"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/worker/taxonomy"
	"github.com/breakfix/breakfix/internal/workerhealth"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	configPath := flag.String("config", "config/app/local.yaml", "Config file path")
	workerID := flag.String("worker-id", defaultWorkerID(), "Unique Taxonomy Worker identity")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load configuration", err)
	}
	if err := cfg.ValidateTaxonomyWorker(); err != nil {
		fatal("validate Taxonomy Worker configuration", err)
	}
	client, err := internalapi.NewTaxonomyWorkflowClient(cfg.Worker.ServerURL, cfg.Worker.APIKey)
	if err != nil {
		fatal("create taxonomy workflow client", err)
	}
	worker, err := taxonomy.New(client, cfg.Agent, taxonomy.Config{WorkerID: *workerID})
	if err != nil {
		fatal("create Taxonomy Worker", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := workerhealth.Run(ctx, workerhealth.Config{Port: cfg.HealthPort, Component: "taxonomy"}, worker.Run); err != nil {
		fatal("Taxonomy Worker stopped", err)
	}
}

func defaultWorkerID() string {
	if value := strings.TrimSpace(os.Getenv("POD_NAME")); value != "" {
		return value
	}
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
		return host
	}
	return "breakfix-taxonomy-worker"
}

func fatal(message string, err error) {
	slog.Error(message, "err", err)
	os.Exit(1)
}
