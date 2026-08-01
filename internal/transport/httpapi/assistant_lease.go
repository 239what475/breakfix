package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// StartAssistantEnvironmentLeaseMaintainer makes active Assistant runs survive
// Server restarts. Browser connections and Worker processes are independent of
// this loop; it only renews the environment activity lease while durable Runs
// remain pending or running.
func (h *Handler) StartAssistantEnvironmentLeaseMaintainer(ctx context.Context) {
	if h.db == nil || h.k8s == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			if err := h.maintainAssistantEnvironmentLeases(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("renew assistant environment leases", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (h *Handler) maintainAssistantEnvironmentLeases(ctx context.Context) error {
	runs, err := h.db.Agent.ListActiveRunsForPurpose(ctx, "assistant")
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.OwnerKind != "environment" || run.OwnerRef == "" || run.SessionID == "" {
			continue
		}
		session, err := h.db.Agent.GetSession(ctx, run.SessionID)
		if err != nil {
			return err
		}
		environment, err := h.findActiveEnvironmentByUID(ctx, session.UserRef, run.OwnerRef)
		if err != nil {
			// The deletion path fences the Run. A concurrent cleanup can briefly
			// observe its old state here without making the reconciler fail.
			continue
		}
		adapter, err := h.environmentRuntimeAdapter(environment.Runtime)
		if err != nil {
			return err
		}
		if err := adapter.renewActivity(ctx, environment.Name, nowActivity()); err != nil {
			return err
		}
	}
	return nil
}
