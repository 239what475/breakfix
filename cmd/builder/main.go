package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/breakfix/breakfix/internal/builder"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	err := builder.Run(context.Background(), builder.Config{
		ServerURL:       os.Getenv("VERIFY_SERVER_URL"),
		VerifyTaskID:    os.Getenv("VERIFY_TASK_ID"),
		SubmissionGrant: os.Getenv("VERIFY_SUBMISSION_GRANT"),
		BaseGrant:       os.Getenv("VERIFY_BASE_GRANT"),
		UploadGrant:     os.Getenv("VERIFY_UPLOAD_GRANT"),
		BaseName:        os.Getenv("VERIFY_BASE_NAME"),
	})
	if err == nil {
		return
	}
	slog.Error("build failed", "err", err)
	if builder.IsArtifactError(err) {
		os.Exit(20)
	}
	os.Exit(1)
}
