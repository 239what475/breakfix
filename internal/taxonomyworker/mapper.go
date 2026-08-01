package taxonomyworker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/breakfix/breakfix/internal/taxonomy"
)

// mapperResult is the model-facing mapping protocol. It intentionally has no
// opaque IDs, taxonomy kinds, titles for existing references, or ChangeSet
// operations. The worker creates all of those from a fixed snapshot.
type mapperResult struct {
	NewSkills            []mapperNewSkill            `json:"new_skills" jsonschema:"required" jsonschema_description:"本轮新增 Skill 定义"`
	NewTags              []mapperNewTag              `json:"new_tags" jsonschema:"required" jsonschema_description:"本轮新增 Tag 定义"`
	Mapping              mapperMapping               `json:"mapping" jsonschema:"required" jsonschema_description:"目标 challenge 的关系"`
	NewSkillRequirements []mapperNewSkillRequirement `json:"new_skill_requirements" jsonschema:"required" jsonschema_description:"本轮新增 Skill 的前置关系"`
}

type mapperNewSkill struct {
	Key             string                   `json:"key" jsonschema:"required" jsonschema_description:"本轮引用该新 Skill 的本地键"`
	Title           string                   `json:"title" jsonschema:"required" jsonschema_description:"Skill 标题"`
	Definition      string                   `json:"definition" jsonschema:"required" jsonschema_description:"可复用能力定义"`
	MappingGuidance taxonomy.MappingGuidance `json:"mapping_guidance" jsonschema:"required" jsonschema_description:"该能力的映射规则"`
}

type mapperNewTag struct {
	Key             string                   `json:"key" jsonschema:"required" jsonschema_description:"本轮引用该新 Tag 的本地键"`
	Title           string                   `json:"title" jsonschema:"required" jsonschema_description:"Tag 标题"`
	Definition      string                   `json:"definition" jsonschema:"required" jsonschema_description:"稳定浏览维度定义"`
	MappingGuidance taxonomy.MappingGuidance `json:"mapping_guidance" jsonschema:"required" jsonschema_description:"该标签的映射规则"`
}

type mapperMapping struct {
	ExistingTagIDs        []string               `json:"existing_tag_ids" jsonschema:"required" jsonschema_description:"复用的已有 Tag ID"`
	NewTagKeys            []string               `json:"new_tag_keys" jsonschema:"required" jsonschema_description:"使用的新增 Tag 本地键"`
	ExistingEntrySkillIDs []string               `json:"existing_entry_skill_ids" jsonschema:"required" jsonschema_description:"复用的已有入口 Skill ID"`
	NewEntrySkillKeys     []string               `json:"new_entry_skill_keys" jsonschema:"required" jsonschema_description:"使用的新增入口 Skill 本地键"`
	PrimaryOutcome        mapperOutcomeReference `json:"primary_outcome" jsonschema:"required" jsonschema_description:"唯一的主要学习结果"`
	SecondaryExistingIDs  []string               `json:"secondary_existing_outcome_ids" jsonschema:"required" jsonschema_description:"复用的次要学习结果 Skill ID"`
	SecondaryNewKeys      []string               `json:"secondary_new_outcome_keys" jsonschema:"required" jsonschema_description:"新增的次要学习结果本地键"`
}

type mapperOutcomeReference struct {
	ExistingID *string `json:"existing_id,omitempty" jsonschema:"oneof_required=existing_skill" jsonschema_description:"已有 Skill ID"`
	NewKey     *string `json:"new_key,omitempty" jsonschema:"oneof_required=new_skill" jsonschema_description:"新增 Skill 的本地键"`
}

type mapperNewSkillRequirement struct {
	SourceKey              string   `json:"source_key" jsonschema:"required" jsonschema_description:"新增 Skill 的本地键"`
	ExistingRequirementIDs []string `json:"existing_requirement_ids" jsonschema:"required" jsonschema_description:"已有前置 Skill ID"`
	NewRequirementKeys     []string `json:"new_requirement_keys" jsonschema:"required" jsonschema_description:"新增前置 Skill 本地键"`
}

