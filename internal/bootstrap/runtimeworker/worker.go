// Package runtimeworker assembles the Runtime Worker process.
package runtimeworker

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
	runtimeexecutor "github.com/breakfix/breakfix/internal/worker/runtime"
	"github.com/breakfix/breakfix/internal/worker/runtime/build"
	"github.com/breakfix/breakfix/internal/worker/runtime/publish"
	"github.com/breakfix/breakfix/internal/worker/runtime/verify"
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
	return "breakfix-runtime-worker"
}

// Run owns Runtime Worker dependency assembly and lifecycle.
func Run(ctx context.Context, configPath, workerID string) error {
	if strings.TrimSpace(workerID) == "" {
		return errors.New("runtime worker ID is required")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if err := cfg.ValidateRuntimeWorker(); err != nil {
		return fmt.Errorf("validate Runtime Worker configuration: %w", err)
	}
	workflowClient, err := internalapi.NewRuntimeActionClient(cfg.Worker.ServerURL, cfg.Worker.APIKey)
	if err != nil {
		return fmt.Errorf("create runtime action client: %w", err)
	}
	incusClient, err := incus.NewReconnectableClient(cfg.Incus, incus.RoleRuntime)
	if err != nil {
		return fmt.Errorf("create Runtime Worker Incus client: %w", err)
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
	builderExecutor, err := build.NewExecutor(incusClient, registryClient, cfg.Incus, cfg.Registry.Repository)
	if err != nil {
		return fmt.Errorf("create runtime builder: %w", err)
	}
	runner, err := runtimeexecutor.New(
		workflowClient,
		builderExecutor,
		publisherExecutor,
		verifierExecutor,
		runtimeexecutor.Config{WorkerID: workerID},
	)
	if err != nil {
		return fmt.Errorf("create Runtime Worker: %w", err)
	}

	return health.Run(ctx, health.Config{
		Port: cfg.HealthPort, Component: "runtime-worker",
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
