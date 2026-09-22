package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appoperations "github.com/breakfix/breakfix/internal/application/operations"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

// An explicitly failed runnable action takes the release to its existing
// terminal failure on the same pass instead of waiting forever: the installer
// stops promising progress for an entry whose queue row already gave up.
func TestInstallerFailsReleaseForFailedRunnableActions(t *testing.T) {
	root, _ := writePortableRelease(t)
	source, err := LoadPortableSource(root)
	if err != nil {
		t.Fatalf("load portable source: %v", err)
	}
	now := time.Date(2026, time.September, 22, 12, 0, 0, 0, time.UTC)
	failure := &runnable.ActionFailure{Class: runnable.FailureInfrastructure, Code: "attempts-exhausted", Summary: "provider kept failing"}

	newInstaller := func(t *testing.T, runtime RunnableStore) (*Installer, *installerFailingStore) {
		t.Helper()
		store := &installerFailingStore{}
		installer, err := NewInstaller(InstallerConfig{
			DataDir: t.TempDir(), ScenariosDir: t.TempDir(),
			ReleaseReference: "registry.example/breakfix/catalog@sha256:" + strings.Repeat("a", 64),
			PollInterval:     time.Second,
			Puller:           nopBundlePuller{}, LayerReader: nopLayerReader{},
			Store: store, Runnable: runtime, Operations: installerOperationsConfig(),
		})
		if err != nil {
			t.Fatalf("create installer: %v", err)
		}
		installer.now = func() time.Time { return now }
		return installer, store
	}

	t.Run("materialization", func(t *testing.T) {
		installer, store := newInstaller(t, &installerFailingRunnable{materializationFailure: failure})
		entries, err := installer.newEntries("release-failed", source)
		if err != nil {
			t.Fatalf("build entries: %v", err)
		}
		store.entries = entries
		if err := installer.advanceEntries(context.Background(), source, catalogdomain.Release{ID: "release-failed", State: catalogdomain.ReleaseInstalling}); err != nil {
			t.Fatalf("advance failed materialization: %v", err)
		}
		if store.materialized != 0 || len(store.failures) != 1 || !strings.Contains(store.failures[0], "attempts-exhausted") {
			t.Fatalf("failed materialization = materialized:%d failures:%#v, want one terminal release failure", store.materialized, store.failures)
		}
	})

	t.Run("verification", func(t *testing.T) {
		installer, store := newInstaller(t, &installerFailingRunnable{verificationFailure: failure})
		entries, err := installer.newEntries("release-failed", source)
		if err != nil {
			t.Fatalf("build entries: %v", err)
		}
		entries[0].State = catalogdomain.EntryVerifying
		entries[0].RunnableRevisionRef = &runnable.RevisionReference{ID: "revision-01", Digest: "sha256:" + strings.Repeat("e", 64)}
		store.entries = entries
		if err := installer.advanceEntries(context.Background(), source, catalogdomain.Release{ID: "release-failed", State: catalogdomain.ReleaseInstalling}); err != nil {
			t.Fatalf("advance failed verification: %v", err)
		}
		if store.verified != 0 || len(store.failures) != 1 || !strings.Contains(store.failures[0], "provider kept failing") {
			t.Fatalf("failed verification = verified:%d failures:%#v, want one terminal release failure", store.verified, store.failures)
		}
	})
}

func installerOperationsConfig() appoperations.Config {
	profile := appoperations.RuntimeProfileConfig{
		ProfileRevision: "profile-01", BaseImage: "registry.example/base@sha256:" + strings.Repeat("d", 64),
		Resources: runnable.ResourceLimits{CPU: "2", MemoryBytes: 2 << 30, EphemeralBytes: 4 << 30, MaxProcesses: 128, MaxConcurrentTasks: 1},
		Network:   runnable.NetworkPrivate, MaxActionTimeout: 1200,
	}
	return appoperations.Config{
		MaxNodes: 4, Node: profile, K8s: profile,
		Lifecycle: runnable.LifecyclePolicy{CreateTimeoutSeconds: 600, ResetTimeoutSeconds: 600, StopTimeoutSeconds: 300, ReapTimeoutSeconds: 300, IdleTTLSeconds: 1800, MaxLifetimeSeconds: 3600},
	}
}

// installerFailingStore records the terminal failure exit; every other
// ReleaseStore method stays unreachable through advanceEntries.
type installerFailingStore struct {
	ReleaseStore
	entries      []catalogdomain.Entry
	materialized int
	verified     int
	failures     []string
}

func (s *installerFailingStore) Entries(context.Context, string) ([]catalogdomain.Entry, error) {
	return s.entries, nil
}

func (s *installerFailingStore) MarkCatalogEntryMaterialized(context.Context, string, runnable.RevisionReference, time.Time) error {
	s.materialized++
	return nil
}

func (s *installerFailingStore) MarkCatalogEntryVerified(context.Context, string, runnable.VerificationReportReference, time.Time) error {
	s.verified++
	return nil
}

func (s *installerFailingStore) FailRelease(_ context.Context, _ string, message string, _ time.Time) (*catalogdomain.Release, error) {
	s.failures = append(s.failures, message)
	return &catalogdomain.Release{}, nil
}

type installerFailingRunnable struct {
	materializationFailure error
	verificationFailure    error
}

func (f *installerFailingRunnable) StoreRunnableSource(context.Context, runnable.SourceArchive, []byte, time.Time) error {
	return nil
}

func (f *installerFailingRunnable) ScheduleMaterialization(_ context.Context, spec runnable.RunnableSpec, stateVersion int64, _ time.Time) (runnable.ActionIdentity, error) {
	digest, err := spec.Digest()
	if err != nil {
		return runnable.ActionIdentity{}, err
	}
	return runnable.ActionIdentity{Content: spec.Identity, SpecDigest: digest, Phase: runnable.ActionMaterializeArtifact, StateVersion: stateVersion}, nil
}

func (f *installerFailingRunnable) ResolveMaterializedRunnableRevision(context.Context, runnable.ActionIdentity) (runnable.RevisionReference, error) {
	return runnable.RevisionReference{}, f.materializationFailure
}

func (f *installerFailingRunnable) ScheduleVerification(_ context.Context, reference runnable.RevisionReference, stateVersion int64, _ time.Time) (runnable.ActionIdentity, error) {
	if err := reference.Validate(); err != nil {
		return runnable.ActionIdentity{}, err
	}
	return runnable.ActionIdentity{Content: runnable.ContentIdentity{Kind: "operations", ID: "entry-01", Revision: "rev-01"}, SpecDigest: "sha256:" + strings.Repeat("f", 64), Phase: runnable.ActionVerify, StateVersion: stateVersion}, nil
}

func (f *installerFailingRunnable) ResolveVerificationForAction(context.Context, runnable.ActionIdentity) (runnable.StoredVerificationReport, error) {
	return runnable.StoredVerificationReport{}, f.verificationFailure
}

type nopBundlePuller struct{}

func (nopBundlePuller) PullOCIArchive(context.Context, string, string) error {
	return errors.New("unexpected pull")
}

type nopLayerReader struct{}

func (nopLayerReader) ReadSourceLayer(string) ([]byte, error) {
	return nil, errors.New("unexpected layer read")
}
