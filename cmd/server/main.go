package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	bootstrapserver "github.com/breakfix/breakfix/internal/bootstrap/server"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	configPath := flag.String("config", "config/breakfix.yaml", "Config file path")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := bootstrapserver.Run(ctx, *configPath); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
