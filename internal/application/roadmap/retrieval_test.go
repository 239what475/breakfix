package roadmap

import (
	"context"
	"testing"

	domain "github.com/breakfix/breakfix/internal/domain/roadmap"
)

func TestRetrievalPinsSearchAndReadsToOneRevision(t *testing.T) {
	revision := domain.Revision{
		Revision: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Domains: []domain.Domain{{
			ID: domain.RuntimeID(domain.KindDomain, "linux-operations"), SourceRef: "linux-operations", Title: "Linux 运维",
			Definition: "Linux 系统与网络运维。", Scope: "主机服务", NonGoals: "Kubernetes 控制平面。",
		}},
		Topics: []domain.Topic{
			{
				ID: domain.RuntimeID(domain.KindTopic, "linux-operations/systemd"), SourceRef: "linux-operations/systemd", Title: "Systemd 服务管理",
				Domain:     domain.Ref{ID: domain.RuntimeID(domain.KindDomain, "linux-operations"), SourceRef: "linux-operations", Title: "Linux 运维"},
				Definition: "诊断 systemd 单元、依赖关系与服务恢复。", Scope: "systemctl 和 journalctl。", NonGoals: "容器编排。", ChallengeGuidance: "题目应验证服务恢复后的可观察状态。",
			},
			{
				ID: domain.RuntimeID(domain.KindTopic, "linux-operations/dns"), SourceRef: "linux-operations/dns", Title: "DNS 与名称解析",
				Domain:     domain.Ref{ID: domain.RuntimeID(domain.KindDomain, "linux-operations"), SourceRef: "linux-operations", Title: "Linux 运维"},
				Definition: "诊断 resolver、DNS 记录与名称解析路径。", Scope: "主机 DNS。", NonGoals: "服务网格。", ChallengeGuidance: "题目应从查询和配置开始。",
			},
		},
		Tags: []domain.Tag{
			{ID: domain.RuntimeID(domain.KindTag, "systemd"), SourceRef: "systemd", Title: "systemd", Description: "题目实质依赖 systemd 服务或日志诊断。"},
			{ID: domain.RuntimeID(domain.KindTag, "linux"), SourceRef: "linux", Title: "Linux", Description: "题目在 Linux 主机场景中完成。"},
		},
		ChallengeBindings: []domain.ChallengeBinding{},
		TopicEdges:        []domain.Edge{},
		ChallengeEdges:    []domain.Edge{},
	}
	retrieval, err := NewRetrieval(revision)
	if err != nil {
		t.Fatalf("new retrieval: %v", err)
	}
	if retrieval.Revision() != revision.Revision {
		t.Fatalf("revision = %q, want %q", retrieval.Revision(), revision.Revision)
	}
	topics, err := retrieval.SearchTopics(TopicSearch{Query: "systemd 服务", Limit: 5})
	if err != nil {
		t.Fatalf("search topics: %v", err)
	}
	if len(topics) == 0 || topics[0].ID != domain.RuntimeID(domain.KindTopic, "linux-operations/systemd") || topics[0].Domain.ID != domain.RuntimeID(domain.KindDomain, "linux-operations") {
		t.Fatalf("topic search = %#v", topics)
	}
	readTopic, err := retrieval.ReadTopic(topics[0].ID)
	if err != nil {
		t.Fatalf("read topic: %v", err)
	}
	if readTopic.Definition == "" || readTopic.ChallengeGuidance == "" {
		t.Fatalf("topic read omitted definition: %#v", readTopic)
	}
	tags, err := retrieval.SearchTags(TagSearch{Query: "systemd", Limit: 5})
	if err != nil {
		t.Fatalf("search tags: %v", err)
	}
	if len(tags) == 0 || tags[0].ID != domain.RuntimeID(domain.KindTag, "systemd") {
		t.Fatalf("tag search = %#v", tags)
	}
	if _, err := retrieval.ReadTag(tags[0].ID); err != nil {
		t.Fatalf("read tag: %v", err)
	}
}

