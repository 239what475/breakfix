package scenario

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ScenarioType is the serialized type of Scenario revisions. Only
// ScenarioOperationsScenario exists today; the field stays as the seam a
// future second content kind would extend.
type ScenarioType string

const (
	ScenarioOperationsScenario ScenarioType = "operations-scenario"
)

func (t ScenarioType) Valid() bool {
	return t == ScenarioOperationsScenario
}

func NormalizeScenarioType(value string) ScenarioType {
	value = strings.TrimSpace(value)
	if value == "" {
		return ScenarioOperationsScenario
	}
	return ScenarioType(value)
}

// RequireOperationsScenario keeps the operations product boundary explicit at
// its ingress points: a future second type must grow its own source and
// publication flow instead of entering the operations Catalog silently.
func RequireOperationsScenario(entry *Entry) error {
	if entry == nil {
		return fmt.Errorf("operations scenario is required")
	}
	if entry.Type != ScenarioOperationsScenario {
		return fmt.Errorf("operations module accepts only operations-scenario, got %q", entry.Type)
	}
	return nil
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
