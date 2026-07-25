package taxonomy

import (
	"errors"
	"fmt"
	"strings"
)

type ChangeOperation string

const (
	ChangeUpsert ChangeOperation = "upsert"
	ChangeDelete ChangeOperation = "delete"
)

// ChangeSet is the only mutable representation produced by taxonomy agents.
// It is applied to a complete immutable snapshot by the model-free Publisher.
type ChangeSet struct {
	Skills            []SkillChange            `json:"skills"`
	Tags              []TagChange              `json:"tags"`
	ChallengeMappings []ChallengeMappingChange `json:"challenge_mappings"`
	SkillMappings     []SkillMappingChange     `json:"skill_mappings"`
}

type SkillChange struct {
	Operation ChangeOperation `json:"operation"`
	ID        string          `json:"id,omitempty"`
	Value     *Skill          `json:"value,omitempty"`
}

type TagChange struct {
	Operation ChangeOperation `json:"operation"`
	ID        string          `json:"id,omitempty"`
	Value     *Tag            `json:"value,omitempty"`
}

type ChallengeMappingChange struct {
	Operation   ChangeOperation   `json:"operation"`
	ChallengeID string            `json:"challenge_id,omitempty"`
	Value       *ChallengeMapping `json:"value,omitempty"`
}

type SkillMappingChange struct {
	Operation ChangeOperation `json:"operation"`
	SourceID  string          `json:"source_id,omitempty"`
	Value     *SkillMapping   `json:"value,omitempty"`
}

func (s ChangeSet) Empty() bool {
	return len(s.Skills) == 0 && len(s.Tags) == 0 && len(s.ChallengeMappings) == 0 && len(s.SkillMappings) == 0
}

func (s ChangeSet) MappingOnlyFor(challengeID string) bool {
	return len(s.Skills) == 0 && len(s.Tags) == 0 && len(s.SkillMappings) == 0 && len(s.ChallengeMappings) == 1 &&
		changeChallengeID(s.ChallengeMappings[0]) == challengeID
}

// ApplyChangeSet returns a new complete snapshot. It does not publish it.
func ApplyChangeSet(base Snapshot, changes ChangeSet) (Snapshot, error) {
	if changes.Empty() {
		return Snapshot{}, errors.New("taxonomy changeset is empty")
	}
	result := base.Clone()
	result.Revision = ""
	var err error
	if result.Skills, err = applySkills(result.Skills, changes.Skills); err != nil {
		return Snapshot{}, err
	}
	if result.Tags, err = applyTags(result.Tags, changes.Tags); err != nil {
		return Snapshot{}, err
	}
	if result.ChallengeMappings, err = applyChallengeMappings(result.ChallengeMappings, changes.ChallengeMappings); err != nil {
		return Snapshot{}, err
	}
	if result.SkillMappings, err = applySkillMappings(result.SkillMappings, changes.SkillMappings); err != nil {
		return Snapshot{}, err
	}
	if err := Validate(result); err != nil {
		return Snapshot{}, err
	}
	return result.Sorted(), nil
}

func applySkills(existing []Skill, changes []SkillChange) ([]Skill, error) {
	values := make(map[string]Skill, len(existing))
	for _, value := range existing {
		values[value.ID] = value
	}
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		id, value, err := validateSkillChange(change)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("changeset modifies skill %q more than once", id)
		}
		seen[id] = struct{}{}
		if change.Operation == ChangeDelete {
			if _, exists := values[id]; !exists {
				return nil, fmt.Errorf("cannot delete unknown skill %q", id)
			}
			delete(values, id)
			continue
		}
		if current, exists := values[id]; exists && value.File == "" {
			value.File = current.File
		}
		values[id] = value
	}
	return skillValues(values), nil
}

func applyTags(existing []Tag, changes []TagChange) ([]Tag, error) {
	values := make(map[string]Tag, len(existing))
	for _, value := range existing {
		values[value.ID] = value
	}
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		id, value, err := validateTagChange(change)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("changeset modifies tag %q more than once", id)
		}
		seen[id] = struct{}{}
		if change.Operation == ChangeDelete {
			if _, exists := values[id]; !exists {
				return nil, fmt.Errorf("cannot delete unknown tag %q", id)
			}
			delete(values, id)
			continue
		}
		if current, exists := values[id]; exists && value.File == "" {
			value.File = current.File
		}
		values[id] = value
	}
	return tagValues(values), nil
}

func applyChallengeMappings(existing []ChallengeMapping, changes []ChallengeMappingChange) ([]ChallengeMapping, error) {
	values := make(map[string]ChallengeMapping, len(existing))
	for _, value := range existing {
		values[value.Challenge.ID] = value
	}
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		id, value, err := validateChallengeMappingChange(change)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("changeset modifies challenge mapping %q more than once", id)
		}
		seen[id] = struct{}{}
		if change.Operation == ChangeDelete {
			if _, exists := values[id]; !exists {
				return nil, fmt.Errorf("cannot delete unknown challenge mapping %q", id)
			}
			delete(values, id)
			continue
		}
		if current, exists := values[id]; exists && value.File == "" {
			value.File = current.File
		}
		values[id] = value
	}
	return challengeMappingValues(values), nil
}

