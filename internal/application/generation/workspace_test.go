package generation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

func TestManagerReusesOneWorkflowWorkspace(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	pvcs := &memoryWorkspacePVCs{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-one"}
	manager := newWorkspaceManager(t, repo, pvcs, sandboxes, &now)

	first, err := manager.Ensure(context.Background(), "workflow-one", []byte("seed-one"))
	if err != nil {
		t.Fatalf("ensure first workspace: %v", err)
	}
	second, err := manager.Ensure(context.Background(), "workflow-one", []byte("must-not-reset"))
	if err != nil {
		t.Fatalf("ensure existing workspace: %v", err)
	}
	if first.ID != second.ID || first.PVCName != second.PVCName || first.State != domain.WorkspaceActive {
		t.Fatalf("workflow did not reuse current workspace: first=%#v second=%#v", first, second)
	}
	if pvcs.created != 1 || sandboxes.created != 1 || sandboxes.resets != 1 || string(sandboxes.lastSeed) != "seed-one" {
		t.Fatalf("workspace lifecycle = pvcs:%d creates:%d resets:%d seed:%q", pvcs.created, sandboxes.created, sandboxes.resets, sandboxes.lastSeed)
	}
}

func TestManagerReusesActiveWorkspaceAfterProvisionDeadline(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	pvcs := &memoryWorkspacePVCs{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-one"}
	manager := newWorkspaceManager(t, repo, pvcs, sandboxes, &now)

	first, err := manager.Ensure(context.Background(), "workflow-one", []byte("seed"))
	if err != nil {
		t.Fatalf("ensure first workspace: %v", err)
	}
	// The provision deadline only fences pending provisioning. A later
	// Generator turn must keep using the active workspace instead of being
	// cancelled by the stale deadline.
	now = now.Add(2 * time.Minute)
	reused, err := manager.Ensure(context.Background(), "workflow-one", []byte("must-not-reset"))
	if err != nil {
		t.Fatalf("reuse active workspace after provision deadline: %v", err)
	}
	if reused.ID != first.ID || reused.State != domain.WorkspaceActive {
		t.Fatalf("active workspace was not reused: first=%#v reused=%#v", first, reused)
	}
	if pvcs.created != 1 || sandboxes.created != 1 || sandboxes.resets != 1 {
		t.Fatalf("active workspace was reprovisioned after its deadline: pvcs:%d creates:%d resets:%d", pvcs.created, sandboxes.created, sandboxes.resets)
	}
}

func TestManagerRebuildsActiveWorkspaceWhenSandboxDisappears(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	pvcs := &memoryWorkspacePVCs{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-one"}
	manager := newWorkspaceManager(t, repo, pvcs, sandboxes, &now)

	first, err := manager.Ensure(context.Background(), "workflow-one", []byte("initial"))
	if err != nil {
		t.Fatalf("ensure initial workspace: %v", err)
	}
	delete(sandboxes.workspaces, first.ID)
	sandboxes.nextID = "sandbox-two"

	second, err := manager.Ensure(context.Background(), "workflow-one", []byte("restore-candidate"))
	if err != nil {
		t.Fatalf("rebuild workspace after sandbox deletion: %v", err)
	}
	if second.ID == first.ID || second.PVCName == first.PVCName || second.SandboxID != "sandbox-two" {
		t.Fatalf("replacement workspace = %#v, previous = %#v", second, first)
	}
	retired, err := repo.GetGeneratorWorkspace(context.Background(), first.ID)
	if err != nil || retired.State != domain.WorkspaceDeleting {
		t.Fatalf("retired workspace = %#v, err=%v", retired, err)
	}
	if pvcs.created != 2 || sandboxes.created != 2 || sandboxes.resets != 2 {
		t.Fatalf("replacement lifecycle = pvcs:%d creates:%d resets:%d", pvcs.created, sandboxes.created, sandboxes.resets)
	}
}

// reconcile completes the ownership lifecycle after the manager's state
// transitions: one reconciler pass executes the drops the deleting CRs
// describe.
func reconcile(t *testing.T, manager *Manager) {
	t.Helper()
	reconciler, err := NewWorkspaceReconciler(manager)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconcile generator workspaces: %v", err)
	}
}

func TestManagerRetiresWorkspaceBeforeReplacement(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	pvcs := &memoryWorkspacePVCs{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-one"}
	manager := newWorkspaceManager(t, repo, pvcs, sandboxes, &now)

	first, err := manager.Ensure(context.Background(), "workflow-one", []byte("initial"))
	if err != nil {
		t.Fatalf("ensure first workspace: %v", err)
	}
	if err := manager.Retire(context.Background(), "workflow-one"); err != nil {
		t.Fatalf("retire workspace: %v", err)
	}
	retired, err := repo.GetGeneratorWorkspace(context.Background(), first.ID)
	if err != nil || retired.State != domain.WorkspaceDeleting {
		t.Fatalf("retired workspace = %#v, err=%v", retired, err)
	}
	sandboxes.nextID = "sandbox-two"
	second, err := manager.Ensure(context.Background(), "workflow-one", []byte("restore-candidate"))
	if err != nil {
		t.Fatalf("ensure replacement workspace: %v", err)
	}
	if second.ID == first.ID || second.PVCName == first.PVCName || second.SandboxID != "sandbox-two" {
		t.Fatalf("replacement workspace = %#v, previous=%#v", second, first)
	}
	if err := manager.CleanupDue(context.Background()); err != nil {
		t.Fatalf("cleanup retired workspace: %v", err)
	}
	reconcile(t, manager)
	retired, err = repo.GetGeneratorWorkspace(context.Background(), first.ID)
	if err != nil || retired.State != domain.WorkspaceDeleted {
		t.Fatalf("cleaned retired workspace = %#v, err=%v", retired, err)
	}
}

func TestManagerLeavesFailedSeedPendingForRetryOrCleanup(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	pvcs := &memoryWorkspacePVCs{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-one", resetErr: errors.New("sandbox unavailable")}
	manager := newWorkspaceManager(t, repo, pvcs, sandboxes, &now)

	if _, err := manager.Ensure(context.Background(), "workflow-one", []byte("seed")); err == nil {
		t.Fatal("seed failure unexpectedly succeeded")
	}
	record, err := repo.GetCurrentGeneratorWorkspace(context.Background(), "workflow-one")
	if err != nil || record.State != domain.WorkspacePending || record.SandboxID != "sandbox-one" {
		t.Fatalf("pending workspace = %#v, err=%v", record, err)
	}
	sandboxes.resetErr = nil
	if _, err := manager.Ensure(context.Background(), "workflow-one", []byte("seed")); err != nil {
		t.Fatalf("retry pending workspace: %v", err)
	}
	if sandboxes.created != 1 || sandboxes.resets != 2 {
		t.Fatalf("retry did not reuse pending sandbox: creates=%d resets=%d", sandboxes.created, sandboxes.resets)
	}
}

func TestManagerCreatesOwnerBeforePVCAndBackfillsStatus(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	pvcs := &memoryWorkspacePVCs{}
	owners := &memoryWorkspaceOwners{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-one"}
	manager := newWorkspaceManagerWithOwners(t, repo, pvcs, sandboxes, owners, &now)

	record, err := manager.Ensure(context.Background(), "workflow-one", []byte("seed"))
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	if owners.created != 1 {
		t.Fatalf("owner creation count = %d, want 1", owners.created)
	}
	if pvcs.lastOwner.Name != record.ID || pvcs.lastOwner.UID == "" || pvcs.lastOwner.APIVersion != "breakfix.dev/v2" || pvcs.lastOwner.Kind != "GeneratorWorkspace" {
		t.Fatalf("pvc owner reference = %#v", pvcs.lastOwner)
	}
	owner := owners.owners[record.ID]
	if owner.State != domain.WorkspaceActive || owner.SandboxID != "sandbox-one" || owner.PVCName != record.PVCName {
		t.Fatalf("activated owner = %#v", owner)
	}
	if owners.status < 1 {
		t.Fatalf("owner status was never backfilled: %#v", owners)
	}
}

func TestManagerRetireAndCleanupDriveOwnerToExplicitDeletion(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	pvcs := &memoryWorkspacePVCs{}
	owners := &memoryWorkspaceOwners{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-one"}
	manager := newWorkspaceManagerWithOwners(t, repo, pvcs, sandboxes, owners, &now)

	first, err := manager.Ensure(context.Background(), "workflow-one", []byte("seed"))
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	if err := manager.Retire(context.Background(), "workflow-one"); err != nil {
		t.Fatalf("retire workspace: %v", err)
	}
	if owners.owners[first.ID].State != domain.WorkspaceDeleting {
		t.Fatalf("retired owner = %#v", owners.owners[first.ID])
	}
	if err := manager.CleanupDue(context.Background()); err != nil {
		t.Fatalf("cleanup workspace: %v", err)
	}
	// The reconciler executes the drop the deleting CR describes and finishes
	// with the explicit owner deletion.
	reconcile(t, manager)
	if owners.deleted != 1 {
		t.Fatalf("owner deletion count = %d, want 1", owners.deleted)
	}
	if _, exists := owners.owners[first.ID]; exists {
		t.Fatal("owner survived cleanup")
	}
	deleted, err := repo.GetGeneratorWorkspace(context.Background(), first.ID)
	if err != nil || deleted.State != domain.WorkspaceDeleted {
		t.Fatalf("cleaned workspace = %#v, err=%v", deleted, err)
	}
	// Terminal rows converge any owner residue without repeating the drop.
	if err := manager.CleanupDue(context.Background()); err != nil {
		t.Fatalf("terminal cleanup: %v", err)
	}
	reconcile(t, manager)
	if owners.deleted != 1 {
		t.Fatalf("terminal cleanup repeated owner deletion: %d", owners.deleted)
	}
	if sandboxes.deleted != 1 {
		t.Fatalf("sandbox deletion count = %d, want 1", sandboxes.deleted)
	}
}

func TestManagerCompensationRewritesOwnerAfterSandboxDelete(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	repo := &failingActivationRepository{memoryWorkspaceRepository: &memoryWorkspaceRepository{}, fail: true}
	pvcs := &memoryWorkspacePVCs{}
	owners := &memoryWorkspaceOwners{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-one"}
	manager := newWorkspaceManagerWithOwners(t, repo, pvcs, sandboxes, owners, &now)

	if _, err := manager.Ensure(context.Background(), "workflow-one", []byte("seed")); err == nil {
		t.Fatal("activate failure unexpectedly succeeded")
	}
	record, err := repo.GetCurrentGeneratorWorkspace(context.Background(), "workflow-one")
	if err != nil || record.State != domain.WorkspacePending {
		t.Fatalf("pending record = %#v, err=%v", record, err)
	}
	owner := owners.owners[record.ID]
	if owner.State != domain.WorkspacePending || owner.SandboxID != "" {
		t.Fatalf("compensated owner = %#v", owner)
	}
}

// failingActivationRepository injects an ActivateGeneratorWorkspace failure so
// the compensation path (Sandbox delete plus owner rewrite) is exercised.
type failingActivationRepository struct {
	*memoryWorkspaceRepository
	fail bool
}

func (r *failingActivationRepository) ActivateGeneratorWorkspace(ctx context.Context, id, sandboxID string, now time.Time) error {
	if r.fail {
		return errors.New("record unavailable")
	}
	return r.memoryWorkspaceRepository.ActivateGeneratorWorkspace(ctx, id, sandboxID, now)
}

func TestManagerAdoptsOwnerWithoutRowAndDropsIt(t *testing.T) {
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	pvcs := &memoryWorkspacePVCs{}
	owners := &memoryWorkspaceOwners{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-live"}
	manager := newWorkspaceManagerWithOwners(t, repo, pvcs, sandboxes, owners, &now)

	live, err := manager.Ensure(context.Background(), "workflow-live", []byte("seed"))
	if err != nil {
		t.Fatalf("ensure live workspace: %v", err)
	}
	// An orphaned owner has no database row: it predates a destructive schema
	// migration, or the row it belonged to was reset away.
	owners.owners["generator-workspace-orphan"] = domain.WorkspaceOwner{
		ID: "generator-workspace-orphan", WorkflowID: "workflow-orphan", Namespace: "opensandbox",
		PVCName: "breakfix-workspace-orphan", SandboxID: "sandbox-orphan", State: domain.WorkspaceActive,
	}
	pvcs.claims = map[string]struct{}{"opensandbox/breakfix-workspace-orphan": {}}

	if err := manager.AdoptOrphanedOwners(context.Background()); err != nil {
		t.Fatalf("adopt orphaned owners: %v", err)
	}
	adopted, err := repo.GetGeneratorWorkspace(context.Background(), "generator-workspace-orphan")
	if err != nil || adopted.State != domain.WorkspaceDeleting || adopted.SandboxID != "sandbox-orphan" {
		t.Fatalf("adopted row = %#v, err=%v", adopted, err)
	}
	if owners.owners["generator-workspace-orphan"].State != domain.WorkspaceDeleting {
		t.Fatalf("adopted owner = %#v", owners.owners["generator-workspace-orphan"])
	}
	intact, err := repo.GetGeneratorWorkspace(context.Background(), live.ID)
	if err != nil || intact.State != domain.WorkspaceActive {
		t.Fatalf("live row was not left untouched: %#v, err=%v", intact, err)
	}
	// The rebuilt deleting row funnels into the regular cleanup; the
	// reconciler's drop completes it with the explicit owner deletion.
	if err := manager.CleanupDue(context.Background()); err != nil {
		t.Fatalf("cleanup adopted workspace: %v", err)
	}
	reconcile(t, manager)
	if _, exists := owners.owners["generator-workspace-orphan"]; exists {
		t.Fatal("adopted owner survived cleanup")
	}
	if _, exists := owners.owners[live.ID]; !exists {
		t.Fatal("live owner was dropped by adoption cleanup")
	}
	if sandboxes.deleted != 1 {
		t.Fatalf("adopted sandbox drop = %d, want 1", sandboxes.deleted)
	}
	// The PVC follows the owner through garbage collection in the real
	// cluster; the drop hardens its owner reference before the CR goes away.
	if pvcs.ownerPatches != 1 {
		t.Fatalf("adopted pvc owner hardening = %d, want 1", pvcs.ownerPatches)
	}
}

func TestWorkspaceReaperRetiresRestartedWorkspaceBeforeAsynchronousCleanup(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	pvcs := &memoryWorkspacePVCs{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-before-restart"}
	manager := newWorkspaceManager(t, repo, pvcs, sandboxes, &now)

	first, err := manager.Ensure(context.Background(), "workflow-recovery", []byte("initial"))
	if err != nil {
		t.Fatalf("create original workspace: %v", err)
	}
	if _, err := repo.AcquireGeneratorWorkspaceTurn(context.Background(), domain.WorkspaceTurn{WorkflowID: "workflow-recovery", ID: "turn-before-restart"}, now); err != nil {
		t.Fatalf("bind original workspace turn: %v", err)
	}

	reaper, err := NewWorkspaceReaper(manager)
	if err != nil {
		t.Fatalf("create workspace reaper: %v", err)
	}
	if err := reaper.Recover(context.Background()); err != nil {
		t.Fatalf("recover restarted workspaces: %v", err)
	}

	retired, err := repo.GetGeneratorWorkspace(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("read retired workspace: %v", err)
	}
	if retired.State != domain.WorkspaceDeleting || retired.ActiveTurnID != "" {
		t.Fatalf("retired workspace = %#v, want deleting workspace without a turn", retired)
	}

	sandboxes.nextID = "sandbox-after-restart"
	replacement, err := manager.Ensure(context.Background(), "workflow-recovery", []byte("durable-seed"))
	if err != nil {
		t.Fatalf("create replacement workspace: %v", err)
	}
	if replacement.ID == first.ID || replacement.PVCName == first.PVCName || replacement.SandboxID != "sandbox-after-restart" {
		t.Fatalf("replacement workspace = %#v, previous = %#v", replacement, first)
	}
	if err := manager.CleanupDue(context.Background()); err != nil {
		t.Fatalf("asynchronously clean retired workspace: %v", err)
	}
	reconcile(t, manager)
	deleted, err := repo.GetGeneratorWorkspace(context.Background(), first.ID)
	if err != nil || deleted.State != domain.WorkspaceDeleted {
		t.Fatalf("cleaned workspace = %#v, err=%v", deleted, err)
	}
}

func newWorkspaceManager(t *testing.T, repo WorkspaceRepository, pvcs *memoryWorkspacePVCs, sandboxes *memoryWorkspaceSandboxes, now *time.Time) *Manager {
	t.Helper()
	return newWorkspaceManagerWithOwners(t, repo, pvcs, sandboxes, &memoryWorkspaceOwners{}, now)
}

func newWorkspaceManagerWithOwners(t *testing.T, repo WorkspaceRepository, pvcs *memoryWorkspacePVCs, sandboxes *memoryWorkspaceSandboxes, owners *memoryWorkspaceOwners, now *time.Time) *Manager {
	t.Helper()
	manager, err := NewManager(repo, pvcs, sandboxes, owners, Config{Namespace: "opensandbox", Storage: "1Gi", ProvisionTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return *now }
	return manager
}

type memoryWorkspaceRepository struct {
	mu        sync.Mutex
	records   map[string]domain.Workspace
	snapshots map[string]string
}

func (r *memoryWorkspaceRepository) CreateGeneratorWorkspace(_ context.Context, record domain.Workspace) (*domain.Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.records == nil {
		r.records = make(map[string]domain.Workspace)
	}
	if existing, ok := r.records[record.ID]; ok {
		return &existing, nil
	}
	if existing, err := r.getCurrentGeneratorWorkspace(record.WorkflowID); err == nil {
		return existing, nil
	}
	r.records[record.ID] = record
	return &record, nil
}

func (r *memoryWorkspaceRepository) GetGeneratorWorkspace(_ context.Context, id string) (*domain.Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getGeneratorWorkspace(id)
}

func (r *memoryWorkspaceRepository) getGeneratorWorkspace(id string) (*domain.Workspace, error) {
	record, ok := r.records[id]
	if !ok {
		return nil, domain.ErrWorkspaceNotFound
	}
	return &record, nil
}

func (r *memoryWorkspaceRepository) GetCurrentGeneratorWorkspace(_ context.Context, workflowID string) (*domain.Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getCurrentGeneratorWorkspace(workflowID)
}

func (r *memoryWorkspaceRepository) getCurrentGeneratorWorkspace(workflowID string) (*domain.Workspace, error) {
	for _, record := range r.records {
		if record.WorkflowID == workflowID && (record.State == domain.WorkspacePending || record.State == domain.WorkspaceActive) {
			copy := record
			return &copy, nil
		}
	}
	return nil, domain.ErrWorkspaceNotFound
}

func (r *memoryWorkspaceRepository) GetGeneratorWorkspaceForTurn(_ context.Context, turn domain.WorkspaceTurn) (*domain.Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !turn.Valid() {
		return nil, domain.ErrWorkspaceTurnLost
	}
	for _, record := range r.records {
		if record.WorkflowID == turn.WorkflowID && record.State == domain.WorkspaceActive && record.ActiveTurnID == turn.ID {
			copy := record
			return &copy, nil
		}
	}
	return nil, domain.ErrWorkspaceTurnLost
}

func (r *memoryWorkspaceRepository) AcquireGeneratorWorkspaceTurn(_ context.Context, turn domain.WorkspaceTurn, now time.Time) (*domain.Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !turn.Valid() {
		return nil, domain.ErrWorkspaceTurnLost
	}
	record, err := r.getCurrentGeneratorWorkspace(turn.WorkflowID)
	if err != nil {
		return nil, err
	}
	if record.State != domain.WorkspaceActive {
		return nil, domain.ErrWorkspaceNotFound
	}
	if record.ActiveTurnID != "" && record.ActiveTurnID != turn.ID {
		return nil, domain.ErrWorkspaceBusy
	}
	record.ActiveTurnID = turn.ID
	record.IdleSince = nil
	record.UpdatedAt = now
	r.records[record.ID] = *record
	return record, nil
}

func (r *memoryWorkspaceRepository) ReleaseGeneratorWorkspaceTurn(_ context.Context, turn domain.WorkspaceTurn, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !turn.Valid() {
		return domain.ErrWorkspaceTurnLost
	}
	for id, record := range r.records {
		if record.WorkflowID == turn.WorkflowID && record.State == domain.WorkspaceActive && record.ActiveTurnID == turn.ID {
			record.ActiveTurnID = ""
			record.IdleSince = nil
			record.UpdatedAt = now
			r.records[id] = record
			return nil
		}
	}
	return domain.ErrWorkspaceTurnLost
}

func (r *memoryWorkspaceRepository) RecordGeneratorWorkspaceSandbox(_ context.Context, id, sandboxID string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.getGeneratorWorkspace(id)
	if err != nil {
		return err
	}
	record.SandboxID, record.UpdatedAt = sandboxID, now
	r.records[id] = *record
	return nil
}

func (r *memoryWorkspaceRepository) ActivateGeneratorWorkspace(_ context.Context, id, sandboxID string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.getGeneratorWorkspace(id)
	if err != nil {
		return err
	}
	record.SandboxID, record.State, record.UpdatedAt = sandboxID, domain.WorkspaceActive, now
	r.records[id] = *record
	return nil
}

func (r *memoryWorkspaceRepository) BeginGeneratorWorkspaceCleanup(_ context.Context, id string, now time.Time) (*domain.Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.getGeneratorWorkspace(id)
	if err != nil {
		return nil, err
	}
	if record.State != domain.WorkspaceDeleted {
		record.State, record.ActiveTurnID, record.UpdatedAt = domain.WorkspaceDeleting, "", now
		record.IdleSince = nil
		r.records[id] = *record
	}
	return record, nil
}

func (r *memoryWorkspaceRepository) RetireCurrentGeneratorWorkspace(_ context.Context, workflowID string, now time.Time) (*domain.Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.getCurrentGeneratorWorkspace(workflowID)
	if err != nil {
		return nil, err
	}
	record.State, record.ActiveTurnID, record.UpdatedAt = domain.WorkspaceDeleting, "", now
	record.IdleSince = nil
	r.records[record.ID] = *record
	return record, nil
}

func (r *memoryWorkspaceRepository) RetireIncompleteGeneratorWorkspaces(_ context.Context, now time.Time) ([]domain.Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	retired := make([]domain.Workspace, 0)
	for id, record := range r.records {
		if record.State != domain.WorkspacePending && record.State != domain.WorkspaceActive {
			continue
		}
		record.State = domain.WorkspaceDeleting
		record.ActiveTurnID = ""
		record.IdleSince = nil
		record.UpdatedAt = now
		r.records[id] = record
		retired = append(retired, record)
	}
	return retired, nil
}

func (r *memoryWorkspaceRepository) MarkGeneratorWorkspaceDeleted(_ context.Context, id string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.getGeneratorWorkspace(id)
	if err != nil {
		return err
	}
	record.State, record.UpdatedAt = domain.WorkspaceDeleted, now
	record.DeletedAt = &now
	r.records[id] = *record
	return nil
}

func (r *memoryWorkspaceRepository) ListCurrentGeneratorWorkspaces(context.Context) ([]domain.Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]domain.Workspace, 0)
	for _, record := range r.records {
		if record.State == domain.WorkspacePending || record.State == domain.WorkspaceActive {
			result = append(result, record)
		}
	}
	return result, nil
}

func (r *memoryWorkspaceRepository) ListExpiredPendingGeneratorWorkspaces(_ context.Context, now time.Time) ([]domain.Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]domain.Workspace, 0)
	for _, record := range r.records {
		if record.State == domain.WorkspacePending && !record.ProvisionDeadline.After(now) {
			result = append(result, record)
		}
	}
	return result, nil
}

func (r *memoryWorkspaceRepository) ListDeletingGeneratorWorkspaces(_ context.Context) ([]domain.Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]domain.Workspace, 0)
	for _, record := range r.records {
		if record.State == domain.WorkspaceDeleting {
			result = append(result, record)
		}
	}
	return result, nil
}

func (r *memoryWorkspaceRepository) ListTerminalGeneratorWorkspaces(context.Context) ([]domain.Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return nil, nil
}

func (r *memoryWorkspaceRepository) ListGeneratorWorkspaceSnapshotTargets(context.Context) ([]domain.WorkspaceSnapshotTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]domain.WorkspaceSnapshotTarget, 0)
	for _, record := range r.records {
		if record.State != domain.WorkspaceActive {
			continue
		}
		result = append(result, domain.WorkspaceSnapshotTarget{Workspace: record, SnapshotDigest: r.snapshotDigest(record.WorkflowID)})
	}
	return result, nil
}

func (r *memoryWorkspaceRepository) AcquireGeneratorWorkspaceSnapshot(_ context.Context, workflowID, holderID string, now time.Time) (*domain.WorkspaceSnapshotTarget, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.getCurrentGeneratorWorkspace(workflowID)
	if err != nil || record.State != domain.WorkspaceActive {
		return nil, domain.ErrWorkspaceNotFound
	}
	if record.ActiveTurnID != "" {
		return nil, domain.ErrWorkspaceBusy
	}
	digest := r.snapshotDigest(workflowID)
	if record.IdleSince != nil && digest != "" {
		return nil, domain.ErrWorkspaceSnapshotCurrent
	}
	record.ActiveTurnID = holderID
	record.IdleSince = nil
	record.UpdatedAt = now
	r.records[record.ID] = *record
	return &domain.WorkspaceSnapshotTarget{Workspace: *record, SnapshotDigest: digest}, nil
}

func (r *memoryWorkspaceRepository) PublishGeneratorWorkspaceSnapshot(_ context.Context, workspaceID, holderID, digest string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.getGeneratorWorkspace(workspaceID)
	if err != nil || record.State != domain.WorkspaceActive || record.ActiveTurnID != holderID {
		return domain.ErrWorkspaceTurnLost
	}
	if r.snapshots == nil {
		r.snapshots = make(map[string]string)
	}
	r.snapshots[record.WorkflowID] = digest
	record.ActiveTurnID = ""
	record.IdleSince = &now
	record.UpdatedAt = now
	r.records[record.ID] = *record
	return nil
}

func (r *memoryWorkspaceRepository) ClearGeneratorWorkspaceSnapshot(_ context.Context, workflowID, expectedDigest string, now time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.snapshotDigest(workflowID) != expectedDigest {
		return false, nil
	}
	delete(r.snapshots, workflowID)
	if record, err := r.getCurrentGeneratorWorkspace(workflowID); err == nil {
		record.IdleSince = nil
		record.UpdatedAt = now
		r.records[record.ID] = *record
	}
	return true, nil
}

func (r *memoryWorkspaceRepository) RetireIdleGeneratorWorkspace(_ context.Context, workspaceID, expectedDigest string, staleBefore, now time.Time) (*domain.Workspace, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record, err := r.getGeneratorWorkspace(workspaceID)
	if err != nil || record.State != domain.WorkspaceActive || record.ActiveTurnID != "" || record.IdleSince == nil || record.IdleSince.After(staleBefore) || r.snapshotDigest(record.WorkflowID) != expectedDigest {
		return nil, domain.ErrWorkspaceNotIdle
	}
	record.State = domain.WorkspaceDeleting
	record.IdleSince = nil
	record.UpdatedAt = now
	r.records[record.ID] = *record
	return record, nil
}

func (r *memoryWorkspaceRepository) ListGeneratorWorkspaceSnapshotReferences(context.Context) ([]domain.WorkspaceSnapshotReference, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]domain.WorkspaceSnapshotReference, 0, len(r.snapshots))
	for workflowID, digest := range r.snapshots {
		result = append(result, domain.WorkspaceSnapshotReference{WorkflowID: workflowID, Digest: digest})
	}
	return result, nil
}

