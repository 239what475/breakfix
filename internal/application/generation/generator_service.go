package generation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

// GeneratorWorkspaceTools is the narrow Server-owned workspace capability
// shared by web Authoring tools and the MCP application API. It intentionally
// exposes neither provider identities nor lifecycle operations.
type GeneratorWorkspaceTools interface {
	ListWorkspaceFiles(context.Context, string) ([]domain.WorkspaceFile, error)
	ReadFile(context.Context, string, string) ([]byte, error)
	WriteFile(context.Context, string, string, []byte, int) error
	ArchiveWorkspace(context.Context, string) ([]byte, error)
	ExecuteWorkspace(context.Context, string, string, string, func(string) error) (int, string, error)
}

// GeneratorServiceConfig holds Server-only facts needed to turn a workspace
// archive into an immutable candidate. Generator clients never configure
// archive storage or runtime snapshot behavior.
type GeneratorServiceConfig struct {
	DataDir         string
	FreezeExecution ExecutionSnapshotter
}

// GenerationView is the ownership-fenced read model returned to either
// Generator client. Candidate archive paths remain Server-private.
type GenerationView struct {
	Workflow  domain.Workflow  `json:"workflow"`
	Candidate *domain.Revision `json:"candidate,omitempty"`
}

// GeneratorService is the single application boundary for user-directed
// challenge generation. It never claims a workflow or runs a background
// Generator model; Generating only means this user's workspace can be edited.
type GeneratorService struct {
	store      GeneratorStore
	plans      GeneratorPlanStore
	workspace  *Manager
	sandboxes  GeneratorWorkspaceTools
	dataDir    string
	freeze     ExecutionSnapshotter
	now        func() time.Time
	cleanupTTL time.Duration
}

func NewGeneratorService(store GeneratorStore, plans GeneratorPlanStore, workspace *Manager, sandboxes GeneratorWorkspaceTools, config GeneratorServiceConfig) (*GeneratorService, error) {
	if store == nil || plans == nil || workspace == nil || sandboxes == nil {
		return nil, errors.New("generator service requires stores, workspace manager, and sandbox tools")
	}
	if strings.TrimSpace(config.DataDir) == "" || config.FreezeExecution == nil {
		return nil, errors.New("generator service requires candidate data directory and runtime snapshotter")
	}
	return &GeneratorService{
		store: store, plans: plans, workspace: workspace, sandboxes: sandboxes,
		dataDir: strings.TrimSpace(config.DataDir), freeze: config.FreezeExecution,
		now: func() time.Time { return time.Now().UTC() }, cleanupTTL: 10 * time.Second,
	}, nil
}

// SetGenerationPlan is used by clients that do not already own a web
// Authoring conversation. A missing session ID creates a normal Authoring
// session first; all later revisions use the usual optimistic Plan boundary.
func (s *GeneratorService) SetGenerationPlan(ctx context.Context, userID, sessionID string, expectedRevision int64, plan authoring.Plan) (*authoring.Session, *authoring.Revision, error) {
	if s == nil || s.store == nil || s.plans == nil {
		return nil, nil, errors.New("generator service is not configured")
	}
	if strings.TrimSpace(userID) == "" || expectedRevision < 0 {
		return nil, nil, errors.New("generation plan requires user and non-negative expected revision")
	}
	if err := plan.ValidateForGeneration(); err != nil {
		return nil, nil, err
	}
	var session *authoring.Session
	var err error
	if strings.TrimSpace(sessionID) == "" {
		if expectedRevision != 0 {
			return nil, nil, authoring.ErrVersionConflict
		}
		session, err = s.plans.CreateAuthoringSession(ctx, authoring.Session{ID: authoring.NewID("author"), UserID: userID}, authoring.Plan{})
		if err != nil {
			return nil, nil, err
		}
	} else {
		session, err = s.plans.GetAuthoringSession(ctx, strings.TrimSpace(sessionID), userID)
		if err != nil {
			return nil, nil, err
		}
	}
	revision, err := s.plans.ReplaceAuthoringPlan(ctx, session.ID, userID, expectedRevision, plan, authoring.StateIntentReview)
	if err != nil {
		return nil, nil, err
	}
	updated, err := s.plans.GetAuthoringSession(ctx, session.ID, userID)
	if err != nil {
		return nil, nil, err
	}
	return updated, revision, nil
}

