package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/breakfix/breakfix/internal/candidateworker"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/incusprovider"
	"github.com/breakfix/breakfix/internal/publisher"
	"github.com/breakfix/breakfix/internal/registry"
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
	if err := cfg.ValidatePublisherWorker(); err != nil {
		fatal("invalid publisher worker configuration", err)
	}
	server, err := candidateworker.NewClient(cfg.Worker.ServerURL, cfg.Worker.APIKey)
	if err != nil {
		fatal("failed to create candidate work client", err)
	}
	incusClient, err := incusprovider.NewReconnectableClient(cfg.Incus, incusprovider.RolePublisher)
	if err != nil {
		fatal("invalid Incus provider configuration", err)
	}
	defer incusClient.Close()
	registryClient, err := registry.NewClient(registry.ClientOptions{
		Credentials:     registry.Credentials{Username: cfg.Registry.Username, Password: cfg.Registry.Password},
		TrustBundleFile: cfg.Registry.TrustBundleFile,
	})
	if err != nil {
		fatal("failed to create registry client", err)
	}
	executor, err := publisher.NewExecutor(server, registryClient, incusClient, cfg.Registry.Address)
	if err != nil {
		fatal("failed to create publisher executor", err)
	}
	kinds := []worklist.Kind{worklist.KindArtifactPublish, worklist.KindArtifactCleanup, worklist.KindChallengePublish}
	worker, err := candidateworker.New(server, map[worklist.Kind]candidateworker.Executor{
		worklist.KindArtifactPublish:  executor,
		worklist.KindArtifactCleanup:  executor,
		worklist.KindChallengePublish: executor,
	}, candidateworker.Config{WorkerID: *workerID, Kinds: kinds})
	if err != nil {
		fatal("failed to create publisher worker", err)
	}
	if err := workerhealth.Run(ctx, workerhealth.Config{
		Port: cfg.HealthPort, Component: "publisher",
		Capabilities: []workerhealth.Capability{
			{Name: "registry", Ready: func(probeCtx context.Context) error {
				return registryClient.Ping(probeCtx, cfg.Registry.Address)
			}},
			{Name: "node-provider", Ready: func(probeCtx context.Context) error {
				_, err := incusClient.Preflight(probeCtx)
				return err
			}},
		},
	}, worker.Run); err != nil {
		fatal("publisher worker stopped", err)
	}
}

func defaultWorkerID() string {
	if value := strings.TrimSpace(os.Getenv("POD_NAME")); value != "" {
		return value
	}
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
		return host
	}
	return "breakfix-publisher-worker"
}

func fatal(message string, err error) {
	slog.Error(message, "err", err)
	os.Exit(1)
}
