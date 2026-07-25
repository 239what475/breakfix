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
		Skills: []taxonomy.Skill{{
			Kind:       taxonomy.KindSkill,
			ID:         "skill-4444444444444444",
			Title:      "Repair a test service",
			Definition: "Diagnose and repair the observable failure in a test challenge.",
			MappingGuidance: taxonomy.MappingGuidance{
				OutcomeWhen: []string{"The challenge requires repairing its central failure."},
			},
			File: "repair-test-service",
		}},
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
			Outcomes:  []taxonomy.OutcomeRef{{ID: "skill-4444444444444444", Title: "Repair a test service", Primary: true}},
			File:      filepath.Base(entry.Dir),
		})
	}
	if _, err := taxonomy.NewStore(root).Publish(snapshot); err != nil {
		t.Fatal(err)
	}
}
