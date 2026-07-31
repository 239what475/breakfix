package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/breakfix/breakfix/internal/worklist"
)

const workDeadlineRecoveryInterval = 10 * time.Second

var deadlineBoundCandidateWorkKinds = [...]worklist.Kind{
	worklist.KindBuild,
	worklist.KindArtifactPublish,
	worklist.KindVerify,
	worklist.KindChallengePublish,
}

// RecoverExpiredWork is the Server-owned deadline reconciler. It does not
// depend on another Worker claim, and it deliberately leaves artifact_cleanup
// outside the deadline path because cleanup is retryable until its resources
// are confirmed absent.
func (h *Handler) RecoverExpiredWork(ctx context.Context) error {
	return h.recoverExpiredWorkAt(ctx, time.Now().UTC())
}

func (h *Handler) recoverExpiredWorkAt(ctx context.Context, now time.Time) error {
	if h == nil || h.db == nil {
		return nil
	}
	if now.IsZero() {
		return errors.New("work deadline recovery requires current time")
	}
	var recoveryErrors []error
	if err := h.db.ExpireDueAgentRuns(ctx, now.UTC()); err != nil {
		recoveryErrors = append(recoveryErrors, fmt.Errorf("expire agent work: %w", err))
	}
	if _, err := h.db.ReconcileFailedGeneratorRuns(ctx, now.UTC()); err != nil {
		recoveryErrors = append(recoveryErrors, fmt.Errorf("project failed generator runs: %w", err))
	}
	for _, kind := range deadlineBoundCandidateWorkKinds {
		if err := h.db.ExpireCandidateWork(ctx, kind, now.UTC()); err != nil {
			recoveryErrors = append(recoveryErrors, fmt.Errorf("expire %s candidate work: %w", kind, err))
		}
	}
	return errors.Join(recoveryErrors...)
}

func (h *Handler) StartWorkDeadlineRecovery(ctx context.Context) {
	if h == nil || h.db == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(workDeadlineRecoveryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := h.RecoverExpiredWork(ctx); err != nil {
					slog.Error("recover expired work", "err", err)
				}
			}
		}
	}()
}
