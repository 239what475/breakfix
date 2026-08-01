package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	bootstrapcontroller "github.com/breakfix/breakfix/internal/bootstrap/controller"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	configPath := flag.String("config", "config/app/local.yaml", "Config file path")
	flag.Parse()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := bootstrapcontroller.Run(ctx, *configPath); err != nil {
		slog.Error("controller manager stopped", "err", err)
		os.Exit(1)
	}
}
