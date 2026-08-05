// Package generateworker assembles the Generate Worker process.
package generateworker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/internalapi"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/oci"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/transport/health"
	"github.com/breakfix/breakfix/internal/worker/generate"
	"github.com/breakfix/breakfix/internal/worker/generate/build"
	"github.com/breakfix/breakfix/internal/worker/generate/publish"
	"github.com/breakfix/breakfix/internal/worker/generate/verify"
)

// DefaultWorkerID derives a stable local identity when Kubernetes has not
// injected POD_NAME.
func DefaultWorkerID() string {
	if value := strings.TrimSpace(os.Getenv("POD_NAME")); value != "" {
		return value
	}
	if host, err := os.Hostname(); err == nil && strings.TrimSpace(host) != "" {
		return host
	}
	return "breakfix-generate-worker"
}

// Run owns Generate Worker dependency assembly and lifecycle.
func Run(ctx context.Context, configPath, workerID string) error {
	if strings.TrimSpace(workerID) == "" {
		return errors.New("generate worker ID is required")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if err := cfg.ValidateGenerateWorker(); err != nil {
		return fmt.Errorf("validate Generate Worker configuration: %w", err)
	}
	workflowClient, err := internalapi.NewGenerationWorkflowClient(cfg.Worker.ServerURL, cfg.Worker.APIKey)
	if err != nil {
		return fmt.Errorf("create generation workflow client: %w", err)
	}
	incusClient, err := incus.NewReconnectableClient(cfg.Incus, incus.RoleGenerate)
	if err != nil {
		return fmt.Errorf("create Generate Worker Incus client: %w", err)
	}
	defer incusClient.Close()
	registryAuthority, err := oci.AuthorityForReference(cfg.Registry.Repository)
	if err != nil {
		return fmt.Errorf("derive Registry authority: %w", err)
	}
	registryClient, err := oci.NewClient(oci.ClientOptions{
		Authority:       registryAuthority,
		Credentials:     oci.Credentials{Username: cfg.Registry.Username, Password: cfg.Registry.Password},
		TrustBundleFile: cfg.Registry.TrustBundleFile,
	})
	if err != nil {
		return fmt.Errorf("create Registry client: %w", err)
	}
	k8sClient, err := kubernetes.New(cfg.Kubeconfig)
	if err != nil {
		return fmt.Errorf("create Kubernetes client: %w", err)
	}
	publisherExecutor, err := publish.NewExecutor(registryClient, incusClient, cfg.Registry.Repository)
	if err != nil {
		return fmt.Errorf("create publisher executor: %w", err)
	}
	verifierExecutor, err := verify.NewExecutor(k8sClient, incusClient, cfg.CRDNamespace)
	if err != nil {
		return fmt.Errorf("create verifier executor: %w", err)
	}
	runner, err := generate.New(
		workflowClient,
		build.NewExecutor(incusClient, cfg.Incus),
		publisherExecutor,
		verifierExecutor,
		generate.Config{WorkerID: workerID},
	)
	if err != nil {
		return fmt.Errorf("create Generate Worker: %w", err)
	}

	return health.Run(ctx, health.Config{
		Port: cfg.HealthPort, Component: "generate",
		Capabilities: []health.Capability{
			{Name: "registry", Ready: func(probeCtx context.Context) error { return registryClient.Ping(probeCtx) }},
			{Name: "kubernetes-api", Ready: func(probeCtx context.Context) error {
				if _, err := k8sClient.ListNodeEnvironments(probeCtx, cfg.CRDNamespace, ""); err != nil {
					return err
				}
				_, err := k8sClient.ListVK8sEnvironments(probeCtx, cfg.CRDNamespace, "")
				return err
			}},
			{Name: "node-provider", Ready: func(probeCtx context.Context) error {
				_, err := incusClient.Preflight(probeCtx)
				return err
			}},
		},
	}, runner.Run)
}
