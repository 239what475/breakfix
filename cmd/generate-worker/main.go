package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	bootstrapgenerate "github.com/breakfix/breakfix/internal/bootstrap/generateworker"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	configPath := flag.String("config", "config/app/local.yaml", "Config file path")
	workerID := flag.String("worker-id", bootstrapgenerate.DefaultWorkerID(), "Unique Generate Worker identity")
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := bootstrapgenerate.Run(ctx, *configPath, *workerID); err != nil {
		fatal("Generate Worker stopped", err)
	}
}

func fatal(message string, err error) {
	slog.Error(message, "err", err)
	os.Exit(1)
}
