package generation

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

func TestGeneratorServiceCreatesPlanRevisionForExternalClient(t *testing.T) {
	store := newGeneratorServiceStore("user-one")
	plans := &generatorServicePlans{}
	service := newGeneratorServiceForTest(t, store, plans, &generatorServiceTools{})

	session, revision, err := service.SetGenerationPlan(context.Background(), "user-one", "", 0, "plan-one", generatorServicePlan())
	if err != nil {
		t.Fatalf("set external generation plan: %v", err)
	}
	if session.ID == "" || session.UserID != "user-one" || revision.Number != 1 || revision.Plan.Metadata.Title != generatorServicePlan().Metadata.Title {
		t.Fatalf("created plan = session:%#v revision:%#v", session, revision)
	}
	if plans.created != 1 || plans.replaced != 1 {
		t.Fatalf("plan store calls = create:%d replace:%d, want one each", plans.created, plans.replaced)
	}
}

func TestGeneratorServiceKeepsWorkspaceTurnAfterToolFailure(t *testing.T) {
	store := newGeneratorServiceStore("user-one")
	workflow := store.addWorkflow("workflow-one")
	tools := &generatorServiceTools{readErr: errors.New("sandbox transport unavailable")}
	service := newGeneratorServiceForTest(t, store, &generatorServicePlans{}, tools)
	first := domain.WorkspaceTurn{WorkflowID: workflow.ID, ID: "turn-one"}
	second := domain.WorkspaceTurn{WorkflowID: workflow.ID, ID: "turn-two"}

	if err := service.StartWorkspaceTurn(context.Background(), "user-one", first); err != nil {
		t.Fatalf("start first workspace turn: %v", err)
	}
	if err := service.StartWorkspaceTurn(context.Background(), "user-one", second); !errors.Is(err, domain.ErrWorkspaceBusy) {
		t.Fatalf("start concurrent workspace turn = %v, want busy", err)
	}
	if _, err := service.ReadWorkspaceFile(context.Background(), "user-one", first, "challenge.yaml", 1, 0); err == nil {
		t.Fatal("read workspace file unexpectedly succeeded")
	}

	if err := service.StartWorkspaceTurn(context.Background(), "user-one", second); !errors.Is(err, domain.ErrWorkspaceBusy) {
		t.Fatalf("start concurrent turn after tool failure = %v, want busy", err)
	}
	record, err := service.workspace.repo.GetCurrentGeneratorWorkspace(context.Background(), workflow.ID)
	if err != nil {
		t.Fatalf("read retained workspace: %v", err)
	}
	if record.State != domain.WorkspaceActive || record.ActiveTurnID != first.ID {
		t.Fatalf("workspace after failed turn = %#v", record)
	}
	if err := service.EndWorkspaceTurn(context.Background(), "user-one", first); err != nil {
		t.Fatalf("end failed workspace turn: %v", err)
	}
	if err := service.StartWorkspaceTurn(context.Background(), "user-one", second); err != nil {
		t.Fatalf("start workspace turn after explicit release: %v", err)
	}
	if err := service.RunWorkspaceCommand(context.Background(), "user-one", second, "", nil); err == nil {
		t.Fatal("empty command unexpectedly succeeded")
	}
	if err := service.StartWorkspaceTurn(context.Background(), "user-one", first); !errors.Is(err, domain.ErrWorkspaceBusy) {
		t.Fatalf("start turn after command validation failure = %v, want busy", err)
	}
}

func TestGeneratorServiceSubmitsOneImmutableCandidatePerIdempotencyKey(t *testing.T) {
	store := newGeneratorServiceStore("user-one")
	workflow := store.addWorkflow("workflow-submit")
	tools := &generatorServiceTools{archive: generatorServiceCandidateArchive(t)}
	service := newGeneratorServiceForTest(t, store, &generatorServicePlans{}, tools)
	turn := domain.WorkspaceTurn{WorkflowID: workflow.ID, ID: "turn-submit"}
	if err := service.StartWorkspaceTurn(context.Background(), "user-one", turn); err != nil {
		t.Fatalf("start workspace turn: %v", err)
	}
	submission := domain.CandidateSubmission{WorkflowID: workflow.ID, TurnID: turn.ID, IdempotencyKey: "candidate-submit-one"}
	first, err := service.SubmitCandidate(context.Background(), "user-one", submission)
	if err != nil {
		t.Fatalf("submit candidate: %v", err)
	}
	second, err := service.SubmitCandidate(context.Background(), "user-one", submission)
	if err != nil {
		t.Fatalf("repeat candidate submission: %v", err)
	}
	if first.ID == "" || first.ID != second.ID || !strings.HasPrefix(first.ID, "candidate-revision-") {
		t.Fatalf("candidate identities = first:%#v second:%#v", first, second)
	}
	if store.submitCalls != 1 || tools.archives != 1 {
		t.Fatalf("candidate submission calls = store:%d archive:%d, want one each", store.submitCalls, tools.archives)
	}
	current, err := service.GetGeneration(context.Background(), "user-one", workflow.ID)
	if err != nil {
		t.Fatalf("read submitted generation: %v", err)
	}
	if current.Workflow.State != domain.StateJudging || current.Candidate == nil || current.Candidate.ID != first.ID {
		t.Fatalf("submitted generation = %#v", current)
	}
	record, err := service.workspace.repo.GetCurrentGeneratorWorkspace(context.Background(), workflow.ID)
	if err != nil {
		t.Fatalf("read submitted workspace: %v", err)
	}
	if record.ActiveTurnID != "" {
		t.Fatalf("submitted workspace still has active turn %q", record.ActiveTurnID)
	}
}

