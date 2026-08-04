// Package roadmaptest provides a stable positive Roadmap fixture for adapter
// and application tests. It represents portable content, not a user-facing
// catalog release.
package roadmaptest

import (
	roadmap "github.com/breakfix/breakfix/internal/domain/roadmap"
)

const (
	NodeChallengePath      = "challenges/node-runtime-fixture"
	SystemdChallengePath   = "challenges/systemd-service-recovery"
	NodeContentRevision    = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	SystemdContentRevision = "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
)

func PortableRevision() roadmap.PortableRevision {
	domain := roadmap.PortableRef{SourceRef: "platform-runtime", Title: "平台运行时"}
	nodeTopic := roadmap.PortableRef{SourceRef: "platform-runtime/node-environment-validation", Title: "节点环境验证"}
	systemdTopic := roadmap.PortableRef{SourceRef: "platform-runtime/systemd-service-recovery", Title: "systemd 服务恢复"}
	tag := roadmap.PortableRef{SourceRef: "runtime-fixture", Title: "运行时验证"}
	return roadmap.PortableRevision{
		Domains: []roadmap.PortableDomain{{
			Kind: roadmap.KindDomain, SourceRef: domain.SourceRef, Title: domain.Title,
			Definition: "面向学习环境运行时与节点行为的运维课程边界。", Scope: "节点环境、服务管理和验证脚本。", NonGoals: "不涵盖 Kubernetes 控制平面实现。",
		}},
		Topics: []roadmap.PortableTopic{
			{Kind: roadmap.KindTopic, SourceRef: nodeTopic.SourceRef, Title: nodeTopic.Title, Domain: domain, Definition: "验证节点环境中可观察的任务完成状态。", Scope: "节点、检查点和运行时初始化。", NonGoals: "不讨论服务管理策略。", ChallengeGuidance: "题目唯一归属此主题，当重点是节点环境与检查点的可观察状态。"},
			{Kind: roadmap.KindTopic, SourceRef: systemdTopic.SourceRef, Title: systemdTopic.Title, Domain: domain, Definition: "诊断并恢复由 systemd 管理的服务。", Scope: "unit 状态、日志和服务重启。", NonGoals: "不覆盖容器编排。", ChallengeGuidance: "题目唯一归属此主题，当修复目标是 systemd 管理的服务状态。"},
		},
		Tags: []roadmap.PortableTag{{Kind: roadmap.KindTag, SourceRef: tag.SourceRef, Title: tag.Title, Description: "任务依赖真实运行时行为和自动检查点验证。"}},
		ChallengeBindings: []roadmap.PortableChallengeBinding{
			{Kind: roadmap.KindChallenge, Challenge: roadmap.PortableChallengeRef{Path: NodeChallengePath, SourceRef: "platform-runtime/node-environment-validation/node-runtime-fixture", Title: "节点运行时验证", ContentRevision: NodeContentRevision}, Topic: nodeTopic, Tags: []roadmap.PortableRef{tag}},
			{Kind: roadmap.KindChallenge, Challenge: roadmap.PortableChallengeRef{Path: SystemdChallengePath, SourceRef: "platform-runtime/systemd-service-recovery/systemd-service-recovery", Title: "修复 systemd 服务", ContentRevision: SystemdContentRevision}, Topic: systemdTopic, Tags: []roadmap.PortableRef{tag}},
		},
		TopicEdges:     []roadmap.PortableEdge{{Source: nodeTopic, Target: systemdTopic, Relation: roadmap.RelationPrecedes, Reason: "服务恢复题依赖对节点环境与检查点状态的基本理解。"}},
		ChallengeEdges: []roadmap.PortableEdge{{Source: roadmap.PortableRef{SourceRef: "platform-runtime/node-environment-validation/node-runtime-fixture", Title: "节点运行时验证"}, Target: roadmap.PortableRef{SourceRef: "platform-runtime/systemd-service-recovery/systemd-service-recovery", Title: "修复 systemd 服务"}, Relation: roadmap.RelationRelated, Reason: "两题都使用同一运行时验证链路。"}},
	}
}

func RuntimeRevision() roadmap.Revision {
	value, err := roadmap.CompilePortable(PortableRevision(), map[string]roadmap.ChallengeRef{
		NodeChallengePath:    {ID: "challenge-node-runtime", SourceRef: "platform-runtime/node-environment-validation/node-runtime-fixture", Title: "节点运行时验证", ContentRevision: NodeContentRevision},
		SystemdChallengePath: {ID: "challenge-systemd-recovery", SourceRef: "platform-runtime/systemd-service-recovery/systemd-service-recovery", Title: "修复 systemd 服务", ContentRevision: SystemdContentRevision},
	})
	if err != nil {
		panic(err)
	}
	return value
}
