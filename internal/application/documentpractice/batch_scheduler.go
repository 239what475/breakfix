package documentpractice

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
)

// batchSchedulerInterval matches the deployment's other background loops.
const batchSchedulerInterval = 2 * time.Second

// MaxSchedulerConcurrency caps the process-wide number of concurrent Agent
// chain ignitions. Per-batch throttles share this global budget.
const MaxSchedulerConcurrency = 8

// BatchScheduler tops up in-flight Agent chains to each Running batch's
// concurrency threshold, backfills item terminal states from the durable
// workflows, and closes batches whose items are all terminal. Every decision
// derives from the database, so a Server restart resumes the batches; only
// the ignition goroutines are process-local.
type BatchScheduler struct {
	service  *Service
	pipeline *AgentPipeline
	slots    chan struct{}
	OnTick   func(error)
	now      func() time.Time

	mu       sync.Mutex
	inFlight map[string]bool
}

func NewBatchScheduler(service *Service, pipeline *AgentPipeline) (*BatchScheduler, error) {
	if service == nil || pipeline == nil {
		return nil, errors.New("documentation batch scheduler requires the service and the Agent pipeline")
	}
	return &BatchScheduler{
		service:  service,
		pipeline: pipeline,
		slots:    make(chan struct{}, MaxSchedulerConcurrency),
		now:      func() time.Time { return time.Now().UTC() },
		inFlight: map[string]bool{},
	}, nil
}

// Run drives the scheduler until the process context is cancelled.
func (s *BatchScheduler) Run(ctx context.Context) error {
	if s == nil {
		return errors.New("documentation batch scheduler is not configured")
	}
	ticker := time.NewTicker(batchSchedulerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			err := s.tick(ctx)
			if s.OnTick != nil {
				s.OnTick(err)
			}
			if err != nil && ctx.Err() == nil {
				slog.Warn("documentation batch scheduler pass", "err", err)
			}
		}
	}
}

func (s *BatchScheduler) tick(ctx context.Context) error {
	active, err := s.service.store.ListSchedulerBatches(ctx, []domain.BatchState{domain.BatchPending, domain.BatchRunning, domain.BatchPaused, domain.BatchCancelled})
	if err != nil {
		return err
	}
	for _, batch := range active {
		// A declared batch starts by itself: after creation no client is
		// needed to keep the rollout moving.
		if batch.State == domain.BatchPending {
			won, err := s.service.store.TransitionBatchState(ctx, batch.ID, domain.BatchPending, domain.BatchRunning, nil, s.now())
			if err != nil {
				return err
			}
			if won {
				slog.Info("documentation batch started", "batch_id", batch.ID)
				batch.State = domain.BatchRunning
			}
		}
		if err := s.reconcileBatch(ctx, batch); err != nil {
			return err
		}
		if batch.State == domain.BatchRunning {
			if err := s.topUpBatch(ctx, batch); err != nil {
				return err
			}
		}
		if err := s.closeIfFinished(ctx, batch); err != nil {
			return err
		}
	}
	return nil
}

// reconcileBatch backfills in-flight items from their workflows: terminal
// workflows map onto the same item terminal, and workflows that left the
// Agent chain for the runtime phases promote their item to Running. A
// Scheduled item whose workflow vanished or sits in Planning with no live
// ignition is a crash leftover; it returns to Pending and re-ignites. A
// mid-chain leftover keeps its stuck workflow visible to the administrator
// instead of being silently failed.
func (s *BatchScheduler) reconcileBatch(ctx context.Context, batch domain.DocumentBatch) error {
	active, err := s.service.store.ActiveBatchItemWorkflowStates(ctx, batch.ID)
	if err != nil {
		return err
	}
	now := s.now()
	for _, activeItem := range active {
		item, workflowState := activeItem.Item, activeItem.WorkflowState
		if workflowState.Terminal() {
			if _, err := s.service.store.TransitionBatchItem(ctx, item.ID, item.State, itemStateForWorkflow(workflowState), "", now); err != nil {
				return err
			}
			continue
		}
		if s.live(item.ID) {
			continue
		}
		switch workflowState {
		case domain.MaterializingArtifact, domain.Verifying, domain.VerificationReviewing, domain.Publishing:
			if _, err := s.service.store.TransitionBatchItem(ctx, item.ID, item.State, domain.ItemRunning, "", now); err != nil {
				return err
			}
		case domain.Planning:
			// The ignition never ran (or the workflow was restarted): re-enqueue.
			if _, err := s.service.store.TransitionBatchItem(ctx, item.ID, item.State, domain.ItemPending, "requeued-after-restart", now); err != nil {
				return err
			}
		}
	}
	return nil
}

