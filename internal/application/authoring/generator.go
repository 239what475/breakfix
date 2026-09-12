package authoring

import (
	"context"

	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/toolresult"
)

// GeneratorOperations is the user-owned generation capability available to an
// interactive Authoring Agent. Its implementation lives in Generation; this
// package keeps the interactive execution boundary independent from that
// application's concrete service and from provider adapters.
type GeneratorOperations interface {
	ConfirmGeneration(context.Context, string, string, generation.StartConfirmation) (*generation.Workflow, error)
	GetGenerationWorkflow(context.Context, string, string) (*generation.Workflow, error)
	GetGenerationCandidate(context.Context, string, string) (*generation.Revision, error)
	ListActiveGenerations(context.Context, string) ([]generation.Workflow, error)

	StartWorkspaceTurn(context.Context, string, generation.WorkspaceTurn) error
	EndWorkspaceTurn(context.Context, string, generation.WorkspaceTurn) error
	ListWorkspaceFiles(context.Context, string, generation.WorkspaceTurn) ([]generation.WorkspaceFile, error)
	ReadWorkspaceContent(context.Context, string, generation.WorkspaceTurn, string, int, int) (string, error)
	WriteWorkspaceFile(context.Context, string, generation.WorkspaceTurn, string, string) error
	ExecuteWorkspaceCommand(context.Context, string, generation.WorkspaceTurn, string) (toolresult.Envelope, error)
	SubmitCandidate(context.Context, string, generation.CandidateSubmission) (*generation.Revision, error)

	ConfirmContent(context.Context, string, generation.ContentConfirmation) (*generation.Workflow, error)
	RequestContentChanges(context.Context, string, generation.ContentChangeRequest) (*generation.Workflow, error)
	CancelGeneration(context.Context, string, generation.Cancellation) (*generation.Workflow, error)
}
