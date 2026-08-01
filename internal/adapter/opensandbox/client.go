// Package opensandbox is the Server-only wrapper around the official SDK.
// Workers access it exclusively through lease-checked Server APIs.
package opensandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sdk "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
)

const workspaceMountPath = "/workspace"

const workspaceArchivePath = "/tmp/breakfix-generator-candidate.tar.gz"

const workspaceRunMetadataKey = "breakfix.generator_run_id"

type Client struct {
	connection sdk.ConnectionConfig
	lifecycle  *sdk.LifecycleClient
	image      string
	cpu        string
	memory     string
}

func New(cfg config.OpenSandboxConfig) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("opensandbox lifecycle API key is required")
	}
	connection := sdk.ConnectionConfig{
		Domain:         strings.TrimRight(cfg.BaseURL, "/"),
		Protocol:       "http",
		APIKey:         cfg.APIKey,
		UseServerProxy: true,
		RequestTimeout: 30 * time.Second,
		DisableMetrics: true,
	}
	return &Client{
		connection: connection,
		lifecycle:  sdk.NewLifecycleClient(connection.GetBaseURL()+"/v1", connection.GetAPIKey(), sdk.WithTimeout(30*time.Second)),
		image:      strings.TrimSpace(cfg.WorkspaceImage),
		cpu:        strings.TrimSpace(cfg.WorkspaceCPU),
		memory:     strings.TrimSpace(cfg.WorkspaceMemory),
	}, nil
}

// FindWorkspace returns the one Sandbox that Server previously created for a
// Generator Run. Metadata recovery closes the narrow window where OpenSandbox
// accepted a create request but Server crashed before it persisted the ID.
func (c *Client) FindWorkspace(ctx context.Context, generatorRunID string) (string, bool, error) {
	if c == nil || c.lifecycle == nil || strings.TrimSpace(generatorRunID) == "" {
		return "", false, errors.New("opensandbox workspace client and generator run id are required")
	}
	result, err := c.lifecycle.ListSandboxes(ctx, sdk.ListOptions{
		Metadata: map[string]string{workspaceRunMetadataKey: strings.TrimSpace(generatorRunID)},
		Page:     1,
		PageSize: 2,
	})
	if err != nil {
		return "", false, fmt.Errorf("list generator workspaces: %w", err)
	}
	if result.Pagination.TotalItems > 1 || len(result.Items) > 1 {
		return "", false, fmt.Errorf("generator run %q owns multiple opensandbox workspaces", generatorRunID)
	}
	if len(result.Items) == 0 {
		return "", false, nil
	}
	if sandboxID := strings.TrimSpace(result.Items[0].ID); sandboxID != "" {
		return sandboxID, true, nil
	}
	return "", false, errors.New("opensandbox returned a workspace without an id")
}

