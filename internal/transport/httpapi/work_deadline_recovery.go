package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const generationDeadlineRecoveryInterval = 10 * time.Second

// RecoverExpiredGenerationWorkflows is Server-owned recovery for the only
// generation execution deadline. Resource reapers run independently from the
// business workflow state and never delay its terminal transition.
func (h *Handler) RecoverExpiredGenerationWorkflows(ctx context.Context) error {
	return h.recoverExpiredGenerationWorkflowsAt(ctx, time.Now().UTC())
}

func (h *Handler) recoverExpiredGenerationWorkflowsAt(ctx context.Context, now time.Time) error {
	if h == nil || h.db == nil {
		return nil
	}
	if now.IsZero() {
		return errors.New("generation deadline recovery requires current time")
	}
	if err := h.db.Generation.RecoverExpiredGenerationWorkflows(ctx, now.UTC()); err != nil {
		return err
	}
	return nil
}

func (h *Handler) StartGenerationDeadlineRecovery(ctx context.Context) {
	if h == nil || h.db == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(generationDeadlineRecoveryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := h.RecoverExpiredGenerationWorkflows(ctx); err != nil {
					slog.Error("recover expired generation workflows", "err", err)
				}
			}
		}
	}()
}