func (s *GeneratorService) ConfirmGeneration(ctx context.Context, userID, sessionID string, confirmation domain.StartConfirmation) (*domain.Workflow, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("generator service is not configured")
	}
	return s.store.CreateGenerationWorkflow(ctx, strings.TrimSpace(sessionID), strings.TrimSpace(userID), confirmation, s.now())
}

func (s *GeneratorService) GetGeneration(ctx context.Context, userID, workflowID string) (*GenerationView, error) {
	workflow, err := s.ownedWorkflow(ctx, userID, workflowID)
	if err != nil {
		return nil, err
	}
	view := &GenerationView{Workflow: *workflow}
	if workflow.CandidateRevisionID == "" {
		return view, nil
	}
	candidateRevision, err := s.store.GetCandidateRevision(ctx, workflow.CandidateRevisionID)
	if err != nil {
		return nil, err
	}
	if candidateRevision.Source != workflow.Source || candidateRevision.SourceRevision != workflow.SourceRevision {
		return nil, errors.New("generation candidate does not belong to workflow")
	}
	view.Candidate = candidateRevision
	return view, nil
}

func (s *GeneratorService) ListActiveGenerations(ctx context.Context, userID string) ([]domain.Workflow, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("generator service is not configured")
	}
	return s.store.ListGenerationWorkflowsForUser(ctx, strings.TrimSpace(userID))
}

// StartWorkspaceTurn creates the Server-owned workspace if needed and binds
// exactly one explicit Generator turn as its writer. A replacement workspace
// after Server recovery receives only the last submitted candidate archive.
func (s *GeneratorService) StartWorkspaceTurn(ctx context.Context, userID string, turn domain.WorkspaceTurn) error {
	workflow, err := s.generatingWorkflow(ctx, userID, turn.WorkflowID)
	if err != nil {
		return err
	}
	if !turn.Valid() {
		return domain.ErrWorkspaceTurnLost
	}
	seed, err := s.workspaceSeed(ctx, *workflow)
	if err != nil {
		return err
	}
	if _, _, err := s.workspace.EnsureFresh(ctx, workflow.ID, seed); err != nil {
		return err
	}
	_, err = s.workspace.repo.AcquireGeneratorWorkspaceTurn(ctx, turn, s.now())
	return err
}

func (s *GeneratorService) EndWorkspaceTurn(ctx context.Context, userID string, turn domain.WorkspaceTurn) error {
	if _, err := s.generatingWorkspace(ctx, userID, turn); err != nil {
		return err
	}
	return s.workspace.repo.ReleaseGeneratorWorkspaceTurn(ctx, turn, s.now())
}

func (s *GeneratorService) ListWorkspaceFiles(ctx context.Context, userID string, turn domain.WorkspaceTurn) ([]domain.WorkspaceFile, error) {
	record, err := s.generatingWorkspace(ctx, userID, turn)
	if err != nil {
		return nil, err
	}
	files, err := s.sandboxes.ListWorkspaceFiles(ctx, record.SandboxID)
	if err != nil {
		s.releaseFailedTurn(turn)
		return nil, err
	}
	return files, nil
}