// CreateWorkspace creates a Sandbox but deliberately does not wait for it to
// become ready. Server persists the returned ID before waiting, so a worker
// retry resumes the same Sandbox instead of creating a duplicate.
func (c *Client) CreateWorkspace(ctx context.Context, pvcName, generatorRunID string) (string, error) {
	if c == nil || c.lifecycle == nil || strings.TrimSpace(pvcName) == "" || strings.TrimSpace(generatorRunID) == "" {
		return "", errors.New("opensandbox workspace client, pvc name, and generator run id are required")
	}
	createIfMissing := false
	sandbox, err := c.lifecycle.CreateSandbox(ctx, sdk.CreateSandboxRequest{
		Image:          &sdk.ImageSpec{URI: c.image},
		Entrypoint:     []string{"sh", "-c", "while true; do sleep 3600; done"},
		ResourceLimits: sdk.ResourceLimits{"cpu": c.cpu, "memory": c.memory},
		Metadata:       map[string]string{workspaceRunMetadataKey: strings.TrimSpace(generatorRunID)},
		NetworkPolicy:  &sdk.NetworkPolicy{DefaultAction: "deny"},
		Volumes: []sdk.Volume{{
			Name:      "workspace",
			MountPath: workspaceMountPath,
			PVC:       &sdk.PVC{ClaimName: pvcName, CreateIfNotExists: &createIfMissing},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("create generator workspace: %w", err)
	}
	if sandboxID := strings.TrimSpace(sandbox.ID); sandboxID != "" {
		return sandboxID, nil
	}
	return "", errors.New("opensandbox created a workspace without an id")
}

// WaitWorkspace waits for an already persisted Sandbox ID to become usable.
// The caller owns the overall provisioning deadline; each Lifecycle request
// has its normal bounded transport timeout.
func (c *Client) WaitWorkspace(ctx context.Context, sandboxID string) error {
	if c == nil || c.lifecycle == nil || strings.TrimSpace(sandboxID) == "" {
		return errors.New("opensandbox workspace client and sandbox id are required")
	}
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for generator workspace %s: %w", sandboxID, err)
		}
		info, err := c.lifecycle.GetSandbox(ctx, sandboxID)
		if err != nil {
			return fmt.Errorf("get generator workspace %s: %w", sandboxID, err)
		}
		switch info.Status.State {
		case sdk.StateFailed, sdk.StateTerminated:
			return fmt.Errorf("generator workspace %s entered terminal state %s: %s", sandboxID, info.Status.State, strings.TrimSpace(info.Status.Reason))
		case sdk.StateRunning:
			sandbox, err := c.connect(ctx, sandboxID)
			if err == nil {
				err = sandbox.Ping(ctx)
			}
			if err == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for generator workspace %s: %w", sandboxID, ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *Client) DeleteWorkspace(ctx context.Context, sandboxID string) error {
	if c == nil || c.lifecycle == nil || strings.TrimSpace(sandboxID) == "" {
		return nil
	}
	err := c.lifecycle.DeleteSandbox(ctx, sandboxID)
	if IsNotFound(err) {
		return nil
	}
	return err
}

func (c *Client) ReadFile(ctx context.Context, sandboxID, path string) ([]byte, error) {
	sandbox, err := c.connect(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	reader, err := sandbox.DownloadFile(ctx, path, "")
	if err != nil {
		return nil, err
	}
	content, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close sandbox file reader: %w", closeErr)
	}
	return content, nil
}

func (c *Client) WriteFile(ctx context.Context, sandboxID, path string, content []byte, mode int) error {
	return c.uploadFile(ctx, sandboxID, path, content, mode)
}

func (c *Client) uploadFile(ctx context.Context, sandboxID, path string, content []byte, mode int) error {
	sandbox, err := c.connect(ctx, sandboxID)
	if err != nil {
		return err
	}
	return sandbox.UploadFile(ctx, strings.NewReader(string(content)), sdk.UploadFileOptions{
		FileName: "content",
		Metadata: sdk.FileMetadata{Path: path, Mode: providerFileMode(mode)},
	})
}

// providerFileMode converts Go's numeric permission bits to the OpenSandbox
// API notation. The provider accepts decimal JSON numbers whose digits denote
// an octal Unix mode, for example 644 rather than Go's decimal value 420.
func providerFileMode(mode int) int {
	mode &= 0o777
	return ((mode>>6)&0o7)*100 + ((mode>>3)&0o7)*10 + (mode & 0o7)
}

// ResetWorkspace atomically replaces the sandbox's visible workspace with the
// supplied immutable artifact. The archive transfer and shell operations stay
// Server-side, so the Generate Worker never receives a Sandbox connection.
func (c *Client) ResetWorkspace(ctx context.Context, sandboxID string, archive []byte) error {
	if _, err := c.Execute(ctx, sandboxID, "rm -rf /workspace/* /workspace/.[!.]* /workspace/..?*; mkdir -p /workspace", "/workspace", nil); err != nil {
		return fmt.Errorf("clear generator workspace: %w", err)
	}
	if len(archive) == 0 {
		return nil
	}
	if err := c.uploadFile(ctx, sandboxID, workspaceArchivePath, archive, 0o600); err != nil {
		return fmt.Errorf("upload generator seed artifact: %w", err)
	}
	result, err := c.Execute(ctx, sandboxID, "tar -xzf "+workspaceArchivePath+" -C /workspace && rm -f "+workspaceArchivePath, "/workspace", nil)
	if err != nil {
		return fmt.Errorf("extract generator seed artifact: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("extract generator seed artifact exited with code %d: %s", result.ExitCode, result.Output)
	}
	return nil
}

// ArchiveWorkspace serializes the current workspace through OpenSandbox. It
// preserves executable modes for challenge scripts and never exposes provider
// credentials or Sandbox IDs to the Worker.
func (c *Client) ArchiveWorkspace(ctx context.Context, sandboxID string) ([]byte, error) {
	result, err := c.Execute(ctx, sandboxID, "tar -C /workspace -czf "+workspaceArchivePath+" .", "/workspace", nil)
	if err != nil {
		return nil, fmt.Errorf("archive generator workspace: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("archive generator workspace exited with code %d: %s", result.ExitCode, result.Output)
	}
	archive, err := c.ReadFile(ctx, sandboxID, workspaceArchivePath)
	if err != nil {
		return nil, fmt.Errorf("download generator workspace archive: %w", err)
	}
	_, _ = c.Execute(context.Background(), sandboxID, "rm -f "+workspaceArchivePath, "/workspace", nil)
	return archive, nil
}

type Execution struct {
	ExitCode int
	Output   string
}

func (c *Client) Execute(ctx context.Context, sandboxID, command, cwd string, onStdout func(string) error) (Execution, error) {
	sandbox, err := c.connect(ctx, sandboxID)
	if err != nil {
		return Execution{}, err
	}
	result, err := sandbox.RunCommandWithOpts(ctx, sdk.RunCommandRequest{Command: command, Cwd: cwd}, &sdk.ExecutionHandlers{
		OnStdout: func(message sdk.OutputMessage) error {
			if onStdout == nil {
				return nil
			}
			return onStdout(message.Text)
		},
	})
	if err != nil {
		return Execution{}, err
	}
	if result.ExitCode == nil {
		return Execution{}, errors.New("opensandbox command returned no exit code")
	}
	return Execution{ExitCode: *result.ExitCode, Output: result.Text()}, nil
}

func (c *Client) connect(ctx context.Context, sandboxID string) (*sdk.Sandbox, error) {
	if c == nil || strings.TrimSpace(sandboxID) == "" {
		return nil, errors.New("opensandbox sandbox id is required")
	}
	return sdk.ConnectSandbox(ctx, c.connection, sandboxID)
}

func IsNotFound(err error) bool {
	var apiErr *sdk.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

func WorkspacePath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "./")
	if path == "" || path == "." {
		return workspaceMountPath
	}
	return strings.TrimRight(workspaceMountPath, "/") + "/" + strings.TrimLeft(path, "/")
}

func ValidateWorkspacePath(path string) error {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "./")
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\\") {
		return errors.New("workspace path must be a non-empty relative slash path")
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid workspace path %q", path)
		}
	}
	return nil
}
