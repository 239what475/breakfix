package runnableworker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/runnable"
)

func TestRunnerCompletesMaterializationWithStableRevisionReference(t *testing.T) {
	action := materializationAction(t, 3)
	store := &runnerStore{action: &action}
	executor := &runnerExecutor{materialize: func(_ context.Context, request runnable.MaterializeRequest) (runnable.RunnableRevision, error) {
		return runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: request.Spec, Artifact: artifactFor(t, request.Spec)}, nil
	}}
	runner, err := NewRunner(store, executor, RunnerConfig{WorkerID: "worker-01"})
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := runner.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("process materialization = %v, %v", processed, err)
	}
	if store.revision == nil {
		t.Fatal("materialization did not store a runnable revision")
	}
	if got, want := store.revision.Reference.ID, actionRecordID("rr", action.Credential.Identity); got != want {
		t.Fatalf("revision reference = %q, want %q", got, want)
	}
	if got := store.revision.Reference.ID; len(got) > runnable.MaxIDLength {
		t.Fatalf("revision reference exceeds limit: %q", got)
	}
}

func TestRunnerUsesClaimAttemptForVerificationReport(t *testing.T) {
	spec := validSpec()
	revision := runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: spec, Artifact: artifactFor(t, spec)}
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	revisionDigest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	action := runnable.ActionContext{
		Credential:       runnable.LeaseCredential{Identity: runnable.ActionIdentity{Content: spec.Identity, SpecDigest: specDigest, Phase: runnable.ActionVerify, StateVersion: 2}, LeaseOwner: "worker-01"},
		Attempt:          4,
		RunnableRevision: &revision, RunnableRevisionRef: runnable.RevisionReference{ID: "revision-01", Digest: revisionDigest},
		RunnableRevisionDigest: revisionDigest,
	}
	store := &runnerStore{action: &action}
	executor := &runnerExecutor{verify: func(_ context.Context, request runnable.VerifyRequest) (runnable.VerificationReport, error) {
		if request.Attempt != 4 {
			t.Fatalf("verification attempt = %d, want 4", request.Attempt)
		}
		report := reportFor(t, revision)
		report.Attempt = request.Attempt
		return report, nil
	}}
	runner, err := NewRunner(store, executor, RunnerConfig{WorkerID: "worker-01"})
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := runner.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("process verification = %v, %v", processed, err)
	}
	if store.report == nil || store.report.Report.Attempt != action.Attempt {
		t.Fatalf("stored verification report = %#v", store.report)
	}
	if got, want := store.report.Reference.ID, actionRecordID("vr", action.Credential.Identity); got != want {
		t.Fatalf("report reference = %q, want %q", got, want)
	}
}

func TestRunnerClassifiesArtifactFailureWithoutCompletingAction(t *testing.T) {
	action := materializationAction(t, 1)
	store := &runnerStore{action: &action}
	executor := &runnerExecutor{materialize: func(context.Context, runnable.MaterializeRequest) (runnable.RunnableRevision, error) {
		return runnable.RunnableRevision{}, runnable.NewArtifactFailure("source-archive-digest", "source archive does not match its declared digest")
	}}
	runner, err := NewRunner(store, executor, RunnerConfig{WorkerID: "worker-01"})
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := runner.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("process artifact failure = %v, %v", processed, err)
	}
	if store.revision != nil || store.failure.class != runnable.FailureArtifact || store.failure.code != "source-archive-digest" {
		t.Fatalf("stored action result = revision %#v failure %#v", store.revision, store.failure)
	}
}

func TestRunnerRequeuesInfrastructureVerificationFailure(t *testing.T) {
	spec := validSpec()
	revision := runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: spec, Artifact: artifactFor(t, spec)}
	specDigest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	revisionDigest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	action := runnable.ActionContext{
		Credential:       runnable.LeaseCredential{Identity: runnable.ActionIdentity{Content: spec.Identity, SpecDigest: specDigest, Phase: runnable.ActionVerify, StateVersion: 2}, LeaseOwner: "worker-01"},
		Attempt:          1,
		RunnableRevision: &revision, RunnableRevisionRef: runnable.RevisionReference{ID: "revision-01", Digest: revisionDigest},
		RunnableRevisionDigest: revisionDigest,
	}
	store := &runnerStore{action: &action}
	executor := &runnerExecutor{verify: func(context.Context, runnable.VerifyRequest) (runnable.VerificationReport, error) {
		report := reportFor(t, revision)
		report.Passed = false
		report.Failure = &runnable.VerificationFailure{Class: runnable.FailureInfrastructure, Component: "provider", Reason: "unavailable", Message: "provider temporarily unavailable"}
		return report, nil
	}}
	runner, err := NewRunner(store, executor, RunnerConfig{WorkerID: "worker-01"})
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := runner.ProcessOne(context.Background()); err != nil || !processed {
		t.Fatalf("process infrastructure failure = %v, %v", processed, err)
	}
	if store.report != nil || store.failure.class != runnable.FailureInfrastructure || store.failure.code != "unavailable" {
		t.Fatalf("stored action result = report %#v failure %#v", store.report, store.failure)
	}
}

