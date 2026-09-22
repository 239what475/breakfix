package generation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

type PVCManager interface {
	EnsureWorkspacePVC(context.Context, string, string, string, string, WorkspaceOwnerReference) error
	EnsureWorkspacePVCOwner(context.Context, string, string, WorkspaceOwnerReference) error
	DeleteWorkspacePVC(context.Context, string, string) error
}

type SandboxManager interface {
	FindWorkspace(context.Context, string) (string, bool, error)
	CreateWorkspace(context.Context, string, string) (string, error)
	WaitWorkspace(context.Context, string) error
	ResetWorkspace(context.Context, string, []byte) error
	DeleteWorkspace(context.Context, string) error
	ListWorkspaceSandboxes(context.Context) ([]string, error)
}

// WorkspaceOwnerReference names the GeneratorWorkspace CR a workspace PVC must
// carry as its ownerReferences entry, so Kubernetes garbage collection drops
// the claim together with the CR.
type WorkspaceOwnerReference struct {
	APIVersion string
	Kind       string
	Name       string
	UID        string
}

// WorkspaceOwnerStore is the cluster-side ownership boundary. Implementations
// keep one GeneratorWorkspace CR per workspace, named after the workspace ID,
// and may only remove the cleanup finalizer once the cluster-external Sandbox
// is gone.
type WorkspaceOwnerStore interface {
	EnsureWorkspaceOwner(ctx context.Context, record domain.Workspace) (WorkspaceOwnerReference, error)
	RecordWorkspaceOwnerStatus(ctx context.Context, record domain.Workspace) error
	RetireWorkspaceOwner(ctx context.Context, workspaceID string) error
	DeleteWorkspaceOwner(ctx context.Context, workspaceID string) error
	ListWorkspaceOwners(ctx context.Context) ([]domain.WorkspaceOwner, error)
}

