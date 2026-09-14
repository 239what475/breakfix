package runtimeenvironment

import (
	"context"
	"errors"
	"testing"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type reconcilerResolver struct {
	revision runnable.RunnableRevision
	err      error
}

func (r reconcilerResolver) ResolveRunnableRevision(context.Context, string, string) (runnable.RunnableRevision, error) {
	return r.revision, r.err
}

type reconcilerProvider struct {
	provision      Observation
	reset          Observation
	provisionErr   error
	resetErr       error
	stopErr        error
	releaseErr     error
	stopDone       bool
	releaseDone    bool
	provisionCalls int
	resetCalls     int
	stopCalls      int
	releaseCalls   int
	lastProvision  Binding
	lastReset      Binding
	lastStop       Binding
	lastRelease    Binding
}

func (p *reconcilerProvider) Provision(_ context.Context, binding Binding) (Observation, error) {
	p.provisionCalls++
	p.lastProvision = binding
	return p.provision, p.provisionErr
}

func (p *reconcilerProvider) Reset(_ context.Context, binding Binding) (Observation, error) {
	p.resetCalls++
	p.lastReset = binding
	return p.reset, p.resetErr
}

func (p *reconcilerProvider) Release(_ context.Context, binding Binding) (bool, error) {
	p.releaseCalls++
	p.lastRelease = binding
	return p.releaseDone, p.releaseErr
}

func (p *reconcilerProvider) Stop(_ context.Context, binding Binding) (bool, error) {
	p.stopCalls++
	p.lastStop = binding
	return p.stopDone, p.stopErr
}

func TestReconcilerFinalizesAndProjectsReadyRuntime(t *testing.T) {
	now := fixedRuntimeEnvironmentTime()
	revision := validRevision(t)
	environment := testRuntimeEnvironment(t, revision, runtimev2.PhasePending, now)
	provider := &reconcilerProvider{provision: Observation{Ready: true, ResourceRefs: []runtimev2.ResourceReference{{Provider: "incus", Kind: "instance", ID: "environment-node"}}, EndpointRefs: []runtimev2.EndpointReference{{Name: "terminal", Ref: "terminal/environment"}}}}
	reconciler, kubeClient := newRuntimeEnvironmentReconciler(t, environment, revision, provider, NewInMemoryReapQueue(), now)

	reconcileRuntimeEnvironmentTimes(t, reconciler, environment.Name, 3)
	current := getRuntimeEnvironment(t, kubeClient, environment.Name)
	if !containsFinalizer(current.Finalizers, finalizer) {
		t.Fatal("runtime environment finalizer was not added")
	}
	if current.Status.Phase != runtimev2.PhaseReady || current.Status.Operation != runtimev2.OperationNone {
		t.Fatalf("status = %#v, want Ready with no operation", current.Status)
	}
	profileDigest, err := revision.Spec.RuntimeProfile.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if current.Status.Runtime.ProfileDigest != profileDigest || current.Status.Runtime.Provider != "node" {
		t.Fatalf("runtime projection = %#v", current.Status.Runtime)
	}
	if len(current.Status.Runtime.ResourceRefs) != 1 || current.Status.Runtime.ResourceRefs[0].ID != "environment-node" {
		t.Fatalf("resource projection = %#v", current.Status.Runtime.ResourceRefs)
	}
	if provider.provisionCalls != 1 || provider.lastProvision.UID != string(environment.UID) {
		t.Fatalf("provision calls=%d binding=%#v", provider.provisionCalls, provider.lastProvision)
	}
	if current.Status.Lifecycle.ExpiresAt == nil || !current.Status.Lifecycle.ExpiresAt.Equal(&metav1.Time{Time: now.Add(10 * time.Minute)}) {
		t.Fatalf("expiresAt = %#v", current.Status.Lifecycle.ExpiresAt)
	}
}

func TestReconcilerFencesResetNonceAndResumesProvisioning(t *testing.T) {
	now := fixedRuntimeEnvironmentTime()
	revision := validRevision(t)
	environment := testRuntimeEnvironment(t, revision, runtimev2.PhaseReady, now)
	environment.Spec.ResetNonce = 1
	environment.Finalizers = []string{finalizer}
	provider := &reconcilerProvider{reset: Observation{Ready: false}, provision: Observation{Ready: true}}
	reconciler, kubeClient := newRuntimeEnvironmentReconciler(t, environment, revision, provider, NewInMemoryReapQueue(), now)

	reconcileRuntimeEnvironmentTimes(t, reconciler, environment.Name, 2)
	current := getRuntimeEnvironment(t, kubeClient, environment.Name)
	if provider.resetCalls != 1 || current.Annotations[resetNonceAnnotation] != "1" {
		t.Fatalf("reset was not fenced: calls=%d annotations=%#v", provider.resetCalls, current.Annotations)
	}
	if current.Status.Phase != runtimev2.PhaseReady || current.Status.Operation != runtimev2.OperationNone {
		t.Fatalf("status after reset = %#v", current.Status)
	}
	if provider.provisionCalls != 1 {
		t.Fatalf("post-reset provisioning calls = %d, want 1", provider.provisionCalls)
	}

	reconcileRuntimeEnvironmentTimes(t, reconciler, environment.Name, 1)
	if provider.resetCalls != 1 {
		t.Fatalf("reset repeated after nonce was observed: calls=%d", provider.resetCalls)
	}
}

func TestReconcilerClassifiesProviderFailures(t *testing.T) {
	now := fixedRuntimeEnvironmentTime()
	revision := validRevision(t)
	for name, test := range map[string]struct {
		err       error
		wantPhase runtimev2.EnvironmentPhase
		wantClass runtimev2.FailureClass
		wantRetry bool
	}{
		"infrastructure": {err: errors.New("provider temporarily unavailable"), wantPhase: runtimev2.PhaseProvisioning, wantClass: runtimev2.FailureInfrastructure, wantRetry: true},
		"artifact":       {err: runnable.NewArtifactFailure("invalid-init", "initialization contract is invalid"), wantPhase: runtimev2.PhaseFailed, wantClass: runtimev2.FailureArtifact},
	} {
		t.Run(name, func(t *testing.T) {
			environment := testRuntimeEnvironment(t, revision, runtimev2.PhaseProvisioning, now)
			environment.Finalizers = []string{finalizer}
			provider := &reconcilerProvider{provisionErr: test.err}
			reconciler, kubeClient := newRuntimeEnvironmentReconciler(t, environment, revision, provider, NewInMemoryReapQueue(), now)

			result, err := reconciler.Reconcile(context.Background(), runtimeEnvironmentRequest(environment.Name))
			if err != nil {
				t.Fatal(err)
			}
			current := getRuntimeEnvironment(t, kubeClient, environment.Name)
			if current.Status.Phase != test.wantPhase || current.Status.Failure == nil || current.Status.Failure.Class != test.wantClass {
				t.Fatalf("failure status = %#v", current.Status)
			}
			if (result.RequeueAfter > 0) != test.wantRetry {
				t.Fatalf("requeue=%s, want retry=%t", result.RequeueAfter, test.wantRetry)
			}
		})
	}
}

func TestReconcilerReapsAsynchronouslyAfterLeaseDrain(t *testing.T) {
	now := fixedRuntimeEnvironmentTime().Add(11 * time.Minute)
	revision := validRevision(t)
	environment := testRuntimeEnvironment(t, revision, runtimev2.PhaseReady, fixedRuntimeEnvironmentTime())
	environment.Finalizers = []string{finalizer}
	provider := &reconcilerProvider{stopDone: true, releaseDone: true}
	queue := NewInMemoryReapQueue()
	reconciler, kubeClient := newRuntimeEnvironmentReconciler(t, environment, revision, provider, queue, now)

	reconcileRuntimeEnvironmentTimes(t, reconciler, environment.Name, 2)
	current := getRuntimeEnvironment(t, kubeClient, environment.Name)
	if current.Status.Phase != runtimev2.PhaseDraining || provider.releaseCalls != 0 {
		t.Fatalf("drain status=%#v release calls=%d", current.Status, provider.releaseCalls)
	}
	key := ReapRequest{Namespace: environment.Namespace, Name: environment.Name, UID: string(environment.UID)}.Key()
	record, err := queue.Get(context.Background(), key)
	if err != nil || record.State != ReapQueued {
		t.Fatalf("reap handoff = %#v, %v", record, err)
	}
	reaper := &Reaper{Queue: queue, Provider: provider, Owner: "reaper-a", Now: func() time.Time { return now }}
	if processed, err := reaper.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("reaper result processed=%t err=%v", processed, err)
	}
	if provider.releaseCalls != 1 {
		t.Fatalf("release calls=%d, want one asynchronous release", provider.releaseCalls)
	}

	reconcileRuntimeEnvironmentTimes(t, reconciler, environment.Name, 1)
	current = getRuntimeEnvironment(t, kubeClient, environment.Name)
	if current.Status.Phase != runtimev2.PhaseReleased || current.Status.Lifecycle.ReleasedAt == nil || containsFinalizer(current.Finalizers, finalizer) {
		t.Fatalf("released environment = %#v finalizers=%#v", current.Status, current.Finalizers)
	}
}

