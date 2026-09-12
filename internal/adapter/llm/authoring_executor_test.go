package llm

import (
	"context"
	"math"
	"testing"

	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/authoring"
)

func TestAuthoringModelRetryConfigUsesDeadlineBoundTransportRetries(t *testing.T) {
	if authoringMaxIterations != math.MaxInt {
		t.Fatalf("authoring max iterations = %d, want %d", authoringMaxIterations, math.MaxInt)
	}
	config := authoringModelRetryConfig()
	if config.MaxRetries != math.MaxInt {
		t.Fatalf("authoring model max retries = %d, want %d", config.MaxRetries, math.MaxInt)
	}
	if config.IsRetryAble == nil || !config.IsRetryAble(context.Background(), context.DeadlineExceeded) {
		t.Fatal("active context did not retry a transient transport failure")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if config.IsRetryAble(ctx, context.DeadlineExceeded) {
		t.Fatal("cancelled context scheduled another model retry")
	}
}

func TestAuthoringInputsExcludePlatformEvents(t *testing.T) {
	conversation := &runtimeConversation{stage: authoring.Stage{BaseRevision: 2}}
	inputs, err := authoringInputs(conversation, []agent.Message{
		{Role: "user", Content: "先设计现场说明"},
		{Role: "assistant", Content: "已经记录第一版现场说明"},
		{Role: "event", Content: `{"kind":"authoring_run_interrupted"}`},
		{Role: "user", Content: "继续完成生成"},
	})
	if err != nil {
		t.Fatalf("build authoring inputs: %v", err)
	}
	if len(inputs) != 3 {
		t.Fatalf("authoring input count = %d, want 3", len(inputs))
	}
}
