package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/breakfix/breakfix/internal/content/challenge"
	roadmapdomain "github.com/breakfix/breakfix/internal/domain/roadmap"
)

// RoadmapStore is the sole runtime source of catalog classification and graph
// data. Portable catalog sources are import/export inputs, never a second
// read model for visible challenges.
type RoadmapStore interface {
	CurrentRoadmap(context.Context) (*roadmapdomain.Revision, error)
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
}

func NewService(challengesDir string, roadmap RoadmapStore) *Service {
	return &Service{challengesDir: challengesDir, roadmap: roadmap}
}

// List exposes only challenge artifacts whose immutable content identity
// matches a binding in the current RoadmapRevision.
func (s *Service) List(ctx context.Context) ([]PublishedChallenge, error) {
	if s == nil || s.roadmap == nil {
		return nil, errors.New("catalog service roadmap is not configured")
	}
	revision, err := s.roadmap.CurrentRoadmap(ctx)
	if errors.Is(err, roadmapdomain.ErrNoCurrentRevision) {
		return []PublishedChallenge{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load current roadmap: %w", err)
	}
	if revision == nil {
		return []PublishedChallenge{}, nil
	}
	entries, err := challenge.List(s.challengesDir)
	if err != nil {
		return nil, err
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

func projectPublishedChallenges(revision roadmapdomain.Revision, entries []challenge.Entry) []PublishedChallenge {
	topics := make(map[string]roadmapdomain.Topic, len(revision.Topics))
	for _, topic := range revision.Topics {
		topics[topic.ID] = topic
	}
	tags := make(map[string]roadmapdomain.Tag, len(revision.Tags))
	for _, tag := range revision.Tags {
		tags[tag.ID] = tag
	}
	bindings := make(map[string]roadmapdomain.ChallengeBinding, len(revision.ChallengeBindings))
	for _, binding := range revision.ChallengeBindings {
		bindings[binding.Challenge.ID] = binding
	}
	result := make([]PublishedChallenge, 0, len(entries))
	for _, entry := range entries {
		binding, exists := bindings[entry.ID]
		if !exists || binding.Challenge.Title != entry.Title || binding.Challenge.ContentRevision != entry.ContentRevision {
			continue
		}
		topic, exists := topics[binding.Topic.ID]
		if !exists {
			continue
		}
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
