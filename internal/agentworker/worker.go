// Package agentworker executes durable Agent Runs. It owns no domain state:
// handlers use Server APIs for domain tools and finalization, while this
// package directly touches only the generic agent runtime repository.
package agentworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
)

type DeltaSink interface {
	EmitDelta(context.Context, agentruntime.Claim, string) error
	EmitTool(context.Context, agentruntime.Claim, string) error
	EmitReset(context.Context, agentruntime.Claim) error
}

type NopDeltaSink struct{}

func (NopDeltaSink) EmitDelta(context.Context, agentruntime.Claim, string) error { return nil }
func (NopDeltaSink) EmitTool(context.Context, agentruntime.Claim, string) error  { return nil }
func (NopDeltaSink) EmitReset(context.Context, agentruntime.Claim) error         { return nil }

type ExecutionResult struct {
	// Message is used by read-only runs. Worker stores this final message and
	// transitions the Run to succeeded in one agent_* transaction.
	Message *agentruntime.Message
	// Finalized means a Server domain-finalize API atomically committed both
	// domain state and Agent Run completion. It cannot be combined with Message.
	Finalized bool
}

// TerminalError marks an executor failure that must end the current logical
// Run rather than use the generic attempt requeue. Domains with their own
// durable failure budget, such as the Taxonomy committee, use this after their
// model-level transport retry has been exhausted.
type TerminalError struct{ cause error }

func (e *TerminalError) Error() string { return e.cause.Error() }
func (e *TerminalError) Unwrap() error { return e.cause }

func Terminal(err error) error {
	if err == nil {
		return nil
	}
	var terminal *TerminalError
	if errors.As(err, &terminal) {
		return err
	}
	return &TerminalError{cause: err}
}

func isTerminal(err error) bool {
	var terminal *TerminalError
	return errors.As(err, &terminal)
}

type Executor interface {
	Execute(context.Context, agentruntime.Claim, Emitter) (ExecutionResult, error)
}

type ExecutorFunc func(context.Context, agentruntime.Claim, Emitter) (ExecutionResult, error)

func (f ExecutorFunc) Execute(ctx context.Context, claim agentruntime.Claim, sink Emitter) (ExecutionResult, error) {
	return f(ctx, claim, sink)
}

type Worker struct {
	store     agentruntime.Repository
	executors map[string]Executor
	sink      DeltaSink
	workerID  string
	leaseTTL  time.Duration
	pollEvery time.Duration
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
}

type Config struct {
	WorkerID  string
	LeaseTTL  time.Duration
	PollEvery time.Duration
}

func New(store agentruntime.Repository, executors map[string]Executor, sink DeltaSink, config Config) (*Worker, error) {
	if store == nil || strings.TrimSpace(config.WorkerID) == "" {
		return nil, errors.New("agent worker requires runtime store and worker id")
	}
	if config.LeaseTTL <= 0 {
		config.LeaseTTL = 45 * time.Second
	}
	if config.PollEvery <= 0 {
		config.PollEvery = time.Second
	}
	if sink == nil {
		sink = NopDeltaSink{}
	}
	return &Worker{
		store:     store,
		executors: cloneExecutors(executors),
		sink:      sink,
		workerID:  config.WorkerID,
		leaseTTL:  config.LeaseTTL,
		pollEvery: config.PollEvery,
		now:       func() time.Time { return time.Now().UTC() },
		sleep:     sleepContext,
	}, nil
}

func (w *Worker) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		claimed, err := w.ProcessOne(ctx)
		if err != nil {
			return err
		}
		if claimed {
			continue
		}
		if err := w.sleep(ctx, w.pollEvery); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return err
		}
	}
}

// ProcessOne claims and executes at most one Run. A returned error means the
// worker loop could not continue; ordinary model, tool, and protocol failures
// are persisted as a scheduled retry and do not crash the process.
func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	now := w.now()
	claim, err := w.store.ClaimNext(ctx, w.workerID, w.leaseTTL, now)
	if err != nil {
		return false, fmt.Errorf("claim agent run: %w", err)
	}
	if claim == nil {
		return false, nil
	}
	w.processClaim(ctx, *claim)
	return true, nil
}

func (w *Worker) processClaim(parent context.Context, claim agentruntime.Claim) {
	executor := w.executors[claim.Run.Purpose]
	if executor == nil {
		w.requeueOrFail(parent, claim, fmt.Errorf("no executor registered for agent run purpose %q", claim.Run.Purpose))
		return
	}
	execCtx, cancel := context.WithDeadline(parent, claim.Run.DeadlineAt)
	defer cancel()

	done := make(chan struct{})
	var leaseLost atomic.Bool
	go w.renewLease(execCtx, claim, cancel, done, &leaseLost)
	result, err := executor.Execute(execCtx, claim, deltaSink{sink: w.sink, claim: claim})
	close(done)
	cancel()
	if err != nil {
		if leaseLost.Load() || parent.Err() != nil {
			return
		}
		if isTerminal(err) {
			w.fail(parent, claim, err)
			return
		}
		w.requeueOrFail(parent, claim, err)
		return
	}
	if result.Finalized {
		if result.Message != nil {
			w.requeueOrFail(parent, claim, errors.New("finalized execution also returned a message"))
			return
		}
		run, err := w.store.GetRun(parent, claim.Run.ID)
		if err != nil || run.Status != agentruntime.RunSucceeded {
			w.requeueOrFail(parent, claim, fmt.Errorf("domain finalize did not complete agent run: %w", err))
		}
		return
	}
	if result.Message == nil {
		w.requeueOrFail(parent, claim, errors.New("executor returned no final message"))
		return
	}
	if err := w.store.CompleteWithMessage(parent, claim, *result.Message, w.now()); err != nil && !errors.Is(err, agentruntime.ErrLeaseLost) {
		w.requeueOrFail(parent, claim, fmt.Errorf("persist final message: %w", err))
	}
}

