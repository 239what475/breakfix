package workspace

import (
	"context"
	"errors"
	"testing"
	"time"
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

	record, err := manager.Ensure(context.Background(), "generator-session-one")
	if err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	if record.State != StateActive || record.SandboxID != "sandbox-one" {
		t.Fatalf("workspace = %#v", record)
	}
	if pvcs.created != 1 || sandboxes.created != 1 {
		t.Fatalf("created pvc=%d sandbox=%d, want one each", pvcs.created, sandboxes.created)
	}
	if _, err := manager.Ensure(context.Background(), "generator-session-one"); err != nil {
		t.Fatalf("resume workspace: %v", err)
	}
	if pvcs.created != 1 || sandboxes.created != 1 {
		t.Fatalf("duplicate ensure created pvc=%d sandbox=%d", pvcs.created, sandboxes.created)
	}

	if err := manager.Cleanup(context.Background(), "generator-session-one"); err != nil {
		t.Fatalf("cleanup workspace: %v", err)
	}
	if sandboxes.deleted != 1 || pvcs.deleted != 1 {
		t.Fatalf("deleted sandbox=%d pvc=%d", sandboxes.deleted, pvcs.deleted)
	}
	if err := manager.Cleanup(context.Background(), "generator-session-one"); err != nil {
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
	if _, err := manager.Ensure(context.Background(), "generator-session-timeout"); err == nil {
		t.Fatal("unknown sandbox create unexpectedly succeeded")
	}
	record, err := repo.GetGeneratorWorkspace(context.Background(), "generator-session-timeout")
	if err != nil || record.State != StatePending || record.SandboxID != "" {
		t.Fatalf("pending workspace = %#v, err=%v", record, err)
	}
	now = now.Add(2 * time.Minute)
	if err := manager.CleanupDue(context.Background()); err != nil {
		t.Fatalf("cleanup expired pending workspace: %v", err)
	}
	record, err = repo.GetGeneratorWorkspace(context.Background(), "generator-session-timeout")
	if err != nil || record.State != StateDeleted {
		t.Fatalf("cleaned workspace = %#v, err=%v", record, err)
	}
	if sandboxes.deleted != 0 || pvcs.deleted != 1 {
		t.Fatalf("unknown sandbox cleanup deleted sandbox=%d pvc=%d", sandboxes.deleted, pvcs.deleted)
	}
}

type memoryRepository struct{ records map[string]Record }

func (r *memoryRepository) CreateGeneratorWorkspace(_ context.Context, record Record) (*Record, error) {
	if r.records == nil {
		r.records = map[string]Record{}
	}
	if current, ok := r.records[record.GeneratorSessionID]; ok {
		copy := current
		return &copy, nil
	}
	r.records[record.GeneratorSessionID] = record
	copy := record
	return &copy, nil
}

func (r *memoryRepository) GetGeneratorWorkspace(_ context.Context, id string) (*Record, error) {
	record, ok := r.records[id]
	if !ok {
		return nil, ErrNotFound
	}
	copy := record
	return &copy, nil
}

func (r *memoryRepository) ActivateGeneratorWorkspace(_ context.Context, id, sandboxID string, now time.Time) error {
	record, ok := r.records[id]
	if !ok {
		return ErrNotFound
	}
	record.State, record.SandboxID, record.UpdatedAt = StateActive, sandboxID, now
	r.records[id] = record
	return nil
}

func (r *memoryRepository) BeginGeneratorWorkspaceCleanup(_ context.Context, id string, now time.Time) (*Record, error) {
	record, ok := r.records[id]
	if !ok {
		return nil, ErrNotFound
	}
	if record.State != StateDeleted {
		record.State, record.UpdatedAt = StateDeleting, now
		r.records[id] = record
	}
	return &record, nil
}

func (r *memoryRepository) MarkGeneratorWorkspaceDeleted(_ context.Context, id string, now time.Time) error {
	record, ok := r.records[id]
	if !ok {
		return ErrNotFound
	}
	record.State, record.UpdatedAt = StateDeleted, now
	record.DeletedAt = &now
	r.records[id] = record
	return nil
}

func (r *memoryRepository) ListExpiredPendingGeneratorWorkspaces(_ context.Context, now time.Time) ([]Record, error) {
	var values []Record
	for _, record := range r.records {
		if record.State == StatePending && !record.ProvisionDeadline.After(now) {
			values = append(values, record)
		}
	}
	return values, nil
}

func (r *memoryRepository) ListDeletingGeneratorWorkspaces(_ context.Context) ([]Record, error) {
	var values []Record
	for _, record := range r.records {
		if record.State == StateDeleting {
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
	nextID    string
	createErr error
	created   int
	deleted   int
}

func (s *memorySandboxes) CreateWorkspace(context.Context, string) (string, error) {
	s.created++
	if s.createErr != nil {
		return "", s.createErr
	}
	return s.nextID, nil
}

func (s *memorySandboxes) DeleteWorkspace(context.Context, string) error { s.deleted++; return nil }
