package agent

import (
	"context"
	"strings"
	"testing"

	roadmapapp "github.com/breakfix/breakfix/internal/application/roadmap"
	"github.com/breakfix/breakfix/internal/domain/generation"
	roadmapdomain "github.com/breakfix/breakfix/internal/domain/roadmap"
)

func TestClassificationConversationRequiresRetrievedExistingReferences(t *testing.T) {
	runtime := newClassifierRuntime()
	conversation := newClassificationConversation(runtime, classifierClaim())
	_, err := conversation.initialOutput(classificationInitialResult{
		Result: generation.ClassificationProposed,
		Topic:  &classificationTopicInput{ExistingID: runtime.topic.ID, Reason: "题目围绕 systemd 服务恢复。"},
	})
	if err == nil || !strings.Contains(err.Error(), "search_topics") {
		t.Fatalf("unretrieved topic error = %v", err)
	}
	if _, err := conversation.searchTopics(context.Background(), `{"query":"systemd","limit":5}`); err != nil {
		t.Fatalf("search topics: %v", err)
	}
	if _, err := conversation.searchTags(context.Background(), `{"query":"linux","limit":5}`); err != nil {
		t.Fatalf("search tags: %v", err)
	}
	output, err := conversation.initialOutput(classificationInitialResult{
		Result: generation.ClassificationProposed,
		Topic:  &classificationTopicInput{ExistingID: runtime.topic.ID, Reason: "题目围绕 systemd 服务恢复。"},
		Tags:   []classificationTagInput{{ExistingID: runtime.tag.ID, Reason: "题目在 Linux 主机场景中完成。"}},
	})
	if err != nil {
		t.Fatalf("build initial output: %v", err)
	}
	if output.Topic == nil || output.Topic.Existing == nil || *output.Topic.Existing != classifierTopicRef() || len(output.Tags) != 1 || output.Tags[0].Existing == nil || *output.Tags[0].Existing != classifierTagRef() {
		t.Fatalf("initial output = %#v", output)
	}
}

func TestClassificationConversationAdjustsOnlyItsPrivateProposal(t *testing.T) {
	runtime := newClassifierRuntime()
	conversation := newClassificationConversation(runtime, classifierClaim())
	conversation.seedProposal(generation.ClassificationProposal{
		Revision: 1, CandidateRevisionID: "candidate-one", RoadmapRevision: classifierRevision,
		Result: generation.ClassificationProposed,
		Topic:  &generation.TopicProposal{Existing: ptr(classifierTopicRef()), Reason: "初始 Topic。"},
		Tags:   []generation.TagProposal{{Existing: ptr(classifierTagRef()), Reason: "初始 Tag。"}},
	})
	if _, err := conversation.setTopic(context.Background(), `{"existing_id":"topic-systemd","reason":"题目目标是恢复 systemd 服务。"}`); err != nil {
		t.Fatalf("set topic: %v", err)
	}
	if _, err := conversation.setTags(context.Background(), `{"tags":[]}`); err != nil {
		t.Fatalf("set tags: %v", err)
	}
	adjustment, err := conversation.adjustment(classificationAdjustmentResult{ChangeScope: generation.ClassificationChangeClassification}, "移除没有筛选价值的标签")
	if err != nil {
		t.Fatalf("finalize adjustment: %v", err)
	}
	if adjustment.Output == nil || adjustment.Output.Topic == nil || adjustment.Output.Topic.Existing == nil || len(adjustment.Output.Tags) != 0 {
		t.Fatalf("adjustment = %#v", adjustment)
	}
	if runtime.topic.Title != "systemd 服务恢复" || runtime.tag.Description != "Linux 主机场景。" {
		t.Fatalf("classifier mutated runtime definitions: topic=%#v tag=%#v", runtime.topic, runtime.tag)
	}
}

