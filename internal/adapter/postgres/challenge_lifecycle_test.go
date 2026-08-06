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
	"github.com/breakfix/breakfix/internal/domain/roadmap"
)

func TestDeprecateAuthoringChallengeRetainsImmutableHistory(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 6, 8, 0, 0, 0, time.UTC)
	fixture := insertChallengeLifecycleFixture(t, database, challengedomain.SourceAuthoring, "author-one", true, now)

	updated, err := database.Challenge.DeprecateAuthoringChallenge(ctx, "author-one", fixture.challenge.ID, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("deprecate authoring challenge: %v", err)
	}
	if updated.State != challengedomain.StateDeprecated || updated.ActiveRevisionID != fixture.revision.ID || updated.SourceSlug != fixture.challenge.SourceSlug {
		t.Fatalf("deprecated challenge = %#v", updated)
	}
	current, err := database.Roadmap.CurrentRoadmap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.ChallengeBindings) != 0 || len(current.ChallengeEdges) != 0 {
		t.Fatalf("deprecated challenge remains in roadmap: %#v", current)
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
	var roadmapEntryCount int
	if err := database.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM roadmap_entries WHERE challenge_id = ?`, fixture.challenge.ID).Scan(&roadmapEntryCount); err != nil {
		t.Fatal(err)
	}
	if roadmapEntryCount != 0 {
		t.Fatalf("roadmap maintenance entry was not removed: %d", roadmapEntryCount)
	}

	if _, err := database.Challenge.DeprecateAuthoringChallenge(ctx, "author-one", fixture.challenge.ID, now.Add(2*time.Minute)); !errors.Is(err, challengedomain.ErrNotMutable) {
		t.Fatalf("repeat deprecation = %v, want not mutable", err)
	}
}

func TestDeprecateAuthoringChallengeRejectsWrongOwnerAndReleaseChallenge(t *testing.T) {
	database := newTestDB(t)
	now := time.Date(2026, time.August, 6, 9, 0, 0, 0, time.UTC)
	authorFixture := insertChallengeLifecycleFixture(t, database, challengedomain.SourceAuthoring, "author-one", false, now)
	if _, err := database.Challenge.DeprecateAuthoringChallenge(context.Background(), "author-two", authorFixture.challenge.ID, now.Add(time.Minute)); !errors.Is(err, challengedomain.ErrNotFound) {
		t.Fatalf("wrong owner deprecation = %v, want not found", err)
	}
	releaseFixture := insertChallengeLifecycleFixture(t, database, challengedomain.SourceRelease, "", false, now.Add(2*time.Minute))
	if _, err := database.Challenge.DeprecateAuthoringChallenge(context.Background(), "author-one", releaseFixture.challenge.ID, now.Add(3*time.Minute)); !errors.Is(err, challengedomain.ErrNotMutable) {
		t.Fatalf("release challenge deprecation = %v, want not mutable", err)
	}
}

type challengeLifecycleFixture struct {
	challenge challengedomain.Challenge
	revision  challengedomain.Revision
}

func insertChallengeLifecycleFixture(t *testing.T, database *Store, sourceKind challengedomain.SourceKind, owner string, withRoadmap bool, now time.Time) challengeLifecycleFixture {
	t.Helper()
	index := strings.TrimPrefix(challenge.NewID(), "chal-")[:8]
	challengeID := "chal-" + index
	revisionID := "chrev-" + strings.Repeat(string(index[0]), 16)
	sourceSlug := "lifecycle-" + index
	contentRevision := "sha256:" + strings.Repeat("a", 64)
	materializedRevision := "sha256:" + strings.Repeat("b", 64)
	artifact := execution.ArtifactReference{Runtime: challenge.RuntimeNode, IncusAlias: "lifecycle-" + index, IncusFingerprint: strings.Repeat("c", 64)}
	fixture := challengeLifecycleFixture{
		challenge: challengedomain.Challenge{
			ID: challengeID, SourceKind: sourceKind, SourceRef: "source-" + index, OwnerUserID: owner,
			State: challengedomain.StateActive, ActiveRevisionID: revisionID, SourceSlug: sourceSlug, CreatedAt: now, UpdatedAt: now,
		},
		revision: challengedomain.Revision{
			ID: revisionID, ChallengeID: challengeID, SourceKind: sourceKind, SourceRef: "source-" + index, SourceRevisionID: "1",
			Title: "Lifecycle challenge " + index, Runtime: challenge.RuntimeNode, ContentRevision: contentRevision, SourceSlug: sourceSlug,
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
	if !withRoadmap {
		return fixture
	}
	domain := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindDomain, "lifecycle"), SourceRef: "lifecycle", Title: "Lifecycle"}
	topic := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindTopic, "lifecycle/topic"), SourceRef: "lifecycle/topic", Title: "Lifecycle topic"}
	value := roadmap.Revision{
		Domains:           []roadmap.Domain{{ID: domain.ID, SourceRef: domain.SourceRef, Title: domain.Title, Definition: "Lifecycle domain.", Scope: "Lifecycle scope.", NonGoals: "None."}},
		Topics:            []roadmap.Topic{{ID: topic.ID, SourceRef: topic.SourceRef, Title: topic.Title, Domain: domain, Definition: "Lifecycle topic.", Scope: "Lifecycle scope.", NonGoals: "None.", ChallengeGuidance: "Use for lifecycle tests."}},
		ChallengeBindings: []roadmap.ChallengeBinding{{Challenge: roadmap.ChallengeRef{ID: challengeID, RevisionID: revisionID, SourceRef: fixture.revision.SourceRef, Title: fixture.revision.Title, ContentRevision: contentRevision, SourceSlug: sourceSlug, MaterializedRevision: materializedRevision}, Topic: topic}},
	}
	if _, err := database.Roadmap.PublishRoadmap(context.Background(), value, now); err != nil {
		t.Fatal(err)
	}
	if _, err := database.conn.ExecContext(context.Background(), `INSERT INTO roadmap_entries (challenge_id, topic_id, topic_processed, challenge_processed, created_at) VALUES (?, ?, TRUE, TRUE, ?)`, challengeID, topic.ID, now); err != nil {
		t.Fatal(err)
	}
	return fixture
}
