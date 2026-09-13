package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/publication"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
)

func TestCatalogRepositoryPublishesRuntimeActionsAndCommitsAtomically(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 12, 0, 0, 123456789, time.UTC)
	digest := catalogdomain.BundleDigest("sha256:" + strings.Repeat("a", 64))
	release := catalogdomain.Release{
		ID: catalogdomain.ReleaseIDForBundle(digest), BundleDigest: digest, State: catalogdomain.ReleasePending,
		SourceAttempt: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	}
	created, inserted, err := database.Catalog.CreateOrGetRelease(ctx, release)
	if err != nil || !inserted || created.ID != release.ID {
		t.Fatalf("create catalog release = %#v, inserted=%v, err=%v", created, inserted, err)
	}

	entry := catalogRepositoryEntry(release.ID, now)
	initialized := *created
	initialized.Name = "catalog-integration"
	initialized.Version = "2026.08.04"
	initialized.SourceDigest = catalogdomain.ContentRevision("sha256:" + strings.Repeat("b", 64))
	initializedRelease, err := database.Catalog.InitializeRelease(ctx, initialized, []catalogdomain.Entry{entry}, now)
	if err != nil || initializedRelease.State != catalogdomain.ReleaseInstalling {
		t.Fatalf("initialize catalog release = %#v, err=%v", initializedRelease, err)
	}

	action, err := database.Catalog.ClaimCatalogRuntimeAction(ctx, "catalog-worker", time.Second, now)
	if err != nil || action == nil {
		t.Fatalf("claim initial catalog runtime action = %#v, err=%v", action, err)
	}
	if action.Identity.Scope != runtime.ScopeCatalogEntry || action.Identity.State != runtime.StateBuilding {
		t.Fatalf("build action = %#v", action)
	}
	build := execution.BuildOutput{Runtime: scenario.RuntimeNode, Incus: &execution.IncusBuildReference{
		Project: "catalog-build", WorkflowID: entry.ID, CandidateRevisionID: entry.ID, Attempt: action.Identity.StateVersion,
		InstanceName: "catalog-build-node", Alias: "catalog-build-node", Fingerprint: strings.Repeat("c", 64),
	}}
	if err := database.Catalog.CompleteCatalogBuild(ctx, *action, build, now); err != nil {
		t.Fatalf("complete catalog build: %v", err)
	}

	action = claimCatalogRuntimeAction(t, database, now)
	if action.Identity.State != runtime.StateArtifactPublishing {
		t.Fatalf("artifact action = %#v", action)
	}
	artifact := execution.ArtifactReference{Runtime: scenario.RuntimeNode, IncusAlias: "catalog-candidate-node", IncusFingerprint: strings.Repeat("d", 64)}
	if err := database.Catalog.CompleteCatalogArtifactPublish(ctx, *action, artifact, now); err != nil {
		t.Fatalf("complete catalog artifact publication: %v", err)
	}

	action = claimCatalogRuntimeAction(t, database, now)
	if action.Identity.State != runtime.StateVerifying {
		t.Fatalf("verification action = %#v", action)
	}
	environment := execution.VerificationEnvironment{
		Runtime: scenario.RuntimeNode, Name: "catalog-verify-node", UID: "catalog-verify-uid", WorkflowID: entry.ID, Attempt: action.Identity.StateVersion,
	}
	if err := database.Catalog.RecordCatalogVerificationEnvironment(ctx, *action, environment, now); err != nil {
		t.Fatalf("record catalog verification environment: %v", err)
	}
	report := execution.VerificationReport{
		Passed: true, Answers: []execution.ExecutionResult{{Location: "host", ExitCode: 0}},
		Checkpoints: []execution.CheckpointResult{{ID: "ready", Passed: true, Summary: "ready"}}, Summary: "catalog verification passed",
	}
	if err := database.Catalog.CompleteCatalogVerification(ctx, *action, report, now); err != nil {
		t.Fatalf("complete catalog verification: %v", err)
	}

	scenarioID := scenario.NewID()
	intent := catalogdomain.Commit{
		ID: catalogdomain.EntryCommitIDFor(initializedRelease.ID, entry.ID), ReleaseID: initializedRelease.ID, EntryID: entry.ID,
		ScenarioID: scenarioID, ScenarioRevisionID: "chrev-aaaaaaaaaaaaaaaa", SourceSlug: scenario.SourceSlugFor(entry.Title, scenarioID),
		State: catalogdomain.CommitPrepared, StateVersion: 1, RuntimeAttempt: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	}
	committing, commits, err := database.Catalog.PrepareReleaseCommit(ctx, initializedRelease.ID, []catalogdomain.Commit{intent}, now)
	if err != nil || committing.State != catalogdomain.ReleaseCommitting || len(commits) != 1 {
		t.Fatalf("prepare catalog commit = release:%#v commits:%#v err=%v", committing, commits, err)
	}

	commitAction := claimCatalogRuntimeAction(t, database, now)
	if commitAction.Identity.Scope != runtime.ScopeCatalogCommit || commitAction.Identity.State != runtime.StateScenarioPublishing {
		t.Fatalf("commit action = %#v", commitAction)
	}
	if err := database.Catalog.RenewCatalogRuntimeLease(ctx, commitAction.Credential(), time.Minute, now.Add(time.Second)); err != nil {
		t.Fatalf("renew catalog commit runtime lease: %v", err)
	}
	finalArtifact := execution.ArtifactReference{Runtime: scenario.RuntimeNode, IncusAlias: "catalog-scenario-node", IncusFingerprint: strings.Repeat("e", 64)}
	if err := database.Catalog.CompleteCatalogScenarioPublication(ctx, *commitAction, finalArtifact, now); err != nil {
		t.Fatalf("publish final catalog artifact: %v", err)
	}
	materializedRevision := "sha256:" + strings.Repeat("f", 64)
	materialized, err := database.Catalog.MarkCommitMaterialized(ctx, initializedRelease.ID, commits[0].ID, materializedRevision, now)
	if err != nil || materialized.State != catalogdomain.CommitMaterialized {
		t.Fatalf("mark catalog source materialized = %#v, err=%v", materialized, err)
	}
	if materialized.MaterializedRevision != materializedRevision {
		t.Fatalf("materialized catalog revision = %q, want %q", materialized.MaterializedRevision, materializedRevision)
	}
	diagnostic, err := publication.NewDiagnostic(publication.Transient(errors.New("temporary materialization filesystem failure")), now)
	if err != nil {
		t.Fatalf("create catalog publication diagnostic: %v", err)
	}
	transient, err := database.Catalog.RecordCatalogFinalizerFailure(ctx, initializedRelease.ID, diagnostic)
	if err != nil {
		t.Fatalf("record catalog publication diagnostic: %v", err)
	}
	if transient.State != catalogdomain.ReleaseCommitting || transient.FinalizerErrorCategory != publication.CategoryTransient || transient.FinalizerNextRetryAt == nil {
		t.Fatalf("catalog transient publication state = %#v", transient)
	}
	reloaded, err := database.Catalog.Release(ctx, initializedRelease.ID)
	if err != nil {
		t.Fatalf("reload catalog publication diagnostic: %v", err)
	}
	if reloaded.FinalizerLastError != "temporary materialization filesystem failure" || reloaded.FinalizerNextRetryAt == nil {
		t.Fatalf("reloaded catalog publication diagnostic = %#v", reloaded)
	}
	authoring := insertScenarioLifecycleFixture(t, database, "authoring", "catalog-race-author", now.Add(time.Second))
	if _, err := database.Catalog.CompleteReleaseCommit(ctx, initializedRelease.ID, now); !errors.Is(err, catalogdomain.ErrBaselineEstablished) {
		t.Fatalf("catalog commit over authoring content = %v, want baseline established", err)
	}
	tx, err := database.conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM scenario_revisions WHERE scenario_id = ?`, authoring.scenario.ID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("remove catalog race scenario revision: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM scenarios WHERE id = ?`, authoring.scenario.ID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("remove catalog race scenario: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit catalog race cleanup: %v", err)
	}
	ready, err := database.Catalog.CompleteReleaseCommit(ctx, initializedRelease.ID, now.Add(time.Minute))
	if err != nil || ready.State != catalogdomain.ReleaseReady {
		t.Fatalf("complete catalog release = %#v, err=%v", ready, err)
	}
	if ready.FinalizerErrorCategory != publication.CategoryUnknown || ready.FinalizerLastError != "" || ready.FinalizerLastAttemptedAt != nil || ready.FinalizerNextRetryAt != nil {
		t.Fatalf("ready catalog release retained finalizer diagnostic = %#v", ready)
	}
	stable, err := database.Scenario.GetScenario(ctx, scenarioID)
	if err != nil || stable.ActiveRevisionID != intent.ScenarioRevisionID {
		t.Fatalf("committed catalog scenario = %#v, err=%v", stable, err)
	}
	published, err := database.Scenario.GetScenarioRevision(ctx, scenarioID, intent.ScenarioRevisionID)
	if err != nil || published.MaterializedRevision != materializedRevision || published.Type != scenario.ScenarioOperationsScenario {
		t.Fatalf("committed catalog revision = %#v, err=%v", published, err)
	}
	if materialized.MaterializedAt == nil || !published.PublishedAt.Equal(materialized.MaterializedAt.UTC()) {
		t.Fatalf("committed catalog published_at = %s, materialized_at = %#v", published.PublishedAt, materialized.MaterializedAt)
	}
	storedCommits, err := database.Catalog.Commits(ctx, initializedRelease.ID)
	if err != nil || len(storedCommits) != 1 || storedCommits[0].State != catalogdomain.CommitCommitted {
		t.Fatalf("stored catalog commits = %#v, err=%v", storedCommits, err)
	}
	bootstrap, err := database.Catalog.CatalogBootstrapState(ctx)
	if err != nil || len(bootstrap.Releases) != 1 || bootstrap.Releases[0].State != catalogdomain.ReleaseReady || bootstrap.PublishedScenarioCount != 1 {
		t.Fatalf("ready catalog bootstrap state = %#v, err=%v", bootstrap, err)
	}
}