func TestClassificationConversationRoutesContentOrClarifiesWithoutProposalMutation(t *testing.T) {
	conversation := newClassificationConversation(newClassifierRuntime(), classifierClaim())
	conversation.seedProposal(generation.ClassificationProposal{
		Revision: 1, CandidateRevisionID: "candidate-one", RoadmapRevision: classifierRevision,
		Result: generation.ClassificationProposed,
		Topic:  &generation.TopicProposal{Existing: ptr(classifierTopicRef()), Reason: "初始 Topic。"},
	})
	content, err := conversation.adjustment(classificationAdjustmentResult{ChangeScope: generation.ClassificationChangeContent}, "把故障现象改得更明确")
	if err != nil || content.ChangeScope != generation.ClassificationChangeContent || content.Output != nil {
		t.Fatalf("content adjustment = %#v, %v", content, err)
	}
	clarification, err := conversation.adjustment(classificationAdjustmentResult{ChangeScope: generation.ClassificationChangeClarify, Clarification: "请说明要修改题目内容还是分类。"}, "我想改一下")
	if err != nil || clarification.ChangeScope != generation.ClassificationChangeClarify || clarification.Output != nil {
		t.Fatalf("clarification adjustment = %#v, %v", clarification, err)
	}
}

const classifierRevision = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type classifierRuntime struct {
	topic roadmapdomain.Topic
	tag   roadmapdomain.Tag
}

func newClassifierRuntime() *classifierRuntime {
	domain := roadmapdomain.Ref{ID: "domain-linux", SourceRef: "linux-operations", Title: "Linux 运维"}
	return &classifierRuntime{
		topic: roadmapdomain.Topic{ID: "topic-systemd", SourceRef: "linux-operations/systemd-service-recovery", Title: "systemd 服务恢复", Domain: domain,
			Definition: "恢复由 systemd 管理的服务。", Scope: "unit 和 journal。", NonGoals: "容器编排。", ChallengeGuidance: "题目主目标是恢复服务时归属此 Topic。"},
		tag: roadmapdomain.Tag{ID: "tag-linux", SourceRef: "linux", Title: "Linux", Description: "Linux 主机场景。"},
	}
}

func (r *classifierRuntime) SearchClassificationTopics(_ context.Context, _ generation.Claim, _ roadmapapp.TopicSearch) ([]roadmapapp.TopicMatch, error) {
	return []roadmapapp.TopicMatch{{ID: r.topic.ID, SourceRef: r.topic.SourceRef, Title: r.topic.Title, Summary: r.topic.Definition, Domain: r.topic.Domain, MatchReason: "标题匹配"}}, nil
}

func (r *classifierRuntime) ReadClassificationTopic(_ context.Context, _ generation.Claim, id string) (*roadmapdomain.Topic, error) {
	if id != r.topic.ID {
		return nil, roadmapdomain.ErrNoCurrentRevision
	}
	value := r.topic
	return &value, nil
}

func (r *classifierRuntime) SearchClassificationTags(_ context.Context, _ generation.Claim, _ roadmapapp.TagSearch) ([]roadmapapp.TagMatch, error) {
	return []roadmapapp.TagMatch{{ID: r.tag.ID, SourceRef: r.tag.SourceRef, Title: r.tag.Title, Summary: r.tag.Description, MatchReason: "标题匹配"}}, nil
}

func (r *classifierRuntime) ReadClassificationTag(_ context.Context, _ generation.Claim, id string) (*roadmapdomain.Tag, error) {
	if id != r.tag.ID {
		return nil, roadmapdomain.ErrNoCurrentRevision
	}
	value := r.tag
	return &value, nil
}

func classifierClaim() generation.Claim {
	return generation.Claim{Workflow: generation.Workflow{
		ID: "workflow-one", Source: generation.Source{Kind: generation.SourceAuthoring, Ref: "authoring-one"}, SourceRevision: "1",
		State: generation.StateClassifying, ClassificationRoadmapRevision: classifierRevision, ActiveAgentRunID: "classifier-run", StateAttempt: 0, LeaseOwner: "lease-one",
	}, LeaseCredential: generation.LeaseCredential{StateAttempt: 0, LeaseOwner: "lease-one"}}
}

func classifierTopicRef() roadmapdomain.Ref {
	return roadmapdomain.Ref{ID: "topic-systemd", SourceRef: "linux-operations/systemd-service-recovery", Title: "systemd 服务恢复"}
}

func classifierTagRef() roadmapdomain.Ref {
	return roadmapdomain.Ref{ID: "tag-linux", SourceRef: "linux", Title: "Linux"}
}

func ptr[T any](value T) *T { return &value }
