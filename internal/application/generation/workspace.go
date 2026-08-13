package generation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

type PVCManager interface {
	EnsureWorkspacePVC(context.Context, string, string, string, string) error
	DeleteWorkspacePVC(context.Context, string, string) error
}

type SandboxManager interface {
	FindWorkspace(context.Context, string) (string, bool, error)
	CreateWorkspace(context.Context, string, string) (string, error)
	WaitWorkspace(context.Context, string) error
	ResetWorkspace(context.Context, string, []byte) error
	DeleteWorkspace(context.Context, string) error
}

type Manager struct {
	repo             WorkspaceRepository
	pvcs             PVCManager
	sandboxes        SandboxManager
	namespace        string
	storage          string
	provisionTimeout time.Duration
	now              func() time.Time
}

type Config struct {
	Namespace        string
	Storage          string
	ProvisionTimeout time.Duration
}

func NewManager(repo WorkspaceRepository, pvcs PVCManager, sandboxes SandboxManager, config Config) (*Manager, error) {
	if repo == nil || pvcs == nil || sandboxes == nil {
		return nil, errors.New("workspace manager requires repository, pvc manager, and sandbox manager")
	}
	if strings.TrimSpace(config.Namespace) == "" || strings.TrimSpace(config.Storage) == "" {
		return nil, errors.New("workspace manager namespace and storage are required")
	}
	if config.ProvisionTimeout <= 0 {
		config.ProvisionTimeout = 5 * time.Minute
	}
	return &Manager{
		repo: repo, pvcs: pvcs, sandboxes: sandboxes,
		namespace: strings.TrimSpace(config.Namespace), storage: strings.TrimSpace(config.Storage),
		provisionTimeout: config.ProvisionTimeout,
		now:              func() time.Time { return time.Now().UTC() },
	}, nil
}

// Ensure creates or resumes the current workspace belonging to a workflow.
// A pending record makes PVC cleanup durable if provisioning cannot complete.
func (m *Manager) Ensure(ctx context.Context, workflowID string, seed []byte) (*domain.Workspace, error) {
	record, _, err := m.EnsureFresh(ctx, workflowID, seed)
	return record, err
}

// EnsureFresh additionally reports whether this call allocated a new durable
// workspace record. The caller may seed a newly created workspace from the
// latest candidate archive; existing workspaces are intentionally untouched.
func (m *Manager) EnsureFresh(ctx context.Context, workflowID string, seed []byte) (*domain.Workspace, bool, error) {
	if m == nil {
		return nil, false, errors.New("workspace manager is not configured")
	}
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return nil, false, errors.New("generation workflow id is required")
	}
	record, err := m.repo.GetCurrentGeneratorWorkspace(ctx, workflowID)
	created := false
	if errors.Is(err, domain.ErrWorkspaceNotFound) {
		now := m.now()
		workspaceID := domain.NewID("generator-workspace")
		record, err = m.repo.CreateGeneratorWorkspace(ctx, domain.Workspace{
			ID:                workspaceID,
			WorkflowID:        workflowID,
			Namespace:         m.namespace,
			PVCName:           domain.NewWorkspacePVCName(workspaceID),
			State:             domain.WorkspacePending,
			ProvisionDeadline: now.Add(m.provisionTimeout),
			CreatedAt:         now,
			UpdatedAt:         now,
		})
		created = err == nil
	}
	if err != nil {
		return nil, false, err
	}
	if record.State == domain.WorkspaceDeleted || record.State == domain.WorkspaceDeleting {
		return nil, false, fmt.Errorf("generator workspace is %s", record.State)
	}
	// An active workspace has already provisioned its PVC and Sandbox. Later
	// Generator turns reuse it without being bounded by the historical
	// provision deadline, which only fences the pending provisioning path.
	if record.State == domain.WorkspaceActive && strings.TrimSpace(record.SandboxID) != "" {
		return record, created, nil
	}
	provisionCtx, cancel := m.provisionContext(ctx, record.ProvisionDeadline)
	defer cancel()
	if err := m.pvcs.EnsureWorkspacePVC(provisionCtx, record.Namespace, record.PVCName, record.WorkflowID, m.storage); err != nil {
		return nil, false, fmt.Errorf("ensure generator workspace pvc: %w", err)
	}
	if strings.TrimSpace(record.SandboxID) == "" {
		sandboxID, found, err := m.sandboxes.FindWorkspace(provisionCtx, record.ID)
		if err != nil {
			return nil, false, fmt.Errorf("find generator sandbox: %w", err)
		}
		if !found {
			sandboxID, err = m.sandboxes.CreateWorkspace(provisionCtx, record.PVCName, record.ID)
			if err != nil {
				return nil, false, fmt.Errorf("create generator sandbox: %w", err)
			}
		}
		if err := m.repo.RecordGeneratorWorkspaceSandbox(provisionCtx, record.ID, sandboxID, m.now()); err != nil {
			_ = m.sandboxes.DeleteWorkspace(context.Background(), sandboxID)
			return nil, false, fmt.Errorf("record generator sandbox: %w", err)
		}
		record, err = m.repo.GetGeneratorWorkspace(provisionCtx, record.ID)
		if err != nil {
			return nil, false, err
		}
	}
	if err := m.sandboxes.WaitWorkspace(provisionCtx, record.SandboxID); err != nil {
		return nil, false, fmt.Errorf("wait for generator sandbox: %w", err)
	}
	// Pending means no model can yet observe the workspace. Seed it before the
	// durable transition to active so a retried provision repeats this exact
	// initialization instead of exposing a partially restored repair context.
	if record.State == domain.WorkspacePending {
		if err := m.sandboxes.ResetWorkspace(provisionCtx, record.SandboxID, seed); err != nil {
			return nil, false, fmt.Errorf("seed generator workspace: %w", err)
		}
	}
	if err := m.repo.ActivateGeneratorWorkspace(provisionCtx, record.ID, record.SandboxID, m.now()); err != nil {
		_ = m.sandboxes.DeleteWorkspace(context.Background(), record.SandboxID)
		return nil, false, fmt.Errorf("record generator sandbox: %w", err)
	}
	record, err = m.repo.GetGeneratorWorkspace(ctx, record.ID)
	return record, created, err
}

