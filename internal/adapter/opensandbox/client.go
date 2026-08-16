// Package opensandbox is the Server-only wrapper around the official SDK.
// Workers access it exclusively through lease-checked Server APIs.
package opensandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	sdk "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/content/workspacearchive"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/toolresult"
)

const workspaceMountPath = "/workspace"

const workspaceIDMetadataKey = "breakfix.generator_workspace_id"

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
// Generator workspace. Metadata recovery closes the narrow window where OpenSandbox
// accepted a create request but Server crashed before it persisted the ID.
func (c *Client) FindWorkspace(ctx context.Context, workspaceID string) (string, bool, error) {
	if c == nil || c.lifecycle == nil || strings.TrimSpace(workspaceID) == "" {
		return "", false, errors.New("opensandbox workspace client and workspace id are required")
	}
	result, err := c.lifecycle.ListSandboxes(ctx, sdk.ListOptions{
		Metadata: map[string]string{workspaceIDMetadataKey: strings.TrimSpace(workspaceID)},
		Page:     1,
		PageSize: 2,
	})
	if err != nil {
		return "", false, fmt.Errorf("list generator workspaces: %w", err)
	}
	if result.Pagination.TotalItems > 1 || len(result.Items) > 1 {
		return "", false, fmt.Errorf("generator workspace %q owns multiple opensandbox sandboxes", workspaceID)
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
func (c *Client) CreateWorkspace(ctx context.Context, pvcName, workspaceID string) (string, error) {
	if c == nil || c.lifecycle == nil || strings.TrimSpace(pvcName) == "" || strings.TrimSpace(workspaceID) == "" {
		return "", errors.New("opensandbox workspace client, pvc name, and workspace id are required")
	}
	createIfMissing := false
	sandbox, err := c.lifecycle.CreateSandbox(ctx, sdk.CreateSandboxRequest{
		Image:          &sdk.ImageSpec{URI: c.image},
		Entrypoint:     []string{"sh", "-c", "while true; do sleep 3600; done"},
		ResourceLimits: sdk.ResourceLimits{"cpu": c.cpu, "memory": c.memory},
		Metadata:       map[string]string{workspaceIDMetadataKey: strings.TrimSpace(workspaceID)},
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
		return nil, classifyWorkspaceOperationError(err)
	}
	content, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, classifyWorkspaceOperationError(readErr)
	}
	if closeErr != nil {
		return nil, classifyWorkspaceOperationError(fmt.Errorf("close sandbox file reader: %w", closeErr))
	}
	return content, nil
}

// ListWorkspaceFiles returns the provider's visible workspace tree without
// exposing its absolute paths or metadata. The Server remains responsible for
// deciding which workflow and turn may use the Sandbox.
func (c *Client) ListWorkspaceFiles(ctx context.Context, sandboxID string) ([]generation.WorkspaceFile, error) {
	sandbox, err := c.connect(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	entries, err := sandbox.ListDirectoryWithDepth(ctx, workspaceMountPath, 32)
	if err != nil {
		return nil, classifyWorkspaceOperationError(err)
	}
	files := make([]generation.WorkspaceFile, 0, len(entries))
	for _, entry := range entries {
		path := strings.TrimPrefix(strings.TrimSpace(entry.Path), workspaceMountPath+"/")
		if path == "" || path == strings.TrimSpace(entry.Path) {
			return nil, fmt.Errorf("workspace provider returned an invalid path %q", entry.Path)
		}
		if err := ValidateWorkspacePath(path); err != nil {
			return nil, fmt.Errorf("workspace provider returned an invalid path %q: %w", entry.Path, err)
		}
		kind := strings.ToLower(strings.TrimSpace(entry.Type))
		files = append(files, generation.WorkspaceFile{
			Path:      path,
			Directory: kind == "directory" || kind == "dir",
			Size:      entry.Size,
		})
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return files, nil
}

func (c *Client) WriteFile(ctx context.Context, sandboxID, path string, content []byte, mode int) error {
	return c.uploadFile(ctx, sandboxID, path, content, mode)
}

func (c *Client) uploadFile(ctx context.Context, sandboxID, path string, content []byte, mode int) error {
	sandbox, err := c.connect(ctx, sandboxID)
	if err != nil {
		return err
	}
	err = sandbox.UploadFile(ctx, bytes.NewReader(content), sdk.UploadFileOptions{
		FileName: "content",
		Metadata: sdk.FileMetadata{Path: path, Mode: providerFileMode(mode)},
	})
	return classifyWorkspaceOperationError(err)
}

// providerFileMode converts Go's numeric permission bits to the OpenSandbox
// API notation. The provider accepts decimal JSON numbers whose digits denote
// an octal Unix mode, for example 644 rather than Go's decimal value 420.
func providerFileMode(mode int) int {
	mode &= 0o777
	return ((mode>>6)&0o7)*100 + ((mode>>3)&0o7)*10 + (mode & 0o7)
}

// ResetWorkspace replaces the sandbox's visible workspace with a verified
// canonical archive. It never delegates archive extraction to a shell.
func (c *Client) ResetWorkspace(ctx context.Context, sandboxID string, archive []byte) error {
	entries := make([]workspacearchive.Entry, 0)
	if len(archive) != 0 {
		var err error
		entries, err = workspacearchive.Decode(archive)
		if err != nil {
			return fmt.Errorf("validate generator workspace seed: %w", err)
		}
	}
	sandbox, err := c.connect(ctx, sandboxID)
	if err != nil {
		return err
	}
	if err := restoreWorkspace(ctx, sandbox, entries); err != nil {
		return fmt.Errorf("restore generator workspace: %w", classifyWorkspaceOperationError(err))
	}
	return nil
}

// ArchiveWorkspace serializes the current workspace through a canonical Go
// encoder. It preserves executable modes without relying on provider shell
// tools or a shared temporary archive path.
func (c *Client) ArchiveWorkspace(ctx context.Context, sandboxID string) ([]byte, error) {
	sandbox, err := c.connect(ctx, sandboxID)
	if err != nil {
		return nil, err
	}
	archive, err := archiveWorkspace(ctx, sandbox)
	if err != nil {
		return nil, fmt.Errorf("archive generator workspace: %w", classifyWorkspaceOperationError(err))
	}
	return archive, nil
}

type workspaceFilesystem interface {
	ListDirectory(context.Context, string) ([]sdk.FileInfo, error)
	DownloadFile(context.Context, string, string, ...sdk.DownloadFileOptions) (io.ReadCloser, error)
	UploadFile(context.Context, io.Reader, sdk.UploadFileOptions) error
	CreateDirectory(context.Context, string, int) error
	DeleteFiles(context.Context, []string) error
	DeleteDirectory(context.Context, string) error
}

func archiveWorkspace(ctx context.Context, sandbox workspaceFilesystem) ([]byte, error) {
	if sandbox == nil {
		return nil, errors.New("workspace filesystem is required")
	}
	type directory struct {
		remote   string
		relative string
	}
	pending := []directory{{remote: workspaceMountPath}}
	seen := make(map[string]struct{})
	entries := make([]workspacearchive.Entry, 0)
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		children, err := sandbox.ListDirectory(ctx, current.remote)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			relative, err := archiveRelativePath(child.Path)
			if err != nil {
				return nil, err
			}
			if pathDirectory(relative) != current.relative {
				return nil, fmt.Errorf("workspace provider returned non-child path %q while listing %q", child.Path, current.remote)
			}
			if _, exists := seen[relative]; exists {
				return nil, fmt.Errorf("workspace provider returned duplicate path %q", child.Path)
			}
			seen[relative] = struct{}{}
			mode, err := workspaceMode(child.Mode)
			if err != nil {
				return nil, fmt.Errorf("workspace provider returned invalid mode for %q: %w", child.Path, err)
			}
			switch strings.ToLower(strings.TrimSpace(child.Type)) {
			case "directory", "dir":
				entries = append(entries, workspacearchive.Entry{Path: relative, Directory: true, Mode: mode})
				pending = append(pending, directory{remote: strings.TrimRight(child.Path, "/"), relative: relative})
			case "file", "regular":
				content, err := downloadWorkspaceFile(ctx, sandbox, child.Path)
				if err != nil {
					return nil, err
				}
				entries = append(entries, workspacearchive.Entry{Path: relative, Mode: mode, Content: content})
			default:
				return nil, fmt.Errorf("workspace provider returned unsupported entry type %q for %q", child.Type, child.Path)
			}
		}
	}
	return workspacearchive.Encode(entries)
}

func restoreWorkspace(ctx context.Context, sandbox workspaceFilesystem, entries []workspacearchive.Entry) error {
	if sandbox == nil {
		return errors.New("workspace filesystem is required")
	}
	if err := clearWorkspace(ctx, sandbox); err != nil {
		return err
	}
	for _, entry := range entries {
		remote := WorkspacePath(entry.Path)
		if entry.Directory {
			if err := sandbox.CreateDirectory(ctx, remote, providerFileMode(entry.Mode)); err != nil {
				return err
			}
			continue
		}
		if err := sandbox.UploadFile(ctx, bytes.NewReader(entry.Content), sdk.UploadFileOptions{
			FileName: "workspace-content",
			Metadata: sdk.FileMetadata{Path: remote, Mode: providerFileMode(entry.Mode)},
		}); err != nil {
			return err
		}
	}
	return nil
}

func clearWorkspace(ctx context.Context, sandbox workspaceFilesystem) error {
	entries, err := sandbox.ListDirectory(ctx, workspaceMountPath)
	if err != nil {
		return err
	}
	files := make([]string, 0, len(entries))
	directories := make([]string, 0, len(entries))
	for _, entry := range entries {
		relative, err := archiveRelativePath(entry.Path)
		if err != nil {
			return err
		}
		if pathDirectory(relative) != "" {
			return fmt.Errorf("workspace provider returned non-child path %q while clearing workspace", entry.Path)
		}
		switch strings.ToLower(strings.TrimSpace(entry.Type)) {
		case "directory", "dir":
			directories = append(directories, entry.Path)
		default:
			files = append(files, entry.Path)
		}
	}
	if len(files) > 0 {
		if err := sandbox.DeleteFiles(ctx, files); err != nil {
			return err
		}
	}
	for _, directory := range directories {
		if err := sandbox.DeleteDirectory(ctx, directory); err != nil {
			return err
		}
	}
	return nil
}

func downloadWorkspaceFile(ctx context.Context, sandbox workspaceFilesystem, remote string) ([]byte, error) {
	reader, err := sandbox.DownloadFile(ctx, remote, "")
	if err != nil {
		return nil, err
	}
	content, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close workspace file reader: %w", closeErr)
	}
	return content, nil
}

func archiveRelativePath(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	prefix := workspaceMountPath + "/"
	if !strings.HasPrefix(remote, prefix) {
		return "", fmt.Errorf("workspace provider returned invalid path %q", remote)
	}
	relative := strings.TrimPrefix(remote, prefix)
	if err := ValidateWorkspacePath(relative); err != nil {
		return "", fmt.Errorf("workspace provider returned invalid path %q: %w", remote, err)
	}
	return relative, nil
}

func pathDirectory(value string) string {
	if index := strings.LastIndex(value, "/"); index >= 0 {
		return value[:index]
	}
	return ""
}

func workspaceMode(value int) (int, error) {
	if value < 0 || value > 777 {
		return 0, errors.New("mode is outside Unix permission bits")
	}
	owner, group, other := value/100, (value/10)%10, value%10
	if owner > 7 || group > 7 || other > 7 {
		return 0, errors.New("mode is not octal")
	}
	return owner<<6 | group<<3 | other, nil
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
		return Execution{}, classifyWorkspaceOperationError(err)
	}
	if result.ExitCode == nil {
		return Execution{}, toolresult.Mark(errors.New("opensandbox command returned no exit code"), toolresult.Failed)
	}
	return Execution{ExitCode: *result.ExitCode, Output: result.Text()}, nil
}

