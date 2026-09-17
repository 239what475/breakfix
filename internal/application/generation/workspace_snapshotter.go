package generation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/workspacearchive"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

const (
	defaultWorkspaceSnapshotInterval     = 30 * time.Second
	defaultWorkspaceSnapshotCleanupGrace = 10 * time.Minute
	defaultWorkspaceIdleTTL              = 24 * time.Hour
)

type workspaceArchiver interface {
	ArchiveWorkspace(context.Context, string) ([]byte, error)
}

// WorkspaceSnapshotter persists editable Generator workspaces independently
// from a user turn. It owns no Sandbox/PVC deletion; WorkspaceReaper remains
// the only process that performs those provider-side operations.
type WorkspaceSnapshotter struct {
	// OnTick optionally reports each background pass to the bootstrap's
	// in-memory service registry.
	OnTick       func(error)
	repo         WorkspaceRepository
	archiver     workspaceArchiver
	store        *workspacearchive.Store
	interval     time.Duration
	idleTTL      time.Duration
	cleanupGrace time.Duration
	now          func() time.Time
	requests     chan string
}

type WorkspaceSnapshotterConfig struct {
	DataDir string
	IdleTTL time.Duration
}

func NewWorkspaceSnapshotter(manager *Manager, archiver workspaceArchiver, config WorkspaceSnapshotterConfig) (*WorkspaceSnapshotter, error) {
	if manager == nil || manager.repo == nil || archiver == nil {
		return nil, errors.New("workspace snapshotter requires manager and archive provider")
	}
	store, err := workspacearchive.NewStore(config.DataDir)
	if err != nil {
		return nil, err
	}
	if config.IdleTTL <= 0 {
		config.IdleTTL = defaultWorkspaceIdleTTL
	}
	return &WorkspaceSnapshotter{
		repo: manager.repo, archiver: archiver, store: store,
		interval: defaultWorkspaceSnapshotInterval, idleTTL: config.IdleTTL,
		cleanupGrace: defaultWorkspaceSnapshotCleanupGrace,
		now:          func() time.Time { return time.Now().UTC() },
		requests:     make(chan string, 128),
	}, nil
}

// Request asks the snapshotter to take a prompt post-turn snapshot. Requests
// are deliberately coalesced: the periodic pass is the durable backstop.
func (s *WorkspaceSnapshotter) Request(workflowID string) {
	if s == nil || strings.TrimSpace(workflowID) == "" {
		return
	}
	select {
	case s.requests <- strings.TrimSpace(workflowID):
	default:
	}
}

func (s *WorkspaceSnapshotter) Run(ctx context.Context) error {
	if s == nil || s.repo == nil || s.archiver == nil || s.store == nil {
		return errors.New("workspace snapshotter is not configured")
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case workflowID := <-s.requests:
			if err := s.SnapshotWorkflow(ctx, workflowID); err != nil && ctx.Err() == nil && !snapshotSkip(err) {
				slog.Warn("snapshot generator workspace after turn", "workflow_id", workflowID, "err", err)
			}
		case <-ticker.C:
			err := s.SnapshotDue(ctx)
			if s.OnTick != nil {
				s.OnTick(err)
			}
			if err != nil && ctx.Err() == nil {
				slog.Warn("snapshot generator workspaces", "err", err)
			}
		}
	}
}

func (s *WorkspaceSnapshotter) SnapshotDue(ctx context.Context) error {
	if s == nil || s.repo == nil {
		return errors.New("workspace snapshotter is not configured")
	}
	targets, err := s.repo.ListGeneratorWorkspaceSnapshotTargets(ctx)
	if err != nil {
		return err
	}
	var result error
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.reconcile(ctx, target); err != nil && !snapshotSkip(err) {
			// One unavailable Sandbox must not defer snapshots for every other
			// workspace until the next periodic pass.
			result = errors.Join(result, fmt.Errorf("snapshot workflow %s: %w", target.Workspace.WorkflowID, err))
		}
	}
	if err := s.cleanup(ctx); err != nil {
		result = errors.Join(result, err)
	}
	return result
}

