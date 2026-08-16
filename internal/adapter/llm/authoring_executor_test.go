package llm

import (
	"context"
	"math"
	"testing"
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
