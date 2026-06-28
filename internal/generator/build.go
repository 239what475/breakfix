package generator

import (
	"context"
	"fmt"
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
		fmt.Fprintf(os.Stderr, "docker build failed: %v\n%s\n", err, string(output))
		return false
	}

	pushCmd := exec.CommandContext(ctx, "docker", "push", imageName)
	pushCmd.Env = os.Environ()
	pushOutput, pushErr := pushCmd.CombinedOutput()
	if pushErr != nil {
		fmt.Fprintf(os.Stderr, "docker push failed: %v\n%s\n", pushErr, string(pushOutput))
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
		fmt.Fprintf(os.Stderr, "kind load failed: %v\n%s\n", loadErr, string(loadOutput))
		return false
	}

	fmt.Printf("  ✓ Image built, pushed, and loaded into Kind: %s\n", imageName)
	return true
}
