package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/catalog"
)

func TestCatalogReleasePersistsEntriesAndStableCommitIdentity(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 1, 9, 0, 0, 0, time.UTC)
	revision := catalog.ContentRevision("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	release, err := database.Catalog.CreateRelease(ctx, catalog.Release{
		ID:                      "release-foundation-1",
		Name:                    "foundation",
		Version:                 "2026.08.01",
		BundleDigest:            catalog.BundleDigest(revision),
		TaxonomyContentRevision: revision,
		CreatedAt:               now,
		UpdatedAt:               now,
	}, []catalog.Entry{{
		ID:              "release-entry-cleanup-logs",
		SourcePath:      "challenges/linux/cleanup-logs",
		ContentRevision: revision,
		CreatedAt:       now,
		UpdatedAt:       now,
	}})
	if err != nil {
		t.Fatalf("create catalog release: %v", err)
	}
	if release.State != catalog.ReleasePending {
		t.Fatalf("release state = %q, want %q", release.State, catalog.ReleasePending)
	}

	entries, err := database.Catalog.ListEntries(ctx, release.ID)
	if err != nil {
		t.Fatalf("list catalog entries: %v", err)
	}
	if len(entries) != 1 || entries[0].ReleaseID != release.ID || entries[0].ContentRevision != revision {
		t.Fatalf("entries = %#v, want one source entry", entries)
	}
	commit, err := database.Catalog.GetEntryCommit(ctx, entries[0].ID)
	if err != nil {
		t.Fatalf("get pending entry commit: %v", err)
	}
	if commit.State != catalog.CommitPending || commit.RuntimeIdentity != nil {
		t.Fatalf("pending commit = %#v, want no runtime identity", commit)
	}

	if _, err := database.Catalog.SetReleaseState(ctx, release.ID, catalog.ReleaseInstalling, "", now.Add(time.Minute)); err != nil {
		t.Fatalf("start catalog installation: %v", err)
	}
	if _, err := database.Catalog.SetEntryState(ctx, entries[0].ID, catalog.EntryReadyToCommit, "", "", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("mark catalog entry ready: %v", err)
	}
	identity := catalog.RuntimeIdentity{ChallengeID: "challenge-6f9d9e9c", Slug: "cleanup-logs-6f9d"}
	prepared, err := database.Catalog.PrepareCommit(ctx, entries[0].ID, identity, now.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("reserve catalog identity: %v", err)
	}
	if prepared.State != catalog.CommitPrepared || prepared.RuntimeIdentity == nil || *prepared.RuntimeIdentity != identity {
		t.Fatalf("prepared commit = %#v, want reserved identity", prepared)
	}

	// Recovery observes the durable identity rather than allocating another one.
	preparedAgain, err := database.Catalog.PrepareCommit(ctx, entries[0].ID, identity, now.Add(4*time.Minute))
	if err != nil {
		t.Fatalf("reuse catalog identity: %v", err)
	}
	if preparedAgain.RuntimeIdentity == nil || *preparedAgain.RuntimeIdentity != identity {
		t.Fatalf("recovered identity = %#v, want %#v", preparedAgain.RuntimeIdentity, identity)
	}
	materialized, err := database.Catalog.MarkCommitMaterialized(ctx, entries[0].ID, now.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("mark catalog commit materialized: %v", err)
	}
	if materialized.State != catalog.CommitMaterialized || materialized.MaterializedAt == nil {
		t.Fatalf("materialized commit = %#v", materialized)
	}
	completed, err := database.Catalog.CompleteCommit(ctx, entries[0].ID, now.Add(6*time.Minute))
	if err != nil {
		t.Fatalf("complete catalog commit: %v", err)
	}
	if completed.State != catalog.CommitCommitted || completed.CommittedAt == nil || !completed.Valid() {
		t.Fatalf("completed commit = %#v", completed)
	}
}
