package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
	roadmaptest "github.com/breakfix/breakfix/internal/testkit/roadmap"
)

func TestCatalogRepositoryPublishesRuntimeActionsAndCommitsAtomically(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 12, 0, 0, 0, time.UTC)
	digest := catalogdomain.BundleDigest("sha256:" + strings.Repeat("a", 64))
	release := catalogdomain.Release{
		ID: catalogdomain.ReleaseIDForBundle(digest), BundleDigest: digest, State: catalogdomain.ReleasePending,
		SourceAttempt: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	}
	created, inserted, err := database.Catalog.CreateOrGetRelease(ctx, release)
	if err != nil || !inserted || created.ID != release.ID {
		t.Fatalf("create catalog release = %#v, inserted=%v, err=%v", created, inserted, err)
	}

	portable := roadmaptest.PortableRevision()
	portable.Topics = portable.Topics[:1]
	portable.ChallengeBindings = portable.ChallengeBindings[:1]
	portable.TopicEdges = nil
	portable.ChallengeEdges = nil
	binding := portable.ChallengeBindings[0]
	entry := catalogRepositoryEntry(release.ID, binding, now)
	initialized := *created
	initialized.Name = "catalog-integration"
	initialized.Version = "2026.08.04"
	initialized.SourceDigest = catalogdomain.ContentRevision("sha256:" + strings.Repeat("b", 64))
	initializedRelease, err := database.Catalog.InitializeRelease(ctx, initialized, []catalogdomain.Entry{entry}, now)
	if err != nil || initializedRelease.State != catalogdomain.ReleaseInstalling {
		t.Fatalf("initialize catalog release = %#v, err=%v", initializedRelease, err)
	}
	assertNoCurrentRoadmap(t, database)

	action, err := database.Catalog.ClaimCatalogRuntimeAction(ctx, "catalog-worker", time.Second, now)
	if err != nil || action == nil {
		t.Fatalf("claim initial catalog runtime action = %#v, err=%v", action, err)
	}
	if action.Identity.Scope != runtime.ScopeCatalogEntry || action.Identity.State != runtime.StateBuilding {
		t.Fatalf("build action = %#v", action)
	}
	build := execution.BuildOutput{Runtime: challenge.RuntimeNode, Incus: &execution.IncusBuildReference{
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
	artifact := execution.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: "catalog-candidate-node", IncusFingerprint: strings.Repeat("d", 64)}
	if err := database.Catalog.CompleteCatalogArtifactPublish(ctx, *action, artifact, now); err != nil {
		t.Fatalf("complete catalog artifact publication: %v", err)
	}

	action = claimCatalogRuntimeAction(t, database, now)
	if action.Identity.State != runtime.StateVerifying {
		t.Fatalf("verification action = %#v", action)
	}
	environment := execution.VerificationEnvironment{
		Runtime: challenge.RuntimeNode, Name: "catalog-verify-node", UID: "catalog-verify-uid", WorkflowID: entry.ID, Attempt: action.Identity.StateVersion,
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

	challengeID := challenge.NewID()
	intent := catalogdomain.Commit{
		ID: catalogdomain.EntryCommitIDFor(initializedRelease.ID, entry.ID), ReleaseID: initializedRelease.ID, EntryID: entry.ID,
		ChallengeID: challengeID, SourceSlug: challenge.SourceSlugFor(binding.Challenge.Title, challengeID),
		State: catalogdomain.CommitPrepared, StateVersion: 1, RuntimeAttempt: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	}
	committing, commits, err := database.Catalog.PrepareReleaseCommit(ctx, initializedRelease.ID, []catalogdomain.Commit{intent}, now)
	if err != nil || committing.State != catalogdomain.ReleaseCommitting || len(commits) != 1 {
		t.Fatalf("prepare catalog commit = release:%#v commits:%#v err=%v", committing, commits, err)
	}
	assertNoCurrentRoadmap(t, database)

	commitAction := claimCatalogRuntimeAction(t, database, now)
	if commitAction.Identity.Scope != runtime.ScopeCatalogCommit || commitAction.Identity.State != runtime.StateChallengePublishing {
		t.Fatalf("commit action = %#v", commitAction)
	}
	if err := database.Catalog.RenewCatalogRuntimeLease(ctx, commitAction.Credential(), time.Minute, now.Add(time.Second)); err != nil {
		t.Fatalf("renew catalog commit runtime lease: %v", err)
	}
	finalArtifact := execution.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: "catalog-challenge-node", IncusFingerprint: strings.Repeat("e", 64)}
	if err := database.Catalog.CompleteCatalogChallengePublication(ctx, *commitAction, finalArtifact, now); err != nil {
		t.Fatalf("publish final catalog artifact: %v", err)
	}
	materialized, err := database.Catalog.MarkCommitMaterialized(ctx, initializedRelease.ID, commits[0].ID, now)
	if err != nil || materialized.State != catalogdomain.CommitMaterialized {
		t.Fatalf("mark catalog source materialized = %#v, err=%v", materialized, err)
	}

	revision, err := roadmap.CompilePortable(portable, map[string]roadmap.ChallengeRef{
		binding.Challenge.Path: {ID: challengeID, SourceRef: binding.Challenge.SourceRef, Title: binding.Challenge.Title, ContentRevision: binding.Challenge.ContentRevision, SourceSlug: intent.SourceSlug, MaterializedRevision: "sha256:" + strings.Repeat("f", 64)},
	})
	if err != nil {
		t.Fatalf("compile catalog roadmap revision: %v", err)
	}
	ready, err := database.Catalog.CompleteReleaseCommit(ctx, initializedRelease.ID, revision, now)
	if err != nil || ready.State != catalogdomain.ReleaseReady {
		t.Fatalf("complete catalog release = %#v, err=%v", ready, err)
	}
	current, err := database.Roadmap.CurrentRoadmap(ctx)
	if err != nil || len(current.ChallengeBindings) != 1 || current.ChallengeBindings[0].Challenge.ID != challengeID {
		t.Fatalf("committed roadmap = %#v, err=%v", current, err)
	}
	storedCommits, err := database.Catalog.Commits(ctx, initializedRelease.ID)
	if err != nil || len(storedCommits) != 1 || storedCommits[0].State != catalogdomain.CommitCommitted {
		t.Fatalf("stored catalog commits = %#v, err=%v", storedCommits, err)
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
	portable := roadmaptest.PortableRevision()
	binding := portable.ChallengeBindings[0]
	entry := catalogRepositoryEntry(created.ID, binding, now)
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

func catalogRepositoryEntry(releaseID string, binding roadmap.PortableChallengeBinding, now time.Time) catalogdomain.Entry {
	return catalogdomain.Entry{
		ID: catalogdomain.EntryIDFor(releaseID, binding.Challenge.Path), ReleaseID: releaseID, SourcePath: binding.Challenge.Path,
		SourceRef: binding.Challenge.SourceRef, Title: binding.Challenge.Title, ContentRevision: catalogdomain.ContentRevision(binding.Challenge.ContentRevision),
		ArchiveSHA256: "sha256:" + strings.Repeat("f", 64),
		Snapshot: execution.Snapshot{Runtime: challenge.RuntimeNode, Checkpoints: []execution.CheckpointSnapshot{{ID: "ready", Node: "host"}},
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

func assertNoCurrentRoadmap(t *testing.T, database *Store) {
	t.Helper()
	_, err := database.Roadmap.CurrentRoadmap(context.Background())
	if !errors.Is(err, roadmap.ErrNoCurrentRevision) {
		t.Fatalf("current roadmap before final catalog commit = %v, want no current revision", err)
	}
}
