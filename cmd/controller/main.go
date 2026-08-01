package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/controller"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	ctrl.SetLogger(logr.FromSlogHandler(slog.Default().Handler()))

	configPath := flag.String("config", "config/breakfix.yaml", "Config file path")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}
	if err := cfg.ValidateController(); err != nil {
		slog.Error("invalid controller configuration", "err", err)
		os.Exit(1)
	}
	k8sClient, err := kubernetes.New(cfg.Kubeconfig)
	if err != nil {
		slog.Error("failed to create K8s client", "err", err)
		os.Exit(1)
	}
	incusClient, err := incus.NewReconnectableClient(cfg.Incus, incus.RoleController)
	if err != nil {
		slog.Error("invalid Incus provider configuration", "err", err)
		os.Exit(1)
	}
	defer incusClient.Close()

	scheme := runtime.NewScheme()
	if err := breakfixv1.AddToScheme(scheme); err != nil {
		slog.Error("failed to add scheme", "err", err)
		os.Exit(1)
	}
	mgr, err := ctrl.NewManager(k8sClient.RESTConfig(), ctrl.Options{
		Scheme:                  scheme,
		HealthProbeBindAddress:  ":" + strconv.Itoa(cfg.HealthPort),
		LeaderElection:          true,
		LeaderElectionID:        "breakfix-controller.breakfix.dev",
		LeaderElectionNamespace: cfg.CRDNamespace,
		Cache: crcache.Options{SyncPeriod: func() *time.Duration {
			d := time.Minute
			return &d
		}()},
	})
	if err != nil {
		slog.Error("failed to create controller manager", "err", err)
		os.Exit(1)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		slog.Error("failed to add controller health check", "err", err)
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		slog.Error("failed to add controller readiness check", "err", err)
		os.Exit(1)
	}
	if err := controller.Setup(mgr, k8sClient, controller.Options{
		RegistryPullSecret:   cfg.Registry.PullSecret,
		Namespace:            cfg.Namespace,
		CRDNamespace:         cfg.CRDNamespace,
		VClusterBinary:       cfg.VClusterBinary,
		VClusterChartRepo:    cfg.VClusterChartRepo,
		VClusterChartVersion: cfg.VClusterChartVersion,
	}, controller.Dependencies{NodeProvider: incus.NewNodeEnvironmentProvider(incusClient)}); err != nil {
		slog.Error("failed to setup controllers", "err", err)
		os.Exit(1)
	}

	slog.Info("Breakfix Controller starting", "namespace", cfg.Namespace, "crd_namespace", cfg.CRDNamespace)
	if err := mgr.Start(ctx); err != nil {
		slog.Error("controller manager stopped", "err", err)
		os.Exit(1)
	}
}
