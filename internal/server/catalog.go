package server

import (
	"errors"
	"fmt"

	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/taxonomy"
)

type publishedChallenge struct {
	Entry    challenge.Entry
	Taxonomy challengeTaxonomy
}

type challengeTaxonomy struct {
	Revision       string
	Tags           []taxonomy.Ref
	PrimaryOutcome taxonomy.Ref
	Outcomes       []taxonomy.OutcomeRef
	EntrySkills    []taxonomyEntrySkill
}

type taxonomyEntrySkill struct {
	Ref      taxonomy.Ref
	Requires []taxonomy.Ref
}

// publishedChallenges reads the publicly visible catalog. A challenge is
// public only after the current immutable taxonomy snapshot maps its exact
// artifact revision; publishing the directory alone is not sufficient.
func (h *Handler) publishedChallenges() ([]publishedChallenge, error) {
	entries, err := challenge.List(h.challengesDir)
	if err != nil {
		return nil, err
	}
	if h.taxonomy == nil {
		return []publishedChallenge{}, nil
	}
	snapshot, err := h.taxonomy.LoadCurrent()
	if errors.Is(err, taxonomy.ErrNoCurrentRevision) {
		return []publishedChallenge{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load current taxonomy: %w", err)
	}
	index, err := taxonomy.NewCatalogIndex(*snapshot, entries)
	if err != nil {
		return nil, fmt.Errorf("build taxonomy catalog index: %w", err)
	}
	result := make([]publishedChallenge, 0, len(entries))
	for _, entry := range entries {
		mapping, exists := index.Mapping(entry.ID)
		if !exists {
			continue
		}
		result = append(result, publishedChallenge{Entry: entry, Taxonomy: projectChallengeTaxonomy(*snapshot, mapping)})
	}
	return result, nil
}

func (h *Handler) publishedChallenge(id string) (*challenge.Entry, error) {
	published, err := h.publishedChallengeWithTaxonomy(id)
	if err != nil {
		return nil, err
	}
	return &published.Entry, nil
}

func (h *Handler) publishedChallengeWithTaxonomy(id string) (*publishedChallenge, error) {
	entries, err := h.publishedChallenges()
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

func projectChallengeTaxonomy(snapshot taxonomy.Snapshot, mapping taxonomy.ChallengeMapping) challengeTaxonomy {
	requires := make(map[string][]taxonomy.Ref, len(snapshot.SkillMappings))
	for _, skillMapping := range snapshot.SkillMappings {
		requires[skillMapping.Source.ID] = append([]taxonomy.Ref{}, skillMapping.Requires...)
	}
	projection := challengeTaxonomy{
		Revision:    snapshot.Revision,
		Tags:        append([]taxonomy.Ref{}, mapping.Tags...),
		Outcomes:    append([]taxonomy.OutcomeRef{}, mapping.Outcomes...),
		EntrySkills: make([]taxonomyEntrySkill, 0, len(mapping.EntrySkills)),
	}
	for _, outcome := range mapping.Outcomes {
		if outcome.Primary {
			projection.PrimaryOutcome = taxonomy.Ref{ID: outcome.ID, Title: outcome.Title}
			break
		}
	}
	for _, entrySkill := range mapping.EntrySkills {
		projection.EntrySkills = append(projection.EntrySkills, taxonomyEntrySkill{
			Ref:      entrySkill,
			Requires: append([]taxonomy.Ref{}, requires[entrySkill.ID]...),
		})
	}
	return projection
}
