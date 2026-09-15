// Package controller assembles the Controller process from configuration and
// concrete infrastructure adapters.
package controller

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/adapter/runnableprovider"
	"github.com/breakfix/breakfix/internal/adapter/vcluster"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	runtimeenvironment "github.com/breakfix/breakfix/internal/controller/runtimeenvironment"
	"github.com/breakfix/breakfix/internal/domain/environment"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
)

// Run owns Controller process assembly and lifecycle.
func Run(ctx context.Context, configPath string) error {
	ctrl.SetLogger(logr.FromSlogHandler(slog.Default().Handler()))

	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	if err := cfg.ValidateController(); err != nil {
		return fmt.Errorf("validate controller configuration: %w", err)
	}
	k8sClient, err := kubernetes.New(cfg.Kubeconfig)
	if err != nil {
		return fmt.Errorf("create Kubernetes client: %w", err)
	}
	incusClient, err := incus.NewReconnectableClient(cfg.Incus, incus.RoleController)
	if err != nil {
		return fmt.Errorf("create Controller Incus client: %w", err)
	}
	defer incusClient.Close()
	database, err := postgres.New(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open Controller database: %w", err)
	}
	defer func() { _ = database.Close() }()
	vclusterClient := &vcluster.Client{BinaryPath: cfg.VClusterBinary}
	if _, err := vclusterClient.Validate(ctx); err != nil {
		return fmt.Errorf("validate vcluster CLI: %w", err)
	}
	vk8sProvider := kubernetes.NewVK8sEnvironmentProvider(k8sClient, vclusterClient, kubernetes.VK8sEnvironmentProviderConfig{
		NamespacePrefix: cfg.Namespace, ControlNamespace: cfg.CRDNamespace,
		RegistryPullSecret: cfg.Registry.PullSecret, VerificationServiceAccount: "breakfix-runtime-worker",
		ChartRepo: cfg.VClusterChartRepo, ChartVersion: cfg.VClusterChartVersion,
	})
	provider, err := runnableprovider.NewEnvironmentProvider(incusClient, vk8sProvider, providerConfig(cfg))
	if err != nil {
		return fmt.Errorf("create public runtime environment provider: %w", err)
	}

	scheme := runtime.NewScheme()
	if err := runtimev2.AddToScheme(scheme); err != nil {
		return fmt.Errorf("add Breakfix API scheme: %w", err)
	}
	syncPeriod := time.Minute
	manager, err := ctrl.NewManager(k8sClient.RESTConfig(), ctrl.Options{
		Scheme:                  scheme,
		HealthProbeBindAddress:  ":" + strconv.Itoa(cfg.HealthPort),
		LeaderElection:          true,
		LeaderElectionID:        "breakfix-controller.breakfix.dev",
		LeaderElectionNamespace: cfg.CRDNamespace,
		Cache:                   crcache.Options{SyncPeriod: &syncPeriod},
	})
	if err != nil {
		return fmt.Errorf("create controller manager: %w", err)
	}
	if err := manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("add health check: %w", err)
	}
	if err := manager.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("add readiness check: %w", err)
	}
	reconciler := &runtimeenvironment.Reconciler{Client: manager.GetClient(), Resolver: database.Runnable, Provider: provider, Reaps: database.Runnable}
	if err := reconciler.SetupWithManager(manager); err != nil {
		return fmt.Errorf("setup RuntimeEnvironment reconciler: %w", err)
	}
	if err := manager.Add(&reaperRunner{reaper: runtimeenvironment.Reaper{Queue: database.Runnable, Provider: provider, Owner: controllerOwner()}}); err != nil {
		return fmt.Errorf("setup RuntimeEnvironment reaper: %w", err)
	}

	slog.Info("Breakfix Controller starting", "namespace", cfg.Namespace, "crd_namespace", cfg.CRDNamespace)
	if err := manager.Start(ctx); err != nil {
		return fmt.Errorf("run controller manager: %w", err)
	}
	return nil
}

func providerConfig(cfg config.Config) runnableprovider.EnvironmentProviderConfig {
	resources := cfg.Runtime.K8s.Resources
	return runnableprovider.EnvironmentProviderConfig{
		Node: runnableprovider.NodeEnvironmentConfig{
			ProfileRevision: cfg.Runtime.Node.ProfileRevision, NetworkPolicyRevision: cfg.Runtime.Node.NetworkPolicyRevision,
			Resources: incus.NodeEnvironmentResources{CPU: cfg.Incus.NodeCPU, Memory: cfg.Incus.NodeMemory, Processes: cfg.Incus.NodeProcesses, RootDisk: cfg.Incus.NodeRootDisk},
		},
		K8s: environment.VK8sRuntime{
			ProfileRevision: cfg.Runtime.K8s.ProfileRevision, Version: cfg.Runtime.K8s.Version, ManagementTerminalImage: cfg.Runtime.K8s.ManagementTerminalImage,
			Resources: environment.VK8sRuntimeResources{
				ControlPlaneCPU: resources.ControlPlaneCPU, ControlPlaneMemory: resources.ControlPlaneMemory, ControlPlaneEphemeralStorage: resources.ControlPlaneEphemeralStorage,
				WorkloadCPU: resources.WorkloadCPU, WorkloadMemory: resources.WorkloadMemory, WorkloadEphemeralStorage: resources.WorkloadEphemeralStorage,
				QuotaCPU: resources.QuotaCPU, QuotaMemory: resources.QuotaMemory, QuotaEphemeralStorage: resources.QuotaEphemeralStorage,
			},
			Network: environment.VK8sNetwork{PublicEgressCIDR: cfg.Runtime.K8s.Network.PublicEgressCIDR, ProtectedCIDRs: append([]string(nil), cfg.Runtime.K8s.Network.ProtectedCIDRs...)},
		},
	}
}

type reaperRunner struct{ reaper runtimeenvironment.Reaper }

func (r *reaperRunner) NeedLeaderElection() bool { return true }

func (r *reaperRunner) Start(ctx context.Context) error {
	for {
		processed, err := r.reaper.RunOnce(ctx)
		if err != nil {
			slog.Warn("reap runtime environment", "err", err)
		}
		wait := time.Second
		if processed {
			wait = 100 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

func controllerOwner() string {
	if value := strings.TrimSpace(os.Getenv("POD_NAME")); value != "" {
		return value
	}
	if hostname, err := os.Hostname(); err == nil && strings.TrimSpace(hostname) != "" {
		return hostname
	}
	return "breakfix-controller"
}
