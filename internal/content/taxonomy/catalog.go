package taxonomy

import (
	"github.com/breakfix/breakfix/internal/content/challenge"
	domain "github.com/breakfix/breakfix/internal/domain/taxonomy"
)

// CatalogIndex is the immutable projection of a taxonomy snapshot that is
// usable with the challenge directories visible to one Server process.
// Mappings whose portable content revision is stale are deliberately excluded.
type CatalogIndex struct {
	Revision string
	byID     map[string]domain.ChallengeMapping
}

func NewCatalogIndex(snapshot domain.Snapshot, challenges []challenge.Entry) (*CatalogIndex, error) {
	if err := domain.Validate(snapshot); err != nil {
		return nil, err
	}
	entries := make(map[string]challenge.Entry, len(challenges))
	for _, entry := range challenges {
		entries[entry.ID] = entry
	}
	index := &CatalogIndex{Revision: snapshot.Revision, byID: make(map[string]domain.ChallengeMapping)}
	for _, mapping := range snapshot.ChallengeMappings {
		entry, exists := entries[mapping.Challenge.ID]
		if !exists || entry.Title != mapping.Challenge.Title || entry.ContentRevision != mapping.Challenge.ContentRevision {
			continue
		}
		index.byID[entry.ID] = cloneChallengeMapping(mapping)
	}
	return index, nil
}

func (i *CatalogIndex) Mapping(challengeID string) (domain.ChallengeMapping, bool) {
	if i == nil {
		return domain.ChallengeMapping{}, false
	}
	mapping, exists := i.byID[challengeID]
	return cloneChallengeMapping(mapping), exists
}

func cloneChallengeMapping(mapping domain.ChallengeMapping) domain.ChallengeMapping {
	mapping.Tags = append([]domain.Ref{}, mapping.Tags...)
	mapping.EntrySkills = append([]domain.Ref{}, mapping.EntrySkills...)
	mapping.Outcomes = append([]domain.OutcomeRef{}, mapping.Outcomes...)
	return mapping
}
