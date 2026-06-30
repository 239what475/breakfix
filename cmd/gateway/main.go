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
	"syscall"
	"time"

	"github.com/breakfix/breakfix/internal/build"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/gateway"
	"github.com/breakfix/breakfix/internal/k8s"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

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
	}
	if err := challenge.SyncChallenges(database, challengesDir); err != nil {
		slog.Error("failed to sync challenges", "err", err)
		os.Exit(1)
	}

	k8sClient, err := k8s.New(cfg.Kubeconfig)
	if err != nil {
		slog.Error("failed to create K8s client", "err", err)
		os.Exit(1)
	}

	router := gateway.SetupRouter(database, k8sClient, cfg)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: router,
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		slog.Info("shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	slog.Info("listening", "port", cfg.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("serve error", "err", err)
	}
}
