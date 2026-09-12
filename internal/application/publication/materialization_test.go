package publication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type materializationStoreStub struct {
	paths []string
	err   error
}

func (s materializationStoreStub) RetainedMaterializationPaths(context.Context) ([]string, error) {
	return append([]string(nil), s.paths...), s.err
}

func TestMaterializationRecoveryRetainsHistoryAndPendingPublications(t *testing.T) {
	root := t.TempDir()
	retained := []string{
		"active/chrev-11111111",
		"superseded/chrev-22222222",
		"deprecated/chrev-33333333",
		"generation-pending/chrev-44444444",
		"catalog-pending/chrev-55555555",
	}
	orphans := []string{
		"generation-conflict/chrev-66666666",
		"catalog-failed/chrev-77777777",
		"crashed-before-commit/chrev-88888888",
	}
	for _, path := range append(append([]string(nil), retained...), orphans...) {
		writeMaterialization(t, root, path)
	}
	writeMaterialization(t, root, "not-a-revision/unrecognized")
	staging := filepath.Join(root, ".tmp-crashed-publication")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}

	reconciler, err := NewMaterializationReconciler(materializationStoreStub{paths: retained}, MaterializationReconcilerConfig{ScenariosDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Recover(context.Background()); err != nil {
		t.Fatalf("recover materializations: %v", err)
	}
	for _, path := range retained {
		assertMaterializationExists(t, root, path)
	}
	for _, path := range orphans {
		assertMaterializationMissing(t, root, path)
	}
	assertMaterializationExists(t, root, "not-a-revision/unrecognized")
	if _, err := os.Lstat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup staging directory remains: %v", err)
	}
}

func TestMaterializationRecoveryFailsClosedWhenRetentionCannotBeDerived(t *testing.T) {
	root := t.TempDir()
	orphan := "must-remain/chrev-aaaaaaaa"
	writeMaterialization(t, root, orphan)

	reconciler, err := NewMaterializationReconciler(materializationStoreStub{err: errors.New("database unavailable")}, MaterializationReconcilerConfig{ScenariosDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Recover(context.Background()); err == nil {
		t.Fatal("expected retention failure")
	}
	assertMaterializationExists(t, root, orphan)
}

func TestMaterializationPeriodicPassDoesNotRemoveFreshStaging(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, time.August, 7, 8, 0, 0, 0, time.UTC)
	fresh := filepath.Join(root, ".tmp-active-publication")
	stale := filepath.Join(root, ".tmp-abandoned-publication")
	for _, path := range []string{fresh, stale} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(fresh, now, now); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stale, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	reconciler, err := NewMaterializationReconciler(materializationStoreStub{}, MaterializationReconcilerConfig{
		ScenariosDir: root, StagingMaxAge: 10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	reconciler.now = func() time.Time { return now }
	if err := reconciler.reconcile(context.Background(), false); err != nil {
		t.Fatalf("reconcile materializations: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh staging directory removed: %v", err)
	}
	if _, err := os.Lstat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale staging directory remains: %v", err)
	}
}

func TestMaterializationRecoveryRejectsInvalidDatabasePathBeforeDeletion(t *testing.T) {
	root := t.TempDir()
	orphan := "must-remain/chrev-bbbbbbbb"
	writeMaterialization(t, root, orphan)

	reconciler, err := NewMaterializationReconciler(materializationStoreStub{paths: []string{"../outside"}}, MaterializationReconcilerConfig{ScenariosDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Recover(context.Background()); err == nil {
		t.Fatal("expected invalid retained path to fail")
	}
	assertMaterializationExists(t, root, orphan)
}

func writeMaterialization(t *testing.T, root, relative string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "marker"), []byte("materialized\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertMaterializationExists(t *testing.T, root, relative string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
		t.Fatalf("materialization %q missing: %v", relative, err)
	}
}

func assertMaterializationMissing(t *testing.T, root, relative string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("materialization %q remains: %v", relative, err)
	}
}
