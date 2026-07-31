package agentworker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
)

func TestRunRetriesClaimFailureWithoutStoppingWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &recoveringStore{cancel: cancel}
	worker, err := New(store, nil, nil, Config{WorkerID: "worker-one", PollEvery: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Run(ctx); err != nil {
		t.Fatalf("run = %v, want nil after shutdown", err)
	}
	if calls := store.claimCalls.Load(); calls != 2 {
		t.Fatalf("claim calls = %d, want retry after one transient failure", calls)
	}
}

type recoveringStore struct {
	cancel     context.CancelFunc
	claimCalls atomic.Int32
}

func (s *recoveringStore) ClaimNext(context.Context, string, time.Duration, time.Time) (*agentruntime.Claim, error) {
	if s.claimCalls.Add(1) == 1 {
		return nil, errors.New("server temporarily unavailable")
	}
	s.cancel()
	return nil, nil
}

func (*recoveringStore) RenewLease(context.Context, agentruntime.Claim, time.Duration, time.Time) error {
	return nil
}

func (*recoveringStore) Requeue(context.Context, agentruntime.Claim, time.Time, string, time.Time) error {
	return nil
}

func (*recoveringStore) CompleteWithMessage(context.Context, agentruntime.Claim, agentruntime.Message, time.Time) error {
	return nil
}

func (*recoveringStore) Fail(context.Context, agentruntime.Claim, string, time.Time) error {
	return nil
}

func (*recoveringStore) GetRun(context.Context, string) (*agentruntime.Run, error) {
	return nil, nil
}
