package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/internalapi"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/oci"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/worker/generate"
	"github.com/breakfix/breakfix/internal/worker/generate/agent"
	"github.com/breakfix/breakfix/internal/worker/generate/build"
	"github.com/breakfix/breakfix/internal/worker/generate/publish"
	"github.com/breakfix/breakfix/internal/worker/generate/verify"
	"github.com/breakfix/breakfix/internal/workerhealth"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	configPath := flag.String("config", "config/app/local.yaml", "Config file path")
	workerID := flag.String("worker-id", defaultWorkerID(), "Unique Generate Worker identity")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("load configuration", err)
	}
	if err := cfg.ValidateGenerateWorker(); err != nil {
		fatal("validate Generate Worker configuration", err)
	}
	workflowClient, err := internalapi.NewGenerationWorkflowClient(cfg.Worker.ServerURL, cfg.Worker.APIKey)
	if err != nil {
		fatal("create generation workflow client", err)
	}
	generatorClient, err := internalapi.NewGeneratorWorkspaceClient(cfg.Worker.ServerURL, cfg.Worker.APIKey)
	if err != nil {
		fatal("create generator workspace client", err)
	}
	generatorExecutor, err := agent.NewExecutor(cfg.Agent, generatorClient)
	if err != nil {
		fatal("create generator executor", err)
	}

	generateIncus, err := incus.NewReconnectableClient(cfg.Incus, incus.RoleGenerate)
	if err != nil {
		fatal("create Generate Worker Incus client", err)
	}
	defer generateIncus.Close()
	registryClient, err := oci.NewClient(oci.ClientOptions{
		Endpoint:        cfg.Registry.ClientAddress,
		Credentials:     oci.Credentials{Username: cfg.Registry.Username, Password: cfg.Registry.Password},
		TrustBundleFile: cfg.Registry.TrustBundleFile,
	})
	if err != nil {
		fatal("create registry client", err)
	}
	k8sClient, err := kubernetes.New(cfg.Kubeconfig)
	if err != nil {
		fatal("create Kubernetes client", err)
	}
	publisherExecutor, err := publish.NewExecutor(registryClient, generateIncus, cfg.Registry.Address)
	if err != nil {
		fatal("create publisher executor", err)
	}
	verifierExecutor, err := verify.NewExecutor(k8sClient, generateIncus, cfg.CRDNamespace)
	if err != nil {
		fatal("create verifier executor", err)
	}
	worker, err := generate.New(workflowClient, generatorExecutor, build.NewExecutor(generateIncus, cfg.Incus), publisherExecutor, verifierExecutor,
		generate.Config{WorkerID: *workerID, Model: cfg.Agent.Model})
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
