package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	challengedomain "github.com/breakfix/breakfix/internal/domain/challenge"
	"github.com/breakfix/breakfix/internal/domain/execution"
)

func TestDeprecateAuthoringChallengeRetainsImmutableHistory(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 6, 8, 0, 0, 0, time.UTC)
	fixture := insertChallengeLifecycleFixture(t, database, challengedomain.SourceAuthoring, "author-one", now)

	updated, err := database.Challenge.DeprecateAuthoringChallenge(ctx, "author-one", fixture.challenge.ID, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("deprecate authoring challenge: %v", err)
	}
	if updated.State != challengedomain.StateDeprecated || updated.ActiveRevisionID != fixture.revision.ID || updated.SourceSlug != fixture.challenge.SourceSlug {
		t.Fatalf("deprecated challenge = %#v", updated)
	}
	stable, err := database.Challenge.GetChallenge(ctx, fixture.challenge.ID)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := database.Challenge.GetChallengeRevision(ctx, fixture.challenge.ID, fixture.revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stable.ActiveRevisionID != fixture.revision.ID || revision.State != challengedomain.RevisionActive || revision.Artifact != fixture.revision.Artifact {
		t.Fatalf("deprecated lifecycle changed immutable history: challenge=%#v revision=%#v", stable, revision)
	}
	active, err := database.Challenge.ListActiveChallengeRevisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range active {
		if value.Challenge.ID == fixture.challenge.ID {
			t.Fatalf("deprecated challenge remains visible in the active catalog: %#v", value)
		}
	}

	if _, err := database.Challenge.DeprecateAuthoringChallenge(ctx, "author-one", fixture.challenge.ID, now.Add(2*time.Minute)); !errors.Is(err, challengedomain.ErrNotMutable) {
		t.Fatalf("repeat deprecation = %v, want not mutable", err)
	}
}

func TestDeprecateAuthoringChallengeRejectsWrongOwnerAndReleaseChallenge(t *testing.T) {
	database := newTestDB(t)
	now := time.Date(2026, time.August, 6, 9, 0, 0, 0, time.UTC)
	authorFixture := insertChallengeLifecycleFixture(t, database, challengedomain.SourceAuthoring, "author-one", now)
	if _, err := database.Challenge.DeprecateAuthoringChallenge(context.Background(), "author-two", authorFixture.challenge.ID, now.Add(time.Minute)); !errors.Is(err, challengedomain.ErrNotFound) {
		t.Fatalf("wrong owner deprecation = %v, want not found", err)
	}
	releaseFixture := insertChallengeLifecycleFixture(t, database, challengedomain.SourceRelease, "", now.Add(2*time.Minute))
	if _, err := database.Challenge.DeprecateAuthoringChallenge(context.Background(), "author-one", releaseFixture.challenge.ID, now.Add(3*time.Minute)); !errors.Is(err, challengedomain.ErrNotMutable) {
		t.Fatalf("release challenge deprecation = %v, want not mutable", err)
	}
}

type challengeLifecycleFixture struct {
	challenge challengedomain.Challenge
	revision  challengedomain.Revision
}

func insertChallengeLifecycleFixture(t *testing.T, database *Store, sourceKind challengedomain.SourceKind, owner string, now time.Time) challengeLifecycleFixture {
	t.Helper()
	index := strings.TrimPrefix(challenge.NewID(), "chal-")[:8]
	challengeID := "chal-" + index
	revisionID := challenge.NewRevisionID()
	sourceSlug := "lifecycle-" + index
	contentRevision := "sha256:" + strings.Repeat("a", 64)
	materializedRevision := "sha256:" + strings.Repeat("b", 64)
	artifact := execution.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: "lifecycle-" + index, IncusFingerprint: strings.Repeat("c", 64)}
	fixture := challengeLifecycleFixture{
		challenge: challengedomain.Challenge{
			ID: challengeID, SourceKind: sourceKind, SourceRef: "lifecycle/topic/" + index, OwnerUserID: owner,
			State: challengedomain.StateActive, ActiveRevisionID: revisionID, SourceSlug: sourceSlug, CreatedAt: now, UpdatedAt: now,
		},
		revision: challengedomain.Revision{
			ID: revisionID, ChallengeID: challengeID, SourceKind: sourceKind, SourceRef: "lifecycle/topic/" + index, SourceRevisionID: "1",
			Title: "Lifecycle challenge " + index, Runtime: challenge.RuntimeNode, Type: challenge.ScenarioOperationsScenario, ContentRevision: contentRevision, SourceSlug: sourceSlug,
			MaterializedPath: challenge.MaterializedPath(sourceSlug, revisionID), MaterializedRevision: materializedRevision, Artifact: artifact,
			State: challengedomain.RevisionActive, PublishedAt: now, CreatedAt: now,
		},
	}
	tx, err := database.conn.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertPersistedChallengeTx(context.Background(), tx, fixture.challenge); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := insertPersistedChallengeRevisionTx(context.Background(), tx, fixture.revision); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return fixture
}