func (r *memoryWorkspaceRepository) snapshotDigest(workflowID string) string {
	if r.snapshots == nil {
		return ""
	}
	return r.snapshots[workflowID]
}

// memoryWorkspacePVCs records the claim lifecycle. Cascade deletion is a
// Kubernetes behavior the fakes cannot express: drops go through the owner
// CR, and the reconciler's owner-reference hardening is tracked separately.
type memoryWorkspacePVCs struct {
	claims           map[string]struct{}
	lastOwner        WorkspaceOwnerReference
	ownerPatches     int
	created, deleted int
}

func (p *memoryWorkspacePVCs) EnsureWorkspacePVC(ctx context.Context, namespace, name, _ string, _ string, owner WorkspaceOwnerReference) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.claims == nil {
		p.claims = make(map[string]struct{})
	}
	key := namespace + "/" + name
	if _, exists := p.claims[key]; !exists {
		p.claims[key] = struct{}{}
		p.created++
	}
	p.lastOwner = owner
	return nil
}

func (p *memoryWorkspacePVCs) EnsureWorkspacePVCOwner(_ context.Context, namespace, name string, owner WorkspaceOwnerReference) error {
	key := namespace + "/" + name
	if _, exists := p.claims[key]; !exists {
		return nil
	}
	p.ownerPatches++
	p.lastOwner = owner
	return nil
}

