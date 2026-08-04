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
	roadmaptest "github.com/breakfix/breakfix/internal/testkit/roadmap"
)

func TestCatalogRepositoryCommitsReleaseAndRoadmapAtomically(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 4, 12, 0, 0, 0, time.UTC)
	digest := catalogdomain.BundleDigest("sha256:" + strings.Repeat("a", 64))
	release := catalogdomain.Release{
		ID:           catalogdomain.ReleaseIDForBundle(digest),
		BundleDigest: digest,
		State:        catalogdomain.ReleasePending,
		DeadlineAt:   now.Add(time.Hour),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	created, inserted, err := database.Catalog.CreateOrGetRelease(ctx, release)
	if err != nil {
		t.Fatalf("create catalog release: %v", err)
	}
	if !inserted || created.ID != release.ID {
		t.Fatalf("created catalog release = %#v, inserted=%v", created, inserted)
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
	if err != nil {
		t.Fatalf("initialize catalog release: %v", err)
	}
	initialized = *initializedRelease
	if initialized.State != catalogdomain.ReleaseInstalling {
		t.Fatalf("initialized release state = %s, want %s", initialized.State, catalogdomain.ReleaseInstalling)
	}

	assertNoCurrentRoadmap(t, database)
	claim := claimCatalogEntry(t, database, initialized.ID, now)
	build := execution.BuildOutput{Runtime: challenge.RuntimeNode, Incus: &execution.IncusBuildReference{
		Project: "catalog-build", WorkflowID: entry.ID, CandidateRevisionID: entry.ID, Attempt: int64(claim.Entry.Attempt),
		InstanceName: "catalog-build-node", Alias: "catalog-build-node", Fingerprint: strings.Repeat("c", 64),
	}}
	if _, err := database.Catalog.CompleteEntryBuild(ctx, claim, build, now); err != nil {
		t.Fatalf("complete catalog build: %v", err)
	}

	claim = claimCatalogEntry(t, database, initialized.ID, now)
	artifact := execution.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: "catalog-candidate-node", IncusFingerprint: strings.Repeat("d", 64)}
	if _, err := database.Catalog.CompleteEntryArtifactPublish(ctx, claim, artifact, now); err != nil {
		t.Fatalf("complete catalog artifact publish: %v", err)
	}

	claim = claimCatalogEntry(t, database, initialized.ID, now)
	environment := execution.VerificationEnvironment{
		Runtime: challenge.RuntimeNode, Name: "catalog-verify-node", UID: "catalog-verify-uid",
		WorkflowID: entry.ID, Attempt: int64(claim.Entry.Attempt),
	}
	if _, err := database.Catalog.RecordEntryVerificationEnvironment(ctx, claim, environment, now); err != nil {
		t.Fatalf("record catalog verification environment: %v", err)
	}
	report := execution.VerificationReport{
		Passed:      true,
		Answers:     []execution.ExecutionResult{{Location: "host", ExitCode: 0}},
		Checkpoints: []execution.CheckpointResult{{ID: "ready", Passed: true, Summary: "ready"}},
		Summary:     "catalog verification passed",
	}
	if _, err := database.Catalog.CompleteEntryVerification(ctx, claim, report, now); err != nil {
		t.Fatalf("complete catalog verification: %v", err)
	}

	challengeID := challenge.NewID()
	intent := catalogdomain.Commit{
		ID:          catalogdomain.EntryCommitIDFor(initialized.ID, entry.ID),
		ReleaseID:   initialized.ID,
		EntryID:     entry.ID,
		ChallengeID: challengeID,
		SourceSlug:  challenge.SourceSlugFor(binding.Challenge.Title, challengeID),
		State:       catalogdomain.CommitPrepared,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	committing, commits, err := database.Catalog.PrepareReleaseCommit(ctx, initialized.ID, []catalogdomain.Commit{intent}, now)
	if err != nil {
		t.Fatalf("prepare catalog commit: %v", err)
	}
	if committing.State != catalogdomain.ReleaseCommitting || len(commits) != 1 || commits[0].ID != intent.ID {
		t.Fatalf("prepared catalog commit = release:%#v commits:%#v", committing, commits)
	}
	assertNoCurrentRoadmap(t, database)

	commitLease, err := database.Catalog.ClaimReleaseCommit(ctx, initialized.ID, "catalog-server", time.Minute, now)
	if err != nil {
		t.Fatalf("claim catalog release commit: %v", err)
	}
	if commitLease == nil {
		t.Fatal("catalog release commit was not claimed")
	}
	finalArtifact := execution.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: "catalog-challenge-node", IncusFingerprint: strings.Repeat("e", 64)}
	committedEntry, err := database.Catalog.CompleteCommitArtifact(ctx, *commitLease, commits[0], finalArtifact, now)
	if err != nil {
		t.Fatalf("publish final catalog artifact: %v", err)
	}
	materialized, err := database.Catalog.MarkCommitMaterialized(ctx, *commitLease, *committedEntry, now)
	if err != nil {
		t.Fatalf("mark catalog source materialized: %v", err)
	}
	if materialized.State != catalogdomain.CommitMaterialized {
		t.Fatalf("materialized catalog commit = %#v", materialized)
	}
	assertNoCurrentRoadmap(t, database)

	revision, err := roadmap.CompilePortable(portable, map[string]roadmap.ChallengeRef{
		binding.Challenge.Path: {
			ID: challengeID, SourceRef: binding.Challenge.SourceRef, Title: binding.Challenge.Title,
			ContentRevision: binding.Challenge.ContentRevision,
		},
	})
	if err != nil {
		t.Fatalf("compile catalog roadmap revision: %v", err)
	}
	ready, err := database.Catalog.CompleteReleaseCommit(ctx, *commitLease, revision, now)
	if err != nil {
		t.Fatalf("complete catalog release commit: %v", err)
	}
	if ready.State != catalogdomain.ReleaseReady {
		t.Fatalf("ready catalog release = %#v", ready)
	}
	current, err := database.Roadmap.CurrentRoadmap(ctx)
	if err != nil {
		t.Fatalf("read committed roadmap: %v", err)
	}
	if len(current.ChallengeBindings) != 1 || current.ChallengeBindings[0].Challenge.ID != challengeID {
		t.Fatalf("committed roadmap = %#v", current)
	}
	storedCommits, err := database.Catalog.Commits(ctx, initialized.ID)
	if err != nil {
		t.Fatalf("read committed catalog entries: %v", err)
	}
	if len(storedCommits) != 1 || storedCommits[0].State != catalogdomain.CommitCommitted {
		t.Fatalf("stored catalog commits = %#v", storedCommits)
	}
	var topicProcessed, challengeProcessed bool
	if err := database.conn.QueryRowContext(ctx, `SELECT topic_processed, challenge_processed FROM roadmap_entries WHERE challenge_id = ?`, challengeID).Scan(&topicProcessed, &challengeProcessed); err != nil {
		t.Fatalf("read catalog roadmap baseline: %v", err)
	}
	if !topicProcessed || !challengeProcessed {
		t.Fatalf("catalog roadmap baseline = topic:%v challenge:%v", topicProcessed, challengeProcessed)
	}
}

