package generate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	app "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/content/candidate"
	"github.com/breakfix/breakfix/internal/domain/agent"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

func TestWorkerCompletesGenerationWorkflowThroughAuthorReview(t *testing.T) {
	store := newGenerationStore(t)
	executors := newGenerationExecutors(t, store.archive)
	worker := newTestWorker(t, store, executors)

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("process generation workflow = %v, %v", processed, err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	wantStates := []generation.WorkflowState{
		generation.StateGenerating,
		generation.StateJudging,
		generation.StateBuilding,
		generation.StateArtifactPublishing,
		generation.StateVerifying,
		generation.StateVerifying,
	}
	if !slicesEqual(store.phaseStates, wantStates) {
		t.Fatalf("phase states = %#v, want %#v", store.phaseStates, wantStates)
	}
	if store.current.Workflow.State != generation.StateNeedsAuthorReview || store.current.LeaseOwner != "" {
		t.Fatalf("workflow after verification = %#v, want NeedsAuthorReview without lease", store.current.Workflow)
	}
	if executors.generateCalls.Load() != 1 || executors.judgeCalls.Load() != 1 || executors.buildCalls.Load() != 1 || executors.publishCalls.Load() != 1 || executors.verifyCalls.Load() != 1 {
		t.Fatalf("phase calls = generate %d judge %d build %d publish %d verify %d", executors.generateCalls.Load(), executors.judgeCalls.Load(), executors.buildCalls.Load(), executors.publishCalls.Load(), executors.verifyCalls.Load())
	}
}

func TestWorkerRenewsLeaseDuringLongPhase(t *testing.T) {
	store := newGenerationStore(t)
	executors := newGenerationExecutors(t, store.archive)
	executors.generateDelay = 1250 * time.Millisecond
	worker, err := New(store, executors, executors, executors.builder, executors.publisher, executors.verifier, Config{
		WorkerID: "generate-test", Model: "test-model", LeaseTTL: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("process long generation phase = %v, %v", processed, err)
	}
	if store.renewCalls.Load() == 0 {
		t.Fatal("worker did not renew its lease during a long phase")
	}
}

func TestWorkerRepairsVerificationFailureBeforeAuthorReview(t *testing.T) {
	store := newGenerationStore(t)
	executors := newGenerationExecutors(t, store.archive)
	executors.verifier.failuresBeforePass.Store(1)
	worker := newTestWorker(t, store, executors)

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("process repaired generation workflow = %v, %v", processed, err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.current.Workflow.State != generation.StateNeedsAuthorReview {
		t.Fatalf("workflow after repair = %s, want NeedsAuthorReview", store.current.Workflow.State)
	}
	if executors.generateCalls.Load() != 2 || executors.judgeCalls.Load() != 2 || executors.buildCalls.Load() != 2 || executors.publishCalls.Load() != 2 || executors.verifyCalls.Load() != 2 {
		t.Fatalf("phase calls after repair = generate %d judge %d build %d publish %d verify %d", executors.generateCalls.Load(), executors.judgeCalls.Load(), executors.buildCalls.Load(), executors.publishCalls.Load(), executors.verifyCalls.Load())
	}
}

type generationExecutors struct {
	archive       []byte
	generateCalls atomic.Int32
	judgeCalls    atomic.Int32
	buildCalls    atomic.Int32
	publishCalls  atomic.Int32
	verifyCalls   atomic.Int32
	generateDelay time.Duration
	builder       *generationBuilder
	publisher     *generationPublisher
	verifier      *generationVerifier
}

func newGenerationExecutors(t *testing.T, archive []byte) *generationExecutors {
	t.Helper()
	result := &generationExecutors{archive: archive}
	result.builder = &generationBuilder{calls: &result.buildCalls}
	result.publisher = &generationPublisher{calls: &result.publishCalls}
	result.verifier = &generationVerifier{calls: &result.verifyCalls}
	return result
}

func (e *generationExecutors) Generate(ctx context.Context, _ generation.Execution) ([]byte, error) {
	e.generateCalls.Add(1)
	if e.generateDelay > 0 {
		timer := time.NewTimer(e.generateDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return append([]byte(nil), e.archive...), nil
}

func (e *generationExecutors) Judge(context.Context, authoring.Plan, *app.Candidate) (app.Judgement, error) {
	e.judgeCalls.Add(1)
	return app.Judgement{Approved: true}, nil
}

func (*generationExecutors) Classify(context.Context, generation.Execution, *app.Candidate) (app.ClassificationCompletion, error) {
	return app.ClassificationCompletion{Initial: &generation.ClassificationOutput{
		Result: generation.ClassificationUnclassifiable, UnclassifiableReason: "test classifier has no roadmap fixture", AdjustmentSuggestion: "provide a classification fixture",
	}}, nil
}

type generationBuilder struct{ calls *atomic.Int32 }

func (e *generationBuilder) Execute(context.Context, generation.Execution, []byte, []byte) (generation.BuildResult, error) {
	e.calls.Add(1)
	return generation.BuildResult{}, nil
}

type generationPublisher struct {
	calls *atomic.Int32
}

func (e *generationPublisher) PublishArtifact(context.Context, generation.Execution, []byte) (generation.ArtifactReference, error) {
	e.calls.Add(1)
	return generation.ArtifactReference{}, nil
}

func (*generationPublisher) PublishChallenge(context.Context, generation.Execution) (generation.ArtifactReference, error) {
	return generation.ArtifactReference{}, nil
}

type generationVerifier struct {
	calls              *atomic.Int32
	failuresBeforePass atomic.Int32
}

func (e *generationVerifier) Execute(ctx context.Context, execution generation.Execution, record func(context.Context, generation.VerificationEnvironment) error) (generation.VerificationReport, error) {
	call := e.calls.Add(1)
	if err := record(ctx, generation.VerificationEnvironment{Runtime: "node", Name: "verification", UID: "uid", WorkflowID: execution.Claim.Workflow.ID, Attempt: 1}); err != nil {
		return generation.VerificationReport{}, err
	}
	if call <= e.failuresBeforePass.Load() {
		return generation.VerificationReport{}, generation.NewArtifactError("CHECKPOINTS_FAILED", "verification checkpoint did not pass")
	}
	return generation.VerificationReport{}, nil
}

type generationStore struct {
	mu            sync.Mutex
	current       generation.Claim
	archive       []byte
	candidate     *generation.WorkerView
	phaseStates   []generation.WorkflowState
	claimReturned bool
	runNumber     int
	renewCalls    atomic.Int32
}

func newGenerationStore(t *testing.T) *generationStore {
	t.Helper()
	deadline := time.Now().UTC().Add(time.Hour)
	archive := candidateArchive(t)
	store := &generationStore{archive: archive}
	store.current = generation.Claim{Workflow: generation.Workflow{
		ID: "generation-workflow-0123456789abcdef", Source: generation.Source{Kind: generation.SourceAuthoring, Ref: "authoring-session"}, SourceRevision: "0",
		State: generation.StateGenerating, StateAttempt: 0, LeaseOwner: "generate-lease",
		NextRunAt: time.Now().UTC(), DeadlineAt: &deadline,
	}, LeaseCredential: generation.LeaseCredential{StateAttempt: 0, LeaseOwner: "generate-lease"}}
	store.candidate = &generation.WorkerView{ID: "candidate-0123456789abcdef", Snapshot: generation.ExecutionSnapshot{
		Runtime: "node", Checkpoints: []generation.CheckpointSnapshot{{ID: "ready", Node: "host"}},
		Node: &generation.NodeRuntimeSnapshot{Nodes: []generation.NodeSnapshot{{Name: "host", Title: "Host"}}},
	}}
	return store
}

func newTestWorker(t *testing.T, store *generationStore, executors *generationExecutors) *Worker {
	t.Helper()
	worker, err := New(store, executors, executors, executors.builder, executors.publisher, executors.verifier, Config{WorkerID: "generate-test", Model: "test-model", LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func (s *generationStore) Claim(context.Context, string, time.Duration) (*generation.Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimReturned || s.current.Workflow.State == generation.StateNeedsAuthorReview {
		return nil, nil
	}
	s.claimReturned = true
	claim := s.current
	return &claim, nil
}

func (s *generationStore) Renew(context.Context, generation.Claim, time.Duration) error {
	s.renewCalls.Add(1)
	return nil
}

func (s *generationStore) Context(_ context.Context, claim generation.Claim) (*generation.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow := s.current.Workflow
	workflow.State = claim.Workflow.State
	result := &generation.Context{Workflow: workflow, Plan: testPlan()}
	if s.candidate != nil && (workflow.State != generation.StateGenerating || workflow.CandidateRevisionID != "") {
		view := *s.candidate
		result.Candidate = &view
	}
	return result, nil
}

func (s *generationStore) StartAgentRun(_ context.Context, claim generation.Claim, request app.StartAgentRunRequest) (*app.StartAgentRunResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runNumber++
	run := agent.Run{ID: "generation-agent-run-" + string(rune('a'+s.runNumber-1)), Status: agent.RunRunning, Purpose: request.Purpose, OwnerKind: "generation-workflow", OwnerRef: claim.Workflow.ID, SessionID: "generator-session"}
	s.current.Workflow.ActiveAgentRunID = run.ID
	return &app.StartAgentRunResponse{Run: run}, nil
}

func (s *generationStore) Phase(_ context.Context, claim generation.Claim, request app.PhaseRequest) (*generation.Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phaseStates = append(s.phaseStates, claim.Workflow.State)
	s.current.Workflow.State = claim.Workflow.State
	s.current.Workflow.ActiveAgentRunID = ""
	switch {
	case request.GeneratedCandidate != nil:
		s.current.Workflow.CandidateRevisionID = s.candidate.ID
		s.candidate.Failure = nil
		s.current.Workflow.State = generation.StateJudging
	case request.Judgement != nil:
		s.current.Workflow.State = generation.StateBuilding
	case request.Build != nil:
		s.current.Workflow.State = generation.StateArtifactPublishing
	case request.ArtifactPublish != nil:
		s.current.Workflow.State = generation.StateVerifying
	case request.VerificationEnvironment != nil:
		return s.currentClaimLocked(), nil
	case request.Verification != nil:
		s.current.Workflow.State = generation.StateNeedsAuthorReview
		s.current.Workflow.LeaseOwner = ""
		s.current.Workflow.LeaseExpiresAt = nil
		s.current.LeaseOwner = ""
		return nil, nil
	case request.ArtifactFailure != nil:
		failure := generation.Failure{Class: generation.FailureArtifact, Code: request.ArtifactFailure.Failure.Code, Summary: request.ArtifactFailure.Failure.Summary}
		s.candidate.Failure = &failure
		s.current.Workflow.State = generation.StateGenerating
	}
	return s.currentClaimLocked(), nil
}

func (s *generationStore) CandidateArchive(context.Context, generation.Claim) ([]byte, string, error) {
	return append([]byte(nil), s.archive...), candidate.Digest(s.archive), nil
}

func (s *generationStore) K8sBase(context.Context, generation.Claim) ([]byte, string, error) {
	return nil, "", nil
}

func (s *generationStore) BuildArchive(context.Context, generation.Claim) ([]byte, string, error) {
	return nil, "", nil
}

func (s *generationStore) currentClaimLocked() *generation.Claim {
	claim := s.current
	claim.Workflow.LeaseOwner = s.current.LeaseOwner
	claim.StateAttempt = claim.Workflow.StateAttempt
	return &claim
}

func testPlan() authoring.Plan {
	return authoring.Plan{Metadata: authoring.Metadata{Title: "Test challenge", Difficulty: "easy", Description: "A test challenge", Runtime: "node"}, Overview: "Repair the host", Checkpoints: []authoring.Checkpoint{{ID: "ready", Title: "Ready", Markdown: "Make it ready", Position: 1}}}
}

func candidateArchive(t *testing.T) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	files := map[string]string{
		"challenge.yaml":         "runtime: node\ntitle: Test challenge\ndifficulty: easy\ndescription: A generated test challenge.\nnodes:\n  - name: host\n    title: Host\ncheckpoints:\n  - id: ready\n    title: Ready\n    description: The host is ready.\n    hint: hints/ready.md\n    node: host\n",
		"problem.md":             "# Test challenge\n",
		"solution.md":            "# Solution\n\n<!-- checkpoint: ready -->\n\nRepair the host.\n",
		"hints/ready.md":         "Inspect the host.\n",
		"nodes/host/generate.sh": "#!/bin/sh\nset -eu\n",
		"nodes/host/answer.sh":   "#!/bin/sh\nset -eu\n",
		"nodes/host/checks.sh":   "#!/bin/sh\nprintf '{\"checks\":[{\"id\":\"ready\",\"passed\":true,\"summary\":\"ready\"}]}\n'\n",
	}
	for name, content := range files {
		mode := int64(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tarWriter, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func slicesEqual(left, right []generation.WorkflowState) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