func TestCatalogBootstrapWaitsForFailedReleaseResourceCleanup(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 7, 10, 0, 0, 0, time.UTC)
	_, entry := createCatalogRuntimeFixture(t, database, now)
	action, err := database.Catalog.ClaimCatalogRuntimeAction(ctx, "catalog-cleanup-worker", time.Minute, now)
	if err != nil || action == nil {
		t.Fatalf("claim failed bootstrap build = %#v, err=%v", action, err)
	}
	build := execution.BuildOutput{Runtime: scenario.RuntimeNode, Incus: &execution.IncusBuildReference{
		Project: "catalog-failed-build", WorkflowID: entry.ID, CandidateRevisionID: entry.ID, Attempt: action.Identity.StateVersion,
		InstanceName: "catalog-failed-node", Alias: "catalog-failed-node", Fingerprint: strings.Repeat("d", 64),
	}}
	if err := database.Catalog.CompleteCatalogBuild(ctx, *action, build, now); err != nil {
		t.Fatalf("complete failed bootstrap build: %v", err)
	}
	action = claimCatalogRuntimeAction(t, database, now)
	if err := database.Catalog.ReportCatalogRuntimeArtifactFailure(ctx, *action, runtime.Failure{
		Class: runtime.FailureArtifact, Code: "BROKEN_ARTIFACT", Summary: "catalog artifact is invalid",
	}, nil, now); err != nil {
		t.Fatalf("fail catalog bootstrap artifact: %v", err)
	}
	if err := database.Catalog.EnsureCatalogResourceReaps(ctx, now); err != nil {
		t.Fatalf("discover failed bootstrap cleanup: %v", err)
	}
	state, err := database.Catalog.CatalogBootstrapState(ctx)
	if err != nil || !state.FailedCleanupPending || state.PublishedScenarioCount != 0 {
		t.Fatalf("failed bootstrap state before cleanup = %#v, err=%v", state, err)
	}
	for {
		claim, err := database.Catalog.ClaimCatalogResourceReap(ctx, "catalog-cleanup-worker", time.Minute, now)
		if err != nil {
			t.Fatalf("claim failed bootstrap cleanup: %v", err)
		}
		if claim == nil {
			break
		}
		if err := database.Catalog.CompleteCatalogResourceReap(ctx, *claim, "", now); err != nil {
			t.Fatalf("complete failed bootstrap cleanup: %v", err)
		}
	}
	state, err = database.Catalog.CatalogBootstrapState(ctx)
	if err != nil || state.FailedCleanupPending {
		t.Fatalf("failed bootstrap state after cleanup = %#v, err=%v", state, err)
	}
}

