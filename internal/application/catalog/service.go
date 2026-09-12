package catalog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
	scenariodomain "github.com/breakfix/breakfix/internal/domain/scenario"
)

// ScenarioLifecycleStore resolves both current and historical content through
// the durable Scenario lifecycle. Current Catalog reads never depend on a
// filesystem scan or a separate relation projection.
type ScenarioLifecycleStore interface {
	ListActiveScenarioRevisions(context.Context) ([]scenariodomain.ActiveRevision, error)
	GetScenario(context.Context, string) (*scenariodomain.Scenario, error)
	GetScenarioRevision(context.Context, string, string) (*scenariodomain.Revision, error)
}

type PublishedScenario struct {
	Catalog CatalogReadModel
	Entry   scenario.Entry
}

// CatalogReadModel is the smallest stable projection consumed by Catalog
// clients. It is revision-aware and intentionally has no relationship graph.
type CatalogReadModel struct {
	ID               string
	ActiveRevisionID string
	Type             scenario.ScenarioType
	Title            string
	Description      string
	Runtime          string
	Tags             []string
	PublishedAt      time.Time
	State            scenariodomain.State
	Available        bool
}

type Service struct {
	scenariosDir string
	availability *Availability
	lifecycle    ScenarioLifecycleStore
}

const readinessCheckTimeout = 2 * time.Second

func NewService(scenariosDir string, availability *Availability, lifecycle ScenarioLifecycleStore) *Service {
	return &Service{scenariosDir: scenariosDir, availability: availability, lifecycle: lifecycle}
}

// Ready verifies the configured immutable release, when one exists. It is
// deliberately independent from Server readiness: Runtime Worker needs the
// Server while the configured release is still being installed.
func (s *Service) Ready(ctx context.Context) error {
	if s == nil {
		return errors.New("catalog service is not configured")
	}
	return s.availability.Ready(ctx)
}

// CheckIntegrity validates each durable active revision against its published
// source directory. It deliberately does not consult Availability: Server
// readiness must remain usable while an optional configured release is still
// being installed for the first time.
func (s *Service) CheckIntegrity(ctx context.Context) error {
	_, _, err := s.currentMaterialized(ctx)
	return err
}

// Readiness applies a short database deadline to the full materialized
// integrity check. It intentionally bypasses the configured release gate.
func (s *Service) Readiness(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, readinessCheckTimeout)
	defer cancel()
	return s.CheckIntegrity(ctx)
}

// List exposes only active immutable revisions that match their durable
// identity and materialized source.
func (s *Service) List(ctx context.Context) ([]PublishedScenario, error) {
	if err := s.Ready(ctx); err != nil {
		return nil, err
	}
	revisions, entries, err := s.currentMaterialized(ctx)
	if err != nil {
		return nil, err
	}
	return projectPublishedScenarios(revisions, entries), nil
}

func (s *Service) Entry(ctx context.Context, id string) (*scenario.Entry, error) {
	published, err := s.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	return &published.Entry, nil
}

func (s *Service) Find(ctx context.Context, id string) (*PublishedScenario, error) {
	entries, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range entries {
		if item.Entry.ID == id {
			return &item, nil
		}
	}
	return nil, scenario.ErrNotFound
}

// HistoricalEntry resolves the exact revision fixed in an existing
// Environment or learning record. It never follows Scenario.ActiveRevisionID
// and accepts superseded or deprecated revisions while verifying that the
// immutable materialized directory still matches the durable publication.
func (s *Service) HistoricalEntry(ctx context.Context, scenarioID, revisionID string) (*scenario.Entry, error) {
	if s == nil || s.lifecycle == nil {
		return nil, scenario.ErrNotFound
	}
	stable, err := s.lifecycle.GetScenario(ctx, scenarioID)
	if err != nil {
		return nil, err
	}
	revision, err := s.lifecycle.GetScenarioRevision(ctx, stable.ID, revisionID)
	if err != nil {
		return nil, err
	}
	return validateMaterializedRevision(s.scenariosDir, *stable, *revision)
}

func (s *Service) currentMaterialized(ctx context.Context) ([]scenariodomain.ActiveRevision, map[string]scenario.Entry, error) {
	if s == nil || s.lifecycle == nil {
		return nil, nil, errors.New("catalog service lifecycle is not configured")
	}
	revisions, err := s.lifecycle.ListActiveScenarioRevisions(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list active scenario revisions: %w", err)
	}
	entries, err := materializedScenarioIndex(revisions, s.scenariosDir)
	if err != nil {
		return nil, nil, err
	}
	return revisions, entries, nil
}

func projectPublishedScenarios(revisions []scenariodomain.ActiveRevision, entries map[string]scenario.Entry) []PublishedScenario {
	result := make([]PublishedScenario, 0, len(revisions))
	for _, active := range revisions {
		entry := entries[active.Scenario.ID]
		result = append(result, PublishedScenario{
			Catalog: CatalogReadModel{
				ID: entry.ID, ActiveRevisionID: entry.RevisionID, Type: entry.Type, Title: entry.Title,
				Description: entry.Description, Runtime: entry.Runtime, Tags: append([]string(nil), entry.Tags...), PublishedAt: entry.PublishedAt.UTC(),
				State: scenariodomain.StateActive, Available: true,
			},
			Entry: entry,
		})
	}
	return result
}