func newGeneratorServiceForTest(t *testing.T, store *generatorServiceStore, plans *generatorServicePlans, tools *generatorServiceTools) *GeneratorService {
	t.Helper()
	now := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	workspaceRepo := &memoryWorkspaceRepository{}
	store.workspace = workspaceRepo
	manager := newWorkspaceManager(t, workspaceRepo, &memoryWorkspacePVCs{}, &memoryWorkspaceSandboxes{nextID: "sandbox-test"}, &now)
	service, err := NewGeneratorService(store, plans, manager, tools, GeneratorServiceConfig{
		DataDir: t.TempDir(),
		FreezeExecution: func(challenge.Entry) (domain.ExecutionSnapshot, error) {
			return generatorServiceSnapshot(), nil
		},
	})
	if err != nil {
		t.Fatalf("create generator service: %v", err)
	}
	service.now = func() time.Time { return now }
	return service
}

type generatorServiceStore struct {
	owner       string
	workflows   map[string]domain.Workflow
	candidates  map[string]domain.Revision
	receipts    map[string]domain.Revision
	workspace   WorkspaceRepository
	submitCalls int
}

func newGeneratorServiceStore(owner string) *generatorServiceStore {
	return &generatorServiceStore{
		owner: owner, workflows: make(map[string]domain.Workflow), candidates: make(map[string]domain.Revision), receipts: make(map[string]domain.Revision),
	}
}