func (s *GeneratorService) ReadWorkspaceFile(ctx context.Context, userID string, turn domain.WorkspaceTurn, path string, offset, limit int) (FileReadResponse, error) {
	record, err := s.generatingWorkspace(ctx, userID, turn)
	if err != nil {
		return FileReadResponse{}, err
	}
	path, err = workspacePath(path)
	if err != nil {
		s.releaseFailedTurn(turn)
		return FileReadResponse{}, err
	}
	content, err := s.sandboxes.ReadFile(ctx, record.SandboxID, path)
	if err != nil {
		s.releaseFailedTurn(turn)
		return FileReadResponse{}, err
	}
	return FileReadResponse{Content: selectWorkspaceLines(string(content), offset, limit)}, nil
}

func (s *GeneratorService) WriteWorkspaceFile(ctx context.Context, userID string, turn domain.WorkspaceTurn, path, content string) error {
	record, err := s.generatingWorkspace(ctx, userID, turn)
	if err != nil {
		return err
	}
	path, err = workspacePath(path)
	if err != nil {
		s.releaseFailedTurn(turn)
		return err
	}
	if err := s.sandboxes.WriteFile(ctx, record.SandboxID, path, []byte(content), 0o644); err != nil {
		s.releaseFailedTurn(turn)
		return err
	}
	return nil
}

func (s *GeneratorService) RunWorkspaceCommand(ctx context.Context, userID string, turn domain.WorkspaceTurn, command string, consume func(ExecuteEvent) error) error {
	record, err := s.generatingWorkspace(ctx, userID, turn)
	if err != nil {
		return err
	}
	if strings.TrimSpace(command) == "" {
		s.releaseFailedTurn(turn)
		return errors.New("generator command is required")
	}
	streamed := false
	exitCode, output, err := s.sandboxes.ExecuteWorkspace(ctx, record.SandboxID, command, "/workspace", func(content string) error {
		streamed = streamed || content != ""
		if content == "" || consume == nil {
			return nil
		}
		return consume(ExecuteEvent{Type: "stdout", Content: content})
	})
	if err != nil {
		s.releaseFailedTurn(turn)
		return err
	}
	if consume == nil {
		return nil
	}
	if streamed {
		output = ""
	}
	if err := consume(ExecuteEvent{Type: "result", Content: output, ExitCode: &exitCode}); err != nil {
		s.releaseFailedTurn(turn)
		return err
	}
	return nil
}

// ArchiveWorkspace keeps archive bytes inside the Server application boundary.
// It exists for trusted Generator tools; public HTTP and MCP operations use
// SubmitCandidate instead of receiving raw candidate data.
func (s *GeneratorService) ArchiveWorkspace(ctx context.Context, userID string, turn domain.WorkspaceTurn) (ArchiveResponse, error) {
	record, err := s.generatingWorkspace(ctx, userID, turn)
	if err != nil {
		return ArchiveResponse{}, err
	}
	archive, err := s.sandboxes.ArchiveWorkspace(ctx, record.SandboxID)
	if err != nil {
		s.releaseFailedTurn(turn)
		return ArchiveResponse{}, err
	}
	return ArchiveResponse{Archive: archive}, nil
}