func TestCatalogSourceAndEntryRuntimeRetriesAreStateScoped(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 13, 0, 0, 0, time.UTC)

	digest := catalogdomain.BundleDigest("sha256:" + strings.Repeat("a", 64))
	sourceRelease := catalogdomain.Release{
		ID: catalogdomain.ReleaseIDForBundle(digest), BundleDigest: digest, State: catalogdomain.ReleasePending,
		SourceAttempt: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := database.Catalog.CreateOrGetRelease(ctx, sourceRelease); err != nil {
		t.Fatalf("create source retry release: %v", err)
	}
	for attempt := 1; attempt <= runtime.MaxAttempts; attempt++ {
		updated, err := database.Catalog.ReportSourceInfrastructureFailure(ctx, sourceRelease.ID, "registry unavailable", now.Add(time.Duration(attempt)*time.Second))
		if err != nil {
			t.Fatalf("record source retry %d: %v", attempt, err)
		}
		if attempt < runtime.MaxAttempts {
			if updated.State != catalogdomain.ReleasePending || updated.SourceAttempt != attempt+1 {
				t.Fatalf("source retry %d = %#v", attempt, updated)
			}
			continue
		}
		if updated.State != catalogdomain.ReleaseFailed {
			t.Fatalf("exhausted source retry = %#v", updated)
		}
	}

	release, entry := createCatalogRuntimeFixture(t, database, now)
	action, err := database.Catalog.ClaimCatalogRuntimeAction(ctx, "catalog-worker", time.Second, now)
	if err != nil || action == nil {
		t.Fatalf("claim initial catalog runtime action = %#v, err=%v", action, err)
	}
	identity := action.Identity
	if identity.Scope != runtime.ScopeCatalogEntry || identity.State != runtime.StateBuilding {
		t.Fatalf("initial catalog runtime action = %#v", action)
	}
	expiredAt := now.Add(2 * time.Second)
	for expiration := 1; expiration <= runtime.MaxAttempts; expiration++ {
		claimed, err := database.Catalog.ClaimCatalogRuntimeAction(ctx, "catalog-worker", time.Second, expiredAt)
		if err != nil {
			t.Fatalf("recover expired entry lease %d: %v", expiration, err)
		}
		if claimed != nil {
			t.Fatalf("expired entry lease %d was claimed without recovery: %#v", expiration, claimed)
		}
		stored, err := database.Catalog.Entry(ctx, entry.ID)
		if err != nil {
			t.Fatalf("read entry after expired lease %d: %v", expiration, err)
		}
		if expiration == runtime.MaxAttempts {
			if stored.State != catalogdomain.EntryFailed || stored.RuntimeAttempt != 0 {
				t.Fatalf("exhausted entry lease recovery = %#v", stored)
			}
			failed, err := database.Catalog.Release(ctx, release.ID)
			if err != nil || failed.State != catalogdomain.ReleaseFailed {
				t.Fatalf("release after exhausted entry lease = %#v, err=%v", failed, err)
			}
			break
		}
		if stored.State != catalogdomain.EntryBuilding || stored.StateVersion != identity.StateVersion || stored.RuntimeAttempt != expiration+1 || stored.LeaseExpires != nil {
			t.Fatalf("entry lease recovery %d = %#v", expiration, stored)
		}
		action = claimCatalogRuntimeAction(t, database, stored.NextRunAt)
		if action.Identity != identity {
			t.Fatalf("entry retry identity changed: got %#v, want %#v", action.Identity, identity)
		}
		stored, err = database.Catalog.Entry(ctx, entry.ID)
		if err != nil || stored.LeaseExpires == nil {
			t.Fatalf("read renewed entry lease = %#v, err=%v", stored, err)
		}
		expiredAt = stored.LeaseExpires.Add(time.Second)
	}
}

