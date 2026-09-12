package llm

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/generation"
	"github.com/breakfix/breakfix/internal/domain/toolresult"
)

func TestAuthoringToolReturnsKnownAndUnknownFailuresAsData(t *testing.T) {
	known := &authoringTool{name: "known", run: func(context.Context, string) (string, error) {
		return "", errors.New("command rejected")
	}}
	raw, err := known.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("known tool failure escaped wrapper: %v", err)
	}
	var knownResult toolresult.Envelope
	if err := json.Unmarshal([]byte(raw), &knownResult); err != nil {
		t.Fatalf("decode known tool result: %v", err)
	}
	if knownResult.Status != toolresult.Failed || knownResult.Error != "command rejected" {
		t.Fatalf("known tool result = %#v", knownResult)
	}

	unknown := &authoringTool{name: "unknown", run: func(context.Context, string) (string, error) {
		return "", context.DeadlineExceeded
	}}
	raw, err = unknown.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("unknown tool failure escaped wrapper: %v", err)
	}
	var unknownResult toolresult.Envelope
	if err := json.Unmarshal([]byte(raw), &unknownResult); err != nil {
		t.Fatalf("decode unknown tool result: %v", err)
	}
	if unknownResult.Status != toolresult.Unknown || unknownResult.Error == "" {
		t.Fatalf("unknown tool result = %#v", unknownResult)
	}
}

func TestAuthoringGeneratorToolsConfirmPlanThenSubmitCandidate(t *testing.T) {
	service := newAuthoringGeneratorToolsService()
	conversation := newAuthoringGeneratorConversation(service)
	ctx := context.Background()

	if _, err := conversation.confirmGeneration(ctx, `{"plan_revision":4}`); err != nil {
		t.Fatalf("confirm persisted plan revision: %v", err)
	}
	if len(service.confirmations) != 1 {
		t.Fatalf("generation confirmations = %#v", service.confirmations)
	}
	confirmation := service.confirmations[0]
	if confirmation.userID != "author-one" || confirmation.sessionID != "authoring-session" || confirmation.value.PlanRevision != 4 || !strings.HasPrefix(confirmation.value.IdempotencyKey, "authoring-confirm-generation-") {
		t.Fatalf("generation confirmation = %#v", confirmation)
	}

	if _, err := conversation.writeWorkspaceFile(ctx, `{"workflow_id":"workflow-one","path":"scenario.yaml","content":"title: test\n"}`); err != nil {
		t.Fatalf("write workspace file: %v", err)
	}
	if len(service.started) != 1 || service.started[0] != (generation.WorkspaceTurn{WorkflowID: "workflow-one", ID: "authoring-run"}) {
		t.Fatalf("workspace turns = %#v", service.started)
	}
	if len(service.writes) != 1 || service.writes[0].path != "scenario.yaml" || service.writes[0].content != "title: test\n" {
		t.Fatalf("workspace writes = %#v", service.writes)
	}

	commandResult, err := conversation.runWorkspaceCommand(ctx, `{"workflow_id":"workflow-one","command":"validate-candidate"}`)
	if err != nil {
		t.Fatalf("run workspace command: %v", err)
	}
	var envelope toolresult.Envelope
	if err := json.Unmarshal([]byte(commandResult), &envelope); err != nil {
		t.Fatalf("decode command envelope: %v", err)
	}
	if envelope.Status != toolresult.Succeeded {
		t.Fatalf("command envelope = %#v", envelope)
	}
	var command struct {
		ExitCode int    `json:"exit_code"`
		Output   string `json:"output"`
	}
	if err := json.Unmarshal(envelope.Data, &command); err != nil {
		t.Fatalf("decode command result: %v", err)
	}
	if command.ExitCode != 0 || command.Output != "candidate is valid\n" {
		t.Fatalf("command result = %#v", command)
	}

	if _, err := conversation.submitCandidate(ctx, `{"workflow_id":"workflow-one"}`); err != nil {
		t.Fatalf("submit candidate: %v", err)
	}
	if len(service.submissions) != 1 {
		t.Fatalf("candidate submissions = %#v", service.submissions)
	}
	submission := service.submissions[0]
	if submission.WorkflowID != "workflow-one" || submission.TurnID != "authoring-run" || !strings.HasPrefix(submission.IdempotencyKey, "authoring-submit-candidate-") {
		t.Fatalf("candidate submission = %#v", submission)
	}
	if conversation.turn != nil {
		t.Fatalf("submitted conversation retained workspace turn %#v", conversation.turn)
	}
	conversation.releaseWorkspaceTurn()
	if len(service.ended) != 0 {
		t.Fatalf("submit candidate should own turn release, explicit releases = %#v", service.ended)
	}
}

func TestAuthoringGeneratorToolsAdvanceReviewAndReleaseUnsubmittedWorkspace(t *testing.T) {
	service := newAuthoringGeneratorToolsService()
	service.candidate = &generation.Revision{ID: "candidate-one"}
	conversation := newAuthoringGeneratorConversation(service)
	ctx := context.Background()

	if _, err := conversation.listWorkspaceFiles(ctx, `{"workflow_id":"workflow-one"}`); err != nil {
		t.Fatalf("list workspace files: %v", err)
	}
	conversation.releaseWorkspaceTurn()
	if len(service.ended) != 1 || service.ended[0] != (generation.WorkspaceTurn{WorkflowID: "workflow-one", ID: "authoring-run"}) {
		t.Fatalf("unsubmitted workspace release = %#v", service.ended)
	}

	if _, err := conversation.confirmContent(ctx, `{"workflow_id":"workflow-one","candidate_revision_id":"candidate-one"}`); err != nil {
		t.Fatalf("confirm content: %v", err)
	}
	if service.contentConfirmation.WorkflowID != "workflow-one" || service.contentConfirmation.CandidateRevisionID != "candidate-one" ||
		!strings.HasPrefix(service.contentConfirmation.IdempotencyKey, "authoring-confirm-content-") {
		t.Fatalf("content confirmation = %#v", service.contentConfirmation)
	}

}

