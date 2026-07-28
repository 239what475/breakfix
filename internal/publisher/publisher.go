package publisher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/breakfix/breakfix/internal/registry"
)

type ArtifactError struct{ Err error }

func (e *ArtifactError) Error() string { return e.Err.Error() }
func (e *ArtifactError) Unwrap() error { return e.Err }

func IsArtifactError(err error) bool {
	var artifact *ArtifactError
	return errors.As(err, &artifact)
}

type Config struct {
	ServerURL           string
	VerifyTaskID        string
	DownloadGrant       string
	StagingImage        string
	RegistryInsecure    bool
	RegistryCredentials registry.Credentials
}

func Run(ctx context.Context, cfg Config) error {
	if strings.TrimSpace(cfg.ServerURL) == "" || strings.TrimSpace(cfg.VerifyTaskID) == "" || strings.TrimSpace(cfg.DownloadGrant) == "" || strings.TrimSpace(cfg.StagingImage) == "" {
		return fmt.Errorf("publisher server URL, task ID, archive grant, and staging image are required")
	}
	if err := cfg.RegistryCredentials.Validate(); err != nil {
		return err
	}
	root, err := os.MkdirTemp("", "breakfix-publisher-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root) //nolint:errcheck
	archive := filepath.Join(root, "image.oci.tar")
	if err := download(ctx, cfg.ServerURL, cfg.VerifyTaskID, cfg.DownloadGrant, archive); err != nil {
		return fmt.Errorf("download OCI archive: %w", err)
	}
	if err := registry.ValidateOCIArchive(archive); err != nil {
		return &ArtifactError{Err: fmt.Errorf("invalid Builder OCI archive: %w", err)}
	}
	if err := (registry.Client{Insecure: cfg.RegistryInsecure, Credentials: cfg.RegistryCredentials}).PushOCIArchive(ctx, cfg.StagingImage, archive); err != nil {
		return fmt.Errorf("publish staging image: %w", err)
	}
	return nil
}

func download(ctx context.Context, serverURL, taskID, grant, destination string) error {
	url := strings.TrimRight(serverURL, "/") + "/api/internal/verify-builds/" + taskID + "/image"
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
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("download OCI archive: status %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
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
