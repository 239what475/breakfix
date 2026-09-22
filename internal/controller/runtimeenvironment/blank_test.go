package runtimeenvironment

import (
	"context"
	"strings"
	"testing"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type staticBlankSource struct {
	plan runnable.BlankRuntimePlan
	err  error
}

func (s staticBlankSource) BlankPlan(string) (runnable.BlankRuntimePlan, error) {
	return s.plan, s.err
}

func blankTestPlan(t *testing.T) runnable.BlankRuntimePlan {
	t.Helper()
	image := "registry.example/breakfix/terminal@sha256:" + strings.Repeat("d", 64)
	plan := runnable.BlankRuntimePlan{
		Profile: runnable.RuntimeProfile{
			Runtime: runnable.RuntimeK8s, ProfileRevision: "k8s-profile-1",
			BaseImage:        image,
			SoftwareVersions: map[string]string{"kubernetes": "v1.30.0"},
			Resources:        runnable.ResourceLimits{CPU: "1", MemoryBytes: 1 << 30, EphemeralBytes: 1 << 30, MaxProcesses: 64, MaxConcurrentTasks: 1},
			Network:          runnable.NetworkPrivate, Topology: "single-kubernetes-cluster",
			ExecutionBoundaries: []runnable.ExecutionBoundary{
				{ID: "management-write", Target: runnable.TargetLocation{Kind: "management", ID: "cluster"}, Permission: runnable.PermissionReadWrite, Network: runnable.NetworkPrivate, MaxTimeout: 900},
				{ID: "management-read", Target: runnable.TargetLocation{Kind: "management", ID: "cluster"}, Permission: runnable.PermissionReadOnly, Network: runnable.NetworkPrivate, MaxTimeout: 900},
			},
		},
		Lifecycle: runnable.LifecyclePolicy{CreateTimeoutSeconds: 60, ResetTimeoutSeconds: 60, StopTimeoutSeconds: 60, ReapTimeoutSeconds: 60, IdleTTLSeconds: 600, MaxLifetimeSeconds: 1800},
		Image:     image,
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	return plan
}

func blankTestEnvironment(plan runnable.BlankRuntimePlan, phase runtimev2.EnvironmentPhase, createdAt time.Time) *runtimev2.RuntimeEnvironment {
	return &runtimev2.RuntimeEnvironment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "breakfix-system", Name: "blank-environment", UID: types.UID("blank-environment-uid"), CreationTimestamp: metav1.NewTime(createdAt)},
		Spec: runtimev2.RuntimeEnvironmentSpec{
			BlankRuntime: &runtimev2.BlankRuntimeSpec{Provider: "k8s"},
			Purpose:      runtimev2.PurposeLearning,
			Lease:        runtimev2.LeaseSpec{RenewedAt: metav1.NewTime(createdAt)},
		},
		Status: runtimev2.RuntimeEnvironmentStatus{Phase: phase, Operation: runtimev2.OperationNone},
	}
}

func TestReconcilerProvisionsBlankEnvironmentFromInstalledPlan(t *testing.T) {
	now := fixedRuntimeEnvironmentTime()
	plan := blankTestPlan(t)
	environment := blankTestEnvironment(plan, runtimev2.PhasePending, now)
	provider := &reconcilerProvider{provision: Observation{Ready: true, ResourceRefs: []runtimev2.ResourceReference{{Provider: "k8s", Kind: "namespace", ID: "runtime-environment"}}}}
	queue := NewInMemoryReapQueue()
	scheme := runtime.NewScheme()
	if err := runtimev2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&runtimev2.RuntimeEnvironment{}).WithObjects(environment).Build()
	reconciler := &Reconciler{Client: kubeClient, Resolver: reconcilerResolver{}, Provider: provider, Reaps: queue, Blank: staticBlankSource{plan: plan}, Now: func() time.Time { return now }}

	reconcileRuntimeEnvironmentTimes(t, reconciler, environment.Name, 3)
	current := getRuntimeEnvironment(t, kubeClient, environment.Name)
	if current.Status.Phase != runtimev2.PhaseReady || current.Status.Operation != runtimev2.OperationNone {
		t.Fatalf("blank status = %#v", current.Status)
	}
	if current.Status.Runtime.Provider != "k8s" {
		t.Fatalf("blank runtime projection = %#v", current.Status.Runtime)
	}
	if provider.provisionCalls != 1 || provider.lastProvision.BlankRuntime == nil || provider.lastProvision.BlankRuntime.Image != plan.Image {
		t.Fatalf("blank provision calls=%d binding=%#v", provider.provisionCalls, provider.lastProvision)
	}
	if provider.lastProvision.RunnableRevision.Spec.Identity != (runnable.ContentIdentity{}) {
		t.Fatalf("blank binding carries a runnable revision: %#v", provider.lastProvision.RunnableRevision)
	}
	if current.Status.Lifecycle.ExpiresAt == nil || !current.Status.Lifecycle.ExpiresAt.Equal(&metav1.Time{Time: now.Add(10 * time.Minute)}) {
		t.Fatalf("blank expiresAt = %#v", current.Status.Lifecycle.ExpiresAt)
	}
}

