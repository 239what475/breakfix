package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/build"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/controller"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/gateway"
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	crcache "sigs.k8s.io/controller-runtime/pkg/cache"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	ctrl.SetLogger(logr.FromSlogHandler(slog.Default().Handler()))

	configPath := flag.String("config", "breakfix.yaml", "Config file path")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}
	_ = os.MkdirAll(cfg.DataDir, 0700)

	slog.Info("Breakfix Gateway starting", "version", build.Version, "data_dir", cfg.DataDir)

	database, err := db.New(filepath.Join(cfg.DataDir, "breakfix.db"))
	if err != nil {
		slog.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer func() { _ = database.Close() }()

	challengesDir := filepath.Join(cfg.DataDir, "challenges")
	if err := os.MkdirAll(challengesDir, 0755); err != nil {
		slog.Error("failed to create challenges dir", "err", err)
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
		Cache: crcache.Options{
			SyncPeriod: func() *time.Duration { d := time.Minute; return &d }(),
		},
	})
	if err != nil {
		slog.Error("failed to create manager", "err", err)
		os.Exit(1)
	}
	if err := controller.Setup(
		mgr,
		k8sClient,
		cfg.RegistryAddr,
		cfg.Namespace,
		cfg.CRDNamespace,
		challengesDir,
		cfg.DataDir,
		cfg.CooldownMinutes,
		cfg.RegistryInsecure,
		cfg.InternalAPIKey,
		cfg.ServerHost,
		cfg.Port,
		cfg.VClusterBinary,
		cfg.VClusterChartRepo,
		cfg.VClusterChartVersion,
		database,
	); err != nil {
		slog.Error("failed to setup controllers", "err", err)
		os.Exit(1)
	}

	router := gateway.SetupRouter(database, k8sClient, cfg, frontendFS)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: router,
	}
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		slog.Info("shutting down")
		runCancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	go func() {
		slog.Info("controller starting", "namespace", cfg.Namespace, "crd_ns", cfg.CRDNamespace)
		if err := mgr.Start(runCtx); err != nil {
			slog.Error("controller error", "err", err)
			os.Exit(1)
		}
	}()

	slog.Info("listening", "port", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("serve error", "err", err)
	}
}