func (s *generatorServiceStore) addWorkflow(id string) domain.Workflow {
	workflow := domain.Workflow{
		ID: id, Source: domain.Source{Kind: domain.SourceAuthoring, Ref: "authoring-session"}, SourceRevision: "1",
		State: domain.StateGenerating, StateVersion: 1, NextRunAt: time.Now().UTC(), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	s.workflows[id] = workflow
	return workflow
}

func (s *generatorServiceStore) CreateGenerationWorkflow(_ context.Context, sessionID, userID string, confirmation domain.StartConfirmation, now time.Time) (*domain.Workflow, error) {
	if userID != s.owner {
		return nil, authoring.ErrNotFound
	}
	key := "confirm:" + sessionID + ":" + confirmation.IdempotencyKey
	if value, ok := s.receipts[key]; ok {
		workflow := s.workflows[value.ID]
		return &workflow, nil
	}
	workflow := domain.Workflow{
		ID:     "workflow-" + fmt.Sprintf("%d", confirmation.PlanRevision),
		Source: domain.Source{Kind: domain.SourceAuthoring, Ref: sessionID}, SourceRevision: fmt.Sprintf("%d", confirmation.PlanRevision),
		State: domain.StateGenerating, StateVersion: 1, NextRunAt: now, CreatedAt: now, UpdatedAt: now,
	}
	s.workflows[workflow.ID] = workflow
	s.receipts[key] = domain.Revision{ID: workflow.ID}
	return &workflow, nil
}

func (s *generatorServiceStore) GetGenerationWorkflowForUser(_ context.Context, workflowID, userID string) (*domain.Workflow, error) {
	if userID != s.owner {
		return nil, authoring.ErrNotFound
	}
	workflow, ok := s.workflows[workflowID]
	if !ok {
		return nil, authoring.ErrNotFound
	}
	return &workflow, nil
}

func (s *generatorServiceStore) ListGenerationWorkflowsForUser(_ context.Context, userID string) ([]domain.Workflow, error) {
	if userID != s.owner {
		return nil, authoring.ErrNotFound
	}
	values := make([]domain.Workflow, 0, len(s.workflows))
	for _, workflow := range s.workflows {
		if !workflow.State.Terminal() {
			values = append(values, workflow)
		}
	}
	return values, nil
}

func (s *generatorServiceStore) GetCandidateRevision(_ context.Context, candidateID string) (*domain.Revision, error) {
	revision, ok := s.candidates[candidateID]
	if !ok {
		return nil, domain.ErrCandidateNotFound
	}
	return &revision, nil
}

func (s *generatorServiceStore) FindSubmittedGenerationCandidate(_ context.Context, sessionID, userID string, submission domain.CandidateSubmission) (*domain.Revision, error) {
	if userID != s.owner {
		return nil, authoring.ErrNotFound
	}
	value, ok := s.receipts["submit:"+sessionID+":"+submission.IdempotencyKey]
	if !ok {
		return nil, nil
	}
	if value.ID == "" || value.Source.Ref != sessionID {
		return nil, authoring.ErrVersionConflict
	}
	return &value, nil
}

func (s *generatorServiceStore) SubmitGenerationCandidate(ctx context.Context, sessionID, userID string, submission domain.CandidateSubmission, revision domain.Revision, now time.Time) (*domain.Revision, error) {
	if userID != s.owner {
		return nil, authoring.ErrNotFound
	}
	key := "submit:" + sessionID + ":" + submission.IdempotencyKey
	if existing, ok := s.receipts[key]; ok {
		return &existing, nil
	}
	workflow, ok := s.workflows[submission.WorkflowID]
	if !ok || workflow.Source.Ref != sessionID || workflow.State != domain.StateGenerating {
		return nil, authoring.ErrInvalidState
	}
	revision.Source = workflow.Source
	revision.SourceRevision = workflow.SourceRevision
	revision.CreatedAt = now
	revision.UpdatedAt = now
	if err := revision.ValidateForCreate(); err != nil {
		return nil, err
	}
	s.submitCalls++
	s.candidates[revision.ID] = revision
	s.receipts[key] = revision
	workflow.CandidateRevisionID = revision.ID
	workflow.State = domain.StateJudging
	workflow.StateVersion++
	workflow.UpdatedAt = now
	s.workflows[workflow.ID] = workflow
	if s.workspace != nil {
		if err := s.workspace.ReleaseGeneratorWorkspaceTurn(ctx, domain.WorkspaceTurn{WorkflowID: submission.WorkflowID, ID: submission.TurnID}, now); err != nil {
			return nil, err
		}
	}
	return &revision, nil
}

func (*generatorServiceStore) ConfirmGenerationContent(context.Context, string, string, domain.ContentConfirmation, time.Time) (*domain.Workflow, error) {
	return nil, errors.New("unexpected content confirmation")
}

func (*generatorServiceStore) RequestGenerationContentChanges(context.Context, string, string, domain.ContentChangeRequest, time.Time) (*domain.Workflow, error) {
	return nil, errors.New("unexpected content change request")
}

func (*generatorServiceStore) ResumeGenerationClassification(context.Context, string, string, domain.ClassificationAdjustmentConfirmation, time.Time) (*domain.Workflow, error) {
	return nil, errors.New("unexpected classification change request")
}

func (*generatorServiceStore) BeginClassificationPublication(context.Context, string, string, string, domain.PublicationConfirmation, time.Time) (*domain.Workflow, error) {
	return nil, errors.New("unexpected publication confirmation")
}

func (s *generatorServiceStore) CancelGenerationWorkflow(_ context.Context, sessionID, userID string, cancellation domain.Cancellation, now time.Time) (*domain.Workflow, error) {
	if userID != s.owner {
		return nil, authoring.ErrNotFound
	}
	workflow, ok := s.workflows[cancellation.WorkflowID]
	if !ok || workflow.Source.Ref != sessionID {
		return nil, authoring.ErrNotFound
	}
	workflow.State = domain.StateCancelled
	workflow.StateVersion++
	workflow.UpdatedAt = now
	s.workflows[workflow.ID] = workflow
	return &workflow, nil
}

type generatorServicePlans struct {
	created   int
	replaced  int
	sessions  map[string]authoring.Session
	revisions map[string]map[int64]authoring.Revision
	receipts  map[string]generatorServicePlanReceipt
}

type generatorServicePlanReceipt struct {
	sessionID string
	expected  int64
	revision  int64
}

func (s *generatorServicePlans) SaveGenerationPlan(_ context.Context, userID, sessionID, newSessionID string, expected int64, idempotencyKey string, plan authoring.Plan) (*authoring.Session, *authoring.Revision, error) {
	if s.sessions == nil {
		s.sessions = make(map[string]authoring.Session)
		s.revisions = make(map[string]map[int64]authoring.Revision)
		s.receipts = make(map[string]generatorServicePlanReceipt)
	}
	if receipt, ok := s.receipts[idempotencyKey]; ok {
		if receipt.expected != expected || (sessionID != "" && receipt.sessionID != sessionID) {
			return nil, nil, authoring.ErrVersionConflict
		}
		session := s.sessions[receipt.sessionID]
		revision := s.revisions[receipt.sessionID][receipt.revision]
		return &session, &revision, nil
	}
	if sessionID == "" {
		s.created++
		sessionID = newSessionID
		s.sessions[sessionID] = authoring.Session{ID: sessionID, UserID: userID, State: authoring.StateDraftConversation}
		s.revisions[sessionID] = map[int64]authoring.Revision{0: {Number: 0, Plan: authoring.Plan{}}}
	}
	session, ok := s.sessions[sessionID]
	if !ok || session.UserID != userID {
		return nil, nil, authoring.ErrNotFound
	}
	if session.CurrentRevision != expected {
		return nil, nil, authoring.ErrVersionConflict
	}
	s.replaced++
	revision := authoring.Revision{Number: expected + 1, Plan: plan, CreatedAt: time.Now().UTC()}
	s.revisions[sessionID][revision.Number] = revision
	session.CurrentRevision = revision.Number
	session.State = authoring.StateIntentReview
	s.sessions[sessionID] = session
	s.receipts[idempotencyKey] = generatorServicePlanReceipt{sessionID: sessionID, expected: expected, revision: revision.Number}
	return &session, &revision, nil
}

type generatorServiceTools struct {
	archive  []byte
	readErr  error
	archives int
}

func (*generatorServiceTools) ListWorkspaceFiles(context.Context, string) ([]domain.WorkspaceFile, error) {
	return []domain.WorkspaceFile{{Path: "challenge.yaml", Size: 1}}, nil
}

func (s *generatorServiceTools) ReadFile(context.Context, string, string) ([]byte, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	return []byte("content\n"), nil
}

func (*generatorServiceTools) WriteFile(context.Context, string, string, []byte, int) error {
	return nil
}

func (s *generatorServiceTools) ArchiveWorkspace(context.Context, string) ([]byte, error) {
	s.archives++
	return append([]byte(nil), s.archive...), nil
}

func (*generatorServiceTools) ExecuteWorkspace(context.Context, string, string, string, func(string) error) (int, string, error) {
	return 0, "", nil
}

func generatorServicePlan() authoring.Plan {
	return authoring.Plan{
		Metadata:    authoring.Metadata{Title: "Generator Service", Difficulty: "easy", Description: "Validate shared generation lifecycle.", Runtime: challenge.RuntimeNode},
		Overview:    "Build a small node environment and validate a durable generation service.",
		Checkpoints: []authoring.Checkpoint{{ID: "ready", Title: "Ready", Markdown: "The service is ready.", Position: 1}},
	}
}

func generatorServiceSnapshot() domain.ExecutionSnapshot {
	return domain.ExecutionSnapshot{
		Runtime:     challenge.RuntimeNode,
		Checkpoints: []domain.CheckpointSnapshot{{ID: "ready", Node: "host"}},
		Node: &domain.NodeRuntimeSnapshot{
			BaseImageFingerprint: strings.Repeat("a", 64), ProfileRevision: "node-profile", NetworkPolicyRevision: "network-profile",
			Nodes:     []domain.NodeSnapshot{{Name: "host", Title: "Host"}},
			Resources: domain.NodeResources{CPU: "1", Memory: "512MiB", Processes: 64, RootDisk: "5GiB"},
		},
	}
}

func generatorServiceCandidateArchive(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	gzipWriter := gzip.NewWriter(&data)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, file := range []struct {
		name    string
		content string
		mode    int64
	}{
		{"challenge.yaml", "runtime: node\ntitle: Generator service candidate\ndifficulty: easy\ndescription: Validate the shared generator service.\nnodes:\n  - name: host\n    title: Host\ncheckpoints:\n  - id: ready\n    title: Ready\n    description: The generated workspace is ready.\n    hint: hints/ready.md\n    node: host\n", 0o644},
		{"problem.md", "# Problem\n\nMake the workspace ready.\n", 0o644},
		{"solution.md", "# Solution\n\n<!-- checkpoint: ready -->\n", 0o644},
		{"hints/ready.md", "# Hint\n\nInspect the host state.\n", 0o644},
		{"nodes/host/generate.sh", "#!/bin/sh\nexit 0\n", 0o755},
		{"nodes/host/answer.sh", "#!/bin/sh\nexit 0\n", 0o755},
		{"nodes/host/checks.sh", "#!/bin/sh\nexit 0\n", 0o755},
	} {
		header := &tar.Header{Name: file.name, Mode: file.mode, Size: int64(len(file.content)), Typeflag: tar.TypeReg}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("write candidate tar header %s: %v", file.name, err)
		}
		if _, err := tarWriter.Write([]byte(file.content)); err != nil {
			t.Fatalf("write candidate tar file %s: %v", file.name, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close candidate tar: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close candidate gzip: %v", err)
	}
	return data.Bytes()
}