// The blank playground reset rides the same one-path semantics: an incomplete
// wipe sustains Resetting with Provision unreachable, and the rebuilt
// terminal's Ready observation alone clears the operation.
func TestReconcilerRunsBlankResetToOneCompletion(t *testing.T) {
	now := fixedRuntimeEnvironmentTime()
	plan := blankTestPlan(t)
	environment := blankTestEnvironment(plan, runtimev2.PhaseReady, now)
	environment.Spec.ResetNonce = 1
	environment.Finalizers = []string{finalizer}
	provider := &reconcilerProvider{reset: Observation{Ready: false}, provision: Observation{Ready: true}}
	queue := NewInMemoryReapQueue()
	scheme := runtime.NewScheme()
	if err := runtimev2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&runtimev2.RuntimeEnvironment{}).WithObjects(environment).Build()
	reconciler := &Reconciler{Client: kubeClient, Resolver: reconcilerResolver{}, Provider: provider, Reaps: queue, Blank: staticBlankSource{plan: plan}, Now: func() time.Time { return now }}

	reconcileRuntimeEnvironmentTimes(t, reconciler, environment.Name, 2)
	current := getRuntimeEnvironment(t, kubeClient, environment.Name)
	if provider.resetCalls != 2 || provider.provisionCalls != 0 {
		t.Fatalf("blank reset calls=%d provision calls=%d, want the wipe sustained and provision unreachable", provider.resetCalls, provider.provisionCalls)
	}
	if current.Status.Phase != runtimev2.PhaseReady || current.Status.Operation != runtimev2.OperationResetting {
		t.Fatalf("blank status during wipe = %#v, want Ready+Resetting", current.Status)
	}
	if current.Annotations[resetNonceAnnotation] != "1" || current.Status.ObservedResetNonce != 1 {
		t.Fatalf("blank reset adoption annotations=%#v status nonce=%d", current.Annotations, current.Status.ObservedResetNonce)
	}

	provider.reset = Observation{Ready: true}
	reconcileRuntimeEnvironmentTimes(t, reconciler, environment.Name, 1)
	current = getRuntimeEnvironment(t, kubeClient, environment.Name)
	if current.Status.Phase != runtimev2.PhaseReady || current.Status.Operation != runtimev2.OperationNone {
		t.Fatalf("blank status after rebuilt ready = %#v, want the operation cleared", current.Status)
	}
	if provider.lastReset.BlankRuntime == nil {
		t.Fatalf("blank reset binding = %#v", provider.lastReset)
	}
}

func TestReconcilerFailsBlankEnvironmentWithoutInstalledPlan(t *testing.T) {
	now := fixedRuntimeEnvironmentTime()
	plan := blankTestPlan(t)
	environment := blankTestEnvironment(plan, runtimev2.PhasePending, now)
	environment.Finalizers = []string{finalizer}
	provider := &reconcilerProvider{}
	scheme := runtime.NewScheme()
	if err := runtimev2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&runtimev2.RuntimeEnvironment{}).WithObjects(environment).Build()
	reconciler := &Reconciler{Client: kubeClient, Resolver: reconcilerResolver{}, Provider: provider, Reaps: NewInMemoryReapQueue(), Now: func() time.Time { return now }}

	reconcileRuntimeEnvironmentTimes(t, reconciler, environment.Name, 1)
	current := getRuntimeEnvironment(t, kubeClient, environment.Name)
	if current.Status.Phase != runtimev2.PhaseFailed || current.Status.Failure == nil || current.Status.Failure.Reason != "blank-runtime-not-installed" {
		t.Fatalf("blank status without plan = %#v", current.Status)
	}
}