func (r mapperResult) ChangeSet(validation taxonomy.MapperValidation) (taxonomy.ChangeSet, error) {
	if err := r.validateShape(); err != nil {
		return taxonomy.ChangeSet{}, err
	}

	existingSkills := make(map[string]taxonomy.Skill, len(validation.Base.Skills))
	for _, skill := range validation.Base.Skills {
		existingSkills[skill.ID] = skill
	}
	existingTags := make(map[string]taxonomy.Tag, len(validation.Base.Tags))
	for _, tag := range validation.Base.Tags {
		existingTags[tag.ID] = tag
	}

	newSkills, err := r.newSkills(validation.Challenge.ID, existingSkills)
	if err != nil {
		return taxonomy.ChangeSet{}, err
	}
	newTags, err := r.newTags(validation.Challenge.ID, existingTags)
	if err != nil {
		return taxonomy.ChangeSet{}, err
	}

	resolveExistingSkill := func(id string) (taxonomy.Ref, error) {
		skill, exists := existingSkills[id]
		if !exists {
			return taxonomy.Ref{}, fmt.Errorf("unknown existing skill %q", id)
		}
		return taxonomy.Ref{ID: skill.ID, Title: skill.Title}, nil
	}
	resolveNewSkill := func(key string) (taxonomy.Ref, error) {
		skill, exists := newSkills[key]
		if !exists {
			return taxonomy.Ref{}, fmt.Errorf("unknown new skill key %q", key)
		}
		return taxonomy.Ref{ID: skill.ID, Title: skill.Title}, nil
	}
	resolveExistingTag := func(id string) (taxonomy.Ref, error) {
		tag, exists := existingTags[id]
		if !exists {
			return taxonomy.Ref{}, fmt.Errorf("unknown existing tag %q", id)
		}
		return taxonomy.Ref{ID: tag.ID, Title: tag.Title}, nil
	}
	resolveNewTag := func(key string) (taxonomy.Ref, error) {
		tag, exists := newTags[key]
		if !exists {
			return taxonomy.Ref{}, fmt.Errorf("unknown new tag key %q", key)
		}
		return taxonomy.Ref{ID: tag.ID, Title: tag.Title}, nil
	}

	mapping := taxonomy.ChallengeMapping{Challenge: validation.Challenge}
	for _, id := range r.Mapping.ExistingTagIDs {
		ref, err := resolveExistingTag(id)
		if err != nil {
			return taxonomy.ChangeSet{}, err
		}
		mapping.Tags = append(mapping.Tags, ref)
	}
	for _, key := range r.Mapping.NewTagKeys {
		ref, err := resolveNewTag(key)
		if err != nil {
			return taxonomy.ChangeSet{}, err
		}
		mapping.Tags = append(mapping.Tags, ref)
	}
	for _, id := range r.Mapping.ExistingEntrySkillIDs {
		ref, err := resolveExistingSkill(id)
		if err != nil {
			return taxonomy.ChangeSet{}, err
		}
		mapping.EntrySkills = append(mapping.EntrySkills, ref)
	}
	for _, key := range r.Mapping.NewEntrySkillKeys {
		ref, err := resolveNewSkill(key)
		if err != nil {
			return taxonomy.ChangeSet{}, err
		}
		mapping.EntrySkills = append(mapping.EntrySkills, ref)
	}
	primary, err := r.resolvePrimaryOutcome(resolveExistingSkill, resolveNewSkill)
	if err != nil {
		return taxonomy.ChangeSet{}, err
	}
	mapping.Outcomes = append(mapping.Outcomes, taxonomy.OutcomeRef{ID: primary.ID, Title: primary.Title, Primary: true})
	for _, id := range r.Mapping.SecondaryExistingIDs {
		ref, err := resolveExistingSkill(id)
		if err != nil {
			return taxonomy.ChangeSet{}, err
		}
		mapping.Outcomes = append(mapping.Outcomes, taxonomy.OutcomeRef{ID: ref.ID, Title: ref.Title})
	}
	for _, key := range r.Mapping.SecondaryNewKeys {
		ref, err := resolveNewSkill(key)
		if err != nil {
			return taxonomy.ChangeSet{}, err
		}
		mapping.Outcomes = append(mapping.Outcomes, taxonomy.OutcomeRef{ID: ref.ID, Title: ref.Title})
	}

	changes := taxonomy.ChangeSet{
		Skills:            make([]taxonomy.SkillChange, 0, len(newSkills)),
		Tags:              make([]taxonomy.TagChange, 0, len(newTags)),
		ChallengeMappings: []taxonomy.ChallengeMappingChange{{Operation: taxonomy.ChangeUpsert, Value: &mapping}},
		SkillMappings:     make([]taxonomy.SkillMappingChange, 0, len(r.NewSkillRequirements)),
	}
	for _, definition := range r.NewSkills {
		value := newSkills[definition.Key]
		changes.Skills = append(changes.Skills, taxonomy.SkillChange{Operation: taxonomy.ChangeUpsert, Value: &value})
	}
	for _, definition := range r.NewTags {
		value := newTags[definition.Key]
		changes.Tags = append(changes.Tags, taxonomy.TagChange{Operation: taxonomy.ChangeUpsert, Value: &value})
	}
	for _, requirement := range r.NewSkillRequirements {
		source, err := resolveNewSkill(requirement.SourceKey)
		if err != nil {
			return taxonomy.ChangeSet{}, err
		}
		value := taxonomy.SkillMapping{Source: source}
		for _, id := range requirement.ExistingRequirementIDs {
			ref, err := resolveExistingSkill(id)
			if err != nil {
				return taxonomy.ChangeSet{}, err
			}
			value.Requires = append(value.Requires, ref)
		}
		for _, key := range requirement.NewRequirementKeys {
			ref, err := resolveNewSkill(key)
			if err != nil {
				return taxonomy.ChangeSet{}, err
			}
			value.Requires = append(value.Requires, ref)
		}
		changes.SkillMappings = append(changes.SkillMappings, taxonomy.SkillMappingChange{Operation: taxonomy.ChangeUpsert, Value: &value})
	}
	return changes, nil
}

