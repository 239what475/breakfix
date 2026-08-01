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
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/generateworker"
	"github.com/breakfix/breakfix/internal/generation"
	"github.com/breakfix/breakfix/internal/generator"
	"github.com/breakfix/breakfix/internal/incusprovider"
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/breakfix/breakfix/internal/publisher"
	"github.com/breakfix/breakfix/internal/registry"
	"github.com/breakfix/breakfix/internal/verifier"
	"github.com/breakfix/breakfix/internal/workerhealth"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	configPath := flag.String("config", "config/breakfix.yaml", "Config file path")
	workerID := flag.String("worker-id", defaultWorkerID(), "Unique Generate Worker identity")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load configuration", err)
	}
	if err := cfg.ValidateGenerateWorker(); err != nil {
		fatal("validate Generate Worker configuration", err)
	}
	workflowClient, err := generation.NewClient(cfg.Worker.ServerURL, cfg.Worker.APIKey)
	if err != nil {
		fatal("create generation workflow client", err)
	}
	generatorClient, err := generator.NewInternalClient(cfg.Worker.ServerURL, cfg.Worker.APIKey)
	if err != nil {
		fatal("create generator workspace client", err)
	}
	generatorExecutor, err := generator.NewExecutor(cfg.Agent, generatorClient)
	if err != nil {
		fatal("create generator executor", err)
	}

	generateIncus, err := incusprovider.NewReconnectableClient(cfg.Incus, incusprovider.RoleGenerate)
	if err != nil {
		fatal("create Generate Worker Incus client", err)
	}
	defer generateIncus.Close()
	registryClient, err := registry.NewClient(registry.ClientOptions{
		Endpoint:        cfg.Registry.ClientAddress,
		Credentials:     registry.Credentials{Username: cfg.Registry.Username, Password: cfg.Registry.Password},
		TrustBundleFile: cfg.Registry.TrustBundleFile,
	})
	if err != nil {
		fatal("create registry client", err)
	}
	k8sClient, err := k8s.New(cfg.Kubeconfig)
	if err != nil {
		fatal("create Kubernetes client", err)
	}
	publisherExecutor, err := publisher.NewExecutor(registryClient, generateIncus, cfg.Registry.Address)
	if err != nil {
		fatal("create publisher executor", err)
	}
	verifierExecutor, err := verifier.NewExecutor(k8sClient, generateIncus, cfg.CRDNamespace)
	if err != nil {
		fatal("create verifier executor", err)
	}
	worker, err := generateworker.New(workflowClient, generatorExecutor, builder.NewExecutor(generateIncus, cfg.Incus), publisherExecutor, verifierExecutor,
		generateworker.Config{WorkerID: *workerID, Model: cfg.Agent.Model})
	if err != nil {
		fatal("create Generate Worker", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := workerhealth.Run(ctx, workerhealth.Config{
		Port: cfg.HealthPort, Component: "generate",
		Capabilities: []workerhealth.Capability{
			{Name: "registry", Ready: func(probeCtx context.Context) error { return registryClient.Ping(probeCtx) }},
			{Name: "kubernetes-api", Ready: func(probeCtx context.Context) error {
				if _, err := k8sClient.ListNodeEnvironments(probeCtx, cfg.CRDNamespace, ""); err != nil {
					return err
				}
				_, err := k8sClient.ListVK8sEnvironments(probeCtx, cfg.CRDNamespace, "")
				return err
			}},
			{Name: "node-provider", Ready: func(probeCtx context.Context) error { _, err := generateIncus.Preflight(probeCtx); return err }},
		},
	}, worker.Run); err != nil {
		fatal("Generate Worker stopped", err)
	}
}

func defaultWorkerID() string {
	if value := strings.TrimSpace(os.Getenv("POD_NAME")); value != "" {
		return value
	}
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
		return host
	}
	return "breakfix-generate-worker"
}

func fatal(message string, err error) {
	slog.Error(message, "err", err)
	os.Exit(1)
}
