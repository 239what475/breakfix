package taxonomy

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// PortableChallengeRef identifies candidate source without a target-platform
// challenge ID. The installer creates that identity only after every release
// entry has passed real verification.
type PortableChallengeRef struct {
	Path            string `yaml:"path" json:"path"`
	Title           string `yaml:"title" json:"title"`
	ContentRevision string `yaml:"contentRevision" json:"contentRevision"`
}

type PortableChallengeMapping struct {
	Challenge   PortableChallengeRef `yaml:"challenge" json:"challenge"`
	Tags        []Ref                `yaml:"tags" json:"tags"`
	EntrySkills []Ref                `yaml:"entry_skills" json:"entry_skills"`
	Outcomes    []OutcomeRef         `yaml:"outcomes" json:"outcomes"`

	File string `yaml:"-" json:"-"`
}

// PortableSnapshot is the taxonomy source shipped with a CatalogRelease. It
// uses source paths and content revisions; runtime Snapshot uses challenge IDs
// after the release has been committed on a target platform.
type PortableSnapshot struct {
	Skills            []Skill
	Tags              []Tag
	ChallengeMappings []PortableChallengeMapping
	SkillMappings     []SkillMapping
}

func ValidatePortable(snapshot PortableSnapshot) error {
	skills, tags, err := validateDefinitions(snapshot.Skills, snapshot.Tags)
	if err != nil {
		return err
	}

	mappings := make(map[string]struct{}, len(snapshot.ChallengeMappings))
	for _, mapping := range snapshot.ChallengeMappings {
		if err := validatePortableChallengeMapping(mapping, skills, tags); err != nil {
			return fmt.Errorf("portable challenge mapping %q: %w", mapping.Challenge.Path, err)
		}
		if _, exists := mappings[mapping.Challenge.Path]; exists {
			return fmt.Errorf("duplicate portable challenge mapping for %q", mapping.Challenge.Path)
		}
		mappings[mapping.Challenge.Path] = struct{}{}
	}
	return validateSkillMappings(snapshot.SkillMappings, skills)
}

func validatePortableChallengeMapping(mapping PortableChallengeMapping, skills map[string]Skill, tags map[string]Tag) error {
	if !validPortableChallengePath(mapping.Challenge.Path) || strings.TrimSpace(mapping.Challenge.Title) == "" || !ValidRevision(mapping.Challenge.ContentRevision) {
		return errors.New("challenge path, title, and contentRevision are required")
	}
	return validateChallengeRelationships(mapping.Tags, mapping.EntrySkills, mapping.Outcomes, skills, tags)
}

func validPortableChallengePath(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.Contains(value, "\\") || path.IsAbs(value) {
		return false
	}
	clean := path.Clean(value)
	return clean == value && strings.HasPrefix(clean, "challenges/") && strings.TrimPrefix(clean, "challenges/") != ""
}
