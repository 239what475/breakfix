package taxonomy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/breakfix/breakfix/internal/challenge"
	domain "github.com/breakfix/breakfix/internal/domain/taxonomy"
)

func TestPublishLoadsCompleteImmutableSnapshotAndAtomicallySwitchesCurrent(t *testing.T) {
	store := NewStore(t.TempDir())
	first, err := store.Publish(testSnapshot("first"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision == "" {
		t.Fatal("publish did not assign a revision")
	}
	current, err := store.LoadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != first.Revision || current.Skills[0].Title != "Repair logs" {
		t.Fatalf("unexpected first current snapshot: %#v", current)
	}
	if target, err := os.Readlink(store.CurrentPath()); err != nil || target != filepath.Join("revisions", first.Revision) {
		t.Fatalf("current pointer = %q, %v", target, err)
	}

	secondSnapshot := testSnapshot("second")
	secondSnapshot.Skills[0].Definition = "Diagnose and repair log archival failures."
	second, err := store.Publish(secondSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision == first.Revision {
		t.Fatal("content change did not produce a new revision")
	}
	if _, err := store.LoadRevision(first.Revision); err != nil {
		t.Fatalf("old immutable revision was lost: %v", err)
	}
	current, err = store.LoadCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != second.Revision || current.Skills[0].Definition != secondSnapshot.Skills[0].Definition {
		t.Fatalf("current did not advance atomically: %#v", current)
	}

	// Publishing exactly the same tree is content-addressed and reuses it.
	reused, err := store.Publish(secondSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if reused.Revision != second.Revision {
		t.Fatalf("identical snapshot changed revision: %s != %s", reused.Revision, second.Revision)
	}
}

func TestLoadCurrentRejectsTamperedSnapshot(t *testing.T) {
	store := NewStore(t.TempDir())
	published, err := store.Publish(testSnapshot("tamper"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.RevisionsPath(), published.Revision, "skills", "repair-logs.yaml")
	if err := os.WriteFile(path, []byte("kind: domain.Skill\nid: skill-1111111111111111\ntitle: changed\ndefinition: changed\nmapping_guidance:\n  outcome_when: [changed]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadCurrent(); err == nil {
		t.Fatal("tampered snapshot loaded")
	}
}

func TestValidateEnforcesReferencesOutcomesAndDAG(t *testing.T) {
	snapshot := testSnapshot("validate")
	snapshot.ChallengeMappings[0].Outcomes[0].Title = "stale title"
	if err := domain.Validate(snapshot); err == nil {
		t.Fatal("stale title accepted")
	}

	snapshot = testSnapshot("validate")
	snapshot.ChallengeMappings[0].EntrySkills = []domain.Ref{{ID: "skill-1111111111111111", Title: "Repair logs"}}
	if err := domain.Validate(snapshot); err == nil {
		t.Fatal("entry/outcome overlap accepted")
	}

	snapshot = testSnapshot("validate")
	snapshot.Skills = append(snapshot.Skills, domain.Skill{
		Kind: domain.KindSkill, ID: "skill-2222222222222222", Title: "Inspect logs", Definition: "Inspect a log directory.",
		MappingGuidance: domain.MappingGuidance{EntryWhen: []string{"The task assumes basic log inspection."}}, File: "inspect-logs",
	})
	snapshot.SkillMappings = []domain.SkillMapping{
		{Source: domain.Ref{ID: "skill-1111111111111111", Title: "Repair logs"}, Requires: []domain.Ref{{ID: "skill-2222222222222222", Title: "Inspect logs"}}, File: "repair-logs"},
		{Source: domain.Ref{ID: "skill-2222222222222222", Title: "Inspect logs"}, Requires: []domain.Ref{{ID: "skill-1111111111111111", Title: "Repair logs"}}, File: "inspect-logs"},
	}
	if err := domain.Validate(snapshot); err == nil {
		t.Fatal("skill requires cycle accepted")
	}
}

func TestCatalogIndexOnlyExposesExactChallengeRevisionMapping(t *testing.T) {
	snapshot := testSnapshot("catalog")
	entry := challenge.Entry{ID: "challenge-demo", Title: "Demo", Revision: snapshot.ChallengeMappings[0].Challenge.Revision}
	index, err := NewCatalogIndex(snapshot, []challenge.Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if mapping, ok := index.Mapping(entry.ID); !ok || len(mapping.Tags) != 1 || mapping.Tags[0].ID != "tag-1111111111111111" {
		t.Fatalf("matching challenge not exposed: %#v, %v", mapping, ok)
	}
	entry.Revision = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	index, err = NewCatalogIndex(snapshot, []challenge.Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := index.Mapping(entry.ID); ok {
		t.Fatal("stale mapping exposed a changed challenge")
	}
}

func TestLoadCurrentMissing(t *testing.T) {
	_, err := NewStore(t.TempDir()).LoadCurrent()
	if !errors.Is(err, domain.ErrNoCurrentRevision) {
		t.Fatalf("LoadCurrent error = %v", err)
	}
}

func TestValidateRejectsSemanticDefinitionID(t *testing.T) {
	snapshot := testSnapshot("semantic-id")
	snapshot.Skills[0].ID = "skill-repair-logs"
	if err := domain.Validate(snapshot); err == nil {
		t.Fatal("semantic skill id accepted")
	}
}

func TestPublishUsesReadableUnicodeSourceFilenames(t *testing.T) {
	store := NewStore(t.TempDir())
	snapshot := testSnapshot("unicode")
	snapshot.Skills[0].File = snapshot.Skills[0].ID
	snapshot.Skills[0].Title = "诊断日志轮转"
	snapshot.Tags[0].File = snapshot.Tags[0].ID
	snapshot.Tags[0].Title = "系统运维"
	snapshot.ChallengeMappings[0].File = snapshot.ChallengeMappings[0].Challenge.ID
	snapshot.ChallengeMappings[0].Challenge.Title = "修复日志权限"
	snapshot.ChallengeMappings[0].Tags[0].Title = snapshot.Tags[0].Title
	snapshot.ChallengeMappings[0].Outcomes[0].Title = snapshot.Skills[0].Title

	published, err := store.Publish(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{
		"skills/诊断日志轮转.yaml",
		"tags/系统运维.yaml",
		"mappings/challenges/修复日志权限.yaml",
	} {
		if _, err := os.Stat(filepath.Join(store.RevisionsPath(), published.Revision, relative)); err != nil {
			t.Fatalf("readable taxonomy source filename %q was not published: %v", relative, err)
		}
	}
}

func testSnapshot(fileSuffix string) domain.Snapshot {
	revision := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	return domain.Snapshot{
		Skills: []domain.Skill{{
			Kind: domain.KindSkill, ID: "skill-1111111111111111", Title: "Repair logs", Definition: "Diagnose and repair failed log cleanup.", File: "repair-logs",
			MappingGuidance: domain.MappingGuidance{OutcomeWhen: []string{"The challenge requires correcting log cleanup behavior."}, ExcludeWhen: []string{"Logs are only incidental."}},
		}},
		Tags: []domain.Tag{{
			Kind: domain.KindTag, ID: "tag-1111111111111111", Title: "Linux", Definition: "Linux administration and troubleshooting.", File: "linux",
			MappingGuidance: domain.MappingGuidance{IncludeWhen: []string{"Linux behavior is central to the task."}, ExcludeWhen: []string{"Linux is only the base image."}},
		}},
		ChallengeMappings: []domain.ChallengeMapping{{
			Challenge: domain.ChallengeRef{ID: "challenge-demo", Title: "Demo", Revision: revision}, File: "demo-" + fileSuffix,
			Tags:     []domain.Ref{{ID: "tag-1111111111111111", Title: "Linux"}},
			Outcomes: []domain.OutcomeRef{{ID: "skill-1111111111111111", Title: "Repair logs", Primary: true}},
		}},
	}
}