func TestReaperRetriesAndFencesLeaseTakeover(t *testing.T) {
	now := fixedRuntimeEnvironmentTime()
	revision := validRevision(t)
	queue := NewInMemoryReapQueue()
	request := testReapRequest(t, revision)
	if err := queue.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	first, err := queue.Claim(context.Background(), "reaper-a", time.Second, now)
	if err != nil || first == nil {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	second, err := queue.Claim(context.Background(), "reaper-b", time.Second, now.Add(2*time.Second))
	if err != nil || second == nil || second.Record.Attempt != 2 {
		t.Fatalf("takeover claim=%#v err=%v", second, err)
	}
	if err := queue.Complete(context.Background(), *first, true, "", now, now); !errors.Is(err, ErrReapLeaseLost) {
		t.Fatalf("stale completion error=%v, want lease lost", err)
	}
	if err := queue.Complete(context.Background(), *second, false, "provider unavailable", now.Add(2*time.Second), now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	provider := &reconcilerProvider{stopDone: true, releaseErr: errors.New("provider unavailable")}
	reaper := &Reaper{Queue: queue, Provider: provider, Owner: "reaper-c", Retry: time.Second, Now: func() time.Time { return now.Add(2 * time.Second) }}
	if processed, err := reaper.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("retry reaper processed=%t err=%v", processed, err)
	}
	record, err := queue.Get(context.Background(), request.Key())
	if err != nil || record.State != ReapQueued || record.LastError != "provider unavailable" {
		t.Fatalf("retry record=%#v err=%v", record, err)
	}
	provider.releaseErr = ErrResourceAbsent
	reaper.Now = func() time.Time { return now.Add(4 * time.Second) }
	if processed, err := reaper.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("absent resource reaper processed=%t err=%v", processed, err)
	}
	record, err = queue.Get(context.Background(), request.Key())
	if err != nil || record.State != ReapSucceeded || record.CompletedAt == nil {
		t.Fatalf("completed record=%#v err=%v", record, err)
	}
}

