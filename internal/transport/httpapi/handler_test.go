package httpapi

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
)

func newHandlerForTest(t testing.TB, database *postgres.Store, client *kubernetes.Client, cfg config.Config) *Handler {
	t.Helper()
	if database != nil {
		if err := seedTestRoadmap(database, cfg); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := NewHandlerWithDependencies(database, client, cfg, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func seedTestRoadmap(database *postgres.Store, cfg config.Config) error {
	entries, err := challenge.List(cfg.ChallengesDir())
	if err != nil || len(entries) == 0 {
		return err
	}
	domain := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindDomain, "test-catalog"), SourceRef: "test-catalog", Title: "Test catalog"}
	topic := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindTopic, "test-catalog/repair"), SourceRef: "test-catalog/repair", Title: "Repair scenarios"}
	tag := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindTag, "test"), SourceRef: "test", Title: "Test"}
	revision := roadmap.Revision{
		Domains: []roadmap.Domain{{ID: domain.ID, SourceRef: domain.SourceRef, Title: domain.Title, Definition: "Test-only catalog domain.", Scope: "HTTP catalog behavior.", NonGoals: "Production curriculum."}},
		Topics: []roadmap.Topic{{ID: topic.ID, SourceRef: topic.SourceRef, Title: topic.Title, Domain: domain, Definition: "Test-only repair topic.", Scope: "Published test challenges.", NonGoals: "Production curriculum.", ChallengeGuidance: "Use only for HTTP test fixtures."}},
		Tags: []roadmap.Tag{{ID: tag.ID, SourceRef: tag.SourceRef, Title: tag.Title, Description: "Test fixture tag."}},
	}
	for _, entry := range entries {
		if entry.SourceSlug == "" {
			return fmt.Errorf("test challenge %q has no source slug", entry.ID)
		}
		revision.ChallengeBindings = append(revision.ChallengeBindings, roadmap.ChallengeBinding{
			Challenge: roadmap.ChallengeRef{ID: entry.ID, SourceRef: topic.SourceRef + "/" + entry.SourceSlug, Title: entry.Title, ContentRevision: entry.ContentRevision},
			Topic:     topic,
			Tags:      []roadmap.Ref{tag},
		})
	}
	_, err = database.Roadmap.PublishRoadmap(context.Background(), revision, time.Now().UTC())
	return err
}
