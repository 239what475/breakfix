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
	"github.com/breakfix/breakfix/internal/content/workspacearchive"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/toolresult"
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
	DataDir           string
	FreezeExecution   ExecutionSnapshotter
	SnapshotRequested func(string)
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
	store             GeneratorStore
	plans             GeneratorPlanStore
	workspace         *Manager
	sandboxes         GeneratorWorkspaceTools
	dataDir           string
	snapshots         *workspacearchive.Store
	freeze            ExecutionSnapshotter
	now               func() time.Time
	cleanupTTL        time.Duration
	snapshotRequested func(string)
}

const workspaceSnapshotWaitInterval = 25 * time.Millisecond

func NewGeneratorService(store GeneratorStore, plans GeneratorPlanStore, workspace *Manager, sandboxes GeneratorWorkspaceTools, config GeneratorServiceConfig) (*GeneratorService, error) {
	if store == nil || plans == nil || workspace == nil || sandboxes == nil {
		return nil, errors.New("generator service requires stores, workspace manager, and sandbox tools")
	}
	if strings.TrimSpace(config.DataDir) == "" || config.FreezeExecution == nil {
		return nil, errors.New("generator service requires candidate data directory and runtime snapshotter")
	}
	snapshots, err := workspacearchive.NewStore(config.DataDir)
	if err != nil {
		return nil, err
	}
	if config.SnapshotRequested == nil {
		config.SnapshotRequested = func(string) {}
	}
	return &GeneratorService{
		store: store, plans: plans, workspace: workspace, sandboxes: sandboxes,
		dataDir: strings.TrimSpace(config.DataDir), snapshots: snapshots, freeze: config.FreezeExecution,
		now: func() time.Time { return time.Now().UTC() }, cleanupTTL: 10 * time.Second, snapshotRequested: config.SnapshotRequested,
	}, nil
}

// SetGenerationPlan is used by clients that do not already own a web
// Authoring conversation. A missing session ID creates a normal Authoring
// session first; all later revisions use the usual optimistic Plan boundary.
func (s *GeneratorService) SetGenerationPlan(ctx context.Context, userID, sessionID string, expectedRevision int64, idempotencyKey string, plan authoring.Plan) (*authoring.Session, *authoring.Revision, error) {
	if s == nil || s.store == nil || s.plans == nil {
		return nil, nil, errors.New("generator service is not configured")
	}
	if strings.TrimSpace(userID) == "" || expectedRevision < 0 || strings.TrimSpace(idempotencyKey) == "" || len(strings.TrimSpace(idempotencyKey)) > 200 {
		return nil, nil, errors.New("generation plan requires user, non-negative expected revision, and idempotency key")
	}
	if err := plan.ValidateForGeneration(); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(sessionID) == "" && expectedRevision != 0 {
		return nil, nil, authoring.ErrVersionConflict
	}
	return s.plans.SaveGenerationPlan(
		ctx,
		strings.TrimSpace(userID),
		strings.TrimSpace(sessionID),
		authoring.NewID("author"),
		expectedRevision,
		strings.TrimSpace(idempotencyKey),
		plan,
	)
}

func (s *GeneratorService) ConfirmGeneration(ctx context.Context, userID, sessionID string, confirmation domain.StartConfirmation) (*domain.Workflow, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("generator service is not configured")
	}
	return s.store.CreateGenerationWorkflow(ctx, strings.TrimSpace(sessionID), strings.TrimSpace(userID), confirmation, s.now())
}

func (s *GeneratorService) GetGeneration(ctx context.Context, userID, workflowID string) (*GenerationView, error) {
	workflow, err := s.GetGenerationWorkflow(ctx, userID, workflowID)
	if err != nil {
		return nil, err
	}
	view := &GenerationView{Workflow: *workflow}
	candidateRevision, err := s.GetGenerationCandidate(ctx, userID, workflowID)
	if err != nil {
		return nil, err
	}
	view.Candidate = candidateRevision
	return view, nil
}

// GetGenerationWorkflow returns one user-owned workflow without exposing a
// provider, archive path, or workspace identity.
func (s *GeneratorService) GetGenerationWorkflow(ctx context.Context, userID, workflowID string) (*domain.Workflow, error) {
	return s.ownedWorkflow(ctx, userID, workflowID)
}

// GetGenerationCandidate returns the current immutable candidate for an owned
// workflow. A workflow that has not received a submission has no candidate.
func (s *GeneratorService) GetGenerationCandidate(ctx context.Context, userID, workflowID string) (*domain.Revision, error) {
	workflow, err := s.ownedWorkflow(ctx, userID, workflowID)
	if err != nil {
		return nil, err
	}
	if workflow.CandidateRevisionID == "" {
		return nil, nil
	}
	candidateRevision, err := s.store.GetCandidateRevision(ctx, workflow.CandidateRevisionID)
	if err != nil {
		return nil, err
	}
	if candidateRevision.Source != workflow.Source || candidateRevision.SourceRevision != workflow.SourceRevision {
		return nil, errors.New("generation candidate does not belong to workflow")
	}
	return candidateRevision, nil
}

