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

	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/controller"
	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
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

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}
	k8sClient, err := k8s.New(cfg.Kubeconfig)
	if err != nil {
		slog.Error("failed to create K8s client", "err", err)
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	if err := breakfixv1.AddToScheme(scheme); err != nil {
		slog.Error("failed to add scheme", "err", err)
		os.Exit(1)
	}
	mgr, err := ctrl.NewManager(k8sClient.RESTConfig(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: ":" + strconv.Itoa(cfg.HealthPort),
		LeaderElection:         false,
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
		RegistryAddr:         cfg.RegistryAddr,
		RegistryInsecure:     cfg.RegistryInsecure,
		Namespace:            cfg.Namespace,
		CRDNamespace:         cfg.CRDNamespace,
		CooldownMinutes:      cfg.CooldownMinutes,
		InternalAPIKey:       cfg.InternalAPIKey,
		ServerHost:           cfg.ServerHost,
		ServerPort:           cfg.Port,
		VClusterBinary:       cfg.VClusterBinary,
		VClusterChartRepo:    cfg.VClusterChartRepo,
		VClusterChartVersion: cfg.VClusterChartVersion,
	}); err != nil {
		slog.Error("failed to setup controllers", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	slog.Info("Breakfix Controller starting", "namespace", cfg.Namespace, "crd_namespace", cfg.CRDNamespace)
	if err := mgr.Start(ctx); err != nil {
		slog.Error("controller manager stopped", "err", err)
		os.Exit(1)
	}
}
