package generator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"
)

// BuildAndPush builds the challenge image and pushes to the registry.
// Uses buildkitd + buildctl (already in the Docker image).
func BuildAndPush(ctx context.Context, imageName, contextDir string, insecure bool) bool {
	sock := "unix:///tmp/buildkit.sock"

	// Write buildkitd config — only add insecure flags when needed
	configDir := "/tmp/buildkit-config"
	os.MkdirAll(configDir, 0755) //nolint:errcheck

	cfg := ""
	if insecure {
		// Extract registry host from imageName (first segment before /)
		host := imageName
		if idx := firstSlash(imageName); idx >= 0 {
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
		return false
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
			return false
		}
	}

	// Build + push via buildctl
	start := time.Now()
	cmd := exec.CommandContext(ctx, "buildctl",
		"--addr", sock,
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context="+contextDir,
		"--local", "dockerfile="+contextDir,
		"--output", "type=image,name="+imageName+",push=true",
	)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("buildctl failed", "err", err, "output", string(output))
		return false
	}

	slog.Info("image built and pushed", "image", imageName, "duration", time.Since(start))
	return true
}

func firstSlash(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}