type Manager struct {
	repo             WorkspaceRepository
	pvcs             PVCManager
	sandboxes        SandboxManager
	owners           WorkspaceOwnerStore
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

func NewManager(repo WorkspaceRepository, pvcs PVCManager, sandboxes SandboxManager, owners WorkspaceOwnerStore, config Config) (*Manager, error) {
	if repo == nil || pvcs == nil || sandboxes == nil || owners == nil {
		return nil, errors.New("workspace manager requires repository, pvc manager, sandbox manager, and owner store")
	}
	if strings.TrimSpace(config.Namespace) == "" || strings.TrimSpace(config.Storage) == "" {
		return nil, errors.New("workspace manager namespace and storage are required")
	}
	if config.ProvisionTimeout <= 0 {
		config.ProvisionTimeout = 5 * time.Minute
	}
	return &Manager{
		repo: repo, pvcs: pvcs, sandboxes: sandboxes, owners: owners,
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
	created := false
	for {
		record, err := m.repo.GetCurrentGeneratorWorkspace(ctx, workflowID)
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
		// An active workspace has already provisioned its PVC and Sandbox. Check
		// the provider binding before reusing it: the provider may have deleted
		// the Sandbox while the durable record remained active.
		if record.State == domain.WorkspaceActive && strings.TrimSpace(record.SandboxID) != "" {
			sandboxID, found, findErr := m.sandboxes.FindWorkspace(ctx, record.ID)
			if findErr != nil {
				return nil, false, fmt.Errorf("verify generator sandbox: %w", findErr)
			}
			if found && strings.TrimSpace(sandboxID) == strings.TrimSpace(record.SandboxID) {
				return record, created, nil
			}
			// A different Sandbox with the same workspace metadata is an orphaned
			// provider resource, not a valid replacement for the recorded binding.
			// Remove it before retiring the record so cleanup cannot leave it
			// behind.
			if found {
				if err := m.sandboxes.DeleteWorkspace(ctx, sandboxID); err != nil {
					return nil, false, fmt.Errorf("delete stale generator sandbox: %w", err)
				}
			}
			if _, retireErr := m.repo.RetireCurrentGeneratorWorkspace(ctx, workflowID, m.now()); retireErr != nil {
				if errors.Is(retireErr, domain.ErrWorkspaceNotFound) {
					continue
				}
				return nil, false, fmt.Errorf("retire unavailable generator workspace: %w", retireErr)
			}
			// The ownership move continues asynchronously: the retired CR is
			// marked deleting so the drop cannot race the replacement, and the
			// reaper heals any missed marking within one pass.
			m.retireOwner(ctx, record.ID)
			// The retired record remains eligible for asynchronous Sandbox/PVC
			// cleanup. The next iteration allocates a fresh workspace identity.
			continue
		}
		provisionCtx, cancel := m.provisionContext(ctx, record.ProvisionDeadline)
		// The owner CR must exist before the claim: the PVC references it as
		// its owner, and a claim without that reference would outlive the
		// workspace it belongs to.
		owner, err := m.owners.EnsureWorkspaceOwner(provisionCtx, *record)
		if err != nil {
			cancel()
			return nil, false, fmt.Errorf("ensure generator workspace owner: %w", err)
		}
		if err := m.pvcs.EnsureWorkspacePVC(provisionCtx, record.Namespace, record.PVCName, record.WorkflowID, m.storage, owner); err != nil {
			cancel()
			return nil, false, fmt.Errorf("ensure generator workspace pvc: %w", err)
		}
		if strings.TrimSpace(record.SandboxID) == "" {
			sandboxID, found, err := m.sandboxes.FindWorkspace(provisionCtx, record.ID)
			if err != nil {
				cancel()
				return nil, false, fmt.Errorf("find generator sandbox: %w", err)
			}
			if !found {
				sandboxID, err = m.sandboxes.CreateWorkspace(provisionCtx, record.PVCName, record.ID)
				if err != nil {
					cancel()
					return nil, false, fmt.Errorf("create generator sandbox: %w", err)
				}
			}
			if err := m.repo.RecordGeneratorWorkspaceSandbox(provisionCtx, record.ID, sandboxID, m.now()); err != nil {
				_ = m.sandboxes.DeleteWorkspace(context.Background(), sandboxID)
				m.compensateOwner(context.Background(), *record)
				cancel()
				return nil, false, fmt.Errorf("record generator sandbox: %w", err)
			}
			record, err = m.repo.GetGeneratorWorkspace(provisionCtx, record.ID)
			if err != nil {
				cancel()
				return nil, false, err
			}
			// Publishing the Sandbox ID to the owner immediately (not only at
			// activation) keeps the leak sanitizer's CR diff safe while the
			// Sandbox is still being waited on and seeded.
			if statusErr := m.owners.RecordWorkspaceOwnerStatus(provisionCtx, *record); statusErr != nil {
				slog.Warn("record generator workspace owner status", "workspace_id", record.ID, "err", statusErr)
			}
		}
		if err := m.sandboxes.WaitWorkspace(provisionCtx, record.SandboxID); err != nil {
			cancel()
			return nil, false, fmt.Errorf("wait for generator sandbox: %w", err)
		}
		// Pending means no model can yet observe the workspace. Seed it before the
		// durable transition to active so a retried provision repeats this exact
		// initialization instead of exposing a partially restored repair context.
		if record.State == domain.WorkspacePending {
			if err := m.sandboxes.ResetWorkspace(provisionCtx, record.SandboxID, seed); err != nil {
				cancel()
				return nil, false, fmt.Errorf("seed generator workspace: %w", err)
			}
		}
		if err := m.repo.ActivateGeneratorWorkspace(provisionCtx, record.ID, record.SandboxID, m.now()); err != nil {
			_ = m.sandboxes.DeleteWorkspace(context.Background(), record.SandboxID)
			m.compensateOwner(context.Background(), *record)
			cancel()
			return nil, false, fmt.Errorf("record generator sandbox: %w", err)
		}
		cancel()
		record, err = m.repo.GetGeneratorWorkspace(ctx, record.ID)
		if err != nil {
			return nil, false, err
		}
		// A lagging status never blocks the activated workspace: the facts are
		// already durable in the row and the reaper converges the CR.
		if statusErr := m.owners.RecordWorkspaceOwnerStatus(ctx, *record); statusErr != nil {
			slog.Warn("record generator workspace owner status", "workspace_id", record.ID, "err", statusErr)
		}
		return record, created, nil
	}
}

func (m *Manager) Retire(ctx context.Context, workflowID string) error {
	if m == nil {
		return errors.New("workspace manager is not configured")
	}
	record, err := m.repo.RetireCurrentGeneratorWorkspace(ctx, strings.TrimSpace(workflowID), m.now())
	if errors.Is(err, domain.ErrWorkspaceNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	// The durable transition already succeeded; a failed CR marking is healed
	// by the reaper's next cleanup pass instead of failing the caller.
	m.retireOwner(ctx, record.ID)
	return nil
}

// Cleanup transitions one workspace to deleting and marks its owner CR. The
// actual drop is the Server-side reconciler's job: it deletes the Sandbox,
// lets Kubernetes garbage-collect the owner-referenced PVC, and completes the
// row once the CR is gone.
func (m *Manager) Cleanup(ctx context.Context, workspaceID string) error {
	if m == nil {
		return errors.New("workspace manager is not configured")
	}
	record, err := m.repo.BeginGeneratorWorkspaceCleanup(ctx, workspaceID, m.now())
	if errors.Is(err, domain.ErrWorkspaceNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.State == domain.WorkspaceDeleted {
		return nil
	}
	m.retireOwner(ctx, record.ID)
	return nil
}

// dropWorkspace executes the irreducible cleanup for one deleting workspace:
// the recorded cluster-external Sandbox dies first, the row is completed, the
// claim is guaranteed to reference its owner even if it predates the
// ownership transfer, and removing the owner then lets Kubernetes
// garbage-collect the claim. Every step is idempotent so the reconciler can
// converge across ticks. Only recorded Sandbox IDs are deleted here; a
// Sandbox no durable fact claims is the leak sanitizer's backstop, so an
// unreachable provider cannot silently wedge the cluster-side cascade.
func (m *Manager) dropWorkspace(ctx context.Context, owner domain.WorkspaceOwner, record *domain.Workspace) error {
	sandboxID := strings.TrimSpace(owner.SandboxID)
	if record != nil && strings.TrimSpace(record.SandboxID) != "" {
		sandboxID = strings.TrimSpace(record.SandboxID)
	}
	if sandboxID != "" {
		if err := m.sandboxes.DeleteWorkspace(ctx, sandboxID); err != nil {
			return fmt.Errorf("delete generator sandbox: %w", err)
		}
	}
	if record != nil && record.State == domain.WorkspaceDeleting {
		if err := m.repo.MarkGeneratorWorkspaceDeleted(ctx, record.ID, m.now()); err != nil {
			return err
		}
	}
	reference, err := m.owners.EnsureWorkspaceOwner(ctx, ownerRecord(owner))
	if err != nil {
		return fmt.Errorf("ensure generator workspace owner for drop: %w", err)
	}
	if err := m.pvcs.EnsureWorkspacePVCOwner(ctx, owner.Namespace, owner.PVCName, reference); err != nil {
		return fmt.Errorf("ensure generator workspace pvc owner: %w", err)
	}
	return m.deleteOwner(ctx, owner.ID)
}

func ownerRecord(owner domain.WorkspaceOwner) domain.Workspace {
	return domain.Workspace{
		ID: owner.ID, WorkflowID: owner.WorkflowID, Namespace: owner.Namespace,
		PVCName: owner.PVCName, SandboxID: owner.SandboxID, State: owner.State,
	}
}

// AdoptOrphanedOwners rebuilds projection rows for GeneratorWorkspace CRs the
// database no longer knows, for example after a destructive schema migration.
// The rebuilt rows go straight to deleting so the regular cleanup drops the
// adopted resources; live rows keep their CRs untouched.
func (m *Manager) AdoptOrphanedOwners(ctx context.Context) error {
	if m == nil {
		return errors.New("workspace manager is not configured")
	}
	owners, err := m.owners.ListWorkspaceOwners(ctx)
	if err != nil {
		return err
	}
	for _, owner := range owners {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.adoptOwner(ctx, owner); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) adoptOwner(ctx context.Context, owner domain.WorkspaceOwner) error {
	_, err := m.repo.GetGeneratorWorkspace(ctx, owner.ID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, domain.ErrWorkspaceNotFound) {
		return err
	}
	if !owner.Rebuildable() {
		slog.Warn("skip generator workspace owner without ownership facts", "workspace_id", owner.ID)
		return nil
	}
	now := m.now()
	if _, err := m.repo.CreateGeneratorWorkspace(ctx, domain.Workspace{
		ID:                owner.ID,
		WorkflowID:        owner.WorkflowID,
		Namespace:         owner.Namespace,
		PVCName:           owner.PVCName,
		SandboxID:         owner.SandboxID,
		State:             domain.WorkspaceDeleting,
		ProvisionDeadline: now,
		CreatedAt:         now,
		UpdatedAt:         now,
	}); err != nil {
		return fmt.Errorf("adopt generator workspace owner %s: %w", owner.ID, err)
	}
	m.retireOwner(ctx, owner.ID)
	return nil
}

// retireOwner marks the owner CR deleting without failing the caller: the
// database row is already deleting and the reaper converges missed CRs.
func (m *Manager) retireOwner(ctx context.Context, workspaceID string) {
	if err := m.owners.RetireWorkspaceOwner(ctx, workspaceID); err != nil {
		slog.Warn("retire generator workspace owner", "workspace_id", workspaceID, "err", err)
	}
}

func (m *Manager) deleteOwner(ctx context.Context, workspaceID string) error {
	if err := m.owners.DeleteWorkspaceOwner(ctx, workspaceID); err != nil {
		return fmt.Errorf("delete generator workspace owner: %w", err)
	}
	return nil
}

// compensateOwner rewrites the owner status after a compensation deleted the
// Sandbox, so the CR never advertises a Sandbox the Server no longer owns.
func (m *Manager) compensateOwner(ctx context.Context, record domain.Workspace) {
	record.SandboxID = ""
	record.State = domain.WorkspacePending
	if err := m.owners.RecordWorkspaceOwnerStatus(ctx, record); err != nil {
		slog.Warn("rewrite generator workspace owner after compensation", "workspace_id", record.ID, "err", err)
	}
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