func applySkillMappings(existing []SkillMapping, changes []SkillMappingChange) ([]SkillMapping, error) {
	values := make(map[string]SkillMapping, len(existing))
	for _, value := range existing {
		values[value.Source.ID] = value
	}
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		id, value, err := validateSkillMappingChange(change)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("changeset modifies skill mapping %q more than once", id)
		}
		seen[id] = struct{}{}
		if change.Operation == ChangeDelete {
			if _, exists := values[id]; !exists {
				return nil, fmt.Errorf("cannot delete unknown skill mapping %q", id)
			}
			delete(values, id)
			continue
		}
		if current, exists := values[id]; exists && value.File == "" {
			value.File = current.File
		}
		values[id] = value
	}
	return skillMappingValues(values), nil
}

func validateSkillChange(change SkillChange) (string, Skill, error) {
	if change.Operation != ChangeUpsert && change.Operation != ChangeDelete {
		return "", Skill{}, fmt.Errorf("invalid skill operation %q", change.Operation)
	}
	if change.Operation == ChangeDelete {
		if strings.TrimSpace(change.ID) == "" || change.Value != nil {
			return "", Skill{}, errors.New("skill delete requires id and no value")
		}
		return change.ID, Skill{}, nil
	}
	if change.Value == nil || strings.TrimSpace(change.ID) != "" && change.ID != change.Value.ID {
		return "", Skill{}, errors.New("skill upsert requires one matching value")
	}
	return change.Value.ID, *change.Value, nil
}

func validateTagChange(change TagChange) (string, Tag, error) {
	if change.Operation != ChangeUpsert && change.Operation != ChangeDelete {
		return "", Tag{}, fmt.Errorf("invalid tag operation %q", change.Operation)
	}
	if change.Operation == ChangeDelete {
		if strings.TrimSpace(change.ID) == "" || change.Value != nil {
			return "", Tag{}, errors.New("tag delete requires id and no value")
		}
		return change.ID, Tag{}, nil
	}
	if change.Value == nil || strings.TrimSpace(change.ID) != "" && change.ID != change.Value.ID {
		return "", Tag{}, errors.New("tag upsert requires one matching value")
	}
	return change.Value.ID, *change.Value, nil
}

func validateChallengeMappingChange(change ChallengeMappingChange) (string, ChallengeMapping, error) {
	if change.Operation != ChangeUpsert && change.Operation != ChangeDelete {
		return "", ChallengeMapping{}, fmt.Errorf("invalid challenge mapping operation %q", change.Operation)
	}
	if change.Operation == ChangeDelete {
		if strings.TrimSpace(change.ChallengeID) == "" || change.Value != nil {
			return "", ChallengeMapping{}, errors.New("challenge mapping delete requires challenge_id and no value")
		}
		return change.ChallengeID, ChallengeMapping{}, nil
	}
	if change.Value == nil || strings.TrimSpace(change.ChallengeID) != "" && change.ChallengeID != change.Value.Challenge.ID {
		return "", ChallengeMapping{}, errors.New("challenge mapping upsert requires one matching value")
	}
	return change.Value.Challenge.ID, *change.Value, nil
}

func validateSkillMappingChange(change SkillMappingChange) (string, SkillMapping, error) {
	if change.Operation != ChangeUpsert && change.Operation != ChangeDelete {
		return "", SkillMapping{}, fmt.Errorf("invalid skill mapping operation %q", change.Operation)
	}
	if change.Operation == ChangeDelete {
		if strings.TrimSpace(change.SourceID) == "" || change.Value != nil {
			return "", SkillMapping{}, errors.New("skill mapping delete requires source_id and no value")
		}
		return change.SourceID, SkillMapping{}, nil
	}
	if change.Value == nil || strings.TrimSpace(change.SourceID) != "" && change.SourceID != change.Value.Source.ID {
		return "", SkillMapping{}, errors.New("skill mapping upsert requires one matching value")
	}
	return change.Value.Source.ID, *change.Value, nil
}

func changeChallengeID(change ChallengeMappingChange) string {
	if change.Operation == ChangeDelete {
		return change.ChallengeID
	}
	if change.Value != nil {
		return change.Value.Challenge.ID
	}
	return ""
}

func skillValues(values map[string]Skill) []Skill {
	result := make([]Skill, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func tagValues(values map[string]Tag) []Tag {
	result := make([]Tag, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func challengeMappingValues(values map[string]ChallengeMapping) []ChallengeMapping {
	result := make([]ChallengeMapping, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func skillMappingValues(values map[string]SkillMapping) []SkillMapping {
	result := make([]SkillMapping, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}
