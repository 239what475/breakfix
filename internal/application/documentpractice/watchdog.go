package documentpractice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

// runnableActionWatchdogBudget wraps the verification environment's 1800s
// MaxLifetime plus a 300s reconciliation margin. It is the grace an exhausted
// runnable action receives before the watchdog stops waiting for a Worker.
const runnableActionWatchdogBudget = 1800*time.Second + 300*time.Second

// WatchdogActionStatus mirrors the public runnable action bound to a
// workflow's current state version. It is read-only evidence for the watchdog.
type WatchdogActionStatus struct {
	Phase          runnable.ActionPhase
	State          string
	Attempt        int
	FailureClass   string
	FailureCode    string
	FailureSummary string
	// UpdatedAt is the public action's last state change. For an exhausted
	// queued action it marks when the final failure re-queued it.
	UpdatedAt time.Time
	// LeaseExpired reports a running action whose lease lapsed at or before
	// the listing instant, so no Worker is progressing it.
	LeaseExpired bool
}

// WatchdogCandidate is one non-terminal workflow whose bound public action
// already failed or exhausted its attempts.
type WatchdogCandidate struct {
	WorkflowID           string
	WorkflowState        domain.WorkflowState
	WorkflowStateVersion int64
	Action               WatchdogActionStatus
}

// watchdogMapping is the watchdog policy. A failed action maps immediately;
// an exhausted action maps only after the stuck budget measured from its last
// state change, and a still-leased running attempt is never killed.
func watchdogMapping(candidate WatchdogCandidate, now time.Time, grace time.Duration) (string, bool) {
	action := candidate.Action
	switch {
	case action.State == "failed":
		reason := "action_failed"
		if action.FailureClass != "" {
			reason += " " + action.FailureClass + "/" + action.FailureCode + ": " + action.FailureSummary
		}
		return reason, true
	case action.Attempt >= 5 && action.State == "queued" && !action.UpdatedAt.After(now.Add(-grace)):
		return fmt.Sprintf("attempts_exhausted queued since %s", action.UpdatedAt.UTC().Format(time.RFC3339)), true
	case action.Attempt >= 5 && action.State == "running" && action.LeaseExpired && !action.UpdatedAt.After(now.Add(-grace)):
		return fmt.Sprintf("attempts_exhausted lease expired since %s", action.UpdatedAt.UTC().Format(time.RFC3339)), true
	}
	return "", false
}

// Watchdog maps every non-terminal workflow whose bound public runnable action
// can no longer complete onto Failed. It is the machine replacement for the
// human force-fail escape hatch in a multi-page rollout: no administrator can
// rescue hundreds of worker-orphaned workflows one by one. Each mapping writes
// a system-owned ledger artifact and no human action audit row, because no
// human operated.
func (p *AgentPipeline) Watchdog(ctx context.Context) error {
	if p == nil {
		return errors.New("documentation Agent pipeline is not configured")
	}
	now := p.now()
	candidates, err := p.service.store.ListWorkflowWatchdogCandidates(ctx, now)
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		reason, ok := watchdogMapping(candidate, now, runnableActionWatchdogBudget)
		if !ok {
			continue
		}
		workflow, err := p.service.store.WatchdogFailWorkflow(ctx, candidate.WorkflowID, reason, candidate.WorkflowStateVersion, now)
		if err != nil {
			// A lost fence means the workflow moved on or the mapping already
			// committed; the next pass re-derives everything from the store.
			slog.Warn("documentation watchdog could not map workflow", "workflow_id", candidate.WorkflowID, "err", err)
			continue
		}
		slog.Warn("documentation watchdog mapped a stranded workflow onto Failed",
			"workflow_id", candidate.WorkflowID, "reason", reason,
			"action_key_phase", string(candidate.Action.Phase), "action_state", candidate.Action.State,
			"attempt", candidate.Action.Attempt, "workflow_state", string(workflow.State))
	}
	return nil
}
