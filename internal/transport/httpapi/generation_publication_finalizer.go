package httpapi

import (
	"context"
	"log/slog"
	"time"
)

const generationPublicationFinalizerInterval = 5 * time.Second

// StartGenerationPublicationFinalizer completes only the Server-owned tail of
// a successful challenge promotion. The Runtime Worker has already recorded
// the immutable promoted reference; this loop never invokes a provider.
func (h *Handler) StartGenerationPublicationFinalizer(ctx context.Context) {
	if h == nil || h.db == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(generationPublicationFinalizerInterval)
		defer ticker.Stop()
		for {
			if err := h.finalizePendingGenerationPublications(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("finalize generation publications", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (h *Handler) finalizePendingGenerationPublications(ctx context.Context) error {
	pending, err := h.db.Generation.PendingGenerationPublicationFinalizations(ctx)
	if err != nil {
		return err
	}
	for _, value := range pending {
		entry, materializeErr := h.materializeCandidatePublication(&value.Candidate)
		if materializeErr != nil {
			slog.Warn("materialize promoted generation candidate", "workflow_id", value.Workflow.ID, "candidate_revision_id", value.Candidate.ID, "err", materializeErr)
			continue
		}
		if err := h.db.Generation.FinalizeGenerationChallengePublication(ctx, value.Workflow.ID, value.Candidate.ID, entry.ContentRevision, time.Now().UTC()); err != nil {
			slog.Warn("finalize promoted generation candidate", "workflow_id", value.Workflow.ID, "candidate_revision_id", value.Candidate.ID, "err", err)
		}
	}
	return nil
}
