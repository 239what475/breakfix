package builder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/registry"
)

// ArtifactError means the candidate itself is malformed or cannot be built.
// The Controller reports this as a completed artifact verification failure.
type ArtifactError struct{ Err error }

func (e *ArtifactError) Error() string { return e.Err.Error() }
func (e *ArtifactError) Unwrap() error { return e.Err }

func IsArtifactError(err error) bool {
	var artifact *ArtifactError
	return errors.As(err, &artifact)
}

type Config struct {
	ServerURL       string
	VerifyTaskID    string
	SubmissionGrant string
	BaseGrant       string
	UploadGrant     string
	BaseName        string
}

func Run(ctx context.Context, cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "breakfix-builder-*")
	if err != nil {
		return fmt.Errorf("create build workspace: %w", err)
	}
	defer os.RemoveAll(root) //nolint:errcheck

	submission := filepath.Join(root, "submission.tar.gz")
	if err := download(ctx, cfg.ServerURL, cfg.VerifyTaskID, "submission", cfg.SubmissionGrant, submission); err != nil {
		return fmt.Errorf("download submission: %w", err)
	}
	challengeDir := filepath.Join(root, "challenge")
	if err := os.MkdirAll(challengeDir, 0755); err != nil {
		return fmt.Errorf("create challenge directory: %w", err)
	}
	f, err := os.Open(submission)
	if err != nil {
		return fmt.Errorf("open submission: %w", err)
	}
	extractErr := challenge.ExtractTarGz(challengeDir, f)
	closeErr := f.Close()
	if extractErr != nil {
		return &ArtifactError{Err: fmt.Errorf("extract submission: %w", extractErr)}
	}
	if closeErr != nil {
		return fmt.Errorf("close submission: %w", closeErr)
	}
	entry, err := challenge.ValidateSubmissionDir(challengeDir)
	if err != nil {
		return &ArtifactError{Err: fmt.Errorf("validate submission: %w", err)}
	}

	baseArchive := filepath.Join(root, "base.oci.tar")
	if err := download(ctx, cfg.ServerURL, cfg.VerifyTaskID, "base", cfg.BaseGrant, baseArchive); err != nil {
		return fmt.Errorf("download base image: %w", err)
	}
	baseLayout := filepath.Join(root, "base")
	if err := registry.ExtractOCIArchive(baseArchive, baseLayout); err != nil {
		return fmt.Errorf("extract trusted base image: %w", err)
	}
	if err := registry.ValidateOCIArchive(baseArchive); err != nil {
		return fmt.Errorf("validate trusted base image: %w", err)
	}

	output := filepath.Join(root, "image.oci.tar")
	if err := build(ctx, challengeDir, baseLayout, cfg.BaseName, output); err != nil {
		return err
	}
	if err := registry.ValidateOCIArchive(output); err != nil {
		return fmt.Errorf("validate build output: %w", err)
	}
	if err := upload(ctx, cfg.ServerURL, cfg.VerifyTaskID, cfg.UploadGrant, output); err != nil {
		return fmt.Errorf("upload OCI archive: %w", err)
	}
	_ = entry // The parse above is intentional: Build never skips candidate validation.
	return nil
}

func (c Config) validate() error {
	if strings.TrimSpace(c.ServerURL) == "" || strings.TrimSpace(c.VerifyTaskID) == "" || strings.TrimSpace(c.SubmissionGrant) == "" || strings.TrimSpace(c.BaseGrant) == "" || strings.TrimSpace(c.UploadGrant) == "" {
		return fmt.Errorf("builder server URL, task ID, and grants are required")
	}
	if c.BaseName != "breakfix-base" && c.BaseName != "breakfix-k8s-base" {
		return fmt.Errorf("unsupported trusted base name %q", c.BaseName)
	}
	return nil
}

func build(ctx context.Context, challengeDir, baseLayout, baseName, output string) error {
	baseDigest, err := registry.OCILayoutRootDigest(baseLayout)
	if err != nil {
		return fmt.Errorf("read trusted base OCI layout: %w", err)
	}
	cmd := exec.CommandContext(ctx, "buildctl-daemonless.sh", buildArgs(challengeDir, baseLayout, baseName, baseDigest, output)...)
	cmd.Env = append(os.Environ(), "BUILDKITD_FLAGS=--oci-worker-no-process-sandbox")
	result, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(result))
	wrapped := fmt.Errorf("build candidate image: %w: %s", err, message)
	if buildOutputIsInfrastructure(message) {
		return wrapped
	}
	return &ArtifactError{Err: wrapped}
}

func buildArgs(challengeDir, baseLayout, baseName, baseDigest, output string) []string {
	const layoutID = "trusted-base"
	contextTarget := "oci-layout://" + layoutID + "@" + baseDigest
	args := []string{
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=" + challengeDir,
		"--local", "dockerfile=" + challengeDir,
		"--oci-layout", layoutID + "=" + baseLayout,
		"--opt", "build-arg:BREAKFIX_BASE_IMAGE=" + baseName,
		"--output", "type=oci,dest=" + output,
		"--progress", "plain",
	}
	// Existing verified artifacts use both FROM breakfix-base:latest and an
	// ARG-expanded FROM ${BREAKFIX_BASE_IMAGE}. Both spellings must resolve to
	// the task-scoped OCI layout rather than a Registry request.
	for _, alias := range []string{baseName, baseName + ":latest"} {
		args = append(args, "--opt", "context:"+alias+"="+contextTarget)
	}
	return args
}

func download(ctx context.Context, serverURL, taskID, kind, grant, destination string) error {
	url := strings.TrimRight(serverURL, "/") + "/api/internal/verify-builds/" + taskID + "/" + kind
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+grant)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("download %s: status %d: %s", kind, response.StatusCode, strings.TrimSpace(string(data)))
	}
	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(file, response.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func upload(ctx context.Context, serverURL, taskID, grant, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	url := strings.TrimRight(serverURL, "/") + "/api/internal/verify-builds/" + taskID + "/image"
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, file)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+grant)
	req.Header.Set("Content-Type", "application/vnd.oci.image.tar")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("upload OCI archive: status %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	return nil
}

func buildOutputIsInfrastructure(output string) bool {
	normalized := strings.ToLower(output)
	for _, signal := range []string{
		"connection refused", "connection reset", "i/o timeout", "tls handshake timeout",
		"no such host", "temporary failure", "server error", "service unavailable",
		"failed to do request", "unauthorized", "too many requests",
	} {
		if strings.Contains(normalized, signal) {
			return true
		}
	}
	return false
}
