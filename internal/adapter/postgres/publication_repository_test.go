package postgres

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
	"github.com/breakfix/breakfix/internal/domain/publication"
)

func TestPublicationRetentionIncludesHistoryAndOnlyNonTerminalIntents(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 7, 9, 0, 0, 0, time.UTC)
	historical := insertScenarioLifecycleFixture(t, database, "authoring", "retention-author", now)
	if _, err := database.Scenario.DeprecateAuthoringScenario(ctx, historical.scenario.OwnerUserID, historical.scenario.ID, now.Add(time.Second)); err != nil {
		t.Fatalf("deprecate historical scenario: %v", err)
	}

	workflow, candidate, publishAt := prepareGenerationPublication(t, database, now.Add(time.Minute))
	candidateWithIntent, err := database.Generation.GetCandidateRevision(ctx, candidate.ID)
	if err != nil || candidateWithIntent.Publication == nil {
		t.Fatalf("load generation publication intent = %#v, err=%v", candidateWithIntent, err)
	}

	release, entry := createCatalogRuntimeFixture(t, database, now.Add(2*time.Minute))
	commitID := catalogdomain.EntryCommitIDFor(release.ID, entry.ID)
	catalogRevisionID := "chrev-cccccccccccccccc"
	catalogSlug := "catalog-retention"
	if _, err := database.conn.ExecContext(ctx, `UPDATE catalog_releases SET state = ?, commit_id = ?, updated_at = ? WHERE id = ?`,
		catalogdomain.ReleaseCommitting, catalogdomain.CommitIDForRelease(release.ID), now.UTC(), release.ID); err != nil {
		t.Fatalf("prepare retention catalog release: %v", err)
	}
	if _, err := database.conn.ExecContext(ctx, `INSERT INTO catalog_release_entry_commits
		(id, release_id, entry_id, scenario_id, scenario_revision_id, source_slug, state, state_version, runtime_attempt,
		next_run_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 1, 1, ?, ?, ?)`,
		commitID, release.ID, entry.ID, scenario.NewID(), catalogRevisionID, catalogSlug, catalogdomain.CommitPrepared, now.UTC(), now.UTC(), now.UTC()); err != nil {
		t.Fatalf("insert retention catalog commit: %v", err)
	}

	paths, err := database.Publication.RetainedMaterializationPaths(ctx)
	if err != nil {
		t.Fatalf("derive retained materializations: %v", err)
	}
	for _, expected := range []string{
		historical.revision.MaterializedPath,
		candidateWithIntent.Publication.TargetPath,
		scenario.MaterializedPath(catalogSlug, catalogRevisionID),
	} {
		if !slices.Contains(paths, expected) {
			t.Fatalf("retained paths %v omit %q", paths, expected)
		}
	}

	diagnostic, err := publication.NewDiagnostic(publication.Deterministic(errors.New("revision conflict")), publishAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Generation.RecordGenerationPublicationFinalizerFailure(ctx, workflow.ID, candidate.ID, diagnostic); err != nil {
		t.Fatalf("fail generation publication: %v", err)
	}
	if _, err := database.Catalog.FailRelease(ctx, release.ID, "catalog commit failed", now.Add(3*time.Minute)); err != nil {
		t.Fatalf("fail catalog release: %v", err)
	}
	paths, err = database.Publication.RetainedMaterializationPaths(ctx)
	if err != nil {
		t.Fatalf("derive terminal retained materializations: %v", err)
	}
	if !slices.Contains(paths, historical.revision.MaterializedPath) {
		t.Fatalf("deprecated history was dropped from retention: %v", paths)
	}
	if slices.Contains(paths, candidateWithIntent.Publication.TargetPath) || slices.Contains(paths, scenario.MaterializedPath(catalogSlug, catalogRevisionID)) {
		t.Fatalf("terminal publication intents remain retained: %v", paths)
	}
	if !slices.IsSorted(paths) || len(paths) != len(uniqueStrings(paths)) {
		t.Fatalf("retained materializations are not a stable set: %v", paths)
	}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		seen[strings.TrimSpace(value)] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	return result
}
