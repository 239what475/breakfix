package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	scenariodomain "github.com/breakfix/breakfix/internal/domain/scenario"
)

func TestDeprecateAuthoringScenarioRetainsImmutableHistory(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 6, 8, 0, 0, 0, time.UTC)
	fixture := insertScenarioLifecycleFixture(t, database, scenariodomain.SourceAuthoring, "author-one", now)

	updated, err := database.Scenario.DeprecateAuthoringScenario(ctx, "author-one", fixture.scenario.ID, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("deprecate authoring scenario: %v", err)
	}
	if updated.State != scenariodomain.StateDeprecated || updated.ActiveRevisionID != fixture.revision.ID || updated.SourceSlug != fixture.scenario.SourceSlug {
		t.Fatalf("deprecated scenario = %#v", updated)
	}
	stable, err := database.Scenario.GetScenario(ctx, fixture.scenario.ID)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := database.Scenario.GetScenarioRevision(ctx, fixture.scenario.ID, fixture.revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stable.ActiveRevisionID != fixture.revision.ID || revision.State != scenariodomain.RevisionActive || revision.RunnableRevisionRef != fixture.revision.RunnableRevisionRef || revision.VerificationReportRef != fixture.revision.VerificationReportRef {
		t.Fatalf("deprecated lifecycle changed immutable history: scenario=%#v revision=%#v", stable, revision)
	}
	active, err := database.Scenario.ListActiveScenarioRevisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range active {
		if value.Scenario.ID == fixture.scenario.ID {
			t.Fatalf("deprecated scenario remains visible in the active catalog: %#v", value)
		}
	}

	if _, err := database.Scenario.DeprecateAuthoringScenario(ctx, "author-one", fixture.scenario.ID, now.Add(2*time.Minute)); !errors.Is(err, scenariodomain.ErrNotMutable) {
		t.Fatalf("repeat deprecation = %v, want not mutable", err)
	}
}

func TestDeprecateAuthoringScenarioRejectsWrongOwnerAndReleaseScenario(t *testing.T) {
	database := newTestDB(t)
	now := time.Date(2026, time.August, 6, 9, 0, 0, 0, time.UTC)
	authorFixture := insertScenarioLifecycleFixture(t, database, scenariodomain.SourceAuthoring, "author-one", now)
	if _, err := database.Scenario.DeprecateAuthoringScenario(context.Background(), "author-two", authorFixture.scenario.ID, now.Add(time.Minute)); !errors.Is(err, scenariodomain.ErrNotFound) {
		t.Fatalf("wrong owner deprecation = %v, want not found", err)
	}
	releaseFixture := insertScenarioLifecycleFixture(t, database, scenariodomain.SourceRelease, "", now.Add(2*time.Minute))
	if _, err := database.Scenario.DeprecateAuthoringScenario(context.Background(), "author-one", releaseFixture.scenario.ID, now.Add(3*time.Minute)); !errors.Is(err, scenariodomain.ErrNotMutable) {
		t.Fatalf("release scenario deprecation = %v, want not mutable", err)
	}
}

type scenarioLifecycleFixture struct {
	scenario scenariodomain.Scenario
	revision scenariodomain.Revision
}

func insertScenarioLifecycleFixture(t *testing.T, database *Store, sourceKind scenariodomain.SourceKind, owner string, now time.Time) scenarioLifecycleFixture {
	t.Helper()
	index := strings.TrimPrefix(scenario.NewID(), "chal-")[:8]
	scenarioID := "chal-" + index
	revisionID := scenario.NewRevisionID()
	sourceSlug := "lifecycle-" + index
	contentRevision := "sha256:" + strings.Repeat("a", 64)
	materializedRevision := "sha256:" + strings.Repeat("b", 64)
	publicRevision := testRunnableRevision(t)
	publicRevision.Spec.Identity = runnable.ContentIdentity{Kind: "operations", ID: scenarioID, Revision: contentRevision}
	specDigest, err := publicRevision.Spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	publicRevision.Artifact.BuiltFromSpecDigest = specDigest
	revisionDigest, err := publicRevision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	runnableRef := runnable.RevisionReference{ID: "runnable-revision-" + index, Digest: revisionDigest}
	if err := database.Runnable.StoreRunnableRevision(context.Background(), runnable.StoredRevision{Reference: runnableRef, Revision: publicRevision, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	report := testVerificationReport(t, publicRevision)
	reportDigest, err := report.Digest(publicRevision)
	if err != nil {
		t.Fatal(err)
	}
	reportRef := runnable.VerificationReportReference{ID: "verification-report-" + index, Digest: reportDigest}
	if err := database.Runnable.StoreVerificationReport(context.Background(), runnable.StoredVerificationReport{Reference: reportRef, Report: report, RunnableRevision: publicRevision, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	fixture := scenarioLifecycleFixture{
		scenario: scenariodomain.Scenario{
			ID: scenarioID, SourceKind: sourceKind, SourceRef: "lifecycle/topic/" + index, OwnerUserID: owner,
			State: scenariodomain.StateActive, ActiveRevisionID: revisionID, SourceSlug: sourceSlug, CreatedAt: now, UpdatedAt: now,
		},
		revision: scenariodomain.Revision{
			ID: revisionID, ScenarioID: scenarioID, SourceKind: sourceKind, SourceRef: "lifecycle/topic/" + index, SourceRevisionID: "1",
			Title: "Lifecycle scenario " + index, Type: scenario.ScenarioOperationsScenario, ContentRevision: contentRevision, SourceSlug: sourceSlug,
			MaterializedPath: scenario.MaterializedPath(sourceSlug, revisionID), MaterializedRevision: materializedRevision, RunnableRevisionRef: runnableRef, VerificationReportRef: reportRef,
			State: scenariodomain.RevisionActive, PublishedAt: now, CreatedAt: now,
		},
	}
	tx, err := database.conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertPersistedScenarioTx(context.Background(), tx, fixture.scenario); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := insertPersistedScenarioRevisionTx(context.Background(), tx, fixture.revision); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return fixture
}
