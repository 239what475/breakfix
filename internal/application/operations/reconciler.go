package operations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	scenariodomain "github.com/breakfix/breakfix/internal/domain/scenario"
)

type ActiveRevisionSource interface {
	ListActiveScenarioRevisions(context.Context) ([]scenariodomain.ActiveRevision, error)
}

type BindingResolver interface {
	ResolveOperationsRevisionBinding(context.Context, string) (runnable.RevisionReference, error)
}

// RevisionPublisher is intentionally small so the reconciliation policy can
// be tested without a filesystem or provider. Publisher's deterministic
// action identity makes every pass safe after process restarts.
type RevisionPublisher interface {
	PrepareRevision(context.Context, string, string, int64, time.Time) (*PreparedPublication, error)
	Finalize(context.Context, PreparedPublication, time.Time) (runnable.RevisionReference, error)
}

type BindingReconciler struct {
	source    ActiveRevisionSource
	bindings  BindingResolver
	publisher RevisionPublisher
	interval  time.Duration
	now       func() time.Time
}

func NewBindingReconciler(source ActiveRevisionSource, bindings BindingResolver, publisher RevisionPublisher, interval time.Duration) (*BindingReconciler, error) {
	if source == nil || bindings == nil || publisher == nil {
		return nil, errors.New("Operations binding reconciler requires source, binding store, and publisher")
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &BindingReconciler{source: source, bindings: bindings, publisher: publisher, interval: interval, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (r *BindingReconciler) Recover(ctx context.Context) error {
	return r.RunOnce(ctx)
}

func (r *BindingReconciler) Run(ctx context.Context) error {
	if r == nil {
		return errors.New("Operations binding reconciler is not configured")
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("reconcile Operations runnable bindings", "err", err)
			}
		}
	}
}

// RunOnce converges every active Operations revision independently. A
// materialization that is still queued/running is expected and leaves the
// revision unbound until a later pass observes its completed result.
func (r *BindingReconciler) RunOnce(ctx context.Context) error {
	if r == nil || r.source == nil || r.bindings == nil || r.publisher == nil {
		return errors.New("Operations binding reconciler is not configured")
	}
	active, err := r.source.ListActiveScenarioRevisions(ctx)
	if err != nil {
		return fmt.Errorf("list active Operations revisions: %w", err)
	}
	for _, value := range active {
		if value.Revision.Type != scenario.ScenarioOperationsScenario {
			continue
		}
		if _, err := r.bindings.ResolveOperationsRevisionBinding(ctx, value.Revision.ID); err == nil {
			continue
		} else if !errors.Is(err, runnable.ErrMaterializationNotReady) {
			return fmt.Errorf("resolve Operations runnable binding %q: %w", value.Revision.ID, err)
		}
		prepared, err := r.publisher.PrepareRevision(ctx, value.Scenario.ID, value.Revision.ID, 1, r.now().UTC())
		if err != nil {
			return fmt.Errorf("prepare Operations runnable revision %q: %w", value.Revision.ID, err)
		}
		if prepared == nil {
			return errors.New("Operations publisher returned an empty prepared publication")
		}
		if _, err := r.publisher.Finalize(ctx, *prepared, r.now().UTC()); err != nil && !errors.Is(err, runnable.ErrMaterializationNotReady) {
			return fmt.Errorf("finalize Operations runnable revision %q: %w", value.Revision.ID, err)
		}
	}
	return nil
}
