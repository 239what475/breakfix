package server

import (
	"errors"
	"fmt"

	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/taxonomy"
)

type publishedChallenge struct {
	Entry   challenge.Entry
	Mapping *taxonomy.ChallengeMapping
}

// publishedChallenges reads the filesystem-backed published catalog. Taxonomy
// mapping is asynchronous classification metadata, not a publication gate.
func (h *Handler) publishedChallenges() ([]publishedChallenge, error) {
	entries, err := challenge.List(h.challengesDir)
	if err != nil {
		return nil, err
	}
	var index *taxonomy.CatalogIndex
	if h.taxonomy != nil {
		snapshot, err := h.taxonomy.LoadCurrent()
		if err != nil && !errors.Is(err, taxonomy.ErrNoCurrentRevision) {
			return nil, fmt.Errorf("load current taxonomy: %w", err)
		}
		if snapshot != nil {
			index, err = taxonomy.NewCatalogIndex(*snapshot, entries)
			if err != nil {
				return nil, fmt.Errorf("build taxonomy catalog index: %w", err)
			}
		}
	}
	result := make([]publishedChallenge, 0, len(entries))
	for _, entry := range entries {
		var mapping *taxonomy.ChallengeMapping
		if index != nil {
			if value, exists := index.Mapping(entry.ID); exists {
				mapping = &value
			}
		}
		result = append(result, publishedChallenge{Entry: entry, Mapping: mapping})
	}
	return result, nil
}

func (h *Handler) publishedChallenge(id string) (*challenge.Entry, error) {
	entries, err := h.publishedChallenges()
	if err != nil {
		return nil, err
	}
	for _, item := range entries {
		if item.Entry.ID == id {
			entry := item.Entry
			return &entry, nil
		}
	}
	return nil, challenge.ErrNotFound
}

func mappingTagTitles(mapping *taxonomy.ChallengeMapping) []string {
	if mapping == nil {
		return []string{}
	}
	tags := make([]string, 0, len(mapping.Tags))
	for _, tag := range mapping.Tags {
		tags = append(tags, tag.Title)
	}
	return tags
}
