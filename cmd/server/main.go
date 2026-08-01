package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/build"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/incusprovider"
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/breakfix/breakfix/internal/server"
	"github.com/breakfix/breakfix/internal/transport/httpapi/ui"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	configPath := flag.String("config", "config/breakfix.yaml", "Config file path")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}
	if err := cfg.ValidateServer(); err != nil {
		slog.Error("invalid server configuration", "err", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		slog.Error("failed to create data directory", "err", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.ChallengesDir(), 0o755); err != nil {
		slog.Error("failed to create challenges directory", "err", err)
		os.Exit(1)
	}

	slog.Info("Breakfix Server starting", "version", build.Version, "data_dir", cfg.DataDir)
	database, err := postgres.New(cfg.DatabaseURL)
	if err != nil {
		slog.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer func() { _ = database.Close() }()

	k8sClient, err := k8s.New(cfg.Kubeconfig)
	if err != nil {
		slog.Error("failed to create K8s client", "err", err)
		os.Exit(1)
	}
	incusClient, err := incusprovider.NewReconnectableClient(cfg.Incus, incusprovider.RoleServer)
	if err != nil {
		slog.Error("invalid Incus provider configuration", "err", err)
		os.Exit(1)
	}
	defer incusClient.Close()
	frontendFS, err := ui.Filesystem()
	if err != nil {
		slog.Error("failed to load embedded web assets", "err", err)
		os.Exit(1)
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	router, err := server.SetupRouter(runCtx, database, k8sClient, cfg, frontendFS, server.Dependencies{NodeTerminal: incusClient})
	if err != nil {
		slog.Error("failed to setup server", "err", err)
		os.Exit(1)
	}
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		slog.Info("server shutting down")
		runCancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	slog.Info("server listening", "port", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "err", err)
	}
}
