package generator

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

// BuildAndPush builds the Docker image and pushes to registry.
// Uses docker build + kind load for local dev.
func BuildAndPush(ctx context.Context, imageName, contextDir string) bool {
	absDir, _ := filepath.Abs(contextDir)
	buildCmd := exec.CommandContext(ctx, "docker", "build", "-t", imageName, absDir)
	buildCmd.Env = os.Environ()
	output, err := buildCmd.CombinedOutput()
	if err != nil {
		slog.Error("docker build failed", "err", err, "output", string(output))
		return false
	}

	pushCmd := exec.CommandContext(ctx, "docker", "push", imageName)
	pushCmd.Env = os.Environ()
	pushOutput, pushErr := pushCmd.CombinedOutput()
	if pushErr != nil {
		slog.Error("docker push failed", "err", pushErr, "output", string(pushOutput))
		return false
	}

	// Load into Kind for local dev
	cluster := "breakfix-dev"
	if v := os.Getenv("KIND_CLUSTER"); v != "" {
		cluster = v
	}
	loadCmd := exec.CommandContext(ctx, "kind", "load", "docker-image", imageName, "--name", cluster)
	loadCmd.Env = os.Environ()
	loadOutput, loadErr := loadCmd.CombinedOutput()
	if loadErr != nil {
		slog.Error("kind load failed", "err", loadErr, "output", string(loadOutput))
		return false
	}

	slog.Info("image built, pushed, and loaded into Kind", "image", imageName)
	return true
}
