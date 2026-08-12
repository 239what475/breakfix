package generation

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const defaultWorkspaceReaperInterval = time.Minute

// WorkspaceReaper owns the periodic retry of durable Generator workspace
// cleanup. The workspace manager remains the sole owner of actual deletion.
type WorkspaceReaper struct {
	manager  *Manager
	interval time.Duration
}

func NewWorkspaceReaper(manager *Manager) (*WorkspaceReaper, error) {
	if manager == nil {
		return nil, errors.New("generator workspace manager is required")
	}
	return &WorkspaceReaper{manager: manager, interval: defaultWorkspaceReaperInterval}, nil
}

func (r *WorkspaceReaper) Recover(ctx context.Context) error {
	if r == nil || r.manager == nil {
		return errors.New("generator workspace reaper is not configured")
	}
	// A Server restart never resumes a remotely executing Generator turn.
	// Retire records first; Run performs the provider cleanup asynchronously so
	// the next user turn can allocate a distinct PVC/Sandbox immediately.
	_, err := r.manager.repo.RetireIncompleteGeneratorWorkspaces(ctx, r.manager.now())
	return err
}

func (r *WorkspaceReaper) Run(ctx context.Context) error {
	if r == nil || r.manager == nil {
		return errors.New("generator workspace reaper is not configured")
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.manager.CleanupDue(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("clean generator workspaces", "err", err)
			}
		}
	}
}
