package assistant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/domain/agent"
)

const defaultLeaseMaintenanceInterval = 10 * time.Second

// LeaseRepository exposes only active Assistant runs and their durable
// sessions. It deliberately has no Environment or HTTP dependency.
type LeaseRepository interface {
	ListActiveRunsForPurpose(context.Context, string) ([]agent.Run, error)
	GetSession(context.Context, string) (*agent.Session, error)
}

// EnvironmentLeaseRenewer resolves and renews an active learning Environment.
// The concrete Kubernetes operation is supplied by Server assembly.
type EnvironmentLeaseRenewer interface {
	RenewAssistantEnvironmentLease(context.Context, string, string) error
}

// LeaseMaintainer keeps an Environment active while its durable Assistant run
// is still pending or running. It does not own browser connections or workers.
type LeaseMaintainer struct {
	repository LeaseRepository
	renewer    EnvironmentLeaseRenewer
	// OnTick optionally reports each renewal pass to the bootstrap's in-memory
	// service registry.
	OnTick     func(error)
	interval   time.Duration
}

func NewLeaseMaintainer(repository LeaseRepository, renewer EnvironmentLeaseRenewer) (*LeaseMaintainer, error) {
	if repository == nil || renewer == nil {
		return nil, errors.New("assistant lease maintainer requires repository and environment renewer")
	}
	return &LeaseMaintainer{repository: repository, renewer: renewer, interval: defaultLeaseMaintenanceInterval}, nil
}

// Recover performs one renewal pass before Server readiness.
func (m *LeaseMaintainer) Recover(ctx context.Context) error { return m.RunOnce(ctx) }

func (m *LeaseMaintainer) Run(ctx context.Context) error {
	if m == nil {
		return errors.New("assistant lease maintainer is not configured")
	}
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			err := m.RunOnce(ctx)
			if m.OnTick != nil {
				m.OnTick(err)
			}
			if err != nil && ctx.Err() == nil {
				slog.Warn("renew assistant environment leases", "err", err)
			}
		}
	}
}

func (m *LeaseMaintainer) RunOnce(ctx context.Context) error {
	if m == nil || m.repository == nil || m.renewer == nil {
		return errors.New("assistant lease maintainer is not configured")
	}
	runs, err := m.repository.ListActiveRunsForPurpose(ctx, "assistant")
	if err != nil {
		return fmt.Errorf("list active assistant runs: %w", err)
	}
	for _, run := range runs {
		if run.OwnerKind != "environment" || strings.TrimSpace(run.OwnerRef) == "" || strings.TrimSpace(run.SessionID) == "" {
			continue
		}
		session, err := m.repository.GetSession(ctx, run.SessionID)
		if err != nil {
			return fmt.Errorf("load assistant session %s: %w", run.SessionID, err)
		}
		err = m.renewer.RenewAssistantEnvironmentLease(ctx, session.UserRef, run.OwnerRef)
		if err != nil {
			return fmt.Errorf("renew assistant environment lease: %w", err)
		}
	}
	return nil
}
