package generator

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// BuildAndPush builds the challenge image and pushes to the registry.
// Uses buildkitd + buildctl (already in the Docker image).
func BuildAndPush(ctx context.Context, imageName, contextDir, baseImage string, insecure bool) bool {
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
		return false
	}
	if trimmed := strings.TrimSpace(string(output)); trimmed != "" {
		slog.Info("buildctl output", "output", trimmed)
	}

	slog.Info("image built and pushed", "image", imageName, "duration", time.Since(start))
	return true
}

// DeleteRegistryImage removes a pushed, unverified image by digest. Registry
// V2 does not reliably support deleting a tag directly, so resolve its manifest
// first and delete the digest returned by the registry.
func DeleteRegistryImage(ctx context.Context, imageName string, insecure bool) error {
	registry, repository, reference, err := registryImageReference(imageName)
	if err != nil {
		return err
	}
	scheme := "https"
	if insecure {
		scheme = "http"
	}
	manifestURL := scheme + "://" + registry + "/v2/" + registryRepositoryPath(repository) + "/manifests/" + url.PathEscape(reference)
	client := &http.Client{Timeout: 20 * time.Second}

	head, err := http.NewRequestWithContext(ctx, http.MethodHead, manifestURL, nil)
	if err != nil {
		return fmt.Errorf("create manifest request: %w", err)
	}
	head.Header.Set("Accept", "application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
	response, err := client.Do(head)
	if err != nil {
		return fmt.Errorf("resolve image manifest: %w", err)
	}
	response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("resolve image manifest: status %d", response.StatusCode)
	}
	digest := strings.TrimSpace(response.Header.Get("Docker-Content-Digest"))
	if digest == "" {
		return fmt.Errorf("resolve image manifest: registry did not return Docker-Content-Digest")
	}

	remove, err := http.NewRequestWithContext(ctx, http.MethodDelete, scheme+"://"+registry+"/v2/"+registryRepositoryPath(repository)+"/manifests/"+url.PathEscape(digest), nil)
	if err != nil {
		return fmt.Errorf("create image delete request: %w", err)
	}
	response, err = client.Do(remove)
	if err != nil {
		return fmt.Errorf("delete image manifest: %w", err)
	}
	response.Body.Close()
	if response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusOK || response.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("delete image manifest: status %d", response.StatusCode)
}

func registryImageReference(imageName string) (registry, repository, reference string, err error) {
	parts := strings.SplitN(strings.TrimSpace(imageName), "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", "", fmt.Errorf("invalid registry image %q", imageName)
	}
	registry, repository = parts[0], parts[1]
	if index := strings.LastIndex(repository, "@"); index >= 0 {
		reference = repository[index+1:]
		repository = repository[:index]
	} else if index := strings.LastIndex(repository, ":"); index > strings.LastIndex(repository, "/") {
		reference = repository[index+1:]
		repository = repository[:index]
	} else {
		reference = "latest"
	}
	if strings.TrimSpace(repository) == "" || strings.TrimSpace(reference) == "" {
		return "", "", "", fmt.Errorf("invalid registry image %q", imageName)
	}
	return registry, repository, reference, nil
}

func registryRepositoryPath(repository string) string {
	parts := strings.Split(repository, "/")
	for index, part := range parts {
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}
