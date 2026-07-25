// Package taxonomy owns the immutable Skill, Tag, and Mapping content model.
// Taxonomy content lives in filesystem snapshots; operational workflow state
// deliberately lives elsewhere.
package taxonomy

import "slices"

const (
	KindSkill = "Skill"
	KindTag   = "Tag"
)

type MappingGuidance struct {
	OutcomeWhen []string `yaml:"outcome_when,omitempty" json:"outcome_when,omitempty"`
	EntryWhen   []string `yaml:"entry_when,omitempty" json:"entry_when,omitempty"`
	IncludeWhen []string `yaml:"include_when,omitempty" json:"include_when,omitempty"`
	ExcludeWhen []string `yaml:"exclude_when,omitempty" json:"exclude_when,omitempty"`
}

type Skill struct {
	Kind            string          `yaml:"kind" json:"kind"`
	ID              string          `yaml:"id" json:"id"`
	Title           string          `yaml:"title" json:"title"`
	Definition      string          `yaml:"definition" json:"definition"`
	MappingGuidance MappingGuidance `yaml:"mapping_guidance" json:"mapping_guidance"`

	// File is the readable source slug without its .yaml suffix. It is never a
	// relationship key and is intentionally omitted from snapshot YAML.
	File string `yaml:"-" json:"-"`
}

type Tag struct {
	Kind            string          `yaml:"kind" json:"kind"`
	ID              string          `yaml:"id" json:"id"`
	Title           string          `yaml:"title" json:"title"`
	Definition      string          `yaml:"definition" json:"definition"`
	MappingGuidance MappingGuidance `yaml:"mapping_guidance" json:"mapping_guidance"`

	File string `yaml:"-" json:"-"`
}

type Ref struct {
	ID    string `yaml:"id" json:"id"`
	Title string `yaml:"title" json:"title"`
}

type OutcomeRef struct {
	ID      string `yaml:"id" json:"id"`
	Title   string `yaml:"title" json:"title"`
	Primary bool   `yaml:"primary" json:"primary"`
}

type ChallengeRef struct {
	ID       string `yaml:"id" json:"id"`
	Title    string `yaml:"title" json:"title"`
	Revision string `yaml:"revision" json:"revision"`
}

type ChallengeMapping struct {
	Challenge   ChallengeRef `yaml:"challenge" json:"challenge"`
	Tags        []Ref        `yaml:"tags" json:"tags"`
	EntrySkills []Ref        `yaml:"entry_skills" json:"entry_skills"`
	Outcomes    []OutcomeRef `yaml:"outcomes" json:"outcomes"`

	File string `yaml:"-" json:"-"`
}

type SkillMapping struct {
	Source   Ref   `yaml:"source" json:"source"`
	Requires []Ref `yaml:"requires" json:"requires"`

	File string `yaml:"-" json:"-"`
}

// Snapshot is a complete immutable taxonomy revision. Relationships are kept
// separate from definitions so a definition stays meaningful as mappings grow.
type Snapshot struct {
	Revision          string
	Skills            []Skill
	Tags              []Tag
	ChallengeMappings []ChallengeMapping
	SkillMappings     []SkillMapping
}

func (s Snapshot) Clone() Snapshot {
	clone := Snapshot{Revision: s.Revision}
	clone.Skills = append([]Skill{}, s.Skills...)
	clone.Tags = append([]Tag{}, s.Tags...)
	clone.ChallengeMappings = append([]ChallengeMapping{}, s.ChallengeMappings...)
	clone.SkillMappings = append([]SkillMapping{}, s.SkillMappings...)
	for i := range clone.Skills {
		clone.Skills[i].MappingGuidance = cloneGuidance(clone.Skills[i].MappingGuidance)
	}
	for i := range clone.Tags {
		clone.Tags[i].MappingGuidance = cloneGuidance(clone.Tags[i].MappingGuidance)
	}
	for i := range clone.ChallengeMappings {
		clone.ChallengeMappings[i].Tags = append([]Ref{}, clone.ChallengeMappings[i].Tags...)
		clone.ChallengeMappings[i].EntrySkills = append([]Ref{}, clone.ChallengeMappings[i].EntrySkills...)
		clone.ChallengeMappings[i].Outcomes = append([]OutcomeRef{}, clone.ChallengeMappings[i].Outcomes...)
	}
	for i := range clone.SkillMappings {
		clone.SkillMappings[i].Requires = append([]Ref{}, clone.SkillMappings[i].Requires...)
	}
	return clone
}

func cloneGuidance(value MappingGuidance) MappingGuidance {
	value.OutcomeWhen = append([]string{}, value.OutcomeWhen...)
	value.EntryWhen = append([]string{}, value.EntryWhen...)
	value.IncludeWhen = append([]string{}, value.IncludeWhen...)
	value.ExcludeWhen = append([]string{}, value.ExcludeWhen...)
	return value
}

func (s Snapshot) Sorted() Snapshot {
	result := s.Clone()
	slices.SortFunc(result.Skills, func(left, right Skill) int { return compareID(left.ID, right.ID) })
	slices.SortFunc(result.Tags, func(left, right Tag) int { return compareID(left.ID, right.ID) })
	slices.SortFunc(result.ChallengeMappings, func(left, right ChallengeMapping) int {
		return compareID(left.Challenge.ID, right.Challenge.ID)
	})
	slices.SortFunc(result.SkillMappings, func(left, right SkillMapping) int {
		return compareID(left.Source.ID, right.Source.ID)
	})
	return result
}

func compareID(left, right string) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}
