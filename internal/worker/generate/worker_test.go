package generate

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	app "github.com/breakfix/breakfix/internal/application/generation"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

func TestWorkerExecutesOnlyRuntimeStates(t *testing.T) {
	store := newRuntimeStore()
	builder := &runtimeBuilder{}
	publisher := &runtimePublisher{}
	verifier := &runtimeVerifier{}
	worker, err := New(store, builder, publisher, verifier, Config{WorkerID: "runtime-test", LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}

	processed, err := worker.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("process runtime action = %v, %v", processed, err)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	want := []generation.WorkflowState{
		generation.StateBuilding,
		generation.StateArtifactPublishing,
		generation.StateVerifying,
		generation.StateVerifying,
	}
	if len(store.phaseStates) != len(want) {
		t.Fatalf("runtime states = %#v, want %#v", store.phaseStates, want)
	}
	for index := range want {
		if store.phaseStates[index] != want[index] {
			t.Fatalf("runtime state %d = %s, want %s", index, store.phaseStates[index], want[index])
		}
	}
	if store.current.Workflow.State != generation.StateNeedsAuthorReview {
		t.Fatalf("workflow state = %s, want NeedsAuthorReview", store.current.Workflow.State)
	}
	if builder.calls.Load() != 1 || publisher.artifactCalls.Load() != 1 || verifier.calls.Load() != 1 {
		t.Fatalf("runtime executor calls = build %d publish %d verify %d", builder.calls.Load(), publisher.artifactCalls.Load(), verifier.calls.Load())
	}
}

func TestWorkerRenewsLeaseDuringRuntimeAction(t *testing.T) {
	store := newRuntimeStore()
	builder := &runtimeBuilder{delay: 1250 * time.Millisecond}
	worker, err := New(store, builder, &runtimePublisher{}, &runtimeVerifier{}, Config{WorkerID: "runtime-test", LeaseTTL: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("process runtime action = %v, %v", processed, err)
	}
	if store.renewCalls.Load() == 0 {
		t.Fatal("runtime worker did not renew its action lease")
	}
}

type runtimeStore struct {
	mu            sync.Mutex
	current       generation.Claim
	phaseStates   []generation.WorkflowState
	claimReturned bool
	renewCalls    atomic.Int32
}

func newRuntimeStore() *runtimeStore {
	deadline := time.Now().UTC().Add(time.Hour)
	return &runtimeStore{current: generation.Claim{
		Workflow: generation.Workflow{
			ID: "generation-workflow-0123456789abcdef", Source: generation.Source{Kind: generation.SourceAuthoring, Ref: "authoring-session"},
			SourceRevision: "0", State: generation.StateBuilding, StateAttempt: 0, LeaseOwner: "runtime-lease", NextRunAt: time.Now().UTC(), DeadlineAt: &deadline,
			CandidateRevisionID: "candidate-0123456789abcdef",
		},
		LeaseCredential: generation.LeaseCredential{StateAttempt: 0, LeaseOwner: "runtime-lease"},
	}}
}

func (s *runtimeStore) Claim(context.Context, string, time.Duration) (*generation.Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimReturned || s.current.Workflow.State == generation.StateNeedsAuthorReview {
		return nil, nil
	}
	s.claimReturned = true
	claim := s.current
	return &claim, nil
}

func (s *runtimeStore) Renew(context.Context, generation.Claim, time.Duration) error {
	s.renewCalls.Add(1)
	return nil
}

func (s *runtimeStore) Context(_ context.Context, claim generation.Claim) (*generation.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	workflow := s.current.Workflow
	workflow.State = claim.Workflow.State
	return &generation.Context{Workflow: workflow, Candidate: &generation.WorkerView{
		ID:       workflow.CandidateRevisionID,
		Snapshot: generation.ExecutionSnapshot{Runtime: "node", Checkpoints: []generation.CheckpointSnapshot{{ID: "ready", Node: "host"}}, Node: &generation.NodeRuntimeSnapshot{Nodes: []generation.NodeSnapshot{{Name: "host", Title: "Host"}}}},
	}}, nil
}

func (s *runtimeStore) Phase(_ context.Context, claim generation.Claim, request app.PhaseRequest) (*generation.Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phaseStates = append(s.phaseStates, claim.Workflow.State)
	s.current.Workflow.State = claim.Workflow.State
	switch {
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
	default:
		return nil, nil
	}
	return s.currentClaimLocked(), nil
}

func (*runtimeStore) CandidateArchive(context.Context, generation.Claim) ([]byte, string, error) {
	return []byte("candidate"), "", nil
}

func (*runtimeStore) K8sBase(context.Context, generation.Claim) ([]byte, string, error) {
	return nil, "", nil
}

func (*runtimeStore) BuildArchive(context.Context, generation.Claim) ([]byte, string, error) {
	return nil, "", nil
}

func (s *runtimeStore) currentClaimLocked() *generation.Claim {
	claim := s.current
	claim.Workflow.LeaseOwner = s.current.LeaseOwner
	claim.StateAttempt = claim.Workflow.StateAttempt
	return &claim
}

type runtimeBuilder struct {
	calls atomic.Int32
	delay time.Duration
}

func (b *runtimeBuilder) Execute(ctx context.Context, _ generation.Execution, _ []byte, _ []byte) (generation.BuildResult, error) {
	b.calls.Add(1)
	if b.delay > 0 {
		timer := time.NewTimer(b.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return generation.BuildResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	return generation.BuildResult{}, nil
}

type runtimePublisher struct{ artifactCalls atomic.Int32 }

func (p *runtimePublisher) PublishArtifact(context.Context, generation.Execution, []byte) (generation.ArtifactReference, error) {
	p.artifactCalls.Add(1)
	return generation.ArtifactReference{}, nil
}

func (*runtimePublisher) PublishChallenge(context.Context, generation.Execution) (generation.ArtifactReference, error) {
	return generation.ArtifactReference{}, nil
}

type runtimeVerifier struct{ calls atomic.Int32 }

func (v *runtimeVerifier) Execute(ctx context.Context, execution generation.Execution, record func(context.Context, generation.VerificationEnvironment) error) (generation.VerificationReport, error) {
	v.calls.Add(1)
	if err := record(ctx, generation.VerificationEnvironment{Runtime: "node", Name: "verification", UID: "uid", WorkflowID: execution.Claim.Workflow.ID, Attempt: 1}); err != nil {
		return generation.VerificationReport{}, err
	}
	return generation.VerificationReport{}, nil
}
