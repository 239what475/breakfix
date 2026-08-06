package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/publication"
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
	pending, err := h.db.Generation.PendingGenerationPublicationFinalizations(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, value := range pending {
		entry, materializeErr := h.materializeCandidatePublication(&value.Candidate)
		if materializeErr != nil {
			if err := h.recordGenerationFinalizerFailure(ctx, value, materializeErr); err != nil {
				slog.Warn("record generation publication diagnostic", "workflow_id", value.Workflow.ID, "candidate_revision_id", value.Candidate.ID, "err", err)
			}
			continue
		}
		if err := h.db.Generation.FinalizeGenerationChallengePublication(ctx, value.Workflow.ID, value.Candidate.ID, entry.ContentRevision, entry.Revision, time.Now().UTC()); err != nil {
			if recordErr := h.recordGenerationFinalizerFailure(ctx, value, classifyGenerationFinalizerError(err)); recordErr != nil {
				slog.Warn("record generation publication diagnostic", "workflow_id", value.Workflow.ID, "candidate_revision_id", value.Candidate.ID, "err", recordErr)
			}
		}
	}
	return nil
}

func classifyGenerationFinalizerError(err error) error {
	if err == nil || publication.CategoryOf(err).Valid() {
		return err
	}
	if errors.Is(err, generation.ErrCandidateInvalidState) || errors.Is(err, generation.ErrClassificationConflict) || errors.Is(err, generation.ErrChallengeSourceRefConflict) {
		return publication.Deterministic(err)
	}
	return publication.Transient(err)
}

func (h *Handler) recordGenerationFinalizerFailure(ctx context.Context, value generation.PublicationFinalization, err error) error {
	diagnostic, diagnosticErr := publication.NewDiagnostic(classifyGenerationFinalizerError(err), time.Now().UTC())
	if diagnosticErr != nil {
		return diagnosticErr
	}
	_, err = h.db.Generation.RecordGenerationPublicationFinalizerFailure(ctx, value.Workflow.ID, value.Candidate.ID, diagnostic)
	return err
}
