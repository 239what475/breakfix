package generation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	domain "github.com/breakfix/breakfix/internal/domain/generation"
)

func TestAgentRunnerRetriesJudgeTechnicalErrorWithinOneRun(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	revision := testAgentCandidate(t)
	workflow := testAgentWorkflow("workflow-retry", domain.StateJudging, revision.ID)
	store := &agentRunnerStore{claim: domain.Claim{
		Workflow:        workflow,
		LeaseCredential: domain.LeaseCredential{StateVersion: 1, LeaseOwner: "server-lease"},
	}, candidate: revision}
	runner := newTestAgentRunner(t, store, &failingJudgeExecutor{calls: &store.judgeCalls})
	runner.now = func() time.Time { return now }

	processed, err := runner.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("process generation agent: %v", err)
	}
	if !processed {
		t.Fatal("generation agent did not claim work")
	}
	if store.judgeCalls != agent.MaxAttempts {
		t.Fatalf("judge calls = %d, want %d", store.judgeCalls, agent.MaxAttempts)
	}
	if store.retryCalls != agent.MaxAttempts {
		t.Fatalf("technical retries = %d, want %d", store.retryCalls, agent.MaxAttempts)
	}
	if store.run.Attempt != agent.MaxAttempts {
		t.Fatalf("final logical run attempt = %d, want %d", store.run.Attempt, agent.MaxAttempts)
	}
}

func TestAgentRunnerFinalizesJudgeResult(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	revision := testAgentCandidate(t)
	workflow := testAgentWorkflow("workflow-judge", domain.StateJudging, revision.ID)
	store := &agentRunnerStore{claim: domain.Claim{
		Workflow:        workflow,
		LeaseCredential: domain.LeaseCredential{StateVersion: 1, LeaseOwner: "server-lease"},
	}, candidate: revision}
	runner := newTestAgentRunner(t, store, approvingJudgeExecutor{})
	runner.now = func() time.Time { return now }

	processed, err := runner.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("process judge: %v", err)
	}
	if !processed || !store.judgementFinalized {
		t.Fatalf("judge processing = processed:%t finalized:%t", processed, store.judgementFinalized)
	}
}

func TestAgentRunnerDispatchesIndependentJudgeWorkflowsConcurrently(t *testing.T) {
	revision := testAgentCandidate(t)
	claims := []domain.Claim{
		{Workflow: testAgentWorkflow("workflow-a", domain.StateJudging, revision.ID), LeaseCredential: domain.LeaseCredential{StateVersion: 1, LeaseOwner: "lease-a"}},
		{Workflow: testAgentWorkflow("workflow-b", domain.StateJudging, revision.ID), LeaseCredential: domain.LeaseCredential{StateVersion: 1, LeaseOwner: "lease-b"}},
	}
	store := &dispatchStore{
		agentRunnerStore: &agentRunnerStore{candidate: revision},
		claims:           claims,
		started:          make(chan struct{}, len(claims)),
		finished:         make(chan struct{}, len(claims)),
	}
	judge := &blockingJudgeExecutor{started: store.started, finished: store.finished}
	runner := newTestAgentRunner(t, store, judge)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- runner.Run(ctx) }()

	for range claims {
		select {
		case <-store.started:
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatalf("timed out waiting for independent workflows to start")
		}
	}

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("generation runner returned error after cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("generation runner did not wait for dispatched workflows to finish")
	}
	if store.retryCalls != 0 {
		t.Fatalf("cancellation consumed technical retries = %d, want 0", store.retryCalls)
	}
	for range claims {
		select {
		case <-store.finished:
		case <-time.After(2 * time.Second):
			t.Fatal("dispatched workflow did not finish before runner returned")
		}
	}
}

func TestAgentRunnerRecoveryInterruptsInternalAgentRuns(t *testing.T) {
	runner := newTestAgentRunner(t, &agentRunnerStore{}, unexpectedJudgeExecutor{})
	if err := runner.Recover(context.Background()); err != nil {
		t.Fatalf("recover generation agent runtime: %v", err)
	}
	store := runner.store.(*agentRunnerStore)
	if store.interruptCalls != 1 {
		t.Fatalf("interrupt calls = %d, want 1", store.interruptCalls)
	}
}

func newTestAgentRunner(t *testing.T, store GenerationAgentStore, judge JudgeRoleExecutor) *AgentRunner {
	t.Helper()
	runner, err := NewAgentRunner(store, judge, AgentRunnerConfig{
		ServerID: "server-test", Model: "test-model",
	})
	if err != nil {
		t.Fatalf("create generation agent runner: %v", err)
	}
	return runner
}

type failingJudgeExecutor struct{ calls *int }

func (e *failingJudgeExecutor) Judge(context.Context, authoring.Plan, *Candidate) (Judgement, error) {
	if e.calls != nil {
		*e.calls = *e.calls + 1
	}
	return Judgement{}, errors.New("model transport unavailable")
}

