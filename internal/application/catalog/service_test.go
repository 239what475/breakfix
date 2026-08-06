package catalog

import (
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/content/challenge"
	roadmap "github.com/breakfix/breakfix/internal/domain/roadmap"
)

func TestProjectPublishedChallengesUsesOneImmutableRevisionAndSeparateOneHopGraphs(t *testing.T) {
	domain := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindDomain, "linux"), SourceRef: "linux", Title: "Linux operations"}
	topicOne := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindTopic, "linux/files"), SourceRef: "linux/files", Title: "Files"}
	topicTwo := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindTopic, "linux/services"), SourceRef: "linux/services", Title: "Services"}
	topicThree := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindTopic, "linux/networking"), SourceRef: "linux/networking", Title: "Networking"}
	tag := roadmap.Ref{ID: roadmap.RuntimeID(roadmap.KindTag, "systemd"), SourceRef: "systemd", Title: "systemd"}
	challengeOne := roadmap.ChallengeRef{ID: "chal-files", RevisionID: "chrev-aaaaaaaaaaaaaaaa", SourceRef: "linux/files/permissions", Title: "Repair file permissions", ContentRevision: catalogTestRevision('a'), SourceSlug: "repair-file-permissions", MaterializedRevision: catalogTestRevision('f')}
	challengeTwo := roadmap.ChallengeRef{ID: "chal-services", RevisionID: "chrev-bbbbbbbbbbbbbbbb", SourceRef: "linux/services/restart", Title: "Restart a service", ContentRevision: catalogTestRevision('b'), SourceSlug: "restart-service", MaterializedRevision: catalogTestRevision('g')}
	challengeThree := roadmap.ChallengeRef{ID: "chal-networking", RevisionID: "chrev-cccccccccccccccc", SourceRef: "linux/networking/dns", Title: "Repair DNS", ContentRevision: catalogTestRevision('c'), SourceSlug: "repair-dns", MaterializedRevision: catalogTestRevision('h')}
	revision := roadmap.Revision{
		Revision: catalogTestRevision('d'),
		Domains:  []roadmap.Domain{{ID: domain.ID, SourceRef: domain.SourceRef, Title: domain.Title, Definition: "Operate Linux systems.", Scope: "Operations.", NonGoals: "Kernel development."}},
		Topics: []roadmap.Topic{
			{ID: topicOne.ID, SourceRef: topicOne.SourceRef, Title: topicOne.Title, Domain: domain, Definition: "Manage files.", Scope: "Files.", NonGoals: "Services.", ChallengeGuidance: "Use for file-state recovery."},
			{ID: topicTwo.ID, SourceRef: topicTwo.SourceRef, Title: topicTwo.Title, Domain: domain, Definition: "Manage services.", Scope: "Services.", NonGoals: "Networking.", ChallengeGuidance: "Use for service recovery."},
			{ID: topicThree.ID, SourceRef: topicThree.SourceRef, Title: topicThree.Title, Domain: domain, Definition: "Manage networking.", Scope: "Networking.", NonGoals: "Files.", ChallengeGuidance: "Use for network recovery."},
		},
		Tags: []roadmap.Tag{{ID: tag.ID, SourceRef: tag.SourceRef, Title: tag.Title, Description: "The task materially depends on systemd."}},
		ChallengeBindings: []roadmap.ChallengeBinding{
			{Challenge: challengeOne, Topic: topicOne, Tags: []roadmap.Ref{tag}},
			{Challenge: challengeTwo, Topic: topicTwo},
			{Challenge: challengeThree, Topic: topicThree},
		},
		TopicEdges: []roadmap.Edge{
			{Source: topicOne, Target: topicTwo, Relation: roadmap.RelationPrecedes, Reason: "File state is useful before service diagnosis."},
			{Source: topicTwo, Target: topicThree, Relation: roadmap.RelationPrecedes, Reason: "Service diagnosis can precede network diagnosis."},
		},
		ChallengeEdges: []roadmap.Edge{
			{Source: roadmap.Ref{ID: challengeOne.ID, SourceRef: challengeOne.SourceRef, Title: challengeOne.Title}, Target: roadmap.Ref{ID: challengeTwo.ID, SourceRef: challengeTwo.SourceRef, Title: challengeTwo.Title}, Relation: roadmap.RelationPrecedes, Reason: "Repair permissions first."},
			{Source: roadmap.Ref{ID: challengeTwo.ID, SourceRef: challengeTwo.SourceRef, Title: challengeTwo.Title}, Target: roadmap.Ref{ID: challengeThree.ID, SourceRef: challengeThree.SourceRef, Title: challengeThree.Title}, Relation: roadmap.RelationPrecedes, Reason: "Restart the service first."},
		},
	}
	entries := map[string]challenge.Entry{
		challengeOne.ID:   {ID: challengeOne.ID, RevisionID: challengeOne.RevisionID, Title: challengeOne.Title, ContentRevision: challengeOne.ContentRevision, SourceSlug: challengeOne.SourceSlug, Revision: challengeOne.MaterializedRevision},
		challengeTwo.ID:   {ID: challengeTwo.ID, RevisionID: challengeTwo.RevisionID, Title: challengeTwo.Title, ContentRevision: challengeTwo.ContentRevision, SourceSlug: challengeTwo.SourceSlug, Revision: challengeTwo.MaterializedRevision},
		challengeThree.ID: {ID: challengeThree.ID, RevisionID: challengeThree.RevisionID, Title: challengeThree.Title, ContentRevision: challengeThree.ContentRevision, SourceSlug: challengeThree.SourceSlug, Revision: challengeThree.MaterializedRevision},
		"chal-stale":      {ID: "chal-stale", Title: "Stale challenge", ContentRevision: catalogTestRevision('e')},
	}

	projected := projectPublishedChallenges(revision, entries)
	if len(projected) != 3 {
		t.Fatalf("projected challenges = %#v", projected)
	}
	first := projected[0]
	if first.Entry.ID != challengeOne.ID || first.Roadmap.Revision != revision.Revision || first.Roadmap.Domain != domain || first.Roadmap.Topic.ID != topicOne.ID {
		t.Fatalf("unexpected catalog projection: %#v", first)
	}
	if len(first.Roadmap.Tags) != 1 || first.Roadmap.Tags[0].ID != tag.ID {
		t.Fatalf("catalog tags = %#v", first.Roadmap.Tags)
	}
	if len(first.Roadmap.TopicNeighbors) != 1 || first.Roadmap.TopicNeighbors[0].Target.ID != topicTwo.ID {
		t.Fatalf("topic neighbors = %#v", first.Roadmap.TopicNeighbors)
	}
	if len(first.Roadmap.ChallengeNeighbors) != 1 || first.Roadmap.ChallengeNeighbors[0].Target.ID != challengeTwo.ID {
		t.Fatalf("challenge neighbors = %#v", first.Roadmap.ChallengeNeighbors)
	}
}

func catalogTestRevision(value rune) string {
	return "sha256:" + strings.Repeat(string(value), 64)
}