func TestReconcilerReapsBlankEnvironmentThroughReapQueue(t *testing.T) {
	now := fixedRuntimeEnvironmentTime().Add(11 * time.Minute)
	plan := blankTestPlan(t)
	environment := blankTestEnvironment(plan, runtimev2.PhaseReady, fixedRuntimeEnvironmentTime())
	environment.Finalizers = []string{finalizer}
	provider := &reconcilerProvider{stopDone: true, releaseDone: true}
	queue := NewInMemoryReapQueue()
	scheme := runtime.NewScheme()
	if err := runtimev2.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&runtimev2.RuntimeEnvironment{}).WithObjects(environment).Build()
	reconciler := &Reconciler{Client: kubeClient, Resolver: reconcilerResolver{}, Provider: provider, Reaps: queue, Blank: staticBlankSource{plan: plan}, Now: func() time.Time { return now }}

	reconcileRuntimeEnvironmentTimes(t, reconciler, environment.Name, 2)
	current := getRuntimeEnvironment(t, kubeClient, environment.Name)
	if current.Status.Phase != runtimev2.PhaseDraining {
		t.Fatalf("blank drain status=%#v", current.Status)
	}
	key := ReapRequest{Namespace: environment.Namespace, Name: environment.Name, UID: string(environment.UID)}.Key()
	record, err := queue.Get(context.Background(), key)
	if err != nil || record.State != ReapQueued {
		t.Fatalf("blank reap handoff = %#v, %v", record, err)
	}
	reaper := &Reaper{Queue: queue, Provider: provider, Owner: "reaper-a", Now: func() time.Time { return now }}
	if processed, err := reaper.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("blank reaper processed=%t err=%v", processed, err)
	}
	if provider.releaseCalls != 1 || provider.lastRelease.BlankRuntime == nil {
		t.Fatalf("blank release calls=%d binding=%#v", provider.releaseCalls, provider.lastRelease)
	}

	reconcileRuntimeEnvironmentTimes(t, reconciler, environment.Name, 1)
	current = getRuntimeEnvironment(t, kubeClient, environment.Name)
	if current.Status.Phase != runtimev2.PhaseReleased || current.Status.Lifecycle.ReleasedAt == nil {
		t.Fatalf("blank released environment = %#v", current.Status)
	}

	reconcileRuntimeEnvironmentTimes(t, reconciler, environment.Name, 1)
	var deleted runtimev2.RuntimeEnvironment
	if err := kubeClient.Get(context.Background(), client.ObjectKey{Namespace: environment.Namespace, Name: environment.Name}, &deleted); err == nil {
		t.Fatalf("reaped blank environment remains: %#v", deleted)
	}
}

func TestValidateSpecSeparatesBlankAndRevisionArms(t *testing.T) {
	plan := blankTestPlan(t)
	now := fixedRuntimeEnvironmentTime()
	environment := blankTestEnvironment(plan, runtimev2.PhasePending, now)
	if err := ValidateSpec(*environment, Plan{Blank: &plan}); err != nil {
		t.Fatalf("valid blank environment rejected: %v", err)
	}
	if err := ValidateSpec(*environment, Plan{Revision: validRevision(t)}); err == nil {
		t.Fatal("revision plan accepted for a blank environment")
	}
	revisionEnvironment := testRuntimeEnvironment(t, validRevision(t), runtimev2.PhasePending, now)
	if err := ValidateSpec(*revisionEnvironment, Plan{Blank: &plan}); err == nil {
		t.Fatal("blank plan accepted for a revision-bound environment")
	}
	environment.Spec.BlankRuntime.Provider = "node"
	if err := ValidateSpec(*environment, Plan{Blank: &plan}); err == nil {
		t.Fatal("blank provider mismatch accepted")
	}
}

func TestReaperUsesBlankPlanLifecycleForBlankBindings(t *testing.T) {
	now := fixedRuntimeEnvironmentTime()
	plan := blankTestPlan(t)
	queue := NewInMemoryReapQueue()
	binding := Binding{Namespace: "breakfix-system", Name: "blank-environment", UID: "blank-environment-uid", Purpose: runnable.PurposeLearning, BlankRuntime: &plan}
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	request := ReapRequest{Namespace: binding.Namespace, Name: binding.Name, UID: binding.UID, Revision: digest, Binding: binding}
	if err := queue.Enqueue(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	// A blank binding carries a zero runnable revision; the reaper must still
	// release through the plan lifecycle instead of expiring instantly.
	provider := &reconcilerProvider{stopDone: true, releaseDone: true}
	reaper := &Reaper{Queue: queue, Provider: provider, Owner: "reaper-a", Now: func() time.Time { return now }}
	if processed, err := reaper.RunOnce(context.Background()); err != nil || !processed {
		t.Fatalf("blank reap processed=%t err=%v", processed, err)
	}
	record, err := queue.Get(context.Background(), request.Key())
	if err != nil || record.State != ReapSucceeded {
		t.Fatalf("blank reap record=%#v err=%v", record, err)
	}
	if provider.lastRelease.BlankRuntime == nil {
		t.Fatal("blank reap released without the blank plan binding")
	}
}
