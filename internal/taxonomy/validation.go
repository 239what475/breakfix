package taxonomy

import (
	"errors"
	"fmt"
	"reflect"
)

// ValidateWorkflowChangeSet enforces the deliberately narrow scope of the
// per-challenge taxonomy workflow. Global taxonomy maintenance is a separate
// future workflow and is the only place that may rewrite existing content.
func ValidateWorkflowChangeSet(changes ChangeSet, entry ChallengeRef, base Snapshot) (Snapshot, error) {
	if changes.Empty() {
		return Snapshot{}, errors.New("taxonomy changeset is empty")
	}
	if len(changes.ChallengeMappings) != 1 {
		return Snapshot{}, errors.New("mapper must emit exactly one challenge mapping change")
	}
	mapping := changes.ChallengeMappings[0]
	if mapping.Operation != ChangeUpsert || mapping.Value == nil {
		return Snapshot{}, errors.New("mapper must upsert the target challenge mapping")
	}
	if mapping.Value.Challenge.ID != entry.ID || mapping.Value.Challenge.Title != entry.Title || mapping.Value.Challenge.Revision != entry.Revision {
		return Snapshot{}, errors.New("mapper challenge mapping must exactly match the verified challenge")
	}
	if mapping.ChallengeID != "" && mapping.ChallengeID != entry.ID {
		return Snapshot{}, errors.New("mapper challenge mapping change id must match the target challenge")
	}

	existingSkills := skillMap(base)
	newSkills := make(map[string]struct{}, len(changes.Skills))
	for _, change := range changes.Skills {
		if change.Operation != ChangeUpsert || change.Value == nil {
			return Snapshot{}, errors.New("per-challenge taxonomy workflow may only create new skills")
		}
		if _, exists := existingSkills[change.Value.ID]; exists {
			return Snapshot{}, fmt.Errorf("new skill collides with existing skill %q", change.Value.ID)
		}
		if _, duplicate := newSkills[change.Value.ID]; duplicate {
			return Snapshot{}, fmt.Errorf("changeset creates skill %q more than once", change.Value.ID)
		}
		newSkills[change.Value.ID] = struct{}{}
	}
	existingTags := tagMap(base)
	for _, change := range changes.Tags {
		if change.Operation != ChangeUpsert || change.Value == nil {
			return Snapshot{}, errors.New("per-challenge taxonomy workflow may only create new tags")
		}
		if _, exists := existingTags[change.Value.ID]; exists {
			return Snapshot{}, fmt.Errorf("new tag collides with existing tag %q", change.Value.ID)
		}
	}
	existingSkillMappings := make(map[string]SkillMapping, len(base.SkillMappings))
	for _, value := range base.SkillMappings {
		existingSkillMappings[value.Source.ID] = value
	}
	for _, change := range changes.SkillMappings {
		if change.Operation != ChangeUpsert || change.Value == nil {
			return Snapshot{}, errors.New("per-challenge taxonomy workflow may only create new skill requires mappings")
		}
		if _, exists := existingSkillMappings[change.Value.Source.ID]; exists {
			return Snapshot{}, fmt.Errorf("new skill requirement collides with existing skill %q", change.Value.Source.ID)
		}
		if _, newSkill := newSkills[change.Value.Source.ID]; !newSkill {
			return Snapshot{}, fmt.Errorf("requires mapping source %q must be a skill created by this changeset", change.Value.Source.ID)
		}
	}

	next, err := ApplyChangeSet(base, changes)
	if err != nil {
		return Snapshot{}, err
	}
	if err := ensureUnchangedOutsideTarget(base, next, entry.ID); err != nil {
		return Snapshot{}, err
	}
	return next, nil
}

func ensureUnchangedOutsideTarget(base, next Snapshot, targetChallengeID string) error {
	for id, value := range skillMap(base) {
		if !sameSkill(value, skillMap(next)[id]) {
			return fmt.Errorf("existing skill %q changed", id)
		}
	}
	for id, value := range tagMap(base) {
		if !sameTag(value, tagMap(next)[id]) {
			return fmt.Errorf("existing tag %q changed", id)
		}
	}
	baseMappings := make(map[string]SkillMapping, len(base.SkillMappings))
	nextMappings := make(map[string]SkillMapping, len(next.SkillMappings))
	for _, value := range base.SkillMappings {
		baseMappings[value.Source.ID] = value
	}
	for _, value := range next.SkillMappings {
		nextMappings[value.Source.ID] = value
	}
	for id, value := range baseMappings {
		if !sameSkillMapping(value, nextMappings[id]) {
			return fmt.Errorf("existing skill mapping %q changed", id)
		}
	}
	baseChallenges := make(map[string]ChallengeMapping, len(base.ChallengeMappings))
	nextChallenges := make(map[string]ChallengeMapping, len(next.ChallengeMappings))
	for _, value := range base.ChallengeMappings {
		baseChallenges[value.Challenge.ID] = value
	}
	for _, value := range next.ChallengeMappings {
		nextChallenges[value.Challenge.ID] = value
	}
	for id, value := range baseChallenges {
		if id == targetChallengeID {
			continue
		}
		if !sameChallengeMapping(value, nextChallenges[id]) {
			return fmt.Errorf("existing challenge mapping %q changed", id)
		}
	}
	return nil
}

