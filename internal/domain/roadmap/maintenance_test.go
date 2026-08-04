package roadmap_test

import (
	"testing"

	"github.com/breakfix/breakfix/internal/domain/roadmap"
	roadmaptest "github.com/breakfix/breakfix/internal/testkit/roadmap"
)

func TestMergeEdgesMakesReversePrecedesRelated(t *testing.T) {
	first := testRef("topic-a", "domain/topic-a", "主题 A")
	second := testRef("topic-b", "domain/topic-b", "主题 B")

	merged, audits, err := roadmap.MergeEdges([]roadmap.Edge{{
		Source: first, Target: second, Relation: roadmap.RelationPrecedes, Reason: "A 是 B 的前置主题。",
	}}, []roadmap.TaskChangeSet{{
		TaskID: "task-b", SnapshotOrder: 1, ChangeSet: roadmap.ChangeSet{Edges: []roadmap.Edge{{
			Source: second, Target: first, Relation: roadmap.RelationPrecedes, Reason: "B 与 A 需要双向关联。",
		}}},
	}})
	if err != nil {
		t.Fatalf("merge edges: %v", err)
	}
	if len(merged) != 1 || merged[0].Relation != roadmap.RelationRelated || merged[0].Source != first || merged[0].Target != second {
		t.Fatalf("merged edges = %#v", merged)
	}
	if !hasOutcome(audits, "converted_related") {
		t.Fatalf("merge audits do not record reverse conversion: %#v", audits)
	}
}

func TestMergeEdgesAuditsRelatedReplacingExistingPrecedes(t *testing.T) {
	first := testRef("topic-a", "domain/topic-a", "主题 A")
	second := testRef("topic-b", "domain/topic-b", "主题 B")

	merged, audits, err := roadmap.MergeEdges([]roadmap.Edge{{
		Source: first, Target: second, Relation: roadmap.RelationPrecedes, Reason: "A 是 B 的前置主题。",
	}}, []roadmap.TaskChangeSet{{
		TaskID: "task-related", SnapshotOrder: 1, ChangeSet: roadmap.ChangeSet{Edges: []roadmap.Edge{{
			Source: first, Target: second, Relation: roadmap.RelationRelated, Reason: "两者应横向对照学习。",
		}}},
	}})
	if err != nil {
		t.Fatalf("merge edges: %v", err)
	}
	if len(merged) != 1 || merged[0].Relation != roadmap.RelationRelated {
		t.Fatalf("merged edges = %#v", merged)
	}
	if !hasOutcome(audits, "related_precedence") || !hasOutcome(audits, "accepted_related") {
		t.Fatalf("merge audits = %#v", audits)
	}
}

func TestMergeEdgesKeepsRelatedAndSkipsCyclesInStableOrder(t *testing.T) {
	first := testRef("topic-a", "domain/topic-a", "主题 A")
	second := testRef("topic-b", "domain/topic-b", "主题 B")
	third := testRef("topic-c", "domain/topic-c", "主题 C")
	fourth := testRef("topic-d", "domain/topic-d", "主题 D")

	merged, audits, err := roadmap.MergeEdges([]roadmap.Edge{
		{Source: first, Target: second, Relation: roadmap.RelationPrecedes, Reason: "A 在 B 之前。"},
		{Source: second, Target: third, Relation: roadmap.RelationPrecedes, Reason: "B 在 C 之前。"},
	}, []roadmap.TaskChangeSet{
		{TaskID: "task-c", SnapshotOrder: 2, ChangeSet: roadmap.ChangeSet{Edges: []roadmap.Edge{{
			Source: third, Target: first, Relation: roadmap.RelationPrecedes, Reason: "候选会形成环。",
		}}}},
		{TaskID: "task-a", SnapshotOrder: 1, ChangeSet: roadmap.ChangeSet{Edges: []roadmap.Edge{{
			Source: fourth, Target: third, Relation: roadmap.RelationRelated, Reason: "C 与 D 需要横向对照。",
		}}}},
	})
	if err != nil {
		t.Fatalf("merge edges: %v", err)
	}
	if len(merged) != 3 || merged[0].Relation != roadmap.RelationPrecedes || merged[1].Relation != roadmap.RelationPrecedes || merged[2].Relation != roadmap.RelationRelated {
		t.Fatalf("merged edges = %#v", merged)
	}
	if merged[0].Source != first || merged[0].Target != second || merged[1].Source != second || merged[1].Target != third || merged[2].Source != third || merged[2].Target != fourth {
		t.Fatalf("merged edge ordering = %#v", merged)
	}
	if !hasOutcome(audits, "accepted_related") || !hasOutcome(audits, "ignored_cycle") {
		t.Fatalf("merge audits = %#v", audits)
	}
}

func TestChangeSetUsesOnlyItsFixedSubjectAndRevision(t *testing.T) {
	revision := roadmaptest.RuntimeRevision()
	revision.Revision = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	subject := roadmap.Subject{
		Ref:             roadmap.Ref{ID: "challenge-node-runtime", SourceRef: "platform-runtime/node-environment-validation/node-runtime-fixture", Title: "节点运行时验证"},
		ContentRevision: roadmaptest.NodeContentRevision,
	}
	other := roadmap.Ref{ID: "challenge-systemd-recovery", SourceRef: "platform-runtime/systemd-service-recovery/systemd-service-recovery", Title: "修复 systemd 服务"}
	changes := roadmap.ChangeSet{Edges: []roadmap.Edge{{
		Source: subject.Ref, Target: other, Relation: roadmap.RelationRelated, Reason: "两题都需要检查运行时状态。",
	}}}
	if err := changes.ValidateFor(roadmap.TaskChallenge, subject, revision); err != nil {
		t.Fatalf("validate task changeset: %v", err)
	}
}

func testRef(id, sourceRef, title string) roadmap.Ref {
	return roadmap.Ref{ID: id, SourceRef: sourceRef, Title: title}
}

func hasOutcome(values []roadmap.MergeAudit, want string) bool {
	for _, value := range values {
		if value.Outcome == want {
			return true
		}
	}
	return false
}
