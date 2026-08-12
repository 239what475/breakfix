package generation

import (
	"context"
	"time"

	"github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

// WorkspaceRepository is the durable boundary consumed by workspace lifecycle
// use cases. PostgreSQL is one implementation, not part of this package.
type WorkspaceRepository interface {
	CreateGeneratorWorkspace(context.Context, generation.Workspace) (*generation.Workspace, error)
	GetGeneratorWorkspace(context.Context, string) (*generation.Workspace, error)
	GetCurrentGeneratorWorkspace(context.Context, string) (*generation.Workspace, error)
	GetGeneratorWorkspaceForTurn(context.Context, generation.WorkspaceTurn) (*generation.Workspace, error)
	AcquireGeneratorWorkspaceTurn(context.Context, generation.WorkspaceTurn, time.Time) (*generation.Workspace, error)
	ReleaseGeneratorWorkspaceTurn(context.Context, generation.WorkspaceTurn, time.Time) error
	RecordGeneratorWorkspaceSandbox(context.Context, string, string, time.Time) error
	ActivateGeneratorWorkspace(context.Context, string, string, time.Time) error
	BeginGeneratorWorkspaceCleanup(context.Context, string, time.Time) (*generation.Workspace, error)
	RetireCurrentGeneratorWorkspace(context.Context, string, time.Time) (*generation.Workspace, error)
	RetireIncompleteGeneratorWorkspaces(context.Context, time.Time) ([]generation.Workspace, error)
	MarkGeneratorWorkspaceDeleted(context.Context, string, time.Time) error
	ListExpiredPendingGeneratorWorkspaces(context.Context, time.Time) ([]generation.Workspace, error)
	ListDeletingGeneratorWorkspaces(context.Context) ([]generation.Workspace, error)
	ListTerminalGeneratorWorkspaces(context.Context) ([]generation.Workspace, error)
}

// GeneratorStore owns the durable, user-facing generation lifecycle. It is
// intentionally narrower than the Server's internal Judge/Classifier runner:
// generator clients cannot claim workflows or create AgentRuns.
type GeneratorStore interface {
	CreateGenerationWorkflow(context.Context, string, string, generation.StartConfirmation, time.Time) (*generation.Workflow, error)
	GetGenerationWorkflowForUser(context.Context, string, string) (*generation.Workflow, error)
	ListGenerationWorkflowsForUser(context.Context, string) ([]generation.Workflow, error)
	GetCandidateRevision(context.Context, string) (*generation.Revision, error)
	FindSubmittedGenerationCandidate(context.Context, string, string, generation.CandidateSubmission) (*generation.Revision, error)
	SubmitGenerationCandidate(context.Context, string, string, generation.CandidateSubmission, generation.Revision, time.Time) (*generation.Revision, error)
	ConfirmGenerationContent(context.Context, string, string, generation.ContentConfirmation, time.Time) (*generation.Workflow, error)
	RequestGenerationContentChanges(context.Context, string, string, generation.ContentChangeRequest, time.Time) (*generation.Workflow, error)
	ResumeGenerationClassification(context.Context, string, string, generation.ClassificationAdjustmentConfirmation, time.Time) (*generation.Workflow, error)
	BeginClassificationPublication(context.Context, string, string, string, generation.PublicationConfirmation, time.Time) (*generation.Workflow, error)
	CancelGenerationWorkflow(context.Context, string, string, generation.Cancellation, time.Time) (*generation.Workflow, error)
}

// GeneratorPlanStore is the authoring boundary used by external Generator
// clients. The web Authoring Agent already owns a session, while MCP can
// create and revise one through this same service.
type GeneratorPlanStore interface {
	CreateAuthoringSession(context.Context, authoring.Session, authoring.Plan) (*authoring.Session, error)
	GetAuthoringSession(context.Context, string, string) (*authoring.Session, error)
	GetAuthoringRevision(context.Context, string, int64) (*authoring.Revision, error)
	ReplaceAuthoringPlan(context.Context, string, string, int64, authoring.Plan, authoring.SessionState) (*authoring.Revision, error)
}
