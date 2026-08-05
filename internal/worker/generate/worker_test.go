package generate

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/challenge"
	domainexecution "github.com/breakfix/breakfix/internal/domain/execution"
	"github.com/breakfix/breakfix/internal/domain/generation"
)

func TestWorkerProcessesOneClaimedRuntimeState(t *testing.T) {
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
	if builder.calls.Load() != 1 || publisher.artifactCalls.Load() != 0 || verifier.calls.Load() != 0 {
		t.Fatalf("runtime executor calls = build %d publish %d verify %d", builder.calls.Load(), publisher.artifactCalls.Load(), verifier.calls.Load())
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.completedStates) != 1 || store.completedStates[0] != generation.StateBuilding {
		t.Fatalf("completed runtime states = %#v", store.completedStates)
	}
}

func TestWorkerRenewsActionLeaseBeforeCompletingLongBuild(t *testing.T) {
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
	if store.completeBuildCalls.Load() != 1 {
		t.Fatalf("build completion calls = %d, want 1", store.completeBuildCalls.Load())
	}
}

func TestWorkerRunsResourceReapingOnlyAfterRuntimeActionCompletes(t *testing.T) {
	base := newRuntimeStore()
	store := &runtimeReapStore{
		runtimeStore: base,
		reapClaim: generation.ResourceReapClaim{
			ResourceReap: generation.ResourceReap{
				CandidateRevisionID: base.action.Candidate.ID,
				Kind:                generation.ResourceReapCandidateArtifact,
				Candidate:           base.action.Candidate,
			},
			ResourceReapCredential: generation.ResourceReapCredential{LeaseOwner: "runtime-reap-lease"},
		},
		reapClaimed: make(chan struct{}),
	}
	builder := &blockingRuntimeBuilder{started: make(chan struct{}), release: make(chan struct{})}
	worker, err := New(store, builder, &runtimePublisher{}, &runtimeVerifier{}, Config{WorkerID: "runtime-test", LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	<-builder.started
	select {
	case <-store.reapClaimed:
		t.Fatal("resource reap started while the runtime action was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(builder.release)
	select {
	case <-store.reapClaimed:
	case <-time.After(time.Second):
		t.Fatal("resource reap did not run after the runtime action completed")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("worker run = %v", err)
	}
}

type runtimeStore struct {
	mu                 sync.Mutex
	action             generation.RuntimeActionContext
	claimed            bool
	completedStates    []generation.WorkflowState
	renewCalls         atomic.Int32
	completeBuildCalls atomic.Int32
}

func newRuntimeStore() *runtimeStore {
	now := time.Now().UTC()
	archive := []byte("candidate")
	archiveDigest := sha256.Sum256(archive)
	digest := "sha256:" + fmt.Sprintf("%x", archiveDigest[:])
	workflow := generation.Workflow{
		ID: "generation-workflow-runtime-test", Source: generation.Source{Kind: generation.SourceAuthoring, Ref: "authoring-session"},
		SourceRevision: "1", State: generation.StateBuilding, StateVersion: 1, RuntimeAttempt: 1,
		LeaseOwner: "runtime-lease", LeaseExpiresAt: ptrTime(now.Add(time.Minute)), NextRunAt: now,
		CreatedAt: now, UpdatedAt: now, CandidateRevisionID: "candidate-runtime-test",
	}
	claim := generation.Claim{Workflow: workflow, LeaseCredential: generation.LeaseCredential{StateVersion: 1, LeaseOwner: workflow.LeaseOwner}}
	snapshot := generation.ExecutionSnapshot{
		Runtime:     challenge.RuntimeNode,
		Checkpoints: []generation.CheckpointSnapshot{{ID: "ready", Node: "host"}},
		Node: &generation.NodeRuntimeSnapshot{
			BaseImageFingerprint: strings.Repeat("a", 64), ProfileRevision: "profile-v1", NetworkPolicyRevision: "network-v1",
			Nodes:     []generation.NodeSnapshot{{Name: "host", Title: "Host"}},
			Resources: generation.NodeResources{CPU: "1", Memory: "512Mi", Processes: 128, RootDisk: "5Gi"},
		},
	}
	action := generation.RuntimeActionContext{
		Claim: claim, Identity: generation.RuntimeActionIdentityFor(claim, workflow.CandidateRevisionID),
		Candidate: generation.WorkerView{ID: workflow.CandidateRevisionID, SourceRevision: "1", ArchiveSHA256: digest, Snapshot: snapshot},
	}
	return &runtimeStore{action: action}
}

func (s *runtimeStore) Claim(context.Context, string, time.Duration) (*generation.RuntimeActionContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed {
		return nil, nil
	}
	s.claimed = true
	action := s.action
	return &action, nil
}

func (s *runtimeStore) Renew(context.Context, generation.RuntimeActionCredential, time.Duration) error {
	s.renewCalls.Add(1)
	return nil
}

func (s *runtimeStore) CandidateArchive(context.Context, generation.RuntimeActionCredential) ([]byte, string, error) {
	archive := []byte("candidate")
	digest := sha256.Sum256(archive)
	return archive, "sha256:" + fmt.Sprintf("%x", digest[:]), nil
}

func (s *runtimeStore) CompleteBuild(_ context.Context, _ generation.RuntimeActionCredential, output generation.BuildOutput) error {
	if err := output.Validate(challenge.RuntimeNode); err != nil {
		return err
	}
	s.completeBuildCalls.Add(1)
	s.mu.Lock()
	s.completedStates = append(s.completedStates, generation.StateBuilding)
	s.mu.Unlock()
	return nil
}

func (s *runtimeStore) CompleteArtifactPublish(context.Context, generation.RuntimeActionCredential, generation.ArtifactReference) error {
	s.mu.Lock()
	s.completedStates = append(s.completedStates, generation.StateArtifactPublishing)
	s.mu.Unlock()
	return nil
}

func (s *runtimeStore) RecordVerificationEnvironment(context.Context, generation.RuntimeActionCredential, generation.VerificationEnvironment) error {
	return nil
}

func (s *runtimeStore) CompleteVerification(context.Context, generation.RuntimeActionCredential, generation.VerificationReport) error {
	s.mu.Lock()
	s.completedStates = append(s.completedStates, generation.StateVerifying)
	s.mu.Unlock()
	return nil
}

func (s *runtimeStore) RecordChallengePublication(context.Context, generation.RuntimeActionCredential, generation.ArtifactReference) error {
	s.mu.Lock()
	s.completedStates = append(s.completedStates, generation.StateChallengePublishing)
	s.mu.Unlock()
	return nil
}

func (s *runtimeStore) ReportInfrastructureFailure(context.Context, generation.RuntimeActionCredential, generation.Failure) error {
	return nil
}

func (s *runtimeStore) ReportArtifactFailure(context.Context, generation.RuntimeActionCredential, generation.Failure, *generation.VerificationReport) error {
	return nil
}

type runtimeBuilder struct {
	calls atomic.Int32
	delay time.Duration
}

type blockingRuntimeBuilder struct {
	started chan struct{}
	release chan struct{}
}

func (b *blockingRuntimeBuilder) ExecuteWork(ctx context.Context, work domainexecution.Work, archive []byte) (domainexecution.BuildOutput, error) {
	close(b.started)
	select {
	case <-ctx.Done():
		return domainexecution.BuildOutput{}, ctx.Err()
	case <-b.release:
	}
	return (&runtimeBuilder{}).ExecuteWork(ctx, work, archive)
}

func (b *runtimeBuilder) ExecuteWork(ctx context.Context, work domainexecution.Work, _ []byte) (domainexecution.BuildOutput, error) {
	b.calls.Add(1)
	if b.delay > 0 {
		timer := time.NewTimer(b.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return domainexecution.BuildOutput{}, ctx.Err()
		case <-timer.C:
		}
	}
	return domainexecution.BuildOutput{Runtime: work.Snapshot.Runtime, Incus: &domainexecution.IncusBuildReference{
		Project: "build", WorkflowID: work.OwnerID, CandidateRevisionID: work.CandidateID, Attempt: work.Attempt,
		InstanceName: "build-instance", Alias: "build-alias", Fingerprint: strings.Repeat("b", 64),
	}}, nil
}

type runtimePublisher struct {
	artifactCalls atomic.Int32
}

func (p *runtimePublisher) PublishArtifactWork(context.Context, domainexecution.Work) (domainexecution.ArtifactReference, error) {
	p.artifactCalls.Add(1)
	return domainexecution.ArtifactReference{}, nil
}

func (*runtimePublisher) PublishChallengeWork(context.Context, domainexecution.Work, string) (domainexecution.ArtifactReference, error) {
	return domainexecution.ArtifactReference{}, nil
}

func (*runtimePublisher) ReapCandidate(context.Context, generation.ResourceReap) error { return nil }

type runtimeVerifier struct{ calls atomic.Int32 }

func (v *runtimeVerifier) ExecuteWork(context.Context, domainexecution.Work, func(context.Context, domainexecution.VerificationEnvironment) error) (domainexecution.VerificationReport, error) {
	v.calls.Add(1)
	return domainexecution.VerificationReport{}, nil
}

func (*runtimeVerifier) ReapVerificationEnvironment(context.Context, generation.ResourceReap) error {
	return nil
}

type runtimeReapStore struct {
	*runtimeStore
	reapClaim   generation.ResourceReapClaim
	reapClaimed chan struct{}
	reaped      atomic.Bool
}

func (s *runtimeReapStore) ClaimResourceReap(_ context.Context, _ string, kind generation.ResourceReapKind, _ time.Duration) (*generation.ResourceReapClaim, error) {
	if kind != s.reapClaim.Kind {
		return nil, nil
	}
	if !s.reaped.CompareAndSwap(false, true) {
		return nil, nil
	}
	close(s.reapClaimed)
	claim := s.reapClaim
	return &claim, nil
}

func (*runtimeReapStore) CompleteResourceReap(context.Context, generation.ResourceReapClaim, string) error {
	return nil
}

func ptrTime(value time.Time) *time.Time { return &value }
