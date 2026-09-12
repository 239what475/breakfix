package catalog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	challengedomain "github.com/breakfix/breakfix/internal/domain/challenge"
)

// ChallengeLifecycleStore resolves both current and historical content through
// the durable Challenge lifecycle. Current Catalog reads never depend on a
// filesystem scan or a separate relation projection.
type ChallengeLifecycleStore interface {
	ListActiveChallengeRevisions(context.Context) ([]challengedomain.ActiveRevision, error)
	GetChallenge(context.Context, string) (*challengedomain.Challenge, error)
	GetChallengeRevision(context.Context, string, string) (*challengedomain.Revision, error)
}

type PublishedChallenge struct {
	Catalog CatalogReadModel
	Entry   challenge.Entry
}

// CatalogReadModel is the smallest stable projection consumed by Catalog
// clients. It is revision-aware and intentionally has no relationship graph.
type CatalogReadModel struct {
	ID               string
	ActiveRevisionID string
	Type             challenge.ScenarioType
	Title            string
	Description      string
	Runtime          string
	Tags             []string
	PublishedAt      time.Time
	State            challengedomain.State
	Available        bool
}

type Service struct {
	challengesDir string
	availability  *Availability
	lifecycle     ChallengeLifecycleStore
}

const readinessCheckTimeout = 2 * time.Second

func NewService(challengesDir string, availability *Availability, lifecycle ChallengeLifecycleStore) *Service {
	return &Service{challengesDir: challengesDir, availability: availability, lifecycle: lifecycle}
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
func (s *Service) List(ctx context.Context) ([]PublishedChallenge, error) {
	if err := s.Ready(ctx); err != nil {
		return nil, err
	}
	revisions, entries, err := s.currentMaterialized(ctx)
	if err != nil {
		return nil, err
	}
	return projectPublishedChallenges(revisions, entries), nil
}

func (s *Service) Entry(ctx context.Context, id string) (*challenge.Entry, error) {
	published, err := s.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	return &published.Entry, nil
}

func (s *Service) Find(ctx context.Context, id string) (*PublishedChallenge, error) {
	entries, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range entries {
		if item.Entry.ID == id {
			return &item, nil
		}
	}
	return nil, challenge.ErrNotFound
}

// HistoricalEntry resolves the exact revision fixed in an existing
// Environment or learning record. It never follows Challenge.ActiveRevisionID
// and accepts superseded or deprecated revisions while verifying that the
// immutable materialized directory still matches the durable publication.
func (s *Service) HistoricalEntry(ctx context.Context, challengeID, revisionID string) (*challenge.Entry, error) {
	if s == nil || s.lifecycle == nil {
		return nil, challenge.ErrNotFound
	}
	stable, err := s.lifecycle.GetChallenge(ctx, challengeID)
	if err != nil {
		return nil, err
	}
	revision, err := s.lifecycle.GetChallengeRevision(ctx, stable.ID, revisionID)
	if err != nil {
		return nil, err
	}
	return validateMaterializedRevision(s.challengesDir, *stable, *revision)
}

func (s *Service) currentMaterialized(ctx context.Context) ([]challengedomain.ActiveRevision, map[string]challenge.Entry, error) {
	if s == nil || s.lifecycle == nil {
		return nil, nil, errors.New("catalog service lifecycle is not configured")
	}
	revisions, err := s.lifecycle.ListActiveChallengeRevisions(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("list active challenge revisions: %w", err)
	}
	entries, err := materializedChallengeIndex(revisions, s.challengesDir)
	if err != nil {
		return nil, nil, err
	}
	return revisions, entries, nil
}

func projectPublishedChallenges(revisions []challengedomain.ActiveRevision, entries map[string]challenge.Entry) []PublishedChallenge {
	result := make([]PublishedChallenge, 0, len(revisions))
	for _, active := range revisions {
		entry := entries[active.Challenge.ID]
		result = append(result, PublishedChallenge{
			Catalog: CatalogReadModel{
				ID: entry.ID, ActiveRevisionID: entry.RevisionID, Type: entry.Type, Title: entry.Title,
				Description: entry.Description, Runtime: entry.Runtime, Tags: append([]string(nil), entry.Tags...), PublishedAt: entry.PublishedAt.UTC(),
				State: challengedomain.StateActive, Available: true,
			},
			Entry: entry,
		})
	}
	return result
}
