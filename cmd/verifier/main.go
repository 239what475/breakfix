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
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/breakfix/breakfix/internal/verifier"
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
	if err := cfg.ValidateVerifierWorker(); err != nil {
		fatal("invalid verifier worker configuration", err)
	}
	server, err := candidateworker.NewClient(cfg.Worker.ServerURL, cfg.Worker.APIKey)
	if err != nil {
		fatal("failed to create candidate work client", err)
	}
	k8sClient, err := k8s.New(cfg.Kubeconfig)
	if err != nil {
		fatal("failed to create Kubernetes client", err)
	}
	incusClient, err := incusprovider.NewReconnectableClient(cfg.Incus, incusprovider.RoleVerifier)
	if err != nil {
		fatal("invalid Incus provider configuration", err)
	}
	defer incusClient.Close()
	executor, err := verifier.NewExecutor(server, k8sClient, incusClient, cfg.CRDNamespace)
	if err != nil {
		fatal("failed to create verifier executor", err)
	}
	worker, err := candidateworker.New(server, map[worklist.Kind]candidateworker.Executor{
		worklist.KindVerify: executor,
	}, candidateworker.Config{WorkerID: *workerID, Kinds: []worklist.Kind{worklist.KindVerify}})
	if err != nil {
		fatal("failed to create verifier worker", err)
	}
	if err := workerhealth.Run(ctx, workerhealth.Config{
		Port: cfg.HealthPort, Component: "verifier",
		Capabilities: []workerhealth.Capability{
			{Name: "kubernetes-api", Ready: func(probeCtx context.Context) error {
				if _, err := k8sClient.ListNodeEnvironments(probeCtx, cfg.CRDNamespace, ""); err != nil {
					return err
				}
				if _, err := k8sClient.ListVK8sEnvironments(probeCtx, cfg.CRDNamespace, ""); err != nil {
					return err
				}
				return nil
			}},
			{Name: "node-provider", Ready: func(probeCtx context.Context) error {
				_, err := incusClient.Preflight(probeCtx)
				return err
			}},
		},
	}, worker.Run); err != nil {
		fatal("verifier worker stopped", err)
	}
}

func defaultWorkerID() string {
	if value := strings.TrimSpace(os.Getenv("POD_NAME")); value != "" {
		return value
	}
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
		return host
	}
	return "breakfix-verifier-worker"
}

func fatal(message string, err error) {
	slog.Error(message, "err", err)
	os.Exit(1)
}