func (w *Worker) fail(ctx context.Context, claim agentruntime.Claim, executionErr error) {
	message := strings.TrimSpace(executionErr.Error())
	if message == "" {
		message = "agent execution failed"
	}
	if err := w.store.Fail(ctx, claim, message, w.now()); err != nil && !errors.Is(err, agentruntime.ErrLeaseLost) {
		slog.Error("fail terminal agent run", "run_id", claim.Run.ID, "attempt", claim.Run.Attempt, "error_class", errorClass(executionErr))
	}
}

func (w *Worker) renewLease(ctx context.Context, claim agentruntime.Claim, cancel context.CancelFunc, done <-chan struct{}, leaseLost *atomic.Bool) {
	interval := w.leaseTTL / 3
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			if err := w.store.RenewLease(context.Background(), claim, w.leaseTTL, w.now()); err != nil {
				if errors.Is(err, agentruntime.ErrLeaseLost) && w.completedByDomain(claim) {
					return
				}
				// A domain finalize can complete the Run while a ticker event is
				// already selectable. Completion wins over this stale renewal.
				select {
				case <-done:
					return
				default:
				}
				slog.Warn("agent run lease renewal failed", "run_id", claim.Run.ID, "attempt", claim.Run.Attempt, "error_class", errorClass(err))
				leaseLost.Store(true)
				cancel()
				return
			}
		}
	}
}

func (w *Worker) completedByDomain(claim agentruntime.Claim) bool {
	run, err := w.store.GetRun(context.Background(), claim.Run.ID)
	return err == nil && run.Status == agentruntime.RunSucceeded
}

func (w *Worker) requeueOrFail(ctx context.Context, claim agentruntime.Claim, executionErr error) {
	message := strings.TrimSpace(executionErr.Error())
	if message == "" {
		message = "agent execution failed"
	}
	now := w.now()
	if !now.Before(claim.Run.DeadlineAt) {
		if err := w.store.Fail(ctx, claim, message, now); err != nil && !errors.Is(err, agentruntime.ErrLeaseLost) {
			slog.Error("fail expired agent run", "run_id", claim.Run.ID, "attempt", claim.Run.Attempt, "error_class", errorClass(err))
		}
		return
	}
	next := now.Add(retryDelay(claim.Run.Attempt))
	if next.After(claim.Run.DeadlineAt) {
		next = claim.Run.DeadlineAt
	}
	if err := w.store.Requeue(ctx, claim, next, message, now); err != nil && !errors.Is(err, agentruntime.ErrLeaseLost) {
		slog.Error("requeue agent run", "run_id", claim.Run.ID, "attempt", claim.Run.Attempt, "error_class", errorClass(err))
	}
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second * time.Duration(1<<(min(attempt-1, 6)))
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func cloneExecutors(executors map[string]Executor) map[string]Executor {
	result := make(map[string]Executor, len(executors))
	for purpose, executor := range executors {
		if strings.TrimSpace(purpose) != "" && executor != nil {
			result[purpose] = executor
		}
	}
	return result
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func errorClass(err error) string {
	if errors.Is(err, agentruntime.ErrLeaseLost) {
		return "lease_lost"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "execution"
}

type deltaSink struct {
	sink  DeltaSink
	claim agentruntime.Claim
}

func (s deltaSink) EmitDelta(ctx context.Context, content string) {
	if strings.TrimSpace(content) == "" {
		return
	}
	// Streaming is intentionally best-effort. The model execution and durable
	// final message must proceed when Server/browser subscribers disappear.
	_ = s.sink.EmitDelta(ctx, s.claim, content)
}

func (s deltaSink) EmitTool(ctx context.Context, name string) {
	if strings.TrimSpace(name) == "" {
		return
	}
	_ = s.sink.EmitTool(ctx, s.claim, name)
}

func (s deltaSink) EmitReset(ctx context.Context) {
	// A retry invalidates only a transient browser draft. It must not affect the
	// durable Run, message history, or the model execution itself.
	_ = s.sink.EmitReset(ctx, s.claim)
}

// Emitter is what executors receive. It deliberately has no read API and is
// never persisted as an Agent event stream.
type Emitter interface {
	EmitDelta(context.Context, string)
	EmitTool(context.Context, string)
	EmitReset(context.Context)
}
