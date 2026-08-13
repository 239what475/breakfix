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
	deleted, err := repo.GetGeneratorWorkspace(context.Background(), first.ID)
	if err != nil || deleted.State != domain.WorkspaceDeleted {
		t.Fatalf("cleaned workspace = %#v, err=%v", deleted, err)
	}
}

func newWorkspaceManager(t *testing.T, repo *memoryWorkspaceRepository, pvcs *memoryWorkspacePVCs, sandboxes *memoryWorkspaceSandboxes, now *time.Time) *Manager {
	t.Helper()
	manager, err := NewManager(repo, pvcs, sandboxes, Config{Namespace: "opensandbox", Storage: "1Gi", ProvisionTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return *now }
	return manager
}

type memoryWorkspaceRepository struct {
	mu      sync.Mutex
	records map[string]domain.Workspace
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

type memoryWorkspacePVCs struct {
	claims           map[string]struct{}
	created, deleted int
}

func (p *memoryWorkspacePVCs) EnsureWorkspacePVC(ctx context.Context, namespace, name, _ string, _ string) error {
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

type memoryWorkspaceSandboxes struct {
	nextID          string
	resetErr        error
	created, resets int
	deleted         int
	lastSeed        []byte
}

func (s *memoryWorkspaceSandboxes) FindWorkspace(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (s *memoryWorkspaceSandboxes) CreateWorkspace(context.Context, string, string) (string, error) {
	s.created++
	return s.nextID, nil
}
func (s *memoryWorkspaceSandboxes) WaitWorkspace(context.Context, string) error { return nil }
func (s *memoryWorkspaceSandboxes) ResetWorkspace(_ context.Context, _ string, seed []byte) error {
	s.resets++
	s.lastSeed = append([]byte(nil), seed...)
	return s.resetErr
}
func (s *memoryWorkspaceSandboxes) DeleteWorkspace(context.Context, string) error {
	s.deleted++
	return nil
}
