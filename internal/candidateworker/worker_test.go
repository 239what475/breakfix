package candidateworker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/candidate"
	"github.com/breakfix/breakfix/internal/worklist"
)

func TestRunRetriesClaimFailureWithoutStoppingWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &recoveringStore{cancel: cancel}
	worker, err := New(store, map[worklist.Kind]Executor{
		worklist.KindBuild: ExecutorFunc(func(context.Context, Claim) error { return nil }),
	}, Config{WorkerID: "worker-one", Kinds: []worklist.Kind{worklist.KindBuild}, PollEvery: time.Millisecond})
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

func (s *recoveringStore) Claim(context.Context, worklist.Kind, string, time.Duration) (*Claim, error) {
	if s.claimCalls.Add(1) == 1 {
		return nil, errors.New("server temporarily unavailable")
	}
	s.cancel()
	return nil, nil
}

func (*recoveringStore) Renew(context.Context, Claim, time.Duration) error { return nil }

func (*recoveringStore) Requeue(context.Context, Claim, time.Time, string, string) error { return nil }

func (*recoveringStore) FailArtifact(context.Context, Claim, candidate.Failure, *candidate.VerificationReport) error {
	return nil
}
