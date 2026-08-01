package taxonomy

import (
	"reflect"
	"testing"

	taxonomyapp "github.com/breakfix/breakfix/internal/application/taxonomy"
	"github.com/breakfix/breakfix/internal/domain/taxonomy"
)

func TestMapperResultBuildsNewDefinitionsWithoutChangingBase(t *testing.T) {
	base := taxonomy.Snapshot{
		Skills: []taxonomy.Skill{{
			Kind: taxonomy.KindSkill, ID: "skill-1111111111111111", Title: "创建可执行脚本", Definition: "创建可独立运行的 Shell 脚本。", File: "executable-script",
			MappingGuidance: taxonomy.MappingGuidance{EntryWhen: []string{"题目假设用户能创建脚本。"}},
		}},
		Tags: []taxonomy.Tag{{
			Kind: taxonomy.KindTag, ID: "tag-1111111111111111", Title: "Shell", Definition: "以 Shell 脚本为核心的题目。", File: "shell",
			MappingGuidance: taxonomy.MappingGuidance{IncludeWhen: []string{"题目要求编写 Shell 脚本。"}},
		}},
	}
	newSkill := "gzip-file-compression"
	result := mapperResult{
		NewSkills: []mapperNewSkill{{
			Key:        newSkill,
			Title:      "gzip 文件压缩",
			Definition: "使用 gzip 压缩文件并保留可重复执行的处理语义。",
			MappingGuidance: taxonomy.MappingGuidance{
				OutcomeWhen: []string{"题目要求用 gzip 压缩文件。"},
			},
		}},
		NewTags: []mapperNewTag{},
		Mapping: mapperMapping{
			ExistingTagIDs:        []string{"tag-1111111111111111"},
			NewTagKeys:            []string{},
			ExistingEntrySkillIDs: []string{"skill-1111111111111111"},
			NewEntrySkillKeys:     []string{},
			PrimaryOutcome:        mapperOutcomeReference{NewKey: &newSkill},
			SecondaryExistingIDs:  []string{},
			SecondaryNewKeys:      []string{},
		},
		NewSkillRequirements: []mapperNewSkillRequirement{},
	}
	entry := taxonomy.ChallengeRef{
		ID: "challenge-test", Title: "测试题目", Revision: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	changes, err := result.ChangeSet(taxonomyapp.MapperValidation{Challenge: entry, Base: base})
	if err != nil {
		t.Fatalf("convert mapper result: %v", err)
	}
	preview, err := taxonomy.ApplyChangeSet(base, changes)
	if err != nil {
		t.Fatalf("apply converted changeset: %v", err)
	}
	if !reflect.DeepEqual(skillByID(preview, base.Skills[0].ID), base.Skills[0]) {
		t.Fatalf("apply changed existing skill: got %#v want %#v", skillByID(preview, base.Skills[0].ID), base.Skills[0])
	}
	next, err := taxonomy.ValidateWorkflowChangeSet(changes, entry, base)
	if err != nil {
		t.Fatalf("validate converted changeset: %v", err)
	}
	if len(changes.Skills) != 1 || changes.Skills[0].Value == nil || changes.Skills[0].Value.ID == base.Skills[0].ID {
		t.Fatalf("converted new skill = %#v", changes.Skills)
	}
	if !reflect.DeepEqual(skillByID(next, base.Skills[0].ID), base.Skills[0]) {
		t.Fatalf("existing skill changed: got %#v want %#v", skillByID(next, base.Skills[0].ID), base.Skills[0])
	}
	if len(next.ChallengeMappings) != 1 || next.ChallengeMappings[0].Outcomes[0].Primary != true {
		t.Fatalf("converted challenge mapping = %#v", next.ChallengeMappings)
	}
}

func skillByID(snapshot taxonomy.Snapshot, id string) taxonomy.Skill {
	for _, skill := range snapshot.Skills {
		if skill.ID == id {
			return skill
		}
	}
	return taxonomy.Skill{}
}
