package authoring

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/agentruntime"
)

func TestAuthoringToolsOwnStageRevisionAndRejectRetiredIntentVersion(t *testing.T) {
	client := &stageRuntimeClient{stage: Stage{StageRevision: 4}}
	conversation := &runtimeConversation{
		claim:  agentruntime.Claim{Run: agentruntime.Run{ID: "authoring-run", Purpose: "authoring"}, WorkItemID: "work-authoring-run", Attempt: 1, LeaseOwner: "lease"},
		client: client,
		stage:  client.stage,
	}

	if _, err := conversation.setMetadata(context.Background(), `{"title":"清理日志","description":"处理过期日志","difficulty":"easy","runtime":"node","reason":"明确题意","difficulty_impact":"难度不变"}`); err != nil {
		t.Fatalf("set metadata: %v", err)
	}
	if _, err := conversation.replaceOverview(context.Background(), `{"markdown":"清理过期日志。","reason":"补充说明","difficulty_impact":"难度不变"}`); err != nil {
		t.Fatalf("replace overview: %v", err)
	}
	if got, want := client.revisions, []int64{4, 5}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("stage revisions = %#v, want %#v", got, want)
	}
	if _, err := conversation.setMetadata(context.Background(), `{"intent_version":6,"title":"清理日志","description":"处理过期日志","difficulty":"easy","runtime":"node","reason":"明确题意","difficulty_impact":"难度不变"}`); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("retired intent_version error = %v, want strict unknown field rejection", err)
	}
}

type stageRuntimeClient struct {
	stage     Stage
	revisions []int64
}

func (c *stageRuntimeClient) LoadContext(context.Context, agentruntime.Claim) (ExecutionContext, error) {
	return ExecutionContext{}, errors.New("not implemented")
}

func (c *stageRuntimeClient) UpdateStage(_ context.Context, _ agentruntime.Claim, revision int64, plan Plan, _ Change) (Stage, error) {
	if revision != c.stage.StageRevision {
		return Stage{}, errors.New("unexpected stage revision")
	}
	c.revisions = append(c.revisions, revision)
	c.stage.StageRevision++
	c.stage.Plan = plan
	return c.stage, nil
}

func (c *stageRuntimeClient) Finalize(context.Context, agentruntime.Claim, string) error {
	return errors.New("not implemented")
}