func fixedRuntimeEnvironmentTime() time.Time {
	return time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
}

func testRuntimeEnvironment(t *testing.T, revision runnable.RunnableRevision, phase runtimev2.EnvironmentPhase, createdAt time.Time) *runtimev2.RuntimeEnvironment {
	t.Helper()
	digest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return &runtimev2.RuntimeEnvironment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "breakfix-system", Name: "runtime-environment", UID: types.UID("runtime-environment-uid"), CreationTimestamp: metav1.NewTime(createdAt)},
		Spec: runtimev2.RuntimeEnvironmentSpec{
			RunnableRevisionRef: runtimev2.RunnableRevisionReference{ID: "revision-01", Digest: digest},
			Purpose:             runtimev2.PurposeVerification,
			Lease:               runtimev2.LeaseSpec{RenewedAt: metav1.NewTime(createdAt)},
		},
		Status: runtimev2.RuntimeEnvironmentStatus{Phase: phase, Operation: runtimev2.OperationNone},
	}
}

func testReapRequest(t *testing.T, revision runnable.RunnableRevision) ReapRequest {
	t.Helper()
	digest, err := revision.Digest()
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{Namespace: "breakfix-system", Name: "runtime-environment", UID: "runtime-environment-uid", Purpose: runnable.PurposeVerification, RunnableRevision: revision}
	return ReapRequest{Namespace: binding.Namespace, Name: binding.Name, UID: binding.UID, Revision: digest, Binding: binding}
}

func newRuntimeEnvironmentReconciler(t *testing.T, environment *runtimev2.RuntimeEnvironment, revision runnable.RunnableRevision, provider *reconcilerProvider, queue ReapQueue, now time.Time) (*Reconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := runtimev2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&runtimev2.RuntimeEnvironment{}).WithObjects(environment).Build()
	return &Reconciler{Client: kubeClient, Resolver: reconcilerResolver{revision: revision}, Provider: provider, Reaps: queue, Now: func() time.Time { return now }}, kubeClient
}

func reconcileRuntimeEnvironmentTimes(t *testing.T, reconciler *Reconciler, name string, count int) {
	t.Helper()
	for range count {
		if _, err := reconciler.Reconcile(context.Background(), runtimeEnvironmentRequest(name)); err != nil {
			t.Fatal(err)
		}
	}
}

func runtimeEnvironmentRequest(name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "breakfix-system", Name: name}}
}

func getRuntimeEnvironment(t *testing.T, kubeClient client.Client, name string) *runtimev2.RuntimeEnvironment {
	t.Helper()
	var environment runtimev2.RuntimeEnvironment
	if err := kubeClient.Get(context.Background(), client.ObjectKey{Namespace: "breakfix-system", Name: name}, &environment); err != nil {
		t.Fatal(err)
	}
	return &environment
}

func containsFinalizer(finalizers []string, value string) bool {
	for _, finalizer := range finalizers {
		if finalizer == value {
			return true
		}
	}
	return false
}
