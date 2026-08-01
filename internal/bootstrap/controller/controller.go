// Package controller assembles the Controller process from configuration and
// concrete infrastructure adapters.
package controller

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	environmentcontroller "github.com/breakfix/breakfix/internal/controller"
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

	scheme := runtime.NewScheme()
	if err := breakfixv1.AddToScheme(scheme); err != nil {
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
	if err := environmentcontroller.Setup(manager, k8sClient, environmentcontroller.Options{
		RegistryPullSecret:   cfg.Registry.PullSecret,
		Namespace:            cfg.Namespace,
		CRDNamespace:         cfg.CRDNamespace,
		VClusterBinary:       cfg.VClusterBinary,
		VClusterChartRepo:    cfg.VClusterChartRepo,
		VClusterChartVersion: cfg.VClusterChartVersion,
	}, environmentcontroller.Dependencies{NodeProvider: incus.NewNodeEnvironmentProvider(incusClient)}); err != nil {
		return fmt.Errorf("setup environment reconcilers: %w", err)
	}

	slog.Info("Breakfix Controller starting", "namespace", cfg.Namespace, "crd_namespace", cfg.CRDNamespace)
	if err := manager.Start(ctx); err != nil {
		return fmt.Errorf("run controller manager: %w", err)
	}
	return nil
}