func (p *memoryWorkspacePVCs) DeleteWorkspacePVC(_ context.Context, namespace, name string) error {
	key := namespace + "/" + name
	if _, exists := p.claims[key]; exists {
		delete(p.claims, key)
		p.deleted++
	}
	return nil
}

// memoryWorkspaceOwners records the owner CR lifecycle the Manager drives:
// creation before provisioning, status backfills, retire markings, and the
// explicit deletion that ends the ownership.
type memoryWorkspaceOwners struct {
	owners                 map[string]domain.WorkspaceOwner
	created, status, count int
	retired, deleted       int
}

func (o *memoryWorkspaceOwners) EnsureWorkspaceOwner(_ context.Context, record domain.Workspace) (WorkspaceOwnerReference, error) {
	if o.owners == nil {
		o.owners = make(map[string]domain.WorkspaceOwner)
	}
	if _, exists := o.owners[record.ID]; !exists {
		o.owners[record.ID] = domain.WorkspaceOwner{
			ID: record.ID, WorkflowID: record.WorkflowID, Namespace: record.Namespace,
			PVCName: record.PVCName, SandboxID: record.SandboxID, State: record.State,
		}
		o.created++
	}
	return WorkspaceOwnerReference{APIVersion: "breakfix.dev/v2", Kind: "GeneratorWorkspace", Name: record.ID, UID: "uid-" + record.ID}, nil
}