func newAuthoringGeneratorConversation(service *authoringGeneratorToolsService) *runtimeConversation {
	return &runtimeConversation{
		runID: "authoring-run", userID: "author-one", sessionID: "authoring-session", userMessageID: "authoring-message",
		generator: service,
		stage:     authoring.Stage{BaseRevision: 4},
	}
}

type authoringGeneratorToolsService struct {
	workflow  generation.Workflow
	candidate *generation.Revision

	confirmations       []authoringGenerationConfirmation
	started             []generation.WorkspaceTurn
	ended               []generation.WorkspaceTurn
	writes              []authoringWorkspaceWrite
	submissions         []generation.CandidateSubmission
	contentConfirmation generation.ContentConfirmation
	cancellation        generation.Cancellation
}

type authoringGenerationConfirmation struct {
	userID    string
	sessionID string
	value     generation.StartConfirmation
}

type authoringWorkspaceWrite struct {
	turn    generation.WorkspaceTurn
	path    string
	content string
}

func newAuthoringGeneratorToolsService() *authoringGeneratorToolsService {
	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	return &authoringGeneratorToolsService{workflow: generation.Workflow{
		ID: "workflow-one", Source: generation.Source{Kind: generation.SourceAuthoring, Ref: "authoring-session"}, SourceRevision: "4",
		State: generation.StateGenerating, StateVersion: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	}}
}

func (s *authoringGeneratorToolsService) ConfirmGeneration(_ context.Context, userID, sessionID string, value generation.StartConfirmation) (*generation.Workflow, error) {
	s.confirmations = append(s.confirmations, authoringGenerationConfirmation{userID: userID, sessionID: sessionID, value: value})
	workflow := s.workflow
	return &workflow, nil
}

func (s *authoringGeneratorToolsService) GetGenerationWorkflow(_ context.Context, _ string, _ string) (*generation.Workflow, error) {
	workflow := s.workflow
	return &workflow, nil
}

func (s *authoringGeneratorToolsService) GetGenerationCandidate(_ context.Context, _ string, _ string) (*generation.Revision, error) {
	if s.candidate == nil {
		return nil, nil
	}
	candidate := *s.candidate
	return &candidate, nil
}

func (s *authoringGeneratorToolsService) ListActiveGenerations(_ context.Context, _ string) ([]generation.Workflow, error) {
	return []generation.Workflow{s.workflow}, nil
}

func (s *authoringGeneratorToolsService) StartWorkspaceTurn(_ context.Context, _ string, turn generation.WorkspaceTurn) error {
	s.started = append(s.started, turn)
	return nil
}

func (s *authoringGeneratorToolsService) EndWorkspaceTurn(_ context.Context, _ string, turn generation.WorkspaceTurn) error {
	s.ended = append(s.ended, turn)
	return nil
}

func (*authoringGeneratorToolsService) ListWorkspaceFiles(context.Context, string, generation.WorkspaceTurn) ([]generation.WorkspaceFile, error) {
	return []generation.WorkspaceFile{{Path: "scenario.yaml", Size: 12}}, nil
}

func (*authoringGeneratorToolsService) ReadWorkspaceContent(context.Context, string, generation.WorkspaceTurn, string, int, int) (string, error) {
	return "title: test\n", nil
}

func (s *authoringGeneratorToolsService) WriteWorkspaceFile(_ context.Context, _ string, turn generation.WorkspaceTurn, path, content string) error {
	s.writes = append(s.writes, authoringWorkspaceWrite{turn: turn, path: path, content: content})
	return nil
}

func (*authoringGeneratorToolsService) ExecuteWorkspaceCommand(context.Context, string, generation.WorkspaceTurn, string) (toolresult.Envelope, error) {
	return toolresult.WithData(toolresult.Succeeded, struct {
		WorkflowID string `json:"workflow_id"`
		ExitCode   int    `json:"exit_code"`
		Output     string `json:"output"`
	}{WorkflowID: "workflow-one", ExitCode: 0, Output: "candidate is valid\n"}, "")
}

func (s *authoringGeneratorToolsService) SubmitCandidate(_ context.Context, _ string, submission generation.CandidateSubmission) (*generation.Revision, error) {
	s.submissions = append(s.submissions, submission)
	s.candidate = &generation.Revision{ID: "candidate-one"}
	candidate := *s.candidate
	return &candidate, nil
}

func (s *authoringGeneratorToolsService) ConfirmContent(_ context.Context, _ string, confirmation generation.ContentConfirmation) (*generation.Workflow, error) {
	s.contentConfirmation = confirmation
	workflow := s.workflow
	workflow.State = generation.StateScenarioPublishing
	return &workflow, nil
}

func (*authoringGeneratorToolsService) RequestContentChanges(_ context.Context, _ string, _ generation.ContentChangeRequest) (*generation.Workflow, error) {
	workflow := generation.Workflow{ID: "workflow-one", State: generation.StateGenerating, StateVersion: 2}
	return &workflow, nil
}

func (s *authoringGeneratorToolsService) CancelGeneration(_ context.Context, _ string, cancellation generation.Cancellation) (*generation.Workflow, error) {
	s.cancellation = cancellation
	workflow := s.workflow
	workflow.State = generation.StateCancelled
	return &workflow, nil
}
