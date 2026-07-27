package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/breakfix/breakfix/internal/registry"
	"github.com/breakfix/breakfix/internal/verifier"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if err := verifier.Run(context.Background(), verifier.Config{
		Kubeconfig:          os.Getenv("KUBECONFIG"),
		RegistryAddr:        envOr("REGISTRY_ADDR", "172.18.0.1:5000/break-fix"),
		RegistryInsecure:    os.Getenv("REGISTRY_INSECURE") == "true",
		RegistryCredentials: registry.Credentials{Username: os.Getenv("REGISTRY_USERNAME"), Password: os.Getenv("REGISTRY_PASSWORD")},
		ServerURL:           os.Getenv("SERVER_INTERNAL_URL"),
		InternalAPIKey:      os.Getenv("SERVER_INTERNAL_API_KEY"),
		VerifyTaskID:        os.Getenv("VERIFY_TASK_ID"),
		VerifyTaskNS:        envOr("VERIFY_TASK_NAMESPACE", "breakfix-system"),
		SubmissionID:        os.Getenv("VERIFY_SUBMISSION_ID"),
	}); err != nil {
		slog.Error("verification infrastructure failure", "err", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return def
}