func (o *memoryWorkspaceOwners) RecordWorkspaceOwnerStatus(_ context.Context, record domain.Workspace) error {
	o.status++
	if o.owners == nil {
		o.owners = make(map[string]domain.WorkspaceOwner)
	}
	o.owners[record.ID] = domain.WorkspaceOwner{
		ID: record.ID, WorkflowID: record.WorkflowID, Namespace: record.Namespace,
		PVCName: record.PVCName, SandboxID: record.SandboxID, State: record.State,
	}
	return nil
}

func (o *memoryWorkspaceOwners) RetireWorkspaceOwner(_ context.Context, workspaceID string) error {
	if record, exists := o.owners[workspaceID]; exists {
		record.State = domain.WorkspaceDeleting
		o.owners[workspaceID] = record
		o.retired++
	}
	return nil
}

func (o *memoryWorkspaceOwners) DeleteWorkspaceOwner(_ context.Context, workspaceID string) error {
	if _, exists := o.owners[workspaceID]; exists {
		delete(o.owners, workspaceID)
		o.deleted++
	}
	return nil
}

func (o *memoryWorkspaceOwners) ListWorkspaceOwners(context.Context) ([]domain.WorkspaceOwner, error) {
	o.count++
	result := make([]domain.WorkspaceOwner, 0, len(o.owners))
	for _, record := range o.owners {
		result = append(result, record)
	}
	return result, nil
}

