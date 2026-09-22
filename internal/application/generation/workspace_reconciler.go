package generation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

const defaultWorkspaceReconcileInterval = time.Minute

// WorkspaceReconciler executes the cluster-side ownership lifecycle: CRs in
// the deleting phase are dropped (Sandbox first, owner last, the PVC follows
// through Kubernetes garbage collection), rows the database lost are adopted
// from their CRs, and live rows without a CR are backfilled. It shares the
// WorkspaceReaper's minute cadence and never touches RuntimeEnvironments.
type WorkspaceReconciler struct {
	// OnTick optionally reports each background pass to the bootstrap's
	// in-memory service registry.
	OnTick            func(error)
	manager           *Manager
	sanitizer         *WorkspaceLeakSanitizer
	reconcileInterval time.Duration
}

func NewWorkspaceReconciler(manager *Manager) (*WorkspaceReconciler, error) {
	if manager == nil {
		return nil, errors.New("generator workspace manager is required")
	}
	return &WorkspaceReconciler{
		manager:           manager,
		sanitizer:         &WorkspaceLeakSanitizer{manager: manager},
		reconcileInterval: defaultWorkspaceReconcileInterval,
	}, nil
}

func (r *WorkspaceReconciler) Run(ctx context.Context) error {
	if r == nil || r.manager == nil {
		return errors.New("generator workspace reconciler is not configured")
	}
	ticker := time.NewTicker(r.reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			err := r.tick(ctx)
			if r.OnTick != nil {
				r.OnTick(err)
			}
			if err != nil && ctx.Err() == nil {
				slog.Warn("reconcile generator workspaces", "err", err)
			}
		}
	}
}

func (r *WorkspaceReconciler) tick(ctx context.Context) error {
	if err := r.ReconcileOnce(ctx); err != nil {
		return err
	}
	return r.sanitizer.SanitizeOnce(ctx)
}

// ReconcileOnce converges one pass of CR-to-database and database-to-CR
// reconciliation. Every step is idempotent; failures skip to the next tick.
func (r *WorkspaceReconciler) ReconcileOnce(ctx context.Context) error {
	if r == nil || r.manager == nil {
		return errors.New("generator workspace reconciler is not configured")
	}
	// Adopting first turns CRs the database forgot into deleting rows, so the
	// drop loop below can converge them in the same or the next pass.
	if err := r.manager.AdoptOrphanedOwners(ctx); err != nil {
		return err
	}
	owners, err := r.manager.owners.ListWorkspaceOwners(ctx)
	if err != nil {
		return err
	}
	ownerIDs := make(map[string]struct{}, len(owners))
	for _, owner := range owners {
		ownerIDs[owner.ID] = struct{}{}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.reconcileOwner(ctx, owner); err != nil {
			return err
		}
	}
	return r.backfillOwners(ctx, ownerIDs)
}

func (r *WorkspaceReconciler) reconcileOwner(ctx context.Context, owner domain.WorkspaceOwner) error {
	if !owner.Rebuildable() {
		// A CR without ownership facts has nothing the Server provisioned. An
		// explicit delete must not wedge on the cleanup finalizer, so resolve
		// it; otherwise the CR stays for the operator's manual list.
		if owner.Terminating {
			return r.manager.owners.DeleteWorkspaceOwner(ctx, owner.ID)
		}
		slog.Warn("skip generator workspace owner without ownership facts", "workspace_id", owner.ID)
		return nil
	}
	record, err := r.manager.repo.GetGeneratorWorkspace(ctx, owner.ID)
	if errors.Is(err, domain.ErrWorkspaceNotFound) {
		// Adoption already ran this pass; a row that vanished between the two
		// lists reappears on the next tick.
		return nil
	}
	if err != nil {
		return err
	}
	switch record.State {
	case domain.WorkspaceDeleting:
		// The CR phase may lag the durable transition if the marking failed;
		// converge it before the drop so the CR never outlives silently.
		if owner.State != domain.WorkspaceDeleting {
			if err := r.manager.owners.RecordWorkspaceOwnerStatus(ctx, *record); err != nil {
				return fmt.Errorf("sync generator workspace owner %s: %w", owner.ID, err)
			}
		}
		return r.manager.dropWorkspace(ctx, owner, record)
	case domain.WorkspaceDeleted:
		// Residue: the row completed while the owner delete failed. The drop
		// retries the external cleanup before removing the owner again.
		return r.manager.dropWorkspace(ctx, owner, record)
	default:
		// Defensive: a deleting CR above a live row would destroy an active
		// workspace. The row is the service-state authority, so heal the CR
		// back instead of dropping.
		if owner.State == domain.WorkspaceDeleting {
			return r.manager.owners.RecordWorkspaceOwnerStatus(ctx, *record)
		}
		return nil
	}
}

// backfillOwners provisions CRs for workspaces that predate the ownership
// transfer or lost theirs: live rows keep working without a CR, but only the
// CR makes the workspace survivable across a database reset.
func (r *WorkspaceReconciler) backfillOwners(ctx context.Context, known map[string]struct{}) error {
	current, err := r.manager.repo.ListCurrentGeneratorWorkspaces(ctx)
	if err != nil {
		return err
	}
	deleting, err := r.manager.repo.ListDeletingGeneratorWorkspaces(ctx)
	if err != nil {
		return err
	}
	for _, record := range append(current, deleting...) {
		if _, found := known[record.ID]; found {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := r.manager.owners.EnsureWorkspaceOwner(ctx, record); err != nil {
			return fmt.Errorf("backfill generator workspace owner %s: %w", record.ID, err)
		}
		if err := r.manager.owners.RecordWorkspaceOwnerStatus(ctx, record); err != nil {
			return fmt.Errorf("backfill generator workspace owner status %s: %w", record.ID, err)
		}
	}
	return nil
}

// WorkspaceLeakSanitizer is the leak backstop behind the ownership transfer.
// It lists Sandboxes tagged with the breakfix.app=generator metadata and
// deletes any whose ID no GeneratorWorkspace CR claims, alerting and auditing
// each removal. It never scans the whole account: untagged Sandboxes belong
// to other applications.
type WorkspaceLeakSanitizer struct {
	manager *Manager
}

// SanitizeOnce diffs one listing of application sandboxes against the CR
// set. Sandboxes without an owner are deliberately deletable: the narrow
// create-to-record window holds no durable reference, so a deleted stray
// simply reprovisions.
func (s *WorkspaceLeakSanitizer) SanitizeOnce(ctx context.Context) error {
	if s == nil || s.manager == nil {
		return errors.New("generator workspace leak sanitizer is not configured")
	}
	sandboxes, err := s.manager.sandboxes.ListWorkspaceSandboxes(ctx)
	if err != nil {
		return fmt.Errorf("list generator sandboxes: %w", err)
	}
	owners, err := s.manager.owners.ListWorkspaceOwners(ctx)
	if err != nil {
		return err
	}
	owned := make(map[string]struct{}, len(owners))
	for _, owner := range owners {
		if id := owner.SandboxID; id != "" {
			owned[id] = struct{}{}
		}
	}
	var result error
	for _, sandboxID := range sandboxes {
		if _, found := owned[sandboxID]; found {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// The structured record is the audit trail for an out-of-band delete.
		slog.Warn("leak sanitizer deletes unowned generator sandbox",
			"sandbox_id", sandboxID, "app_metadata", "generator")
		if err := s.manager.sandboxes.DeleteWorkspace(ctx, sandboxID); err != nil {
			result = errors.Join(result, fmt.Errorf("delete unowned generator sandbox %s: %w", sandboxID, err))
		}
	}
	return result
}
