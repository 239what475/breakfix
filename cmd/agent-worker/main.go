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
	"github.com/breakfix/breakfix/internal/assistant"
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/generator"
	"github.com/breakfix/breakfix/internal/taxonomy"
	"github.com/breakfix/breakfix/internal/workerhealth"
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
	if err := cfg.ValidateAgentWorker(); err != nil {
		slog.Error("invalid agent worker configuration", "err", err)
		os.Exit(1)
	}
	runtimeClient, err := agentworker.NewInternalClient(cfg.Worker.ServerURL, cfg.Worker.APIKey)
	if err != nil {
		slog.Error("failed to create agent worklist client", "err", err)
		os.Exit(1)
	}

	serverClient, err := assistant.NewInternalClient(cfg.Worker.ServerURL, cfg.Worker.APIKey)
	if err != nil {
		slog.Error("failed to create server internal client", "err", err)
		os.Exit(1)
	}
	assistantExecutor, err := assistant.NewWorkerExecutor(cfg.Agent, serverClient)
	if err != nil {
		slog.Error("failed to create assistant executor", "err", err)
		os.Exit(1)
	}
	authoringClient, err := authoring.NewInternalClient(cfg.Worker.ServerURL, cfg.Worker.APIKey)
	if err != nil {
		slog.Error("failed to create authoring internal client", "err", err)
		os.Exit(1)
	}
	authoringExecutor, err := authoring.NewWorkerExecutor(cfg.Agent, authoringClient)
	if err != nil {
		slog.Error("failed to create authoring executor", "err", err)
		os.Exit(1)
	}
	taxonomyClient, err := taxonomy.NewInternalClient(cfg.Worker.ServerURL, cfg.Worker.APIKey)
	if err != nil {
		slog.Error("failed to create taxonomy internal client", "err", err)
		os.Exit(1)
	}
	taxonomyExecutor, err := taxonomy.NewWorkerExecutor(cfg.Agent, taxonomyClient)
	if err != nil {
		slog.Error("failed to create taxonomy executor", "err", err)
		os.Exit(1)
	}
	generatorClient, err := generator.NewInternalClient(cfg.Worker.ServerURL, cfg.Worker.APIKey)
	if err != nil {
		slog.Error("failed to create generator internal client", "err", err)
		os.Exit(1)
	}
	generatorExecutor, err := generator.NewWorkerExecutor(cfg.Agent, generatorClient)
	if err != nil {
		slog.Error("failed to create generator executor", "err", err)
		os.Exit(1)
	}
	worker, err := agentworker.New(runtimeClient, map[string]agentworker.Executor{
		"assistant":                   assistantExecutor,
		"authoring":                   authoringExecutor,
		generator.RuntimePurpose:      generatorExecutor,
		taxonomy.RuntimePurposeMapper: taxonomyExecutor,
		taxonomy.RuntimePurposeReview: taxonomyExecutor,
	}, assistant.DeltaSink{Client: serverClient}, agentworker.Config{WorkerID: *workerID})
	if err != nil {
		slog.Error("failed to create agent worker", "err", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := workerhealth.Run(ctx, workerhealth.Config{Port: cfg.HealthPort, Component: "agent"}, worker.Run); err != nil {
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
