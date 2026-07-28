package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/breakfix/breakfix/internal/publisher"
	"github.com/breakfix/breakfix/internal/registry"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	err := publisher.Run(context.Background(), publisher.Config{
		ServerURL:           os.Getenv("VERIFY_SERVER_URL"),
		VerifyTaskID:        os.Getenv("VERIFY_TASK_ID"),
		DownloadGrant:       os.Getenv("VERIFY_ARCHIVE_GRANT"),
		StagingImage:        os.Getenv("VERIFY_STAGING_IMAGE"),
		RegistryInsecure:    os.Getenv("REGISTRY_INSECURE") == "true",
		RegistryCredentials: registry.Credentials{Username: os.Getenv("REGISTRY_USERNAME"), Password: os.Getenv("REGISTRY_PASSWORD")},
	})
	if err == nil {
		return
	}
	slog.Error("publish failed", "err", err)
	if publisher.IsArtifactError(err) {
		os.Exit(20)
	}
	os.Exit(1)
}