// SubmitCandidate freezes the bound workspace into one immutable revision,
// then lets the Server-owned Judge take over. The submission itself ends the
// workspace turn atomically with the workflow transition.
func (s *GeneratorService) SubmitCandidate(ctx context.Context, userID string, submission domain.CandidateSubmission) (*domain.Revision, error) {
	if s == nil || s.store == nil || !submission.Valid() {
		return nil, errors.New("generator candidate submission is invalid")
	}
	workflow, err := s.ownedWorkflow(ctx, userID, submission.WorkflowID)
	if err != nil {
		return nil, err
	}
	turn := domain.WorkspaceTurn{WorkflowID: submission.WorkflowID, ID: submission.TurnID}
	if workflow.Source.Kind != domain.SourceAuthoring || strings.TrimSpace(workflow.Source.Ref) == "" {
		s.releaseFailedTurn(turn)
		return nil, errors.New("generator workflow source is invalid")
	}
	if repeated, err := s.store.FindSubmittedGenerationCandidate(ctx, workflow.Source.Ref, userID, submission); err != nil || repeated != nil {
		if err != nil {
			s.releaseFailedTurn(turn)
		}
		return repeated, err
	}
	record, err := s.generatingWorkspace(ctx, userID, turn)
	if err != nil {
		return nil, err
	}
	archive, err := s.sandboxes.ArchiveWorkspace(ctx, record.SandboxID)
	if err != nil {
		s.releaseFailedTurn(turn)
		return nil, err
	}
	inspected, err := InspectCandidateArchive(archive)
	if err != nil {
		s.releaseFailedTurn(turn)
		return nil, domain.NewArtifactError("CANDIDATE_INVALID", err.Error())
	}
	snapshot, err := s.freeze(inspected.Entry)
	if err != nil {
		s.releaseFailedTurn(turn)
		return nil, domain.NewArtifactError("CANDIDATE_RUNTIME_INVALID", err.Error())
	}
	candidateID := domain.NewID("candidate-revision")
	archivePath, digest, err := candidate.SaveArchiveAtomic(s.dataDir, candidateID, inspected.Archive)
	if err != nil {
		s.releaseFailedTurn(turn)
		return nil, fmt.Errorf("persist candidate archive: %w", err)
	}
	revision, err := s.store.SubmitGenerationCandidate(ctx, workflow.Source.Ref, userID, submission, domain.Revision{
		ID: candidateID, ArchivePath: archivePath, ArchiveSHA256: digest, Snapshot: snapshot,
	}, s.now())
	if err != nil {
		// A commit may have succeeded even when its response was lost. Preserve
		// this Server-owned archive rather than risking deletion of a committed
		// candidate; a later retry resolves the durable receipt.
		s.releaseFailedTurn(turn)
		return nil, err
	}
	if revision.ID != candidateID {
		if err := removeCandidateArchive(s.dataDir, candidateID); err != nil {
			return nil, err
		}
	}
	return revision, nil
}

func (s *GeneratorService) ConfirmContent(ctx context.Context, userID string, confirmation domain.ContentConfirmation) (*domain.Workflow, error) {
	workflow, err := s.ownedWorkflow(ctx, userID, confirmation.WorkflowID)
	if err != nil {
		return nil, err
	}
	return s.store.ConfirmGenerationContent(ctx, workflow.Source.Ref, userID, confirmation, s.now())
}

func (s *GeneratorService) RequestContentChanges(ctx context.Context, userID string, request domain.ContentChangeRequest) (*domain.Workflow, error) {
	workflow, err := s.ownedWorkflow(ctx, userID, request.WorkflowID)
	if err != nil {
		return nil, err
	}
	return s.store.RequestGenerationContentChanges(ctx, workflow.Source.Ref, userID, request, s.now())
}

func (s *GeneratorService) RequestClassificationChanges(ctx context.Context, userID string, confirmation domain.ClassificationAdjustmentConfirmation) (*domain.Workflow, error) {
	workflow, err := s.ownedWorkflow(ctx, userID, confirmation.WorkflowID)
	if err != nil {
		return nil, err
	}
	return s.store.ResumeGenerationClassification(ctx, workflow.Source.Ref, userID, confirmation, s.now())
}

func (s *GeneratorService) ConfirmClassificationAndPublish(ctx context.Context, userID string, confirmation domain.PublicationConfirmation) (*domain.Workflow, error) {
	workflow, err := s.ownedWorkflow(ctx, userID, confirmation.WorkflowID)
	if err != nil {
		return nil, err
	}
	if workflow.CandidateRevisionID != confirmation.CandidateRevisionID {
		return nil, authoring.ErrVersionConflict
	}
	revision, err := s.store.GetCandidateRevision(ctx, confirmation.CandidateRevisionID)
	if err != nil {
		return nil, err
	}
	archive, err := candidate.ReadArchive(revision.ArchivePath, revision.ArchiveSHA256)
	if err != nil {
		return nil, err
	}
	inspected, err := InspectCandidateArchive(archive)
	if err != nil {
		return nil, domain.NewArtifactError("CANDIDATE_INVALID", err.Error())
	}
	return s.store.BeginClassificationPublication(ctx, workflow.Source.Ref, userID, inspected.Entry.Title, confirmation, s.now())
}

