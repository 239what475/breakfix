package generation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	appoperations "github.com/breakfix/breakfix/internal/application/operations"
	"github.com/breakfix/breakfix/internal/content/candidate"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

type RunnableCoordinatorStore interface {
	ListRunnableGenerationCandidates(context.Context) ([]domain.Workflow, error)
	CandidateForWorkflow(context.Context, string) (*domain.Revision, error)
	MarkGenerationCandidateMaterialized(context.Context, string, runnable.RevisionReference, time.Time) error
	MarkGenerationCandidateVerified(context.Context, string, runnable.VerificationReportReference, time.Time) error
}

type RunnableCoordinatorStoreRuntime interface {
	StoreRunnableSource(context.Context, runnable.SourceArchive, []byte, time.Time) error
	ScheduleMaterialization(context.Context, runnable.RunnableSpec, int64, time.Time) (runnable.ActionIdentity, error)
	ResolveMaterializedRunnableRevision(context.Context, runnable.ActionIdentity) (runnable.RevisionReference, error)
	ScheduleVerification(context.Context, runnable.RevisionReference, int64, time.Time) (runnable.ActionIdentity, error)
	ResolveVerificationForAction(context.Context, runnable.ActionIdentity) (runnable.StoredVerificationReport, error)
}

// RunnableCoordinator is the Generation application-side projection of public
// action completion. It owns no Worker lease, provider retry, artifact, or
// Environment state; those remain in runnable_actions and RuntimeEnvironment.
type RunnableCoordinator struct {
	store      RunnableCoordinatorStore
	runnable   RunnableCoordinatorStoreRuntime
	operations appoperations.Config
	interval   time.Duration
	now        func() time.Time
}

func NewRunnableCoordinator(store RunnableCoordinatorStore, runnableStore RunnableCoordinatorStoreRuntime, operations appoperations.Config, interval time.Duration) (*RunnableCoordinator, error) {
	if store == nil || runnableStore == nil || interval <= 0 {
		return nil, errors.New("generation runnable coordinator requires stores and interval")
	}
	return &RunnableCoordinator{store: store, runnable: runnableStore, operations: operations, interval: interval, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (c *RunnableCoordinator) Recover(ctx context.Context) error { return c.RunOnce(ctx) }

func (c *RunnableCoordinator) Run(ctx context.Context) error {
	if c == nil {
		return errors.New("generation runnable coordinator is not configured")
	}
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := c.RunOnce(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("reconcile generation runnable actions", "err", err)
			}
		}
	}
}

func (c *RunnableCoordinator) RunOnce(ctx context.Context) error {
	if c == nil || c.store == nil || c.runnable == nil {
		return errors.New("generation runnable coordinator is not configured")
	}
	workflows, err := c.store.ListRunnableGenerationCandidates(ctx)
	if err != nil {
		return err
	}
	for _, workflow := range workflows {
		candidateRevision, err := c.store.CandidateForWorkflow(ctx, workflow.ID)
		if err != nil {
			return err
		}
		if candidateRevision == nil {
			return errors.New("generation runnable workflow has no candidate")
		}
		archive, err := candidate.ReadArchive(candidateRevision.ArchivePath, candidateRevision.ArchiveDigest)
		if err != nil {
			return err
		}
		source, sourceBytes, contentRevision, err := FreezeCandidateSource(archive)
		if err != nil {
			return err
		}
		if source != candidateRevision.SourceArchive || contentRevision != candidateRevision.ContentRevision {
			return errors.New("candidate frozen source differs from durable source")
		}
		inspected, err := InspectCandidateArchive(archive)
		if err != nil {
			return err
		}
		spec, err := appoperations.Compile(appoperations.Input{ContentID: candidateRevision.ID, ContentRevision: candidateRevision.ContentRevision, Entry: inspected.Entry, Source: source}, c.operations)
		if err != nil {
			return err
		}
		now := c.now().UTC()
		if err := c.runnable.StoreRunnableSource(ctx, source, sourceBytes, now); err != nil {
			return err
		}
		switch workflow.State {
		case domain.StateMaterializingArtifact:
			action, err := c.runnable.ScheduleMaterialization(ctx, spec, workflow.StateVersion, now)
			if err != nil {
				return err
			}
			reference, err := c.runnable.ResolveMaterializedRunnableRevision(ctx, action)
			if errors.Is(err, runnable.ErrMaterializationNotReady) {
				continue
			}
			if err != nil {
				return err
			}
			if err := c.store.MarkGenerationCandidateMaterialized(ctx, workflow.ID, reference, now); err != nil {
				return err
			}
		case domain.StateVerifying:
			if candidateRevision.RunnableRevisionRef == nil {
				return fmt.Errorf("generation workflow %q has no runnable revision", workflow.ID)
			}
			action, err := c.runnable.ScheduleVerification(ctx, *candidateRevision.RunnableRevisionRef, workflow.StateVersion, now)
			if err != nil {
				return err
			}
			report, err := c.runnable.ResolveVerificationForAction(ctx, action)
			if errors.Is(err, runnable.ErrMaterializationNotReady) {
				continue
			}
			if err != nil {
				return err
			}
			if err := c.store.MarkGenerationCandidateVerified(ctx, workflow.ID, report.Reference, now); err != nil {
				return err
			}
		default:
			return fmt.Errorf("generation workflow %q has unsupported runnable state %s", workflow.ID, workflow.State)
		}
	}
	return nil
}
