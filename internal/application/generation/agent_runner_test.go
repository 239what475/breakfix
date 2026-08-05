package generation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

func TestAgentRunnerRetriesKnownTechnicalErrorWithinOneRun(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	workflow := testAgentWorkflow("workflow-retry", domain.StateGenerating)
	store := &agentRunnerStore{claim: domain.Claim{
		Workflow:        workflow,
		LeaseCredential: domain.LeaseCredential{StateVersion: 1, LeaseOwner: "server-lease"},
	}}
	runner := newTestAgentRunner(t, store, &failingGeneratorExecutor{calls: &store.generatorCalls})
	runner.now = func() time.Time { return now }

	processed, err := runner.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("process generation agent: %v", err)
	}
	if !processed {
		t.Fatal("generation agent did not claim work")
	}
	if store.generatorCalls != agent.MaxAttempts {
		t.Fatalf("generator calls = %d, want %d", store.generatorCalls, agent.MaxAttempts)
	}
	if store.retryCalls != agent.MaxAttempts {
		t.Fatalf("technical retries = %d, want %d", store.retryCalls, agent.MaxAttempts)
	}
	if store.run.Attempt != agent.MaxAttempts {
		t.Fatalf("final logical run attempt = %d, want %d", store.run.Attempt, agent.MaxAttempts)
	}
}

func TestAgentRunnerRecoveryReplacesInterruptedGeneratorWorkspace(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	repo := &memoryWorkspaceRepository{}
	pvcs := &memoryWorkspacePVCs{}
	sandboxes := &memoryWorkspaceSandboxes{nextID: "sandbox-before-restart"}
	manager := newWorkspaceManager(t, repo, pvcs, sandboxes, &now)
	first, err := manager.Ensure(context.Background(), "workflow-recovery", []byte("candidate-before-restart"))
	if err != nil {
		t.Fatalf("create original workspace: %v", err)
	}

	store := &agentRunnerStore{interrupted: []domain.InterruptedAgentRun{{
		WorkflowID: "workflow-recovery",
		State:      domain.StateGenerating,
	}}}
	runner := newTestAgentRunnerWithWorkspace(t, store, &failingGeneratorExecutor{}, manager)
	runner.now = func() time.Time { return now }
	if err := runner.Recover(context.Background()); err != nil {
		t.Fatalf("recover generation agent runtime: %v", err)
	}

	retired, err := repo.GetGeneratorWorkspace(context.Background(), first.ID)
	if err != nil {
		t.Fatalf("read retired workspace: %v", err)
	}
	if retired.State != domain.WorkspaceDeleting {
		t.Fatalf("interrupted workspace state = %s, want deleting", retired.State)
	}
	sandboxes.nextID = "sandbox-after-restart"
	replacement, err := manager.Ensure(context.Background(), "workflow-recovery", []byte("candidate-from-durable-facts"))
	if err != nil {
		t.Fatalf("create replacement workspace: %v", err)
	}
	if replacement.ID == first.ID || replacement.PVCName == first.PVCName || replacement.SandboxID != "sandbox-after-restart" {
		t.Fatalf("replacement workspace = %#v, original = %#v", replacement, first)
	}
}

func newTestAgentRunner(t *testing.T, store *agentRunnerStore, executor GeneratorRoleExecutor) *AgentRunner {
	t.Helper()
	return newTestAgentRunnerWithWorkspace(t, store, executor, newWorkspaceManager(t, &memoryWorkspaceRepository{}, &memoryWorkspacePVCs{}, &memoryWorkspaceSandboxes{}, new(time.Time)))
}

func newTestAgentRunnerWithWorkspace(t *testing.T, store *agentRunnerStore, executor GeneratorRoleExecutor, workspace *Manager) *AgentRunner {
	t.Helper()
	runner, err := NewAgentRunner(store, executor, stubClassifierExecutor{}, workspace, AgentRunnerConfig{
		ServerID: "server-test",
		Model:    "test-model",
		DataDir:  t.TempDir(),
		FreezeExecution: func(challenge.Entry) (domain.ExecutionSnapshot, error) {
			return domain.ExecutionSnapshot{}, nil
		},
	})
	if err != nil {
		t.Fatalf("create generation agent runner: %v", err)
	}
	return runner
}

type failingGeneratorExecutor struct{ calls *int }