func (r mapperResult) validateShape() error {
	if r.NewSkills == nil || r.NewTags == nil || r.NewSkillRequirements == nil {
		return errors.New("mapper result must explicitly contain new_skills, new_tags, and new_skill_requirements arrays")
	}
	if r.Mapping.ExistingTagIDs == nil || r.Mapping.NewTagKeys == nil || r.Mapping.ExistingEntrySkillIDs == nil ||
		r.Mapping.NewEntrySkillKeys == nil || r.Mapping.SecondaryExistingIDs == nil || r.Mapping.SecondaryNewKeys == nil {
		return errors.New("mapper mapping must explicitly contain every reference array")
	}
	if (r.Mapping.PrimaryOutcome.ExistingID == nil) == (r.Mapping.PrimaryOutcome.NewKey == nil) {
		return errors.New("mapper mapping requires exactly one primary outcome reference")
	}
	for _, requirement := range r.NewSkillRequirements {
		if requirement.ExistingRequirementIDs == nil || requirement.NewRequirementKeys == nil {
			return errors.New("new skill requirement must explicitly contain both requirement arrays")
		}
	}
	return nil
}

func (r mapperResult) resolvePrimaryOutcome(existing func(string) (taxonomy.Ref, error), created func(string) (taxonomy.Ref, error)) (taxonomy.Ref, error) {
	if r.Mapping.PrimaryOutcome.ExistingID != nil {
		return existing(*r.Mapping.PrimaryOutcome.ExistingID)
	}
	return created(*r.Mapping.PrimaryOutcome.NewKey)
}

func (r mapperResult) newSkills(challengeID string, existing map[string]taxonomy.Skill) (map[string]taxonomy.Skill, error) {
	result := make(map[string]taxonomy.Skill, len(r.NewSkills))
	ids := make(map[string]string, len(r.NewSkills))
	for _, definition := range r.NewSkills {
		if err := validateMapperKey(definition.Key); err != nil {
			return nil, fmt.Errorf("new skill key: %w", err)
		}
		if _, exists := result[definition.Key]; exists {
			return nil, fmt.Errorf("new skill key %q appears more than once", definition.Key)
		}
		id := mapperDefinitionID(challengeID, taxonomy.KindSkill, definition.Key)
		if _, exists := existing[id]; exists {
			return nil, fmt.Errorf("new skill key %q collides with existing skill %q", definition.Key, id)
		}
		if other, exists := ids[id]; exists {
			return nil, fmt.Errorf("new skill keys %q and %q produce the same opaque id", other, definition.Key)
		}
		ids[id] = definition.Key
		result[definition.Key] = taxonomy.Skill{
			Kind:            taxonomy.KindSkill,
			ID:              id,
			Title:           definition.Title,
			Definition:      definition.Definition,
			MappingGuidance: definition.MappingGuidance,
		}
	}
	return result, nil
}

func (r mapperResult) newTags(challengeID string, existing map[string]taxonomy.Tag) (map[string]taxonomy.Tag, error) {
	result := make(map[string]taxonomy.Tag, len(r.NewTags))
	ids := make(map[string]string, len(r.NewTags))
	for _, definition := range r.NewTags {
		if err := validateMapperKey(definition.Key); err != nil {
			return nil, fmt.Errorf("new tag key: %w", err)
		}
		if _, exists := result[definition.Key]; exists {
			return nil, fmt.Errorf("new tag key %q appears more than once", definition.Key)
		}
		id := mapperDefinitionID(challengeID, taxonomy.KindTag, definition.Key)
		if _, exists := existing[id]; exists {
			return nil, fmt.Errorf("new tag key %q collides with existing tag %q", definition.Key, id)
		}
		if other, exists := ids[id]; exists {
			return nil, fmt.Errorf("new tag keys %q and %q produce the same opaque id", other, definition.Key)
		}
		ids[id] = definition.Key
		result[definition.Key] = taxonomy.Tag{
			Kind:            taxonomy.KindTag,
			ID:              id,
			Title:           definition.Title,
			Definition:      definition.Definition,
			MappingGuidance: definition.MappingGuidance,
		}
	}
	return result, nil
}

func validateMapperKey(value string) error {
	if value == "" || len(value) > 64 || strings.TrimSpace(value) != value {
		return errors.New("must be a non-empty lowercase ASCII kebab key no longer than 64 bytes")
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9' && index > 0) || (character == '-' && index > 0 && index < len(value)-1) {
			continue
		}
		return errors.New("must be a lowercase ASCII kebab key")
	}
	return nil
}

func mapperDefinitionID(challengeID, kind, key string) string {
	sum := sha256.Sum256([]byte("breakfix/taxonomy/definition/v1\x00" + challengeID + "\x00" + kind + "\x00" + key))
	prefix := "skill-"
	if kind == taxonomy.KindTag {
		prefix = "tag-"
	}
	return prefix + hex.EncodeToString(sum[:8])
}
