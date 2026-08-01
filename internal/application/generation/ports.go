package generation

import (
	"context"
	"time"

	"github.com/breakfix/breakfix/internal/domain/generation"
)

// WorkspaceRepository is the durable boundary consumed by workspace lifecycle
// use cases. PostgreSQL is one implementation, not part of this package.
type WorkspaceRepository interface {
	CreateGeneratorWorkspace(context.Context, generation.Workspace) (*generation.Workspace, error)
	GetGeneratorWorkspace(context.Context, string) (*generation.Workspace, error)
	RecordGeneratorWorkspaceSandbox(context.Context, string, string, time.Time) error
	ActivateGeneratorWorkspace(context.Context, string, string, time.Time) error
	BeginGeneratorWorkspaceCleanup(context.Context, string, time.Time) (*generation.Workspace, error)
	MarkGeneratorWorkspaceDeleted(context.Context, string, time.Time) error
	ListExpiredPendingGeneratorWorkspaces(context.Context, time.Time) ([]generation.Workspace, error)
	ListDeletingGeneratorWorkspaces(context.Context) ([]generation.Workspace, error)
	ListTerminalGeneratorWorkspaces(context.Context) ([]generation.Workspace, error)
}