func TestCatalogDeterministicFinalizerFailureIsTerminal(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 15, 0, 0, 0, time.UTC)
	digest := catalogdomain.BundleDigest("sha256:" + strings.Repeat("c", 64))
	release := catalogdomain.Release{
		ID: catalogdomain.ReleaseIDForBundle(digest), BundleDigest: digest, State: catalogdomain.ReleasePending,
		SourceAttempt: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	}
	created, inserted, err := database.Catalog.CreateOrGetRelease(ctx, release)
	if err != nil || !inserted {
		t.Fatalf("create catalog finalizer release = %#v, inserted=%v, err=%v", created, inserted, err)
	}
	entry := catalogRepositoryEntry(created.ID, now)
	initialized := *created
	initialized.Name = "catalog-finalizer"
	initialized.Version = "2026.08.04"
	initialized.SourceDigest = catalogdomain.ContentRevision("sha256:" + strings.Repeat("d", 64))
	if _, err := database.Catalog.InitializeRelease(ctx, initialized, []catalogdomain.Entry{entry}, now); err != nil {
		t.Fatalf("initialize catalog finalizer release: %v", err)
	}
	diagnostic, err := publication.NewDiagnostic(publication.Deterministic(errors.New("catalog commit does not match immutable source")), now)
	if err != nil {
		t.Fatalf("create deterministic catalog diagnostic: %v", err)
	}
	failed, err := database.Catalog.RecordCatalogFinalizerFailure(ctx, initialized.ID, diagnostic)
	if err != nil {
		t.Fatalf("record deterministic catalog diagnostic: %v", err)
	}
	if failed.State != catalogdomain.ReleaseFailed || failed.FinalizerErrorCategory != publication.CategoryDeterministic || failed.FinalizerNextRetryAt != nil {
		t.Fatalf("failed catalog release = %#v", failed)
	}
	reloaded, err := database.Catalog.Release(ctx, initialized.ID)
	if err != nil {
		t.Fatalf("reload failed catalog release: %v", err)
	}
	if reloaded.FinalizerLastError != "catalog commit does not match immutable source" || reloaded.FinalizerLastAttemptedAt == nil {
		t.Fatalf("reloaded failed catalog diagnostic = %#v", reloaded)
	}
}