func TestRunnerRenewsLongRunningActionLease(t *testing.T) {
	action := materializationAction(t, 1)
	store := &runnerStore{action: &action}
	started := make(chan struct{})
	release := make(chan struct{})
	executor := &runnerExecutor{materialize: func(_ context.Context, request runnable.MaterializeRequest) (runnable.RunnableRevision, error) {
		close(started)
		<-release
		return runnable.RunnableRevision{FormatVersion: runnable.FormatVersion, Spec: request.Spec, Artifact: artifactFor(t, request.Spec)}, nil
	}}
	runner, err := NewRunner(store, executor, RunnerConfig{WorkerID: "worker-01", LeaseTTL: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := runner.ProcessOne(context.Background())
		done <- err
	}()
	<-started
	select {
	case <-time.After(1200 * time.Millisecond):
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not renew its action lease")
	}
	if store.renewCalls.Load() == 0 {
		t.Fatal("runner did not renew its action lease")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("process long-running action: %v", err)
	}
}

func materializationAction(t *testing.T, attempt int64) runnable.ActionContext {
	t.Helper()
	spec := validSpec()
	digest, err := spec.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return runnable.ActionContext{
		Credential: runnable.LeaseCredential{Identity: runnable.ActionIdentity{Content: spec.Identity, SpecDigest: digest, Phase: runnable.ActionMaterializeArtifact, StateVersion: 1}, LeaseOwner: "worker-01"},
		Attempt:    attempt,
		Spec:       &spec,
	}
}

type runnerStore struct {
	mu         sync.Mutex
	action     *runnable.ActionContext
	revision   *runnable.StoredRevision
	report     *runnable.StoredVerificationReport
	failure    runnerFailure
	renewCalls atomic.Int32
}

type runnerFailure struct {
	class runnable.FailureClass
	code  string
}

func (s *runnerStore) Claim(context.Context, string, time.Duration) (*runnable.ActionContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.action == nil {
		return nil, nil
	}
	result := *s.action
	s.action = nil
	return &result, nil
}

func (s *runnerStore) Renew(context.Context, runnable.LeaseCredential, time.Duration) error {
	s.renewCalls.Add(1)
	return nil
}

func (s *runnerStore) CompleteMaterialization(_ context.Context, _ runnable.LeaseCredential, value runnable.StoredRevision) error {
	if err := value.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revision = &value
	return nil
}

func (s *runnerStore) CompleteVerification(_ context.Context, _ runnable.LeaseCredential, value runnable.StoredVerificationReport) error {
	if err := value.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.report = &value
	return nil
}

func (s *runnerStore) ReportFailure(_ context.Context, _ runnable.LeaseCredential, class runnable.FailureClass, code, summary string) error {
	if !class.Valid() || code == "" || summary == "" {
		return errors.New("invalid runnable failure")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failure = runnerFailure{class: class, code: code}
	return nil
}

type runnerExecutor struct {
	materialize func(context.Context, runnable.MaterializeRequest) (runnable.RunnableRevision, error)
	verify      func(context.Context, runnable.VerifyRequest) (runnable.VerificationReport, error)
}

func (e *runnerExecutor) Materialize(ctx context.Context, request runnable.MaterializeRequest) (runnable.RunnableRevision, error) {
	if e.materialize == nil {
		return runnable.RunnableRevision{}, errors.New("unexpected materialization")
	}
	return e.materialize(ctx, request)
}

func (e *runnerExecutor) Verify(ctx context.Context, request runnable.VerifyRequest) (runnable.VerificationReport, error) {
	if e.verify == nil {
		return runnable.VerificationReport{}, errors.New("unexpected verification")
	}
	return e.verify(ctx, request)
}
