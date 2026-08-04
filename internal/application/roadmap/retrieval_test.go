package roadmap

import (
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