func catalogRepositoryEntry(releaseID string, binding roadmap.PortableChallengeBinding, now time.Time) catalogdomain.Entry {
	return catalogdomain.Entry{
		ID:              catalogdomain.EntryIDFor(releaseID, binding.Challenge.Path),
		ReleaseID:       releaseID,
		SourcePath:      binding.Challenge.Path,
		SourceRef:       binding.Challenge.SourceRef,
		Title:           binding.Challenge.Title,
		ContentRevision: catalogdomain.ContentRevision(binding.Challenge.ContentRevision),
		ArchiveSHA256:   "sha256:" + strings.Repeat("f", 64),
		Snapshot: execution.Snapshot{
			Runtime:     challenge.RuntimeNode,
			Checkpoints: []execution.CheckpointSnapshot{{ID: "ready", Node: "host"}},
			Node: &execution.NodeRuntimeSnapshot{
				BaseImageFingerprint:  strings.Repeat("a", 64),
				ProfileRevision:       "catalog-node-profile",
				NetworkPolicyRevision: "catalog-node-network",
				Nodes:                 []execution.NodeSnapshot{{Name: "host", Title: "Host"}},
				Resources: execution.NodeResources{
					CPU: "1", Memory: "512MiB", Processes: 128, RootDisk: "5GiB",
				},
			},
		},
		State:     catalogdomain.EntryPending,
		NextRunAt: now,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func claimCatalogEntry(t *testing.T, database *Store, releaseID string, now time.Time) catalogdomain.EntryClaim {
	t.Helper()
	claim, err := database.Catalog.ClaimEntry(context.Background(), releaseID, "catalog-server", time.Minute, now)
	if err != nil {
		t.Fatalf("claim catalog entry: %v", err)
	}
	if claim == nil {
		t.Fatal("catalog entry was not claimed")
	}
	return *claim
}

func assertNoCurrentRoadmap(t *testing.T, database *Store) {
	t.Helper()
	_, err := database.Roadmap.CurrentRoadmap(context.Background())
	if !errors.Is(err, roadmap.ErrNoCurrentRevision) {
		t.Fatalf("current roadmap before final catalog commit = %v, want no current revision", err)
	}
}