type approvingJudgeExecutor struct{}

func (approvingJudgeExecutor) Judge(context.Context, authoring.Plan, *Candidate) (Judgement, error) {
	return Judgement{Approved: true}, nil
}

type unexpectedJudgeExecutor struct{}

func (unexpectedJudgeExecutor) Judge(context.Context, authoring.Plan, *Candidate) (Judgement, error) {
	return Judgement{}, errors.New("unexpected judge execution")
}

type agentRunnerStore struct {
	claim              domain.Claim
	claimed            bool
	run                agent.Run
	candidate          domain.Revision
	judgeCalls         int
	retryCalls         int
	interruptCalls     int
	judgementFinalized bool
}

type dispatchStore struct {
	*agentRunnerStore
	mu       sync.Mutex
	claims   []domain.Claim
	started  chan struct{}
	finished chan struct{}
}

func (s *dispatchStore) ClaimGenerationAgentWorkflow(context.Context, string, time.Duration, time.Time) (*domain.Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.claims) == 0 {
		return nil, nil
	}
	claim := s.claims[0]
	s.claims = s.claims[1:]
	return &claim, nil
}

func (s *dispatchStore) LoadGenerationContext(_ context.Context, claim domain.Claim, _ time.Time) (*domain.Context, error) {
	return &domain.Context{Workflow: claim.Workflow, Plan: authoring.Plan{}}, nil
}

func (s *dispatchStore) StartGenerationAgentRun(_ context.Context, claim domain.Claim, input agent.CreateRun, now time.Time) (*agent.Run, error) {
	return &agent.Run{
		ID: input.ID, Purpose: input.Purpose, OwnerKind: input.OwnerKind, OwnerRef: claim.Workflow.ID,
		Status: agent.RunRunning, Attempt: 1, DeadlineAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (s *dispatchStore) RetryGenerationAgentRun(context.Context, domain.Claim, string, string, time.Time) (*agent.Run, error) {
	s.mu.Lock()
	s.retryCalls++
	s.mu.Unlock()
	return nil, nil
}

type blockingJudgeExecutor struct {
	started  chan struct{}
	finished chan struct{}
}

func (e *blockingJudgeExecutor) Judge(ctx context.Context, _ authoring.Plan, _ *Candidate) (Judgement, error) {
	e.started <- struct{}{}
	defer func() { e.finished <- struct{}{} }()
	<-ctx.Done()
	return Judgement{}, ctx.Err()
}

func (s *agentRunnerStore) ClaimGenerationAgentWorkflow(context.Context, string, time.Duration, time.Time) (*domain.Claim, error) {
	if s.claimed || !s.claim.Valid() {
		return nil, nil
	}
	s.claimed = true
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

func (s *agentRunnerStore) InterruptActiveGenerationAgentRuns(context.Context, string, time.Time) error {
	s.interruptCalls++
	return nil
}

func (s *agentRunnerStore) GetCandidateRevision(_ context.Context, id string) (*domain.Revision, error) {
	if id != s.candidate.ID {
		return nil, errors.New("unexpected candidate read")
	}
	value := s.candidate
	return &value, nil
}

func (s *agentRunnerStore) FinalizeGenerationJudgement(context.Context, domain.Claim, string, bool, string, time.Time) error {
	s.judgementFinalized = true
	return nil
}

func (s *agentRunnerStore) ReportGenerationArtifactFailure(context.Context, domain.Claim, domain.WorkflowState, domain.Failure, time.Time) error {
	return errors.New("unexpected artifact failure")
}

func (s *agentRunnerStore) RenewGenerationLease(context.Context, domain.Claim, time.Duration, time.Time) error {
	return nil
}

func testAgentWorkflow(id string, state domain.WorkflowState, candidateID string) domain.Workflow {
	workflow := domain.Workflow{
		ID: id, Source: domain.Source{Kind: domain.SourceAuthoring, Ref: "authoring-session"}, SourceRevision: "1",
		State: state, CandidateRevisionID: candidateID, StateVersion: 1, NextRunAt: time.Now().UTC(),
	}
	if state.AgentState() {
		expiresAt := time.Now().UTC().Add(time.Hour)
		workflow.AgentLeaseOwner = "server-agent-lease"
		workflow.AgentLeaseExpiresAt = &expiresAt
	}
	return workflow
}

func testAgentCandidate(t *testing.T) domain.Revision {
	t.Helper()
	root := t.TempDir()
	id := "candidate-0123456789abcdef"
	path, digest, err := candidate.SaveArchiveAtomic(root, id, generatorServiceCandidateArchive(t))
	if err != nil {
		t.Fatalf("save candidate archive: %v", err)
	}
	return domain.Revision{ID: id, ArchivePath: path, ArchiveDigest: digest}
}