// File is a readable filename selected when a snapshot is materialized. It is
// not taxonomy content, so publishing another mapping must not treat a file
// stem normalization as a semantic conflict.
func sameSkill(left, right Skill) bool {
	left.File = ""
	right.File = ""
	left.MappingGuidance = normalizedGuidance(left.MappingGuidance)
	right.MappingGuidance = normalizedGuidance(right.MappingGuidance)
	return reflect.DeepEqual(left, right)
}

func sameTag(left, right Tag) bool {
	left.File = ""
	right.File = ""
	left.MappingGuidance = normalizedGuidance(left.MappingGuidance)
	right.MappingGuidance = normalizedGuidance(right.MappingGuidance)
	return reflect.DeepEqual(left, right)
}

func sameSkillMapping(left, right SkillMapping) bool {
	left.File = ""
	right.File = ""
	left.Requires = normalizeEmpty(left.Requires)
	right.Requires = normalizeEmpty(right.Requires)
	return reflect.DeepEqual(left, right)
}

func sameChallengeMapping(left, right ChallengeMapping) bool {
	left.File = ""
	right.File = ""
	left.Tags = normalizeEmpty(left.Tags)
	right.Tags = normalizeEmpty(right.Tags)
	left.EntrySkills = normalizeEmpty(left.EntrySkills)
	right.EntrySkills = normalizeEmpty(right.EntrySkills)
	left.Outcomes = normalizeEmpty(left.Outcomes)
	right.Outcomes = normalizeEmpty(right.Outcomes)
	return reflect.DeepEqual(left, right)
}

func normalizedGuidance(value MappingGuidance) MappingGuidance {
	value.OutcomeWhen = normalizeEmpty(value.OutcomeWhen)
	value.EntryWhen = normalizeEmpty(value.EntryWhen)
	value.IncludeWhen = normalizeEmpty(value.IncludeWhen)
	value.ExcludeWhen = normalizeEmpty(value.ExcludeWhen)
	return value
}

func normalizeEmpty[T any](values []T) []T {
	if len(values) == 0 {
		return nil
	}
	return values
}

func ReferencedDefinitionsUnchanged(base, current Snapshot, changes ChangeSet) bool {
	if !changes.MappingOnlyFor(changeChallengeID(changes.ChallengeMappings[0])) {
		return false
	}
	mapping := changes.ChallengeMappings[0].Value
	if mapping == nil {
		return false
	}
	baseSkills, currentSkills := skillMap(base), skillMap(current)
	baseTags, currentTags := tagMap(base), tagMap(current)
	for _, ref := range append(append([]Ref{}, mapping.EntrySkills...), outcomeRefs(mapping.Outcomes)...) {
		if !sameSkill(baseSkills[ref.ID], currentSkills[ref.ID]) {
			return false
		}
	}
	for _, ref := range mapping.Tags {
		if !sameTag(baseTags[ref.ID], currentTags[ref.ID]) {
			return false
		}
	}
	return true
}

func skillMap(snapshot Snapshot) map[string]Skill {
	result := make(map[string]Skill, len(snapshot.Skills))
	for _, skill := range snapshot.Skills {
		result[skill.ID] = skill
	}
	return result
}

func tagMap(snapshot Snapshot) map[string]Tag {
	result := make(map[string]Tag, len(snapshot.Tags))
	for _, tag := range snapshot.Tags {
		result[tag.ID] = tag
	}
	return result
}

func outcomeRefs(outcomes []OutcomeRef) []Ref {
	result := make([]Ref, 0, len(outcomes))
	for _, outcome := range outcomes {
		result = append(result, Ref{ID: outcome.ID, Title: outcome.Title})
	}
	return result
}