// topUpBatch claims Pending items until the batch's in-flight count reaches
// its concurrency threshold and ignites them on bounded goroutines.
func (s *BatchScheduler) topUpBatch(ctx context.Context, batch domain.DocumentBatch) error {
	inFlight, err := s.service.store.ListBatchItemsByStates(ctx, batch.ID, []domain.BatchItemState{domain.ItemScheduled, domain.ItemRunning})
	if err != nil {
		return err
	}
	budget := batch.Concurrency - len(inFlight)
	if budget <= 0 {
		return nil
	}
	pending, err := s.service.store.ListBatchItemsByStates(ctx, batch.ID, []domain.BatchItemState{domain.ItemPending})
	if err != nil {
		return err
	}
	for _, item := range pending {
		if budget <= 0 {
			return nil
		}
		select {
		case s.slots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		won, err := s.service.store.TransitionBatchItem(ctx, item.ID, domain.ItemPending, domain.ItemScheduled, "", s.now())
		if err != nil {
			<-s.slots
			return err
		}
		if !won {
			<-s.slots
			continue
		}
		budget--
		s.track(item.ID, true)
		go s.ignite(item) // #nosec G118 -- batch ignition deliberately outlives the scheduler tick's request context
	}
	return nil
}

// ignite runs one synchronous Agent chain to its first public runtime state
// (or straight to a terminal state) and records the outcome on the item.
func (s *BatchScheduler) ignite(item domain.BatchItem) {
	defer func() {
		<-s.slots
		s.track(item.ID, false)
	}()
	result, err := s.pipeline.Start(context.Background(), item.WorkflowID, item.PagePath, item.Anchor, nil)
	now := s.now()
	if err != nil {
		if _, transitionErr := s.service.store.TransitionBatchItem(context.Background(), item.ID, domain.ItemScheduled, domain.ItemFailed, failureSummary(err), now); transitionErr != nil {
			slog.Warn("documentation batch scheduler could not fail its item", "item_id", item.ID, "err", transitionErr)
		}
		return
	}
	next := domain.ItemRunning
	if result.Workflow.State.Terminal() {
		next = itemStateForWorkflow(result.Workflow.State)
	}
	if _, err := s.service.store.TransitionBatchItem(context.Background(), item.ID, domain.ItemScheduled, next, "", now); err != nil {
		slog.Warn("documentation batch scheduler could not advance its item", "item_id", item.ID, "err", err)
	}
}

// closeIfFinished moves Running and Paused batches with no in-flight or
// pending items left to Completed. Cancelled batches close only when their
// in-flight items drained.
func (s *BatchScheduler) closeIfFinished(ctx context.Context, batch domain.DocumentBatch) error {
	if batch.State != domain.BatchRunning && batch.State != domain.BatchPaused && batch.State != domain.BatchCancelled {
		return nil
	}
	inFlight, err := s.service.store.ListBatchItemsByStates(ctx, batch.ID, []domain.BatchItemState{domain.ItemScheduled, domain.ItemRunning})
	if err != nil {
		return err
	}
	if len(inFlight) > 0 {
		return nil
	}
	if batch.State == domain.BatchCancelled {
		return nil
	}
	pending, err := s.service.store.ListBatchItemsByStates(ctx, batch.ID, []domain.BatchItemState{domain.ItemPending})
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		return nil
	}
	if _, err := s.service.store.TransitionBatchState(ctx, batch.ID, batch.State, domain.BatchCompleted, nil, s.now()); err != nil {
		return err
	}
	slog.Info("documentation batch completed", "batch_id", batch.ID)
	return nil
}

func (s *BatchScheduler) track(itemID string, live bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if live {
		s.inFlight[itemID] = true
		return
	}
	delete(s.inFlight, itemID)
}

func (s *BatchScheduler) live(itemID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight[itemID]
}

// itemStateForWorkflow maps a terminal workflow state onto the item terminal
// with the same name.
func itemStateForWorkflow(state domain.WorkflowState) domain.BatchItemState {
	switch state {
	case domain.Published:
		return domain.ItemPublished
	case domain.NoPractice:
		return domain.ItemNoPractice
	case domain.Rejected:
		return domain.ItemRejected
	case domain.Failed:
		return domain.ItemFailed
	}
	return domain.ItemRunning
}

// failureSummary bounds a Start error to a storable, human-readable detail.
func failureSummary(err error) string {
	summary := err.Error()
	if len(summary) > 500 {
		summary = summary[:500]
	}
	return summary
}
