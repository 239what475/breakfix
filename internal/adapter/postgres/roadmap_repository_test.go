package postgres

import (
	"context"
	"testing"
	"time"

	roadmaptest "github.com/breakfix/breakfix/internal/testkit/roadmap"
)

func TestRoadmapRepositoryPublishesImmutableCurrentRevision(t *testing.T) {
	database := newTestDB(t)
	ctx := context.Background()
	first, err := database.Roadmap.PublishRoadmap(ctx, roadmaptest.RuntimeRevision(), time.Now().UTC())
	if err != nil {
		t.Fatalf("publish first roadmap: %v", err)
	}
	if first.Revision == "" {
		t.Fatal("roadmap publication did not assign a revision")
	}
	current, err := database.Roadmap.CurrentRoadmap(ctx)
	if err != nil {
		t.Fatalf("load current roadmap: %v", err)
	}
	if current.Revision != first.Revision || len(current.ChallengeBindings) != 2 {
		t.Fatalf("current roadmap = %#v", current)
	}

	reused, err := database.Roadmap.PublishRoadmap(ctx, roadmaptest.RuntimeRevision(), time.Now().UTC())
	if err != nil {
		t.Fatalf("republish identical roadmap: %v", err)
	}
	if reused.Revision != first.Revision {
		t.Fatalf("identical roadmap changed revision: %q != %q", reused.Revision, first.Revision)
	}

	updated := roadmaptest.RuntimeRevision()
	updated.Domains[0].Definition = "更新后的运行时课程边界。"
	second, err := database.Roadmap.PublishRoadmap(ctx, updated, time.Now().UTC())
	if err != nil {
		t.Fatalf("publish updated roadmap: %v", err)
	}
	if second.Revision == first.Revision {
		t.Fatal("roadmap content change did not create a new revision")
	}
	firstAgain, err := database.Roadmap.RoadmapRevision(ctx, first.Revision)
	if err != nil {
		t.Fatalf("load original roadmap: %v", err)
	}
	if firstAgain.Domains[0].Definition == second.Domains[0].Definition {
		t.Fatalf("immutable roadmap revision was changed: %#v", firstAgain)
	}
}