func (s *WorkspaceSnapshotter) SnapshotWorkflow(ctx context.Context, workflowID string) error {
	if s == nil || s.repo == nil {
		return errors.New("workspace snapshotter is not configured")
	}
	targets, err := s.repo.ListGeneratorWorkspaceSnapshotTargets(ctx)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if target.Workspace.WorkflowID == workflowID {
			return s.reconcile(ctx, target)
		}
	}
	return nil
}

func (s *WorkspaceSnapshotter) reconcile(ctx context.Context, target domain.WorkspaceSnapshotTarget) error {
	now := s.now().UTC()
	if target.SnapshotDigest != "" && target.Workspace.IdleSince != nil {
		if _, err := s.store.Read(target.Workspace.WorkflowID, target.SnapshotDigest); err == nil {
			if !target.Workspace.IdleSince.Add(s.idleTTL).After(now) {
				_, err := s.repo.RetireIdleGeneratorWorkspace(ctx, target.Workspace.ID, target.SnapshotDigest, now.Add(-s.idleTTL), now)
				return err
			}
			return nil
		}
		cleared, err := s.repo.ClearGeneratorWorkspaceSnapshot(ctx, target.Workspace.WorkflowID, target.SnapshotDigest, now)
		if err != nil {
			return fmt.Errorf("clear invalid generator workspace snapshot: %w", err)
		}
		if !cleared {
			return nil
		}
	}

	holder := domain.NewWorkspaceSnapshotHolderID()
	acquired, err := s.repo.AcquireGeneratorWorkspaceSnapshot(ctx, target.Workspace.WorkflowID, holder, now)
	if err != nil {
		return err
	}
	held := true
	defer func() {
		if !held {
			return
		}
		releaseCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.repo.ReleaseGeneratorWorkspaceTurn(releaseCtx, domain.WorkspaceTurn{WorkflowID: acquired.Workspace.WorkflowID, ID: holder}, s.now().UTC()); err != nil && !errors.Is(err, domain.ErrWorkspaceTurnLost) {
			slog.Warn("release failed generator workspace snapshot holder", "workspace_id", acquired.Workspace.ID, "err", err)
		}
	}()
	archive, err := s.archiver.ArchiveWorkspace(ctx, acquired.Workspace.SandboxID)
	if err != nil {
		return fmt.Errorf("archive generator workspace: %w", err)
	}
	_, digest, err := s.store.Save(acquired.Workspace.WorkflowID, archive)
	if err != nil {
		return fmt.Errorf("persist generator workspace snapshot: %w", err)
	}
	if err := s.repo.PublishGeneratorWorkspaceSnapshot(ctx, acquired.Workspace.ID, holder, digest, s.now().UTC()); err != nil {
		return fmt.Errorf("publish generator workspace snapshot: %w", err)
	}
	held = false
	return nil
}

func (s *WorkspaceSnapshotter) cleanup(ctx context.Context) error {
	references, err := s.repo.ListGeneratorWorkspaceSnapshotReferences(ctx)
	if err != nil {
		return err
	}
	values := make([]workspacearchive.Reference, 0, len(references))
	for _, reference := range references {
		values = append(values, workspacearchive.Reference{WorkflowID: reference.WorkflowID, Digest: reference.Digest})
	}
	return s.store.Cleanup(values, s.now().UTC().Add(-s.cleanupGrace))
}

func snapshotSkip(err error) bool {
	return errors.Is(err, domain.ErrWorkspaceBusy) ||
		errors.Is(err, domain.ErrWorkspaceSnapshotCurrent) ||
		errors.Is(err, domain.ErrWorkspaceNotIdle) ||
		errors.Is(err, domain.ErrWorkspaceNotFound)
}
