package generation

import (
	"context"
	"errors"
	"testing"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

func TestManagerCreatesOneOwnedWorkspaceAndCleansIt(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	repo := &memoryRepository{}
	pvcs := &memoryPVCs{}
	sandboxes := &memorySandboxes{nextID: "sandbox-one"}
	manager, err := NewManager(repo, pvcs, sandboxes, Config{Namespace: "opensandbox", Storage: "1Gi", ProvisionTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }

	record, err := manager.Ensure(context.Background(), "generator-run-one")
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	if record.State != domain.WorkspaceActive || record.SandboxID != "sandbox-one" {
		t.Fatalf("workspace = %#v", record)
	}
	if pvcs.created != 1 || sandboxes.created != 1 {
		t.Fatalf("created pvc=%d sandbox=%d, want one each", pvcs.created, sandboxes.created)
	}
	if _, err := manager.Ensure(context.Background(), "generator-run-one"); err != nil {
		t.Fatalf("resume workspace: %v", err)
	}
	if pvcs.created != 1 || sandboxes.created != 1 {
		t.Fatalf("duplicate ensure created pvc=%d sandbox=%d", pvcs.created, sandboxes.created)
	}

	if err := manager.Cleanup(context.Background(), "generator-run-one"); err != nil {
		t.Fatalf("cleanup workspace: %v", err)
	}
	if sandboxes.deleted != 1 || pvcs.deleted != 1 {
		t.Fatalf("deleted sandbox=%d pvc=%d", sandboxes.deleted, pvcs.deleted)
	}
	if err := manager.Cleanup(context.Background(), "generator-run-one"); err != nil {
		t.Fatalf("repeat cleanup workspace: %v", err)
	}
	if sandboxes.deleted != 1 || pvcs.deleted != 1 {
		t.Fatalf("repeat cleanup deleted sandbox=%d pvc=%d", sandboxes.deleted, pvcs.deleted)
	}
}

func TestManagerCleansPendingWorkspaceAfterUnknownSandboxCreate(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	repo := &memoryRepository{}
	pvcs := &memoryPVCs{}
	sandboxes := &memorySandboxes{createErr: errors.New("request timeout after remote create")}
	manager, err := NewManager(repo, pvcs, sandboxes, Config{Namespace: "opensandbox", Storage: "1Gi", ProvisionTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }
	if _, err := manager.Ensure(context.Background(), "generator-run-timeout"); err == nil {
		t.Fatal("unknown sandbox create unexpectedly succeeded")
	}
	record, err := repo.GetGeneratorWorkspace(context.Background(), "generator-run-timeout")
	if err != nil || record.State != domain.WorkspacePending || record.SandboxID != "" {
		t.Fatalf("pending workspace = %#v, err=%v", record, err)
	}
	now = now.Add(2 * time.Minute)
	if err := manager.CleanupDue(context.Background()); err != nil {
		t.Fatalf("cleanup expired pending workspace: %v", err)
	}
	record, err = repo.GetGeneratorWorkspace(context.Background(), "generator-run-timeout")
	if err != nil || record.State != domain.WorkspaceDeleted {
		t.Fatalf("cleaned workspace = %#v, err=%v", record, err)
	}
	if sandboxes.deleted != 0 || pvcs.deleted != 1 {
		t.Fatalf("unknown sandbox cleanup deleted sandbox=%d pvc=%d", sandboxes.deleted, pvcs.deleted)
	}
}

func TestManagerResumesPersistedSandboxAfterReadinessFailure(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	repo := &memoryRepository{}
	pvcs := &memoryPVCs{}
	sandboxes := &memorySandboxes{nextID: "sandbox-one", waitErrors: []error{errors.New("sandbox still pending")}}
	manager, err := NewManager(repo, pvcs, sandboxes, Config{Namespace: "opensandbox", Storage: "1Gi", ProvisionTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }

	if _, err := manager.Ensure(context.Background(), "generator-run-resume"); err == nil {
		t.Fatal("first ensure unexpectedly succeeded")
	}
	record, err := repo.GetGeneratorWorkspace(context.Background(), "generator-run-resume")
	if err != nil || record.State != domain.WorkspacePending || record.SandboxID != "sandbox-one" {
		t.Fatalf("persisted pending workspace = %#v, err=%v", record, err)
	}
	if _, err := manager.Ensure(context.Background(), "generator-run-resume"); err != nil {
		t.Fatalf("resume workspace: %v", err)
	}
	if sandboxes.created != 1 || sandboxes.waited != 2 {
		t.Fatalf("created=%d waited=%d, want one create and two waits", sandboxes.created, sandboxes.waited)
	}
}

func TestManagerRecoversUnknownCreatedSandboxFromMetadata(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	repo := &memoryRepository{}
	pvcs := &memoryPVCs{}
	sandboxes := &memorySandboxes{foundID: "sandbox-recovered"}
	manager, err := NewManager(repo, pvcs, sandboxes, Config{Namespace: "opensandbox", Storage: "1Gi", ProvisionTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }

	record, err := manager.Ensure(context.Background(), "generator-run-recovered")
	if err != nil {
		t.Fatalf("recover workspace: %v", err)
	}
	if record.SandboxID != "sandbox-recovered" || sandboxes.created != 0 || sandboxes.waited != 1 {
		t.Fatalf("workspace=%#v created=%d waited=%d", record, sandboxes.created, sandboxes.waited)
	}
}

func TestManagerCleanupFindsSandboxWhenCreateResponseWasLost(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	repo := &memoryRepository{records: map[string]domain.Workspace{
		"generator-run-lost-response": {
			GeneratorRunID: "generator-run-lost-response", Namespace: "opensandbox", PVCName: domain.NewWorkspacePVCName("generator-run-lost-response"),
			State: domain.WorkspacePending, ProvisionDeadline: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
		},
	}}
	pvcs := &memoryPVCs{}
	sandboxes := &memorySandboxes{foundID: "sandbox-lost-response"}
	manager, err := NewManager(repo, pvcs, sandboxes, Config{Namespace: "opensandbox", Storage: "1Gi", ProvisionTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }

	if err := manager.Cleanup(context.Background(), "generator-run-lost-response"); err != nil {
		t.Fatalf("cleanup workspace: %v", err)
	}
	if sandboxes.deleted != 1 || sandboxes.deletedIDs[0] != "sandbox-lost-response" || pvcs.deleted != 1 {
		t.Fatalf("cleanup deleted=%v pvc=%d", sandboxes.deletedIDs, pvcs.deleted)
	}
}

func TestManagerCleansTerminalRunWorkspace(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	repo := &memoryRepository{
		records: map[string]domain.Workspace{
			"generator-run-terminal": {
				GeneratorRunID: "generator-run-terminal", Namespace: "opensandbox", PVCName: domain.NewWorkspacePVCName("generator-run-terminal"),
				SandboxID: "sandbox-terminal", State: domain.WorkspaceActive, ProvisionDeadline: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
			},
		},
		terminal: map[string]bool{"generator-run-terminal": true},
	}
	pvcs := &memoryPVCs{}
	sandboxes := &memorySandboxes{}
	manager, err := NewManager(repo, pvcs, sandboxes, Config{Namespace: "opensandbox", Storage: "1Gi", ProvisionTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }

	if err := manager.CleanupDue(context.Background()); err != nil {
		t.Fatalf("cleanup terminal workspace: %v", err)
	}
	record, err := repo.GetGeneratorWorkspace(context.Background(), "generator-run-terminal")
	if err != nil || record.State != domain.WorkspaceDeleted {
		t.Fatalf("terminal workspace = %#v, err=%v", record, err)
	}
	if sandboxes.deleted != 1 || pvcs.deleted != 1 {
		t.Fatalf("terminal cleanup deleted sandbox=%d pvc=%d", sandboxes.deleted, pvcs.deleted)
	}
}

func TestManagerCleansTerminalPendingWorkspace(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	repo := &memoryRepository{
		records: map[string]domain.Workspace{
			"generator-run-pending": {
				GeneratorRunID: "generator-run-pending", Namespace: "opensandbox", PVCName: domain.NewWorkspacePVCName("generator-run-pending"),
				State: domain.WorkspacePending, ProvisionDeadline: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
			},
		},
		terminal: map[string]bool{"generator-run-pending": true},
	}
	pvcs := &memoryPVCs{}
	sandboxes := &memorySandboxes{}
	manager, err := NewManager(repo, pvcs, sandboxes, Config{Namespace: "opensandbox", Storage: "1Gi", ProvisionTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }

	if err := manager.CleanupDue(context.Background()); err != nil {
		t.Fatalf("cleanup terminal pending workspace: %v", err)
	}
	record, err := repo.GetGeneratorWorkspace(context.Background(), "generator-run-pending")
	if err != nil || record.State != domain.WorkspaceDeleted {
		t.Fatalf("terminal pending workspace = %#v, err=%v", record, err)
	}
	if sandboxes.deleted != 0 || pvcs.deleted != 1 {
		t.Fatalf("terminal pending cleanup deleted sandbox=%d pvc=%d", sandboxes.deleted, pvcs.deleted)
	}
}

type memoryRepository struct {
	records  map[string]domain.Workspace
	terminal map[string]bool
}

func (r *memoryRepository) CreateGeneratorWorkspace(_ context.Context, record domain.Workspace) (*domain.Workspace, error) {
	if r.records == nil {
		r.records = map[string]domain.Workspace{}
	}
	if current, ok := r.records[record.GeneratorRunID]; ok {
		copy := current
		return &copy, nil
	}
	r.records[record.GeneratorRunID] = record
	copy := record
	return &copy, nil
}

func (r *memoryRepository) GetGeneratorWorkspace(_ context.Context, id string) (*domain.Workspace, error) {
	record, ok := r.records[id]
	if !ok {
		return nil, domain.ErrWorkspaceNotFound
	}
	copy := record
	return &copy, nil
}

func (r *memoryRepository) RecordGeneratorWorkspaceSandbox(_ context.Context, id, sandboxID string, now time.Time) error {
	record, ok := r.records[id]
	if !ok {
		return domain.ErrWorkspaceNotFound
	}
	if record.State != domain.WorkspacePending || (record.SandboxID != "" && record.SandboxID != sandboxID) {
		return errors.New("workspace sandbox cannot be recorded")
	}
	record.SandboxID, record.UpdatedAt = sandboxID, now
	r.records[id] = record
	return nil
}

func (r *memoryRepository) ActivateGeneratorWorkspace(_ context.Context, id, sandboxID string, now time.Time) error {
	record, ok := r.records[id]
	if !ok {
		return domain.ErrWorkspaceNotFound
	}
	record.State, record.SandboxID, record.UpdatedAt = domain.WorkspaceActive, sandboxID, now
	r.records[id] = record
	return nil
}

func (r *memoryRepository) BeginGeneratorWorkspaceCleanup(_ context.Context, id string, now time.Time) (*domain.Workspace, error) {
	record, ok := r.records[id]
	if !ok {
		return nil, domain.ErrWorkspaceNotFound
	}
	if record.State != domain.WorkspaceDeleted {
		record.State, record.UpdatedAt = domain.WorkspaceDeleting, now
		r.records[id] = record
	}
	return &record, nil
}

func (r *memoryRepository) MarkGeneratorWorkspaceDeleted(_ context.Context, id string, now time.Time) error {
	record, ok := r.records[id]
	if !ok {
		return domain.ErrWorkspaceNotFound
	}
	record.State, record.UpdatedAt = domain.WorkspaceDeleted, now
	record.DeletedAt = &now
	r.records[id] = record
	return nil
}

func (r *memoryRepository) ListExpiredPendingGeneratorWorkspaces(_ context.Context, now time.Time) ([]domain.Workspace, error) {
	var values []domain.Workspace
	for _, record := range r.records {
		if record.State == domain.WorkspacePending && !record.ProvisionDeadline.After(now) {
			values = append(values, record)
		}
	}
	return values, nil
}

func (r *memoryRepository) ListDeletingGeneratorWorkspaces(_ context.Context) ([]domain.Workspace, error) {
	var values []domain.Workspace
	for _, record := range r.records {
		if record.State == domain.WorkspaceDeleting {
			values = append(values, record)
		}
	}
	return values, nil
}

func (r *memoryRepository) ListTerminalGeneratorWorkspaces(_ context.Context) ([]domain.Workspace, error) {
	var values []domain.Workspace
	for id := range r.terminal {
		if record, ok := r.records[id]; ok && (record.State == domain.WorkspacePending || record.State == domain.WorkspaceActive) {
			values = append(values, record)
		}
	}
	return values, nil
}

type memoryPVCs struct {
	claims           map[string]bool
	created, deleted int
}

func (p *memoryPVCs) EnsureWorkspacePVC(_ context.Context, namespace, name, _ string, _ string) error {
	if p.claims == nil {
		p.claims = map[string]bool{}
	}
	key := namespace + "/" + name
	if !p.claims[key] {
		p.claims[key] = true
		p.created++
	}
	return nil
}

func (p *memoryPVCs) DeleteWorkspacePVC(_ context.Context, namespace, name string) error {
	delete(p.claims, namespace+"/"+name)
	p.deleted++
	return nil
}

type memorySandboxes struct {
	nextID     string
	foundID    string
	createErr  error
	findErr    error
	waitErrors []error
	created    int
	waited     int
	deleted    int
	deletedIDs []string
}

func (s *memorySandboxes) FindWorkspace(context.Context, string) (string, bool, error) {
	if s.findErr != nil {
		return "", false, s.findErr
	}
	return s.foundID, s.foundID != "", nil
}

func (s *memorySandboxes) CreateWorkspace(context.Context, string, string) (string, error) {
	s.created++
	if s.createErr != nil {
		return "", s.createErr
	}
	return s.nextID, nil
}

func (s *memorySandboxes) WaitWorkspace(context.Context, string) error {
	s.waited++
	if len(s.waitErrors) == 0 {
		return nil
	}
	err := s.waitErrors[0]
	s.waitErrors = s.waitErrors[1:]
	return err
}

func (s *memorySandboxes) DeleteWorkspace(_ context.Context, id string) error {
	s.deleted++
	s.deletedIDs = append(s.deletedIDs, id)
	return nil
}