func createCatalogRuntimeFixture(t *testing.T, database *Store, now time.Time) (*catalogdomain.Release, catalogdomain.Entry) {
	t.Helper()
	digest := catalogdomain.BundleDigest("sha256:" + strings.Repeat("b", 64))
	release := catalogdomain.Release{
		ID: catalogdomain.ReleaseIDForBundle(digest), BundleDigest: digest, State: catalogdomain.ReleasePending,
		SourceAttempt: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	}
	created, inserted, err := database.Catalog.CreateOrGetRelease(context.Background(), release)
	if err != nil || !inserted {
		t.Fatalf("create entry runtime release = %#v, inserted=%v, err=%v", created, inserted, err)
	}
	entry := catalogRepositoryEntry(created.ID, now)
	initializedInput := *created
	initializedInput.Name = "runtime-retry"
	initializedInput.Version = "v1"
	initializedInput.SourceDigest = catalogdomain.ContentRevision("sha256:" + strings.Repeat("c", 64))
	initialized, err := database.Catalog.InitializeRelease(context.Background(), initializedInput, []catalogdomain.Entry{entry}, now)
	if err != nil || initialized.State != catalogdomain.ReleaseInstalling {
		t.Fatalf("initialize entry runtime release = %#v, err=%v", initialized, err)
	}
	return initialized, entry
}

func catalogRepositoryEntry(releaseID string, now time.Time) catalogdomain.Entry {
	return catalogdomain.Entry{
		ID: catalogdomain.EntryIDFor(releaseID, "scenarios/linux/runtime-fixture"), ReleaseID: releaseID, SourcePath: "scenarios/linux/runtime-fixture",
		SourceRef: "node-runtime-fixture", Title: "Runtime fixture", Type: scenario.ScenarioOperationsScenario, Tags: []string{"runtime-fixture"}, ContentRevision: catalogdomain.ContentRevision("sha256:" + strings.Repeat("e", 64)),
		ArchiveSHA256: "sha256:" + strings.Repeat("f", 64),
		Snapshot: execution.Snapshot{Runtime: scenario.RuntimeNode, Checkpoints: []execution.CheckpointSnapshot{{ID: "ready", Node: "host"}},
			Node: &execution.NodeRuntimeSnapshot{BaseImageFingerprint: strings.Repeat("a", 64), ProfileRevision: "catalog-node-profile", NetworkPolicyRevision: "catalog-node-network",
				Nodes: []execution.NodeSnapshot{{Name: "host", Title: "Host"}}, Resources: execution.NodeResources{CPU: "1", Memory: "512MiB", Processes: 128, RootDisk: "5GiB"}}},
		State: catalogdomain.EntryBuilding, StateVersion: 1, RuntimeAttempt: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

func claimCatalogRuntimeAction(t *testing.T, database *Store, now time.Time) *runtime.Context {
	t.Helper()
	action, err := database.Catalog.ClaimCatalogRuntimeAction(context.Background(), "catalog-worker", time.Minute, now)
	if err != nil || action == nil {
		t.Fatalf("claim catalog runtime action = %#v, err=%v", action, err)
	}
	return action
}