func (m *Manager) Retire(ctx context.Context, workflowID string) error {
	if m == nil {
		return errors.New("workspace manager is not configured")
	}
	_, err := m.repo.RetireCurrentGeneratorWorkspace(ctx, strings.TrimSpace(workflowID), m.now())
	if errors.Is(err, domain.ErrWorkspaceNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return nil
}

func (m *Manager) Cleanup(ctx context.Context, workspaceID string) error {
	if m == nil {
		return errors.New("workspace manager is not configured")
	}
	record, err := m.repo.BeginGeneratorWorkspaceCleanup(ctx, workspaceID, m.now())
	if errors.Is(err, domain.ErrWorkspaceNotFound) || (err == nil && record.State == domain.WorkspaceDeleted) {
		return nil
	}
	if err != nil {
		return err
	}
	sandboxID := strings.TrimSpace(record.SandboxID)
	if sandboxID == "" {
		var found bool
		sandboxID, found, err = m.sandboxes.FindWorkspace(ctx, record.ID)
		if err != nil {
			return fmt.Errorf("find generator sandbox for cleanup: %w", err)
		}
		if !found {
			sandboxID = ""
		}
	}
	if sandboxID != "" {
		if err := m.sandboxes.DeleteWorkspace(ctx, sandboxID); err != nil {
			return fmt.Errorf("delete generator sandbox: %w", err)
		}
	}
	if err := m.pvcs.DeleteWorkspacePVC(ctx, record.Namespace, record.PVCName); err != nil {
		return fmt.Errorf("delete generator workspace pvc: %w", err)
	}
	return m.repo.MarkGeneratorWorkspaceDeleted(ctx, record.ID, m.now())
}

func (m *Manager) CleanupDue(ctx context.Context) error {
	if m == nil {
		return errors.New("workspace manager is not configured")
	}
	pending, err := m.repo.ListExpiredPendingGeneratorWorkspaces(ctx, m.now())
	if err != nil {
		return err
	}
	deleting, err := m.repo.ListDeletingGeneratorWorkspaces(ctx)
	if err != nil {
		return err
	}
	terminal, err := m.repo.ListTerminalGeneratorWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, record := range append(append(pending, deleting...), terminal...) {
		if err := m.Cleanup(ctx, record.ID); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) provisionContext(ctx context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	remaining := deadline.Sub(m.now())
	if remaining <= 0 {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		return cancelled, func() {}
	}
	return context.WithTimeout(ctx, remaining)
}
