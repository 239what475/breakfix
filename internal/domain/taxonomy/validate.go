package taxonomy

import (
	"errors"
	"fmt"
	"strings"

	"github.com/breakfix/breakfix/internal/challenge"
)

var ErrNoCurrentRevision = errors.New("taxonomy current revision does not exist")

// Validate checks a complete snapshot without consulting mutable challenge
// directories. Challenge freshness is evaluated when the catalog index is
// constructed so a newly published, not-yet-mapped challenge remains hidden
// instead of breaking the already published catalog.
func Validate(snapshot Snapshot) error {
	skills, tags, err := validateDefinitions(snapshot.Skills, snapshot.Tags)
	if err != nil {
		return err
	}

	challengeMappings := make(map[string]struct{}, len(snapshot.ChallengeMappings))
	for _, mapping := range snapshot.ChallengeMappings {
		if err := validateChallengeMapping(mapping, skills, tags); err != nil {
			return fmt.Errorf("challenge mapping %q: %w", mapping.Challenge.ID, err)
		}
		if _, exists := challengeMappings[mapping.Challenge.ID]; exists {
			return fmt.Errorf("duplicate challenge mapping for %q", mapping.Challenge.ID)
		}
		challengeMappings[mapping.Challenge.ID] = struct{}{}
	}

	return validateSkillMappings(snapshot.SkillMappings, skills)
}

func validateDefinitions(values []Skill, tagValues []Tag) (map[string]Skill, map[string]Tag, error) {
	skills := make(map[string]Skill, len(values))
	for _, skill := range values {
		if err := validateDefinition(skill.Kind, skill.ID, skill.Title, skill.Definition, skill.MappingGuidance, KindSkill); err != nil {
			return nil, nil, fmt.Errorf("skill %q: %w", skill.ID, err)
		}
		if _, exists := skills[skill.ID]; exists {
			return nil, nil, fmt.Errorf("duplicate skill id %q", skill.ID)
		}
		skills[skill.ID] = skill
	}
	tags := make(map[string]Tag, len(tagValues))
	for _, tag := range tagValues {
		if err := validateDefinition(tag.Kind, tag.ID, tag.Title, tag.Definition, tag.MappingGuidance, KindTag); err != nil {
			return nil, nil, fmt.Errorf("tag %q: %w", tag.ID, err)
		}
		if _, exists := tags[tag.ID]; exists {
			return nil, nil, fmt.Errorf("duplicate tag id %q", tag.ID)
		}
		tags[tag.ID] = tag
	}
	return skills, tags, nil
}

func validateSkillMappings(mappings []SkillMapping, skills map[string]Skill) error {
	edges := make(map[string][]string, len(mappings))
	for _, mapping := range mappings {
		if err := validateSkillMapping(mapping, skills); err != nil {
			return fmt.Errorf("skill mapping %q: %w", mapping.Source.ID, err)
		}
		if _, exists := edges[mapping.Source.ID]; exists {
			return fmt.Errorf("duplicate skill mapping for %q", mapping.Source.ID)
		}
		requires := make([]string, 0, len(mapping.Requires))
		for _, prerequisite := range mapping.Requires {
			requires = append(requires, prerequisite.ID)
		}
		edges[mapping.Source.ID] = requires
	}
	return validateDAG(skills, edges)
}

func validateDefinition(kind, id, title, definition string, guidance MappingGuidance, expectedKind string) error {
	if kind != expectedKind {
		return fmt.Errorf("kind must be %q", expectedKind)
	}
	if !validDefinitionID(id, expectedKind) {
		return fmt.Errorf("invalid opaque %s id %q", strings.ToLower(expectedKind), id)
	}
	if strings.TrimSpace(title) == "" {
		return errors.New("title is required")
	}
	if strings.TrimSpace(definition) == "" {
		return errors.New("definition is required")
	}
	if len(guidance.OutcomeWhen)+len(guidance.EntryWhen)+len(guidance.IncludeWhen)+len(guidance.ExcludeWhen) == 0 {
		return errors.New("mapping_guidance is required")
	}
	for _, value := range append(append(append([]string{}, guidance.OutcomeWhen...), guidance.EntryWhen...), append(guidance.IncludeWhen, guidance.ExcludeWhen...)...) {
		if strings.TrimSpace(value) == "" {
			return errors.New("mapping_guidance cannot contain an empty rule")
		}
	}
	return nil
}

