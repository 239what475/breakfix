package server

import (
	"errors"
	"fmt"

	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/taxonomy"
)

type publishedChallenge struct {
	Entry   challenge.Entry
	Mapping taxonomy.ChallengeMapping
}

// publishedChallenges joins the current immutable taxonomy snapshot with the
// currently visible challenge artifacts. An unmapped or changed artifact is
// intentionally absent rather than becoming public through directory scanning.
func (h *Handler) publishedChallenges() ([]publishedChallenge, error) {
	entries, err := challenge.List(h.challengesDir)
	if err != nil {
		return nil, err
	}
	if h.taxonomy == nil {
		return nil, nil
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

func mappingTagTitles(mapping taxonomy.ChallengeMapping) []string {
	tags := make([]string, 0, len(mapping.Tags))
	for _, tag := range mapping.Tags {
		tags = append(tags, tag.Title)
	}
	return tags
}
