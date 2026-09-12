package scenario

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ScenarioType identifies how a runnable item is organized and presented.
// Both types share the same runtime contract, but their source metadata has
// different semantics.
type ScenarioType string

const (
	ScenarioDocumentationExample ScenarioType = "documentation-example"
	ScenarioOperationsScenario   ScenarioType = "operations-scenario"
)

func (t ScenarioType) Valid() bool {
	return t == ScenarioDocumentationExample || t == ScenarioOperationsScenario
}

func NormalizeScenarioType(value string) ScenarioType {
	value = strings.TrimSpace(value)
	if value == "" {
		return ScenarioOperationsScenario
	}
	return ScenarioType(value)
}

const (
	MaxScenarioTags      = 8
	MinScenarioTagLength = 1
	MaxScenarioTagLength = 32
)

// NormalizeTags applies the stable tag contract used by portable and
// materialized scenario revisions. Tags are intentionally plain strings:
// there is no mutable Tag entity or post-publication relationship to update.
func NormalizeTags(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, raw := range values {
		tag := strings.ToLower(strings.TrimSpace(raw))
		length := utf8.RuneCountInString(tag)
		if length < MinScenarioTagLength || length > MaxScenarioTagLength {
			return nil, fmt.Errorf("scenario tag %q must contain %d-%d characters", raw, MinScenarioTagLength, MaxScenarioTagLength)
		}
		for _, r := range tag {
			if !validTagRune(r) {
				return nil, fmt.Errorf("scenario tag %q contains unsupported character %q", raw, r)
			}
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		result = append(result, tag)
	}
	if len(result) > MaxScenarioTags {
		return nil, fmt.Errorf("scenario has too many tags: %d (maximum %d)", len(result), MaxScenarioTags)
	}
	slices.Sort(result)
	return result, nil
}

func validTagRune(r rune) bool {
	return r == '.' || r == '+' || r == '-' ||
		(r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') ||
		(r >= 'A' && r <= 'Z') || unicode.Is(unicode.Han, r)
}
