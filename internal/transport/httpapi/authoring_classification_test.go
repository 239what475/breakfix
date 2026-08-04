package httpapi

import (
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/roadmap"
)

func TestAuthoringClassificationProposalProjectsExistingDefinitions(t *testing.T) {
	domain := roadmap.Ref{ID: "domain-linux", SourceRef: "linux-operations", Title: "Linux 运维"}
	topic := roadmap.Topic{
		ID: "topic-systemd", SourceRef: "linux-operations/systemd-service-recovery", Title: "systemd 服务恢复", Domain: domain,
		Definition: "诊断并恢复由 systemd 管理的服务。", Scope: "unit 状态、日志和服务重启。",
		NonGoals: "不覆盖容器编排。", ChallengeGuidance: "修复目标是 systemd 管理的服务状态时归属此 Topic。",
	}
	tag := roadmap.Tag{ID: "tag-linux", SourceRef: "linux", Title: "Linux", Description: "Linux 主机场景。"}
	revision := &roadmap.Revision{Revision: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Topics: []roadmap.Topic{topic}, Tags: []roadmap.Tag{tag}}
	proposal := &generation.ClassificationProposal{
		Revision: 1, CandidateRevisionID: "candidate-one", RoadmapRevision: revision.Revision, Result: generation.ClassificationProposed,
		Topic:     &generation.TopicProposal{Existing: &roadmap.Ref{ID: topic.ID, SourceRef: topic.SourceRef, Title: topic.Title}, Reason: "题目的核心目标是恢复 systemd 服务。"},
		Tags:      []generation.TagProposal{{Existing: &roadmap.Ref{ID: tag.ID, SourceRef: tag.SourceRef, Title: tag.Title}, Reason: "题目在 Linux 主机场景中完成。"}},
		UpdatedAt: time.Date(2026, time.August, 4, 12, 0, 0, 0, time.UTC),
	}

	projected, err := toAPIAuthoringClassificationProposal(proposal, revision)
	if err != nil {
		t.Fatalf("project classification proposal: %v", err)
	}
	if projected.Topic == nil || projected.Topic.Existing == nil || projected.Topic.Existing.Definition != topic.Definition ||
		projected.Topic.Existing.Scope != topic.Scope || projected.Topic.Existing.NonGoals != topic.NonGoals ||
		projected.Topic.Existing.ChallengeGuidance != topic.ChallengeGuidance || projected.Topic.Existing.Domain != toAPIRoadmapReference(domain) {
		t.Fatalf("existing topic projection = %#v", projected.Topic)
	}
	if len(projected.Tags) != 1 || projected.Tags[0].Existing == nil || projected.Tags[0].Existing.Description != tag.Description || projected.Tags[0].Reason != proposal.Tags[0].Reason {
		t.Fatalf("existing tag projection = %#v", projected.Tags)
	}
}