func (s *GeneratorService) ListActiveGenerations(ctx context.Context, userID string) ([]domain.Workflow, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("generator service is not configured")
	}
	return s.store.ListGenerationWorkflowsForUser(ctx, strings.TrimSpace(userID))
}

// StartWorkspaceTurn creates the Server-owned workspace if needed and binds
// exactly one explicit Generator turn as its writer. A replacement workspace
// after Server recovery restores its latest durable snapshot, then falls back
// to the last submitted candidate archive when no snapshot remains.
func (s *GeneratorService) StartWorkspaceTurn(ctx context.Context, userID string, turn domain.WorkspaceTurn) error {
	if !turn.Valid() {
		return domain.ErrWorkspaceTurnLost
	}
	// Idle retirement and turn acquisition share the workspace row lock, but
	// provisioning necessarily happens before acquiring that lock. If
	// retirement wins in that small window, repeat once to allocate the next
	// workspace identity and seed it from the durable snapshot.
	for pass := 0; pass < 2; pass++ {
		workflow, err := s.generatingWorkflow(ctx, userID, turn.WorkflowID)
		if err != nil {
			return err
		}
		seed, err := s.workspaceSeed(ctx, userID, *workflow)
		if err != nil {
			return err
		}
		if _, _, err := s.workspace.EnsureFresh(ctx, workflow.ID, seed); err != nil {
			return err
		}
		err = s.acquireWorkspaceTurn(ctx, turn)
		if !errors.Is(err, domain.ErrWorkspaceNotFound) || pass == 1 {
			return err
		}
	}
	return domain.ErrWorkspaceNotFound
}