// ExecuteWorkspace is the application-facing shape of Execute. It keeps the
// OpenSandbox SDK result type inside this adapter while exposing only the
// typed command result required by the Server workspace service.
func (c *Client) ExecuteWorkspace(ctx context.Context, sandboxID, command, cwd string, onStdout func(string) error) (int, string, error) {
	result, err := c.Execute(ctx, sandboxID, command, cwd, onStdout)
	if err != nil {
		return 0, "", err
	}
	return result.ExitCode, result.Output, nil
}

func (c *Client) connect(ctx context.Context, sandboxID string) (*sdk.Sandbox, error) {
	if c == nil || strings.TrimSpace(sandboxID) == "" {
		return nil, errors.New("opensandbox sandbox id is required")
	}
	sandbox, err := sdk.ConnectSandbox(ctx, c.connection, sandboxID)
	if err != nil {
		return nil, classifyWorkspaceOperationError(err)
	}
	return sandbox, nil
}

func classifyWorkspaceOperationError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *sdk.APIError
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == http.StatusRequestTimeout || apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode >= http.StatusInternalServerError {
			return toolresult.Mark(err, toolresult.Unknown)
		}
		return toolresult.Mark(err, toolresult.Failed)
	}
	return toolresult.Mark(err, toolresult.StatusForError(err))
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