func TestPlannerRetrievalSearchesAndReadsOtherChallengesAtItsFixedRevision(t *testing.T) {
	revision := domain.Revision{
		Revision: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Domains: []domain.Domain{{
			ID: domain.RuntimeID(domain.KindDomain, "linux-operations"), SourceRef: "linux-operations", Title: "Linux 运维",
			Definition: "Linux 系统与网络运维。", Scope: "主机服务", NonGoals: "Kubernetes 控制平面。",
		}},
		Topics: []domain.Topic{{
			ID: domain.RuntimeID(domain.KindTopic, "linux-operations/systemd"), SourceRef: "linux-operations/systemd", Title: "Systemd 服务管理",
			Domain:     domain.Ref{ID: domain.RuntimeID(domain.KindDomain, "linux-operations"), SourceRef: "linux-operations", Title: "Linux 运维"},
			Definition: "诊断 systemd 单元、依赖关系与服务恢复。", Scope: "systemctl 和 journalctl。", NonGoals: "容器编排。", ChallengeGuidance: "题目应验证服务恢复后的可观察状态。",
		}},
		Tags: []domain.Tag{{ID: domain.RuntimeID(domain.KindTag, "linux"), SourceRef: "linux", Title: "Linux", Description: "题目在 Linux 主机场景中完成。"}},
		ChallengeBindings: []domain.ChallengeBinding{
			{Challenge: domain.ChallengeRef{ID: "challenge-systemd-failed", RevisionID: "chrev-aaaaaaaaaaaaaaaa", SourceRef: "linux-operations/systemd/failed-service", Title: "修复失败的 systemd 服务", ContentRevision: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SourceSlug: "failed-systemd-service", MaterializedRevision: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, Topic: domain.Ref{ID: domain.RuntimeID(domain.KindTopic, "linux-operations/systemd"), SourceRef: "linux-operations/systemd", Title: "Systemd 服务管理"}, Tags: []domain.Ref{{ID: domain.RuntimeID(domain.KindTag, "linux"), SourceRef: "linux", Title: "Linux"}}},
			{Challenge: domain.ChallengeRef{ID: "challenge-systemd-logs", RevisionID: "chrev-cccccccccccccccc", SourceRef: "linux-operations/systemd/journal-diagnosis", Title: "使用 journalctl 诊断服务", ContentRevision: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", SourceSlug: "systemd-journal-diagnosis", MaterializedRevision: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}, Topic: domain.Ref{ID: domain.RuntimeID(domain.KindTopic, "linux-operations/systemd"), SourceRef: "linux-operations/systemd", Title: "Systemd 服务管理"}, Tags: []domain.Ref{{ID: domain.RuntimeID(domain.KindTag, "linux"), SourceRef: "linux", Title: "Linux"}}},
		},
		TopicEdges:     []domain.Edge{},
		ChallengeEdges: []domain.Edge{},
	}
	subject := domain.Subject{Ref: domain.Ref{ID: "challenge-systemd-failed", SourceRef: "linux-operations/systemd/failed-service", Title: "修复失败的 systemd 服务"}, ContentRevision: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	reader := &plannerChallengeReader{}
	retrieval, err := NewPlannerRetrieval(revision, domain.TaskChallenge, subject, reader)
	if err != nil {
		t.Fatalf("create planner retrieval: %v", err)
	}
	matches, err := retrieval.SearchChallenges(ChallengeSearch{Query: "journalctl 服务", Limit: 5})
	if err != nil {
		t.Fatalf("search other challenges: %v", err)
	}
	if len(matches) != 1 || matches[0].ID != "challenge-systemd-logs" {
		t.Fatalf("challenge matches = %#v", matches)
	}
	detail, err := retrieval.ReadChallenge(context.Background(), matches[0].ID)
	if err != nil {
		t.Fatalf("read matched challenge: %v", err)
	}
	if detail.Challenge.ID != matches[0].ID || detail.Problem == "" || detail.Solution == "" || reader.binding.Challenge.ID != matches[0].ID {
		t.Fatalf("challenge detail = %#v, reader binding = %#v", detail, reader.binding)
	}
}

type plannerChallengeReader struct {
	binding domain.ChallengeBinding
}

func (r *plannerChallengeReader) ReadRoadmapChallenge(_ context.Context, binding domain.ChallengeBinding) (ChallengeContent, error) {
	r.binding = binding
	return ChallengeContent{
		Runtime: "node", Difficulty: "intermediate", Description: "通过日志定位 systemd 服务故障。",
		Problem: "服务启动失败，请使用 journalctl 找到根因。", Solution: "检查 unit 和日志后修复配置。",
		Checkpoints: []ChallengeCheckpoint{{ID: "service-active", Title: "服务已恢复", Description: "systemd 服务处于 active 状态。"}},
	}, nil
}
