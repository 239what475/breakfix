package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/breakfix/breakfix/internal/generator"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))

	topic := flag.String("topic", "", "Challenge topic")
	outputDir := flag.String("output", "data/challenges", "Output directory")
	flag.Parse()

	if *topic == "" {
		*topic = os.Getenv("TOPIC")
	}
	if *topic == "" {
		slog.Error("--topic is required")
		os.Exit(1)
	}

	g := &generator.Generator{
		Topic:      *topic,
		OutputDir:  *outputDir,
		Registry:   envOr("REGISTRY", "localhost:5000"),
		ACRNS:      envOr("ACR_NAMESPACE", "break-fix"),
		Kubeconfig: os.Getenv("KUBECONFIG"),
	}

	if err := g.Run(context.Background()); err != nil {
		slog.Error("generator failed", "err", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