func validDefinitionID(id, kind string) bool {
	prefix := ""
	switch kind {
	case KindSkill:
		prefix = "skill-"
	case KindTag:
		prefix = "tag-"
	default:
		return false
	}
	value := strings.TrimPrefix(id, prefix)
	if value == id || len(value) != 16 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func validateChallengeMapping(mapping ChallengeMapping, skills map[string]Skill, tags map[string]Tag) error {
	if !challenge.ValidID(mapping.Challenge.ID) || strings.TrimSpace(mapping.Challenge.Title) == "" || !ValidRevision(mapping.Challenge.ContentRevision) {
		return errors.New("challenge id, title, and contentRevision are required")
	}
	return validateChallengeRelationships(mapping.Tags, mapping.EntrySkills, mapping.Outcomes, skills, tags)
}

func validateChallengeRelationships(tagsRef, entrySkills []Ref, outcomes []OutcomeRef, skills map[string]Skill, tags map[string]Tag) error {
	if len(tagsRef) == 0 {
		return errors.New("at least one tag is required")
	}
	if len(outcomes) == 0 {
		return errors.New("at least one outcome is required")
	}
	if err := validateRefs(tagsRef, tags); err != nil {
		return fmt.Errorf("tags: %w", err)
	}
	if err := validateRefs(entrySkills, skills); err != nil {
		return fmt.Errorf("entry_skills: %w", err)
	}
	seenOutcomes := make(map[string]struct{}, len(outcomes))
	entry := make(map[string]struct{}, len(entrySkills))
	for _, skill := range entrySkills {
		entry[skill.ID] = struct{}{}
	}
	primary := 0
	for _, outcome := range outcomes {
		skill, exists := skills[outcome.ID]
		if !exists || skill.Title != outcome.Title {
			return fmt.Errorf("outcome references unknown or renamed skill %q", outcome.ID)
		}
		if _, exists := seenOutcomes[outcome.ID]; exists {
			return fmt.Errorf("duplicate outcome %q", outcome.ID)
		}
		if _, exists := entry[outcome.ID]; exists {
			return fmt.Errorf("skill %q cannot be both entry and outcome", outcome.ID)
		}
		seenOutcomes[outcome.ID] = struct{}{}
		if outcome.Primary {
			primary++
		}
	}
	if primary != 1 {
		return fmt.Errorf("exactly one primary outcome is required, got %d", primary)
	}
	return nil
}

type definition interface {
	definitionID() string
	definitionTitle() string
}

func (s Skill) definitionID() string    { return s.ID }
func (s Skill) definitionTitle() string { return s.Title }
func (t Tag) definitionID() string      { return t.ID }
func (t Tag) definitionTitle() string   { return t.Title }

func validateRefs[T definition](refs []Ref, definitions map[string]T) error {
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		definition, exists := definitions[ref.ID]
		if !exists || definition.definitionTitle() != ref.Title || strings.TrimSpace(ref.Title) == "" {
			return fmt.Errorf("references unknown or renamed definition %q", ref.ID)
		}
		if _, exists := seen[ref.ID]; exists {
			return fmt.Errorf("duplicate reference %q", ref.ID)
		}
		seen[ref.ID] = struct{}{}
	}
	return nil
}

func validateSkillMapping(mapping SkillMapping, skills map[string]Skill) error {
	source, exists := skills[mapping.Source.ID]
	if !exists || source.Title != mapping.Source.Title || strings.TrimSpace(mapping.Source.Title) == "" {
		return fmt.Errorf("source references unknown or renamed skill %q", mapping.Source.ID)
	}
	if len(mapping.Requires) == 0 {
		return errors.New("requires cannot be empty; omit the mapping when a skill has no prerequisites")
	}
	if err := validateRefs(mapping.Requires, skills); err != nil {
		return fmt.Errorf("requires: %w", err)
	}
	for _, prerequisite := range mapping.Requires {
		if prerequisite.ID == mapping.Source.ID {
			return fmt.Errorf("skill %q cannot require itself", mapping.Source.ID)
		}
	}
	return nil
}

func validateDAG(skills map[string]Skill, edges map[string][]string) error {
	state := make(map[string]uint8, len(skills))
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("skill.requires contains a cycle at %q", id)
		case 2:
			return nil
		}
		state[id] = 1
		for _, next := range edges[id] {
			if err := visit(next); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range skills {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func ValidRevision(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	for _, r := range value[len("sha256:"):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
