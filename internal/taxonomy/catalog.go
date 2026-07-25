package taxonomy

import "github.com/breakfix/breakfix/internal/challenge"

// CatalogIndex is the immutable projection of a taxonomy snapshot that is
// usable with the challenge directories visible to one Server process.
// Mappings whose artifact revision is stale are deliberately excluded.
type CatalogIndex struct {
	Revision string
	byID     map[string]ChallengeMapping
}

func NewCatalogIndex(snapshot Snapshot, challenges []challenge.Entry) (*CatalogIndex, error) {
	if err := Validate(snapshot); err != nil {
		return nil, err
	}
	entries := make(map[string]challenge.Entry, len(challenges))
	for _, entry := range challenges {
		entries[entry.ID] = entry
	}
	index := &CatalogIndex{Revision: snapshot.Revision, byID: make(map[string]ChallengeMapping)}
	for _, mapping := range snapshot.ChallengeMappings {
		entry, exists := entries[mapping.Challenge.ID]
		if !exists || entry.Title != mapping.Challenge.Title || entry.Revision != mapping.Challenge.Revision {
			continue
		}
		index.byID[entry.ID] = cloneChallengeMapping(mapping)
	}
	return index, nil
}

func (i *CatalogIndex) Mapping(challengeID string) (ChallengeMapping, bool) {
	if i == nil {
		return ChallengeMapping{}, false
	}
	mapping, exists := i.byID[challengeID]
	return cloneChallengeMapping(mapping), exists
}

func cloneChallengeMapping(mapping ChallengeMapping) ChallengeMapping {
	mapping.Tags = append([]Ref{}, mapping.Tags...)
	mapping.EntrySkills = append([]Ref{}, mapping.EntrySkills...)
	mapping.Outcomes = append([]OutcomeRef{}, mapping.Outcomes...)
	return mapping
}
