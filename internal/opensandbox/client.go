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
	"github.com/breakfix/breakfix/internal/config"
)

const workspaceMountPath = "/workspace"

type Client struct {
	connection sdk.ConnectionConfig
	lifecycle  *sdk.LifecycleClient
	image      string
	timeout    int
}

func New(cfg config.OpenSandboxConfig) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("opensandbox lifecycle API key is required")
	}
	timeout, err := cfg.Timeout()
	if err != nil {
		return nil, err
	}
	seconds := int(timeout.Seconds())
	if seconds < 60 {
		return nil, errors.New("opensandbox workspace_timeout must be at least 60s")
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
		timeout:    seconds,
	}, nil
}

func (c *Client) CreateWorkspace(ctx context.Context, pvcName string) (string, error) {
	if c == nil || c.lifecycle == nil || strings.TrimSpace(pvcName) == "" {
		return "", errors.New("opensandbox workspace client and pvc name are required")
	}
	createIfMissing := false
	sandbox, err := sdk.CreateSandbox(ctx, c.connection, sdk.SandboxCreateOptions{
		Image:          c.image,
		Entrypoint:     []string{"sh", "-c", "while true; do sleep 3600; done"},
		ResourceLimits: sdk.ResourceLimits{"cpu": "1", "memory": "1Gi"},
		TimeoutSeconds: &c.timeout,
		ReadyTimeout:   2 * time.Minute,
		NetworkPolicy:  &sdk.NetworkPolicy{DefaultAction: "deny"},
		Volumes: []sdk.Volume{{
			Name:      "workspace",
			MountPath: workspaceMountPath,
			PVC:       &sdk.PVC{ClaimName: pvcName, CreateIfNotExists: &createIfMissing},
		}},
	})
	if err != nil {
		return "", err
	}
	return sandbox.ID(), nil
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
	defer reader.Close()
	return io.ReadAll(reader)
}

func (c *Client) WriteFile(ctx context.Context, sandboxID, path string, content []byte, mode int) error {
	sandbox, err := c.connect(ctx, sandboxID)
	if err != nil {
		return err
	}
	return sandbox.UploadFile(ctx, strings.NewReader(string(content)), sdk.UploadFileOptions{
		FileName: "content",
		Metadata: sdk.FileMetadata{Path: path, Mode: mode},
	})
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
	if path == "" || path == "." {
		return workspaceMountPath
	}
	return strings.TrimRight(workspaceMountPath, "/") + "/" + strings.TrimLeft(path, "/")
}

func ValidateWorkspacePath(path string) error {
	path = strings.TrimSpace(path)
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