func (s *GeneratorService) CancelGeneration(ctx context.Context, userID string, cancellation domain.Cancellation) (*domain.Workflow, error) {
	workflow, err := s.ownedWorkflow(ctx, userID, cancellation.WorkflowID)
	if err != nil {
		return nil, err
	}
	cancelled, err := s.store.CancelGenerationWorkflow(ctx, workflow.Source.Ref, userID, cancellation, s.now())
	if err != nil {
		return nil, err
	}
	s.retireWorkspace(cancelled.ID)
	return cancelled, nil
}

func (s *GeneratorService) ownedWorkflow(ctx context.Context, userID, workflowID string) (*domain.Workflow, error) {
	if s == nil || s.store == nil || strings.TrimSpace(userID) == "" || strings.TrimSpace(workflowID) == "" {
		return nil, authoring.ErrNotFound
	}
	return s.store.GetGenerationWorkflowForUser(ctx, strings.TrimSpace(workflowID), strings.TrimSpace(userID))
}

func (s *GeneratorService) generatingWorkflow(ctx context.Context, userID, workflowID string) (*domain.Workflow, error) {
	workflow, err := s.ownedWorkflow(ctx, userID, workflowID)
	if err != nil {
		return nil, err
	}
	if workflow.State != domain.StateGenerating {
		return nil, authoring.ErrInvalidState
	}
	return workflow, nil
}

func (s *GeneratorService) generatingWorkspace(ctx context.Context, userID string, turn domain.WorkspaceTurn) (*domain.Workspace, error) {
	if !turn.Valid() {
		return nil, domain.ErrWorkspaceTurnLost
	}
	if _, err := s.generatingWorkflow(ctx, userID, turn.WorkflowID); err != nil {
		return nil, err
	}
	record, err := s.workspace.repo.GetGeneratorWorkspaceForTurn(ctx, turn)
	if err != nil {
		return nil, err
	}
	if record.State != domain.WorkspaceActive || strings.TrimSpace(record.SandboxID) == "" {
		return nil, domain.ErrWorkspaceTurnLost
	}
	return record, nil
}

func (s *GeneratorService) workspaceSeed(ctx context.Context, workflow domain.Workflow) ([]byte, error) {
	if workflow.CandidateRevisionID == "" {
		return nil, nil
	}
	revision, err := s.store.GetCandidateRevision(ctx, workflow.CandidateRevisionID)
	if err != nil {
		return nil, err
	}
	if revision.Source != workflow.Source || revision.SourceRevision != workflow.SourceRevision {
		return nil, errors.New("generator repair candidate does not belong to workflow")
	}
	archive, err := candidate.ReadArchive(revision.ArchivePath, revision.ArchiveSHA256)
	if err != nil {
		return nil, fmt.Errorf("read generator repair archive: %w", err)
	}
	return archive, nil
}

func (s *GeneratorService) releaseFailedTurn(turn domain.WorkspaceTurn) {
	if s == nil || s.workspace == nil || !turn.Valid() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cleanupTTL)
	defer cancel()
	_ = s.workspace.repo.ReleaseGeneratorWorkspaceTurn(ctx, turn, s.now())
}

func (s *GeneratorService) retireWorkspace(workflowID string) {
	if s == nil || s.workspace == nil || strings.TrimSpace(workflowID) == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), s.cleanupTTL)
		defer cancel()
		_ = s.workspace.Retire(ctx, workflowID)
	}()
}

func removeCandidateArchive(dataDir, candidateID string) error {
	path := candidate.ArchivePath(dataDir, candidateID)
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		return fmt.Errorf("remove uncommitted candidate archive: %w", err)
	}
	return nil
}
