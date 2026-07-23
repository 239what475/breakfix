package authoring

import "context"

// Repository keeps session metadata and immutable plan revisions. Verified
// source files stay on the filesystem and are referred to by relative paths.
type Repository interface {
	CreateAuthoringSession(context.Context, Session, Plan) (*Session, error)
	GetAuthoringSession(context.Context, string, string) (*Session, error)
	GetLatestOpenAuthoringSession(context.Context, string) (*Session, error)
	GetAuthoringSessionInternal(context.Context, string) (*Session, error)
	GetAuthoringRevision(context.Context, string, int64) (*Revision, error)
	ListAuthoringMessages(context.Context, string) ([]Message, error)
	AppendAuthoringMessage(context.Context, string, Message) error
	ReplaceAuthoringPlan(context.Context, string, string, int64, Plan, SessionState) (*Revision, error)
	SetAuthoringAgentStarted(context.Context, string) error
	SetAuthoringWorkflowStarted(context.Context, string) error
	BeginGeneration(context.Context, string, string, int64, string) (*Session, error)
	RestartGeneration(context.Context, string, string, string, string, string) (*Session, error)
	AttachVerificationTask(context.Context, string, string, int64, string) error
	CompleteVerification(context.Context, string, string, Artifact, Verification) error
	BeginPublish(context.Context, string, string, int64, string) (*Revision, error)
	CompletePublish(context.Context, string, string, int64, string) error
	AbortPublish(context.Context, string, string, string) error
	SetAuthoringState(context.Context, string, SessionState, string) error
	FindLatestAuthoringArtifact(context.Context, string, int64) (*Artifact, error)
}
