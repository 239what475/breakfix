package verifier

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ArtifactBuildError means the artifact itself cannot be built. The caller
// reports it as a completed verification result instead of retrying the Job.
type ArtifactBuildError struct {
	Err error
}

func (e *ArtifactBuildError) Error() string {
	return e.Err.Error()
}

func (e *ArtifactBuildError) Unwrap() error {
	return e.Err
}

// BuildAndPush builds the challenge image and pushes to the registry.
// Uses buildkitd + buildctl (already in the Docker image).
func BuildAndPush(ctx context.Context, imageName, contextDir, baseImage string, insecure bool) error {
	sock := "unix:///tmp/buildkit.sock"

	// Write buildkitd config — only add insecure flags when needed
	configDir := "/tmp/buildkit-config"
	os.MkdirAll(configDir, 0755) //nolint:errcheck

	cfg := ""
	if insecure {
		// Extract registry host from imageName (first segment before /)
		host := imageName
		if idx := strings.IndexByte(imageName, '/'); idx >= 0 {
			host = imageName[:idx]
		}
		cfg = fmt.Sprintf(`
[registry."%s"]
  http = true
  insecure = true
`, host)
		os.WriteFile(configDir+"/buildkitd.toml", []byte(cfg), 0644) //nolint:errcheck
	}

	// Start buildkitd
	args := []string{"--root", "/var/lib/buildkit", "--addr", sock}
	if insecure {
		args = append(args, "--config", configDir+"/buildkitd.toml")
	}
	buildkitdCmd := exec.CommandContext(ctx, "buildkitd", args...)
	buildkitdCmd.Env = os.Environ()
	if err := buildkitdCmd.Start(); err != nil {
		slog.Error("start buildkitd", "err", err)
		return fmt.Errorf("start buildkitd: %w", err)
	}
	defer func() { _ = buildkitdCmd.Process.Kill() }()

	// Wait for daemon socket to appear then daemon to be ready
	for i := 0; i < 60; i++ {
		time.Sleep(time.Second)
		cmd := exec.CommandContext(ctx, "buildctl", "--addr", sock, "debug", "workers")
		output, _ := cmd.CombinedOutput()
		if cmd.ProcessState != nil && cmd.ProcessState.Success() {
			slog.Info("buildkitd ready", "after", time.Duration(i+1)*time.Second)
			break
		}
		if i == 59 {
			slog.Error("buildkitd not ready after 60s", "output", string(output))
			return fmt.Errorf("buildkitd did not become ready: %s", strings.TrimSpace(string(output)))
		}
	}

	// Build + push via buildctl
	start := time.Now()
	buildArgs := []string{
		"--addr", sock,
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=" + contextDir,
		"--local", "dockerfile=" + contextDir,
	}
	if strings.TrimSpace(baseImage) != "" {
		buildArgs = append(buildArgs, "--opt", "build-arg:BREAKFIX_BASE_IMAGE="+baseImage)
	}
	buildArgs = append(buildArgs, "--output", "type=image,name="+imageName+",push=true")

	cmd := exec.CommandContext(ctx, "buildctl", append(buildArgs, "--progress", "plain")...)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("buildctl failed", "err", err, "output", string(output))
		buildErr := fmt.Errorf("buildctl: %w: %s", err, strings.TrimSpace(string(output)))
		if buildOutputIsInfrastructure(string(output)) {
			return buildErr
		}
		return &ArtifactBuildError{Err: buildErr}
	}
	if trimmed := strings.TrimSpace(string(output)); trimmed != "" {
		slog.Info("buildctl output", "output", trimmed)
	}

	slog.Info("image built and pushed", "image", imageName, "duration", time.Since(start))
	return nil
}

func buildOutputIsInfrastructure(output string) bool {
	normalized := strings.ToLower(output)
	for _, signal := range []string{
		"connection refused", "connection reset", "i/o timeout", "tls handshake timeout",
		"no such host", "temporary failure", "server error", "service unavailable",
		"failed to push", "failed to do request", "unauthorized", "too many requests",
	} {
		if strings.Contains(normalized, signal) {
			return true
		}
	}
	return false
}

func isArtifactBuildError(err error) bool {
	var buildErr *ArtifactBuildError
	return errors.As(err, &buildErr)
}