// acquireWorkspaceTurn gives an interactive writer priority over the
// Server-owned snapshotter. A snapshot holder is short lived and does not
// represent a competing user turn, so returning a conflict here would expose
// a background implementation detail to Authoring and MCP clients.
func (s *GeneratorService) acquireWorkspaceTurn(ctx context.Context, turn domain.WorkspaceTurn) error {
	for {
		if _, err := s.workspace.repo.AcquireGeneratorWorkspaceTurn(ctx, turn, s.now()); err == nil || !errors.Is(err, domain.ErrWorkspaceBusy) {
			return err
		}
		current, err := s.workspace.repo.GetCurrentGeneratorWorkspace(ctx, turn.WorkflowID)
		if err != nil {
			return err
		}
		if current.ActiveTurnID == "" {
			continue
		}
		if !domain.IsWorkspaceSnapshotHolder(current.ActiveTurnID) {
			return domain.ErrWorkspaceBusy
		}
		timer := time.NewTimer(workspaceSnapshotWaitInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *GeneratorService) EndWorkspaceTurn(ctx context.Context, userID string, turn domain.WorkspaceTurn) error {
	if _, err := s.generatingWorkspace(ctx, userID, turn); err != nil {
		return err
	}
	if err := s.workspace.repo.ReleaseGeneratorWorkspaceTurn(ctx, turn, s.now()); err != nil {
		return err
	}
	s.snapshotRequested(turn.WorkflowID)
	return nil
}

func (s *GeneratorService) ListWorkspaceFiles(ctx context.Context, userID string, turn domain.WorkspaceTurn) ([]domain.WorkspaceFile, error) {
	record, err := s.generatingWorkspace(ctx, userID, turn)
	if err != nil {
		return nil, err
	}
	files, err := s.sandboxes.ListWorkspaceFiles(ctx, record.SandboxID)
	if err != nil {
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
		return FileReadResponse{}, err
	}
	content, err := s.sandboxes.ReadFile(ctx, record.SandboxID, path)
	if err != nil {
		return FileReadResponse{}, err
	}
	return FileReadResponse{Content: selectWorkspaceLines(string(content), offset, limit)}, nil
}

// ReadWorkspaceContent is the narrow string result used by interactive Agent
// tools. It keeps the line-selection policy in GeneratorService rather than
// asking an adapter to reconstruct a provider file read.
func (s *GeneratorService) ReadWorkspaceContent(ctx context.Context, userID string, turn domain.WorkspaceTurn, path string, offset, limit int) (string, error) {
	value, err := s.ReadWorkspaceFile(ctx, userID, turn, path, offset, limit)
	if err != nil {
		return "", err
	}
	return value.Content, nil
}

func (s *GeneratorService) WriteWorkspaceFile(ctx context.Context, userID string, turn domain.WorkspaceTurn, path, content string) error {
	record, err := s.generatingWorkspace(ctx, userID, turn)
	if err != nil {
		return err
	}
	path, err = workspacePath(path)
	if err != nil {
		return err
	}
	if err := s.sandboxes.WriteFile(ctx, record.SandboxID, path, []byte(content), 0o644); err != nil {
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
		return err
	}
	if consume == nil {
		return nil
	}
	if streamed {
		output = ""
	}
	if err := consume(ExecuteEvent{Type: "result", Content: output, ExitCode: &exitCode}); err != nil {
		return err
	}
	return nil
}

// ExecuteWorkspaceCommand collects one command's streamed and final output
// for a function tool response. The provider execution remains inside
// GeneratorService and the caller never receives a sandbox identity.
func (s *GeneratorService) ExecuteWorkspaceCommand(ctx context.Context, userID string, turn domain.WorkspaceTurn, command string) (toolresult.Envelope, error) {
	var output strings.Builder
	exitCode := 0
	err := s.RunWorkspaceCommand(ctx, userID, turn, command, func(event ExecuteEvent) error {
		output.WriteString(event.Content)
		if event.ExitCode != nil {
			exitCode = *event.ExitCode
		}
		return nil
	})
	if err != nil {
		// A provider failure is a tool result, not an Eino execution failure.
		// The workspace turn remains held so the web Agent can inspect it before
		// choosing another command. MCP releases its short turn in its caller.
		result, marshalErr := toolresult.WithData(toolresult.StatusForError(err), WorkspaceCommand{
			WorkflowID: turn.WorkflowID,
			ExitCode:   exitCode,
			Output:     output.String(),
		}, toolresult.Message(err))
		return result, marshalErr
	}
	data := WorkspaceCommand{WorkflowID: turn.WorkflowID, ExitCode: exitCode, Output: output.String()}
	if exitCode != 0 {
		result, marshalErr := toolresult.WithData(toolresult.Failed, data, fmt.Sprintf("command exited with code %d", exitCode))
		return result, marshalErr
	}
	result, marshalErr := toolresult.WithData(toolresult.Succeeded, data, "")
	return result, marshalErr
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
		return ArchiveResponse{}, err
	}
	archive, err = workspacearchive.Canonicalize(archive)
	if err != nil {
		return ArchiveResponse{}, fmt.Errorf("canonicalize workspace archive: %w", err)
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
		return nil, errors.New("generator workflow source is invalid")
	}
	if repeated, err := s.store.FindSubmittedGenerationCandidate(ctx, workflow.Source.Ref, userID, submission); err != nil || repeated != nil {
		return repeated, err
	}
	record, err := s.generatingWorkspace(ctx, userID, turn)
	if err != nil {
		return nil, err
	}
	archive, err := s.sandboxes.ArchiveWorkspace(ctx, record.SandboxID)
	if err != nil {
		return nil, err
	}
	inspected, err := InspectCandidateArchive(archive)
	if err != nil {
		return nil, domain.NewArtifactError("CANDIDATE_INVALID", err.Error())
	}
	snapshot, err := s.freeze(inspected.Entry)
	if err != nil {
		return nil, domain.NewArtifactError("CANDIDATE_RUNTIME_INVALID", err.Error())
	}
	candidateID := domain.NewID("candidate-revision")
	archivePath, digest, err := candidate.SaveArchiveAtomic(s.dataDir, candidateID, inspected.Archive)
	if err != nil {
		return nil, fmt.Errorf("persist candidate archive: %w", err)
	}
	revision, err := s.store.SubmitGenerationCandidate(ctx, workflow.Source.Ref, userID, submission, domain.Revision{
		ID: candidateID, ArchivePath: archivePath, ArchiveSHA256: digest, Snapshot: snapshot,
	}, s.now())
	if err != nil {
		// A commit may have succeeded even when its response was lost. Preserve
		// this Server-owned archive rather than risking deletion of a committed
		// candidate; the caller can inspect workflow state before deciding what
		// to do next.
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

func (s *GeneratorService) workspaceSeed(ctx context.Context, userID string, workflow domain.Workflow) ([]byte, error) {
	if workflow.WorkspaceSnapshotDigest != "" {
		archive, err := s.snapshots.Read(workflow.ID, workflow.WorkspaceSnapshotDigest)
		if err == nil {
			return archive, nil
		}
		cleared, clearErr := s.workspace.repo.ClearGeneratorWorkspaceSnapshot(ctx, workflow.ID, workflow.WorkspaceSnapshotDigest, s.now())
		if clearErr != nil {
			return nil, fmt.Errorf("clear invalid generator workspace snapshot: %w", clearErr)
		}
		if !cleared {
			current, reloadErr := s.store.GetGenerationWorkflowForUser(ctx, workflow.ID, userID)
			if reloadErr != nil {
				return nil, reloadErr
			}
			if current.WorkspaceSnapshotDigest != workflow.WorkspaceSnapshotDigest {
				return s.workspaceSeed(ctx, userID, *current)
			}
		}
	}
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

func workspacePath(value string) (string, error) {
	value = strings.TrimSpace(strings.TrimPrefix(value, "./"))
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", errors.New("workspace path must be a non-empty relative slash path")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid workspace path %q", value)
		}
	}
	return "/workspace/" + value, nil
}

func selectWorkspaceLines(content string, offset, limit int) string {
	if offset < 1 {
		offset = 1
	}
	lines := strings.SplitAfter(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	start := offset - 1
	if start >= len(lines) {
		return ""
	}
	end := len(lines)
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	return strings.Join(lines[start:end], "")
}
