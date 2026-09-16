package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestCatalogRepositoryCommitsOnlyMatchingPublicRunnableReferences(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.September, 16, 14, 0, 0, 0, time.UTC)
	bundle := catalogdomain.BundleDigest(catalogTestDigest("a"))
	release := catalogdomain.Release{
		ID: catalogdomain.ReleaseIDForBundle(bundle), BundleDigest: bundle, State: catalogdomain.ReleasePending,
		SourceAttempt: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	}
	created, inserted, err := database.Catalog.CreateOrGetRelease(ctx, release)
	if err != nil || !inserted {
		t.Fatalf("create catalog release = %#v, inserted=%v, err=%v", created, inserted, err)
	}
	entry := catalogPublicEntry(created.ID, now)
	initialized := *created
	initialized.Name = "public-runnable-catalog"
	initialized.Version = "2026.09.16"
	initialized.SourceDigest = catalogdomain.ContentRevision(catalogTestDigest("b"))
	if _, err := database.Catalog.InitializeRelease(ctx, initialized, []catalogdomain.Entry{entry}, now); err != nil {
		t.Fatalf("initialize catalog release: %v", err)
	}

	mismatch, mismatchReference := catalogStoredRunnableRevision(t, entry, "other-entry", "runnable-revision-mismatch", now)
	if err := database.Runnable.StoreRunnableRevision(ctx, mismatch); err != nil {
		t.Fatalf("store mismatched runnable revision: %v", err)
	}
	if err := database.Catalog.MarkCatalogEntryMaterialized(ctx, entry.ID, mismatchReference, now); err == nil || !strings.Contains(err.Error(), "does not match entry identity") {
		t.Fatalf("accept mismatched runnable revision = %v", err)
	}

	storedRevision, revisionReference := catalogStoredRunnableRevision(t, entry, entry.ID, "runnable-revision-catalog", now)
	if err := database.Runnable.StoreRunnableRevision(ctx, storedRevision); err != nil {
		t.Fatalf("store catalog runnable revision: %v", err)
	}
	if err := database.Catalog.MarkCatalogEntryMaterialized(ctx, entry.ID, revisionReference, now); err != nil {
		t.Fatalf("mark catalog entry materialized: %v", err)
	}
	if err := database.Catalog.MarkCatalogEntryMaterialized(ctx, entry.ID, revisionReference, now); err != nil {
		t.Fatalf("repeat catalog materialization: %v", err)
	}

	mismatchReport, mismatchReportReference := catalogStoredVerificationReport(t, mismatch.Revision, "verification-report-mismatch", now)
	if err := database.Runnable.StoreVerificationReport(ctx, mismatchReport); err != nil {
		t.Fatalf("store mismatched verification report: %v", err)
	}
	if err := database.Catalog.MarkCatalogEntryVerified(ctx, entry.ID, mismatchReportReference, now); err == nil || !strings.Contains(err.Error(), "does not match runnable revision") {
		t.Fatalf("accept mismatched verification report = %v", err)
	}

	storedReport, reportReference := catalogStoredVerificationReport(t, storedRevision.Revision, "verification-report-catalog", now)
	if err := database.Runnable.StoreVerificationReport(ctx, storedReport); err != nil {
		t.Fatalf("store catalog verification report: %v", err)
	}
	if err := database.Catalog.MarkCatalogEntryVerified(ctx, entry.ID, reportReference, now); err != nil {
		t.Fatalf("mark catalog entry verified: %v", err)
	}
	if err := database.Catalog.MarkCatalogEntryVerified(ctx, entry.ID, reportReference, now); err != nil {
		t.Fatalf("repeat catalog verification: %v", err)
	}

	scenarioID := scenario.NewID()
	commit := catalogdomain.Commit{
		ID: catalogdomain.EntryCommitIDFor(created.ID, entry.ID), ReleaseID: created.ID, EntryID: entry.ID,
		ScenarioID: scenarioID, ScenarioRevisionID: scenario.NewRevisionID(), SourceSlug: scenario.SourceSlugFor(entry.Title, scenarioID),
		State: catalogdomain.CommitPrepared, CreatedAt: now, UpdatedAt: now,
	}
	committing, commits, err := database.Catalog.PrepareReleaseCommit(ctx, created.ID, []catalogdomain.Commit{commit}, now)
	if err != nil || committing.State != catalogdomain.ReleaseCommitting || len(commits) != 1 {
		t.Fatalf("prepare catalog commit = release:%#v commits:%#v err:%v", committing, commits, err)
	}
	materializedRevision := catalogTestDigest("c")
	if _, err := database.Catalog.MarkCommitMaterialized(ctx, created.ID, commits[0].ID, materializedRevision, now.Add(time.Minute)); err != nil {
		t.Fatalf("mark catalog commit materialized: %v", err)
	}
	ready, err := database.Catalog.CompleteReleaseCommit(ctx, created.ID, now.Add(2*time.Minute))
	if err != nil || ready.State != catalogdomain.ReleaseReady {
		t.Fatalf("complete catalog release = %#v, %v", ready, err)
	}
	if repeated, err := database.Catalog.CompleteReleaseCommit(ctx, created.ID, now.Add(3*time.Minute)); err != nil || repeated.State != catalogdomain.ReleaseReady {
		t.Fatalf("repeat catalog release completion = %#v, %v", repeated, err)
	}

	published, err := database.Scenario.GetScenarioRevision(ctx, scenarioID, commit.ScenarioRevisionID)
	if err != nil {
		t.Fatalf("read catalog scenario revision: %v", err)
	}
	if published.RunnableRevisionRef != revisionReference || published.VerificationReportRef != reportReference || published.MaterializedRevision != materializedRevision {
		t.Fatalf("published public references = %#v", published)
	}
}