type memoryWorkspaceSandboxes struct {
	nextID          string
	resetErr        error
	created, resets int
	deleted         int
	lastSeed        []byte
	workspaces      map[string]string
}

func (s *memoryWorkspaceSandboxes) FindWorkspace(_ context.Context, workspaceID string) (string, bool, error) {
	sandboxID, found := s.workspaces[workspaceID]
	return sandboxID, found, nil
}
func (s *memoryWorkspaceSandboxes) CreateWorkspace(_ context.Context, _, workspaceID string) (string, error) {
	s.created++
	if s.workspaces == nil {
		s.workspaces = make(map[string]string)
	}
	s.workspaces[workspaceID] = s.nextID
	return s.nextID, nil
}
func (s *memoryWorkspaceSandboxes) WaitWorkspace(context.Context, string) error { return nil }
func (s *memoryWorkspaceSandboxes) ResetWorkspace(_ context.Context, _ string, seed []byte) error {
	s.resets++
	s.lastSeed = append([]byte(nil), seed...)
	return s.resetErr
}
func (s *memoryWorkspaceSandboxes) DeleteWorkspace(_ context.Context, sandboxID string) error {
	s.deleted++
	for workspaceID, currentID := range s.workspaces {
		if currentID == sandboxID {
			delete(s.workspaces, workspaceID)
		}
	}
	return nil
}

func (s *memoryWorkspaceSandboxes) ListWorkspaceSandboxes(context.Context) ([]string, error) {
	seen := make(map[string]struct{}, len(s.workspaces))
	result := make([]string, 0, len(s.workspaces))
	for _, sandboxID := range s.workspaces {
		if _, exists := seen[sandboxID]; exists {
			continue
		}
		seen[sandboxID] = struct{}{}
		result = append(result, sandboxID)
	}
	return result, nil
}