func (e *failingGeneratorExecutor) Generate(context.Context, domain.Execution) ([]byte, error) {
	if e.calls != nil {
		*e.calls = *e.calls + 1
	}
	return nil, errors.New("model transport unavailable")
}

func (*failingGeneratorExecutor) Judge(context.Context, authoring.Plan, *Candidate) (Judgement, error) {
	return Judgement{}, errors.New("unexpected judge execution")
}

type stubClassifierExecutor struct{}

func (stubClassifierExecutor) Classify(context.Context, domain.Execution, *Candidate) (ClassificationCompletion, error) {
	return ClassificationCompletion{}, errors.New("unexpected classifier execution")
}

type agentRunnerStore struct {
	claim          domain.Claim
	claimed        bool
	run            agent.Run
	generatorCalls int
	retryCalls     int
	interrupted    []domain.InterruptedAgentRun
}

func (s *agentRunnerStore) ClaimGenerationAgentWorkflow(context.Context, string, time.Duration, time.Time) (*domain.Claim, error) {
	if s.claimed || !s.claim.Valid() {
		return nil, nil
	}
	s.claimed = true
	claim := s.claim
	return &claim, nil
}

func (s *agentRunnerStore) GetGenerationClaim(context.Context, string, domain.LeaseCredential, time.Time) (*domain.Claim, error) {
	claim := s.claim
	return &claim, nil
}

func (s *agentRunnerStore) LoadGenerationContext(context.Context, domain.Claim, time.Time) (*domain.Context, error) {
	return &domain.Context{Workflow: s.claim.Workflow, Plan: authoring.Plan{}}, nil
}

func (s *agentRunnerStore) StartGenerationAgentRun(_ context.Context, claim domain.Claim, input agent.CreateRun, now time.Time) (*agent.Run, error) {
	s.run = agent.Run{
		ID: input.ID, Purpose: input.Purpose, OwnerKind: input.OwnerKind, OwnerRef: input.OwnerRef,
		Status: agent.RunRunning, Attempt: 1, DeadlineAt: now.Add(time.Hour),
	}
	claim.Workflow.ActiveAgentRunID = s.run.ID
	return &s.run, nil
}

func (s *agentRunnerStore) RetryGenerationAgentRun(_ context.Context, _ domain.Claim, _ string, _ string, _ time.Time) (*agent.Run, error) {
	s.retryCalls++
	if s.run.Attempt >= agent.MaxAttempts {
		return nil, nil
	}
	s.run.Attempt++
	return &s.run, nil
}

func (s *agentRunnerStore) InterruptActiveGenerationAgentRuns(context.Context, string, time.Time) ([]domain.InterruptedAgentRun, error) {
	return append([]domain.InterruptedAgentRun(nil), s.interrupted...), nil
}

func (s *agentRunnerStore) GetCandidateRevision(context.Context, string) (*domain.Revision, error) {
	return nil, errors.New("unexpected candidate read")
}

func (s *agentRunnerStore) FinalizeGeneratedCandidate(context.Context, domain.Claim, string, domain.Revision, time.Time) error {
	return errors.New("unexpected generated candidate finalization")
}

func (s *agentRunnerStore) FinalizeGenerationJudgement(context.Context, domain.Claim, string, bool, string, time.Time) error {
	return errors.New("unexpected judgement finalization")
}

func (s *agentRunnerStore) FinalizeGenerationClassification(context.Context, domain.Claim, string, domain.ClassificationOutput, time.Time) error {
	return errors.New("unexpected classification finalization")
}

func (s *agentRunnerStore) FinalizeGenerationClassificationAdjustment(context.Context, domain.Claim, domain.ClassificationAdjustment, time.Time) error {
	return errors.New("unexpected classification adjustment finalization")
}

func (s *agentRunnerStore) ReportGenerationArtifactFailure(context.Context, domain.Claim, domain.WorkflowState, domain.Failure, *domain.VerificationReport, time.Time) error {
	return errors.New("unexpected artifact failure")
}

func (s *agentRunnerStore) RenewGenerationLease(context.Context, domain.Claim, time.Duration, time.Time) error {
	return nil
}

func testAgentWorkflow(id string, state domain.WorkflowState) domain.Workflow {
	return domain.Workflow{
		ID:             id,
		Source:         domain.Source{Kind: domain.SourceAuthoring, Ref: "authoring-session"},
		SourceRevision: "1",
		State:          state,
		StateVersion:   1,
		NextRunAt:      time.Now().UTC(),
	}
}