func catalogPublicEntry(releaseID string, now time.Time) catalogdomain.Entry {
	contentRevision := catalogdomain.ContentRevision(catalogTestDigest("d"))
	return catalogdomain.Entry{
		ID: catalogdomain.EntryIDFor(releaseID, "scenarios/public-runnable"), ReleaseID: releaseID,
		SourcePath: "scenarios/public-runnable", SourceRef: "public-runnable", Title: "Public Runnable Catalog", Type: scenario.ScenarioOperationsScenario,
		Tags: []string{"catalog", "public-runnable"}, ContentRevision: contentRevision,
		Source: runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "runnable-source://sha256/" + strings.TrimPrefix(catalogTestDigest("e"), "sha256:"), Digest: catalogTestDigest("e")},
		State:  catalogdomain.EntryMaterializing, StateVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func catalogStoredRunnableRevision(t *testing.T, entry catalogdomain.Entry, contentID, id string, now time.Time) (runnable.StoredRevision, runnable.RevisionReference) {
	t.Helper()
	revision := testRunnableRevision(t)
	revision.Spec.Identity = runnable.ContentIdentity{Kind: "operations", ID: contentID, Revision: string(entry.ContentRevision)}
	revision.Spec.Source = entry.Source
	specDigest, err := revision.Spec.Digest()
	if err != nil {
		t.Fatalf("digest catalog runnable spec: %v", err)
	}
	revision.Artifact.BuiltFromSpecDigest = specDigest
	digest, err := revision.Digest()
	if err != nil {
		t.Fatalf("digest catalog runnable revision: %v", err)
	}
	reference := runnable.RevisionReference{ID: id, Digest: digest}
	return runnable.StoredRevision{Reference: reference, Revision: revision, CreatedAt: now}, reference
}

func catalogStoredVerificationReport(t *testing.T, revision runnable.RunnableRevision, id string, now time.Time) (runnable.StoredVerificationReport, runnable.VerificationReportReference) {
	t.Helper()
	report := testVerificationReport(t, revision)
	digest, err := report.Digest(revision)
	if err != nil {
		t.Fatalf("digest catalog verification report: %v", err)
	}
	reference := runnable.VerificationReportReference{ID: id, Digest: digest}
	return runnable.StoredVerificationReport{Reference: reference, Report: report, RunnableRevision: revision, CreatedAt: now}, reference
}

func catalogTestDigest(character string) string { return "sha256:" + strings.Repeat(character, 64) }
