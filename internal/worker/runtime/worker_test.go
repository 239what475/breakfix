package runtimeworker

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
	runtime "github.com/breakfix/breakfix/internal/domain/runtime"
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
	if len(store.completedStates) != 1 || store.completedStates[0] != runtime.StateBuilding {
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

func TestWorkerRunsResourceReapingWhileRuntimeActionIsRunning(t *testing.T) {
	base := newRuntimeStore()
	store := &runtimeReapStore{
		runtimeStore: base,
		reapClaim: runtime.ReapClaim{
			Reap: runtime.Reap{
				Scope:      runtime.ScopeGenerationWorkflow,
				ResourceID: base.action.Identity.CandidateID,
				Kind:       runtime.ReapCandidateArtifact,
				Snapshot:   base.action.Snapshot,
			},
			ReapCredential: runtime.ReapCredential{Attempt: 1, LeaseOwner: "runtime-reap-lease"},
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
	case <-time.After(time.Second):
		t.Fatal("resource reap did not run while the runtime action was still running")
	}
	close(builder.release)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("worker run = %v", err)
	}
}

type runtimeStore struct {
	mu                 sync.Mutex
	action             runtime.Context
	claimed            bool
	completedStates    []runtime.State
	renewCalls         atomic.Int32
	completeBuildCalls atomic.Int32
}

func newRuntimeStore() *runtimeStore {
	archive := []byte("candidate")
	archiveDigest := sha256.Sum256(archive)
	digest := "sha256:" + fmt.Sprintf("%x", archiveDigest[:])
	snapshot := domainexecution.Snapshot{
		Runtime:     challenge.RuntimeNode,
		Checkpoints: []domainexecution.CheckpointSnapshot{{ID: "ready", Node: "host"}},
		Node: &domainexecution.NodeRuntimeSnapshot{
			BaseImageFingerprint: strings.Repeat("a", 64), ProfileRevision: "profile-v1", NetworkPolicyRevision: "network-v1",
			Nodes:     []domainexecution.NodeSnapshot{{Name: "host", Title: "Host"}},
			Resources: domainexecution.NodeResources{CPU: "1", Memory: "512Mi", Processes: 128, RootDisk: "5Gi"},
		},
	}
	action := runtime.Context{
		Identity: runtime.Identity{
			Scope: runtime.ScopeGenerationWorkflow, OwnerID: "generation-workflow-runtime-test", CandidateID: "candidate-runtime-test",
			State: runtime.StateBuilding, StateVersion: 1,
		},
		LeaseOwner: "runtime-lease", ArchiveSHA256: digest, Snapshot: snapshot,
	}
	return &runtimeStore{action: action}
}

func (s *runtimeStore) Claim(context.Context, string, time.Duration) (*runtime.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed {
		return nil, nil
	}
	s.claimed = true
	action := s.action
	return &action, nil
}

func (s *runtimeStore) Renew(context.Context, runtime.Credential, time.Duration) error {
	s.renewCalls.Add(1)
	return nil
}

func (s *runtimeStore) Archive(context.Context, runtime.Credential) ([]byte, string, error) {
	archive := []byte("candidate")
	digest := sha256.Sum256(archive)
	return archive, "sha256:" + fmt.Sprintf("%x", digest[:]), nil
}

func (s *runtimeStore) CompleteBuild(_ context.Context, _ runtime.Credential, output domainexecution.BuildOutput) error {
	if err := output.Validate(challenge.RuntimeNode); err != nil {
		return err
	}
	s.completeBuildCalls.Add(1)
	s.mu.Lock()
	s.completedStates = append(s.completedStates, runtime.StateBuilding)
	s.mu.Unlock()
	return nil
}

func (s *runtimeStore) CompleteArtifactPublish(context.Context, runtime.Credential, domainexecution.ArtifactReference) error {
	s.mu.Lock()
	s.completedStates = append(s.completedStates, runtime.StateArtifactPublishing)
	s.mu.Unlock()
	return nil
}

func (s *runtimeStore) RecordVerificationEnvironment(context.Context, runtime.Credential, domainexecution.VerificationEnvironment) error {
	return nil
}

func (s *runtimeStore) CompleteVerification(context.Context, runtime.Credential, domainexecution.VerificationReport) error {
	s.mu.Lock()
	s.completedStates = append(s.completedStates, runtime.StateVerifying)
	s.mu.Unlock()
	return nil
}

func (s *runtimeStore) RecordChallengePublication(context.Context, runtime.Credential, domainexecution.ArtifactReference) error {
	s.mu.Lock()
	s.completedStates = append(s.completedStates, runtime.StateChallengePublishing)
	s.mu.Unlock()
	return nil
}

func (s *runtimeStore) ReportInfrastructureFailure(context.Context, runtime.Credential, runtime.Failure) error {
	return nil
}

func (s *runtimeStore) ReportArtifactFailure(context.Context, runtime.Credential, runtime.Failure, *domainexecution.VerificationReport) error {
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

func (*runtimePublisher) PublishChallengeWork(context.Context, domainexecution.Work, string, string) (domainexecution.ArtifactReference, error) {
	return domainexecution.ArtifactReference{}, nil
}

func (*runtimePublisher) ReapResource(context.Context, runtime.Reap) error { return nil }

type runtimeVerifier struct{ calls atomic.Int32 }

func (v *runtimeVerifier) ExecuteWork(context.Context, domainexecution.Work, func(context.Context, domainexecution.VerificationEnvironment) error) (domainexecution.VerificationReport, error) {
	v.calls.Add(1)
	return domainexecution.VerificationReport{}, nil
}

func (*runtimeVerifier) ReapVerificationEnvironment(context.Context, runtime.Reap) error {
	return nil
}

type runtimeReapStore struct {
	*runtimeStore
	reapClaim   runtime.ReapClaim
	reapClaimed chan struct{}
	reaped      atomic.Bool
}

func (s *runtimeReapStore) ClaimResourceReap(_ context.Context, _ string, _ time.Duration) (*runtime.ReapClaim, error) {
	if !s.reaped.CompareAndSwap(false, true) {
		return nil, nil
	}
	close(s.reapClaimed)
	claim := s.reapClaim
	return &claim, nil
}

func (*runtimeReapStore) CompleteResourceReap(context.Context, runtime.ReapClaim, string) error {
	return nil
}
