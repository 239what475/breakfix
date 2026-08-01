package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/breakfix/breakfix/internal/challenge"
	domain "github.com/breakfix/breakfix/internal/domain/taxonomy"
	"github.com/breakfix/breakfix/internal/taxonomy"
)

// SnapshotReader is the immutable taxonomy projection used to decide which
// challenge artifacts are visible in the catalog.
type SnapshotReader interface {
	LoadCurrent() (*domain.Snapshot, error)
}

// ReleaseGate makes the installation commit the sole catalog visibility
// boundary. Files may exist while a release is being committed, but callers
// must not observe them until the durable release state is Ready.
type ReleaseGate interface {
	HasReadyRelease(context.Context) (bool, error)
}

type PublishedChallenge struct {
	Entry    challenge.Entry
	Taxonomy ChallengeTaxonomy
}

type ChallengeTaxonomy struct {
	Revision       string
	Tags           []domain.Ref
	PrimaryOutcome domain.Ref
	Outcomes       []domain.OutcomeRef
	EntrySkills    []EntrySkill
}

type EntrySkill struct {
	Ref      domain.Ref
	Requires []domain.Ref
}

type Service struct {
	challengesDir string
	snapshots     SnapshotReader
	gate          ReleaseGate
}

func NewService(challengesDir string, snapshots SnapshotReader, gate ReleaseGate) *Service {
	return &Service{challengesDir: challengesDir, snapshots: snapshots, gate: gate}
}

// publishedChallenges reads the publicly visible catalog. A challenge is
// public only after the current immutable taxonomy snapshot maps its exact
// portable content revision; publishing the directory alone is not sufficient.
func (s *Service) List(ctx context.Context) ([]PublishedChallenge, error) {
	if s == nil || s.snapshots == nil || s.gate == nil {
		return nil, errors.New("catalog service dependencies are not configured")
	}
	ready, err := s.gate.HasReadyRelease(ctx)
	if err != nil {
		return nil, fmt.Errorf("read catalog release visibility: %w", err)
	}
	if !ready {
		return []PublishedChallenge{}, nil
	}
	entries, err := challenge.List(s.challengesDir)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.snapshots.LoadCurrent()
	if errors.Is(err, domain.ErrNoCurrentRevision) {
		return []PublishedChallenge{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load current taxonomy: %w", err)
	}
	index, err := taxonomy.NewCatalogIndex(*snapshot, entries)
	if err != nil {
		return nil, fmt.Errorf("build taxonomy catalog index: %w", err)
	}
	result := make([]PublishedChallenge, 0, len(entries))
	for _, entry := range entries {
		mapping, exists := index.Mapping(entry.ID)
		if !exists {
			continue
		}
		result = append(result, PublishedChallenge{Entry: entry, Taxonomy: projectChallengeTaxonomy(*snapshot, mapping)})
	}
	return result, nil
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

func projectChallengeTaxonomy(snapshot domain.Snapshot, mapping domain.ChallengeMapping) ChallengeTaxonomy {
	requires := make(map[string][]domain.Ref, len(snapshot.SkillMappings))
	for _, skillMapping := range snapshot.SkillMappings {
		requires[skillMapping.Source.ID] = append([]domain.Ref{}, skillMapping.Requires...)
	}
	projection := ChallengeTaxonomy{
		Revision:    snapshot.Revision,
		Tags:        append([]domain.Ref{}, mapping.Tags...),
		Outcomes:    append([]domain.OutcomeRef{}, mapping.Outcomes...),
		EntrySkills: make([]EntrySkill, 0, len(mapping.EntrySkills)),
	}
	for _, outcome := range mapping.Outcomes {
		if outcome.Primary {
			projection.PrimaryOutcome = domain.Ref{ID: outcome.ID, Title: outcome.Title}
			break
		}
	}
	for _, entrySkill := range mapping.EntrySkills {
		projection.EntrySkills = append(projection.EntrySkills, EntrySkill{
			Ref:      entrySkill,
			Requires: append([]domain.Ref{}, requires[entrySkill.ID]...),
		})
	}
	return projection
}
