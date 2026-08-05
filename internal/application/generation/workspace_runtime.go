package generation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/candidate"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

// GeneratorWorkspaceStore is the narrow durable boundary used by Generator
// tools. It is intentionally claim-fenced: the tools cannot browse arbitrary
// workflows or candidate archives.
type GeneratorWorkspaceStore interface {
	GetGenerationClaim(context.Context, string, domain.LeaseCredential, time.Time) (*domain.Claim, error)
	LoadGenerationContext(context.Context, domain.Claim, time.Time) (*domain.Context, error)
	GetCandidateRevision(context.Context, string) (*domain.Revision, error)
}

// GeneratorWorkspaceSandbox is the Server-only file and command surface for
// an already-created OpenSandbox workspace. It carries no lifecycle API or
// Kubernetes credential into an Agent tool.
type GeneratorWorkspaceSandbox interface {
	ReadFile(context.Context, string, string) ([]byte, error)
	WriteFile(context.Context, string, string, []byte, int) error
	ArchiveWorkspace(context.Context, string) ([]byte, error)
	ExecuteWorkspace(context.Context, string, string, string, func(string) error) (int, string, error)
}

// WorkspaceRuntime implements the Generator's typed tool surface directly in
// Server. It replaces the former Worker-to-Server HTTP workspace proxy.
type WorkspaceRuntime struct {
	store     GeneratorWorkspaceStore
	manager   *Manager
	sandboxes GeneratorWorkspaceSandbox
	now       func() time.Time
}

func NewWorkspaceRuntime(store GeneratorWorkspaceStore, manager *Manager, sandboxes GeneratorWorkspaceSandbox) (*WorkspaceRuntime, error) {
	if store == nil || manager == nil || sandboxes == nil {
		return nil, errors.New("generator workspace runtime requires store, manager, and sandbox service")
	}
	return &WorkspaceRuntime{store: store, manager: manager, sandboxes: sandboxes, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (r *WorkspaceRuntime) LoadWorkspace(ctx context.Context, claim domain.Claim) (WorkspaceContext, error) {
	current, err := r.currentClaim(ctx, claim)
	if err != nil {
		return WorkspaceContext{}, err
	}
	value, err := r.store.LoadGenerationContext(ctx, *current, r.now())
	if err != nil {
		return WorkspaceContext{}, err
	}
	var seed []byte
	if candidateID := strings.TrimSpace(current.Workflow.CandidateRevisionID); candidateID != "" {
		revision, err := r.store.GetCandidateRevision(ctx, candidateID)
		if err != nil {
			return WorkspaceContext{}, fmt.Errorf("read generator repair candidate: %w", err)
		}
		if revision.Source != current.Workflow.Source || revision.SourceRevision != current.Workflow.SourceRevision {
			return WorkspaceContext{}, errors.New("generator repair candidate does not belong to workflow lineage")
		}
		seed, err = candidate.ReadArchive(revision.ArchivePath, revision.ArchiveSHA256)
		if err != nil {
			return WorkspaceContext{}, fmt.Errorf("read generator repair archive: %w", err)
		}
	}
	if _, err := r.manager.Ensure(ctx, current.Workflow.ID, seed); err != nil {
		return WorkspaceContext{}, err
	}
	return WorkspaceContext{Plan: value.Plan, Feedback: value.Feedback}, nil
}

func (r *WorkspaceRuntime) ReadFile(ctx context.Context, claim domain.Claim, path string, offset, limit int) (FileReadResponse, error) {
	record, err := r.workspaceForClaim(ctx, claim)
	if err != nil {
		return FileReadResponse{}, err
	}
	path, err = workspacePath(path)
	if err != nil {
		return FileReadResponse{}, err
	}
	content, err := r.sandboxes.ReadFile(ctx, record.SandboxID, path)
	if err != nil {
		return FileReadResponse{}, err
	}
	return FileReadResponse{Content: selectWorkspaceLines(string(content), offset, limit)}, nil
}

func (r *WorkspaceRuntime) WriteFile(ctx context.Context, claim domain.Claim, path, content string) error {
	record, err := r.workspaceForClaim(ctx, claim)
	if err != nil {
		return err
	}
	path, err = workspacePath(path)
	if err != nil {
		return err
	}
	return r.sandboxes.WriteFile(ctx, record.SandboxID, path, []byte(content), 0o644)
}

func (r *WorkspaceRuntime) Execute(ctx context.Context, claim domain.Claim, command string, consume func(ExecuteEvent) error) error {
	record, err := r.workspaceForClaim(ctx, claim)
	if err != nil {
		return err
	}
	if strings.TrimSpace(command) == "" {
		return errors.New("generator command is required")
	}
	streamed := false
	exitCode, output, err := r.sandboxes.ExecuteWorkspace(ctx, record.SandboxID, command, "/workspace", func(content string) error {
		streamed = streamed || content != ""
		if content == "" || consume == nil {
			return nil
		}
		return consume(ExecuteEvent{Type: "stdout", Content: content})
	})
	if err != nil {
		return err
	}
	if consume == nil {
		return nil
	}
	if streamed {
		output = ""
	}
	return consume(ExecuteEvent{Type: "result", Content: output, ExitCode: &exitCode})
}

func (r *WorkspaceRuntime) ArchiveWorkspace(ctx context.Context, claim domain.Claim) (ArchiveResponse, error) {
	record, err := r.workspaceForClaim(ctx, claim)
	if err != nil {
		return ArchiveResponse{}, err
	}
	archive, err := r.sandboxes.ArchiveWorkspace(ctx, record.SandboxID)
	if err != nil {
		return ArchiveResponse{}, err
	}
	return ArchiveResponse{Archive: archive}, nil
}

func (r *WorkspaceRuntime) workspaceForClaim(ctx context.Context, claim domain.Claim) (*domain.Workspace, error) {
	current, err := r.currentClaim(ctx, claim)
	if err != nil {
		return nil, err
	}
	record, err := r.manager.repo.GetCurrentGeneratorWorkspace(ctx, current.Workflow.ID)
	if err != nil {
		return nil, err
	}
	if record.State != domain.WorkspaceActive || strings.TrimSpace(record.SandboxID) == "" {
		return nil, errors.New("generator workspace is not active")
	}
	return record, nil
}

func (r *WorkspaceRuntime) currentClaim(ctx context.Context, claim domain.Claim) (*domain.Claim, error) {
	if !claim.Valid() || claim.Workflow.State != domain.StateGenerating {
		return nil, errors.New("generator workspace requires a Generating workflow claim")
	}
	current, err := r.store.GetGenerationClaim(ctx, claim.Workflow.ID, claim.LeaseCredential, r.now())
	if err != nil {
		return nil, err
	}
	if current.Workflow.State != domain.StateGenerating || current.Workflow.ActiveAgentRunID != claim.Workflow.ActiveAgentRunID {
		return nil, domain.ErrLeaseLost
	}
	return current, nil
}

func workspacePath(value string) (string, error) {
	value = strings.TrimSpace(strings.TrimPrefix(value, "./"))
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", errors.New("workspace path must be a non-empty relative slash path")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid workspace path %q", value)
		}
	}
	return "/workspace/" + value, nil
}

func selectWorkspaceLines(content string, offset, limit int) string {
	if offset < 1 {
		offset = 1
	}
	lines := strings.SplitAfter(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start := offset - 1
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return strings.Join(lines[start:end], "")
}
