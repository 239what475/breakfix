package server

import (
	"path/filepath"
	"testing"

	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/taxonomy"
)

func seedTestTaxonomy(t *testing.T, root string) {
	t.Helper()
	entries, err := challenge.List(filepath.Join(root, "challenges"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := taxonomy.Snapshot{
		Skills: []taxonomy.Skill{
			{
				Kind:       taxonomy.KindSkill,
				ID:         "skill-4444444444444444",
				Title:      "Repair a test service",
				Definition: "Diagnose and repair the observable failure in a test challenge.",
				MappingGuidance: taxonomy.MappingGuidance{
					OutcomeWhen: []string{"The challenge requires repairing its central failure."},
				},
				File: "repair-test-service",
			},
			{
				Kind:       taxonomy.KindSkill,
				ID:         "skill-5555555555555555",
				Title:      "Inspect a test service",
				Definition: "Inspect the observable state of a test service.",
				MappingGuidance: taxonomy.MappingGuidance{
					EntryWhen: []string{"The challenge assumes basic service inspection."},
				},
				File: "inspect-test-service",
			},
			{
				Kind:       taxonomy.KindSkill,
				ID:         "skill-6666666666666666",
				Title:      "Read test service state",
				Definition: "Read basic state exposed by a test service.",
				MappingGuidance: taxonomy.MappingGuidance{
					EntryWhen: []string{"The challenge assumes state reading."},
				},
				File: "read-test-service-state",
			},
		},
		Tags: []taxonomy.Tag{{
			Kind:       taxonomy.KindTag,
			ID:         "tag-4444444444444444",
			Title:      "Test",
			Definition: "Test-only catalog classification.",
			MappingGuidance: taxonomy.MappingGuidance{
				IncludeWhen: []string{"The challenge is a test fixture."},
			},
			File: "test",
		}},
	}
	for _, entry := range entries {
		snapshot.ChallengeMappings = append(snapshot.ChallengeMappings, taxonomy.ChallengeMapping{
			Challenge: taxonomy.ChallengeRef{ID: entry.ID, Title: entry.Title, Revision: entry.Revision},
			Tags:      []taxonomy.Ref{{ID: "tag-4444444444444444", Title: "Test"}},
			EntrySkills: []taxonomy.Ref{{
				ID: "skill-5555555555555555", Title: "Inspect a test service",
			}},
			Outcomes: []taxonomy.OutcomeRef{{ID: "skill-4444444444444444", Title: "Repair a test service", Primary: true}},
			File:     filepath.Base(entry.Dir),
		})
	}
	snapshot.SkillMappings = []taxonomy.SkillMapping{{
		Source:   taxonomy.Ref{ID: "skill-5555555555555555", Title: "Inspect a test service"},
		Requires: []taxonomy.Ref{{ID: "skill-6666666666666666", Title: "Read test service state"}},
		File:     "inspect-test-service",
	}}
	if _, err := taxonomy.NewStore(root).Publish(snapshot); err != nil {
		t.Fatal(err)
	}
}
