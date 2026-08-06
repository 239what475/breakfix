package catalog

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	challengedomain "github.com/breakfix/breakfix/internal/domain/challenge"
	roadmapdomain "github.com/breakfix/breakfix/internal/domain/roadmap"
)

// RoadmapStore is the sole runtime source of catalog classification and graph
// data. Portable catalog sources are import/export inputs, never a second
// read model for visible challenges.
type RoadmapStore interface {
	CurrentRoadmap(context.Context) (*roadmapdomain.Revision, error)
}

// ChallengeLifecycleStore resolves immutable historical content through the
// durable Challenge lifecycle. Current Catalog reads remain a Roadmap
// projection; historical callers must not follow the mutable active pointer.
type ChallengeLifecycleStore interface {
	GetChallenge(context.Context, string) (*challengedomain.Challenge, error)
	GetChallengeRevision(context.Context, string, string) (*challengedomain.Revision, error)
}

type PublishedChallenge struct {
	Entry   challenge.Entry
	Roadmap ChallengeRoadmap
}

type ChallengeRoadmap struct {
	Revision           string
	Domain             roadmapdomain.Ref
	Topic              roadmapdomain.Topic
	Tags               []roadmapdomain.Tag
	TopicNeighbors     []roadmapdomain.Edge
	ChallengeNeighbors []roadmapdomain.Edge
}

type Service struct {
	challengesDir string
	roadmap       RoadmapStore
	availability  *Availability
	lifecycle     ChallengeLifecycleStore
}

const readinessCheckTimeout = 2 * time.Second

func NewService(challengesDir string, roadmap RoadmapStore, availability *Availability, lifecycle ChallengeLifecycleStore) *Service {
	return &Service{challengesDir: challengesDir, roadmap: roadmap, availability: availability, lifecycle: lifecycle}
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

// CheckIntegrity validates the current runtime Roadmap against the published
// source directories. It deliberately does not consult Availability: Server
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

// List exposes only challenge artifacts whose immutable content identity
// matches a binding in the current RoadmapRevision.
func (s *Service) List(ctx context.Context) ([]PublishedChallenge, error) {
	if err := s.Ready(ctx); err != nil {
		return nil, err
	}
	revision, entries, err := s.currentMaterialized(ctx)
	if err != nil {
		return nil, err
	}
	if revision == nil {
		return []PublishedChallenge{}, nil
	}
	return projectPublishedChallenges(*revision, entries), nil
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
	if revision.ChallengeID != stable.ID || revision.SourceKind != stable.SourceKind || revision.SourceSlug != stable.SourceSlug ||
		challenge.ValidateMaterializedPath(revision.MaterializedPath, revision.SourceSlug, revision.ID) != nil {
		return nil, materializedIntegrity(roadmapdomain.ChallengeRef{ID: stable.ID, RevisionID: revisionID, SourceSlug: stable.SourceSlug}, "historical revision identity conflicts with its durable lifecycle")
	}
	entry, err := challenge.ValidateDir(filepath.Join(s.challengesDir, filepath.FromSlash(revision.MaterializedPath)))
	if err != nil {
		return nil, materializedIntegrity(roadmapdomain.ChallengeRef{ID: stable.ID, RevisionID: revision.ID, SourceSlug: revision.SourceSlug}, "invalid historical materialized source: %v", err)
	}
	expectedImage := revision.Artifact.IncusFingerprint
	if revision.Runtime == challenge.RuntimeK8s {
		expectedImage = revision.Artifact.OCIReference
	}
	if entry.ID != revision.ChallengeID || entry.RevisionID != revision.ID || entry.SourceSlug != revision.SourceSlug ||
		entry.Title != revision.Title || entry.Runtime != revision.Runtime || entry.ContentRevision != revision.ContentRevision ||
		entry.Revision != revision.MaterializedRevision || entry.Image != expectedImage {
		return nil, materializedIntegrity(roadmapdomain.ChallengeRef{ID: stable.ID, RevisionID: revision.ID, SourceSlug: revision.SourceSlug}, "historical materialized source does not match its durable revision")
	}
	return entry, nil
}

func (s *Service) currentMaterialized(ctx context.Context) (*roadmapdomain.Revision, map[string]challenge.Entry, error) {
	if s == nil || s.roadmap == nil {
		return nil, nil, errors.New("catalog service roadmap is not configured")
	}
	revision, err := s.roadmap.CurrentRoadmap(ctx)
	if errors.Is(err, roadmapdomain.ErrNoCurrentRevision) {
		return nil, map[string]challenge.Entry{}, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("load current roadmap: %w", err)
	}
	if revision == nil {
		return nil, map[string]challenge.Entry{}, nil
	}
	entries, err := materializedChallengeIndex(*revision, s.challengesDir)
	if err != nil {
		return nil, nil, err
	}
	return revision, entries, nil
}

func projectPublishedChallenges(revision roadmapdomain.Revision, entries map[string]challenge.Entry) []PublishedChallenge {
	topics := make(map[string]roadmapdomain.Topic, len(revision.Topics))
	for _, topic := range revision.Topics {
		topics[topic.ID] = topic
	}
	tags := make(map[string]roadmapdomain.Tag, len(revision.Tags))
	for _, tag := range revision.Tags {
		tags[tag.ID] = tag
	}
	result := make([]PublishedChallenge, 0, len(revision.ChallengeBindings))
	for _, binding := range revision.ChallengeBindings {
		entry := entries[binding.Challenge.ID]
		topic := topics[binding.Topic.ID]
		projection := ChallengeRoadmap{
			Revision:           revision.Revision,
			Domain:             topic.Domain,
			Topic:              topic,
			Tags:               make([]roadmapdomain.Tag, 0, len(binding.Tags)),
			TopicNeighbors:     oneHopEdges(revision.TopicEdges, topic.ID),
			ChallengeNeighbors: oneHopEdges(revision.ChallengeEdges, binding.Challenge.ID),
		}
		for _, reference := range binding.Tags {
			if tag, exists := tags[reference.ID]; exists {
				projection.Tags = append(projection.Tags, tag)
			}
		}
		result = append(result, PublishedChallenge{Entry: entry, Roadmap: projection})
	}
	return result
}

func oneHopEdges(edges []roadmapdomain.Edge, id string) []roadmapdomain.Edge {
	result := make([]roadmapdomain.Edge, 0)
	for _, edge := range edges {
		if edge.Source.ID == id || edge.Target.ID == id {
			result = append(result, edge)
		}
	}
	return result
}
