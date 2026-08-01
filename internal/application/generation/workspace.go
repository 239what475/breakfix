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

// Ensure creates or resumes the one workspace belonging to a Generator Run.
// A pending record makes PVC cleanup durable if provisioning cannot complete.
func (m *Manager) Ensure(ctx context.Context, generatorRunID string) (*domain.Workspace, error) {
	if m == nil {
		return nil, errors.New("workspace manager is not configured")
	}
	generatorRunID = strings.TrimSpace(generatorRunID)
	if generatorRunID == "" {
		return nil, errors.New("generator run id is required")
	}
	record, err := m.repo.GetGeneratorWorkspace(ctx, generatorRunID)
	if errors.Is(err, domain.ErrWorkspaceNotFound) {
		now := m.now()
		record, err = m.repo.CreateGeneratorWorkspace(ctx, domain.Workspace{
			GeneratorRunID:    generatorRunID,
			Namespace:         m.namespace,
			PVCName:           domain.NewWorkspacePVCName(generatorRunID),
			State:             domain.WorkspacePending,
			ProvisionDeadline: now.Add(m.provisionTimeout),
			CreatedAt:         now,
			UpdatedAt:         now,
		})
	}
	if err != nil {
		return nil, err
	}
	if record.State == domain.WorkspaceDeleted || record.State == domain.WorkspaceDeleting {
		return nil, fmt.Errorf("generator workspace is %s", record.State)
	}
	provisionCtx, cancel := m.provisionContext(ctx, record.ProvisionDeadline)
	defer cancel()
	if err := m.pvcs.EnsureWorkspacePVC(provisionCtx, record.Namespace, record.PVCName, record.GeneratorRunID, m.storage); err != nil {
		return nil, fmt.Errorf("ensure generator workspace pvc: %w", err)
	}
	if record.State == domain.WorkspaceActive && strings.TrimSpace(record.SandboxID) != "" {
		return record, nil
	}
	if strings.TrimSpace(record.SandboxID) == "" {
		sandboxID, found, err := m.sandboxes.FindWorkspace(provisionCtx, record.GeneratorRunID)
		if err != nil {
			return nil, fmt.Errorf("find generator sandbox: %w", err)
		}
		if !found {
			sandboxID, err = m.sandboxes.CreateWorkspace(provisionCtx, record.PVCName, record.GeneratorRunID)
			if err != nil {
				return nil, fmt.Errorf("create generator sandbox: %w", err)
			}
		}
		if err := m.repo.RecordGeneratorWorkspaceSandbox(provisionCtx, record.GeneratorRunID, sandboxID, m.now()); err != nil {
			_ = m.sandboxes.DeleteWorkspace(context.Background(), sandboxID)
			return nil, fmt.Errorf("record generator sandbox: %w", err)
		}
		record, err = m.repo.GetGeneratorWorkspace(provisionCtx, record.GeneratorRunID)
		if err != nil {
			return nil, err
		}
	}
	if err := m.sandboxes.WaitWorkspace(provisionCtx, record.SandboxID); err != nil {
		return nil, fmt.Errorf("wait for generator sandbox: %w", err)
	}
	if err := m.repo.ActivateGeneratorWorkspace(provisionCtx, record.GeneratorRunID, record.SandboxID, m.now()); err != nil {
		_ = m.sandboxes.DeleteWorkspace(context.Background(), record.SandboxID)
		return nil, fmt.Errorf("record generator sandbox: %w", err)
	}
	return m.repo.GetGeneratorWorkspace(ctx, record.GeneratorRunID)
}

func (m *Manager) Cleanup(ctx context.Context, generatorRunID string) error {
	if m == nil {
		return errors.New("workspace manager is not configured")
	}
	record, err := m.repo.BeginGeneratorWorkspaceCleanup(ctx, generatorRunID, m.now())
	if errors.Is(err, domain.ErrWorkspaceNotFound) || (err == nil && record.State == domain.WorkspaceDeleted) {
		return nil
	}
	if err != nil {
		return err
	}
	sandboxID := strings.TrimSpace(record.SandboxID)
	if sandboxID == "" {
		var found bool
		sandboxID, found, err = m.sandboxes.FindWorkspace(ctx, record.GeneratorRunID)
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
	return m.repo.MarkGeneratorWorkspaceDeleted(ctx, record.GeneratorRunID, m.now())
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
		if err := m.Cleanup(ctx, record.GeneratorRunID); err != nil {
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
