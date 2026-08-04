package roadmap_test

import (
	"reflect"
	"testing"

	"github.com/breakfix/breakfix/internal/domain/roadmap"
	roadmaptest "github.com/breakfix/breakfix/internal/testkit/roadmap"
)

func TestCompilePortableBuildsDeterministicCompleteRevision(t *testing.T) {
	first := roadmaptest.RuntimeRevision()
	second := roadmaptest.RuntimeRevision()
	if err := first.Validate(); err != nil {
		t.Fatalf("validate compiled roadmap: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("compiled roadmap is not deterministic: %#v != %#v", first, second)
	}
	if len(first.Domains) != 1 || len(first.Topics) != 2 || len(first.Tags) != 1 || len(first.ChallengeBindings) != 2 {
		t.Fatalf("compiled roadmap has incomplete projection: %#v", first)
	}
	for _, binding := range first.ChallengeBindings {
		if binding.Topic.ID == "" || binding.Topic.SourceRef == "" || binding.Challenge.ID == "" {
			t.Fatalf("binding lacks runtime and portable identities: %#v", binding)
		}
	}
	if first.TopicEdges[0].Relation != roadmap.RelationPrecedes || first.ChallengeEdges[0].Relation != roadmap.RelationRelated {
		t.Fatalf("unexpected compiled graph: %#v %#v", first.TopicEdges, first.ChallengeEdges)
	}
}
