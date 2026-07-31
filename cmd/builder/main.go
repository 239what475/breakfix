package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/breakfix/breakfix/internal/builder"
	"github.com/breakfix/breakfix/internal/candidateworker"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/incusprovider"
	"github.com/breakfix/breakfix/internal/workerhealth"
	"github.com/breakfix/breakfix/internal/worklist"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	configPath := flag.String("config", "config/breakfix.yaml", "Config file path")
	workerID := flag.String("worker-id", defaultWorkerID(), "Unique Worker identity")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("failed to load config", err)
	}
	if err := cfg.ValidateBuilderWorker(); err != nil {
		fatal("invalid builder worker configuration", err)
	}
	server, err := candidateworker.NewClient(cfg.Worker.ServerURL, cfg.Worker.APIKey)
	if err != nil {
		fatal("failed to create candidate work client", err)
	}
	incusClient, err := incusprovider.NewReconnectableClient(cfg.Incus, incusprovider.RoleBuilder)
	if err != nil {
		fatal("invalid Incus provider configuration", err)
	}
	defer incusClient.Close()
	executor, err := builder.NewExecutor(server, incusClient, cfg.Incus)
	if err != nil {
		fatal("failed to create builder executor", err)
	}
	worker, err := candidateworker.New(server, map[worklist.Kind]candidateworker.Executor{
		worklist.KindBuild: executor,
	}, candidateworker.Config{WorkerID: *workerID, Kinds: []worklist.Kind{worklist.KindBuild}})
	if err != nil {
		fatal("failed to create builder worker", err)
	}
	if err := workerhealth.Run(ctx, workerhealth.Config{
		Port: cfg.HealthPort, Component: "builder",
		Capabilities: []workerhealth.Capability{{Name: "node-provider", Ready: func(probeCtx context.Context) error {
			_, err := incusClient.Preflight(probeCtx)
			return err
		}}},
	}, worker.Run); err != nil {
		fatal("builder worker stopped", err)
	}
}

func defaultWorkerID() string {
	if value := strings.TrimSpace(os.Getenv("POD_NAME")); value != "" {
		return value
	}
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
		return host
	}
	return "breakfix-builder-worker"
}

func fatal(message string, err error) {
	slog.Error(message, "err", err)
	os.Exit(1)
}
