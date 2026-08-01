package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/incusprovider"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeNodeProvider struct {
	identity         incusprovider.NodeEnvironmentIdentity
	observation      incusprovider.NodeEnvironmentObservation
	preflightErr     error
	provisionErr     error
	observeErr       error
	deleteErr        error
	execResult       incusprovider.ExecNodeResult
	execErr          error
	provisionCalls   int
	observeCalls     int
	preflightCalls   int
	deleteCalls      int
	execCalls        int
	lastExecRequest  incusprovider.ExecNodeRequest
	lastProvisionReq incusprovider.ProvisionNodeEnvironmentRequest
}

func (f *fakeNodeProvider) Preflight(context.Context) (incusprovider.PreflightResult, error) {
	f.preflightCalls++
	return incusprovider.PreflightResult{}, f.preflightErr
}

func (f *fakeNodeProvider) NodeEnvironmentIdentity(string, []string) (incusprovider.NodeEnvironmentIdentity, error) {
	return f.identity, nil
}

func (f *fakeNodeProvider) ProvisionNodeEnvironment(_ context.Context, request incusprovider.ProvisionNodeEnvironmentRequest) (incusprovider.NodeEnvironmentObservation, error) {
	f.provisionCalls++
	f.lastProvisionReq = request
	return f.observation, f.provisionErr
}

func (f *fakeNodeProvider) ObserveNodeEnvironment(_ context.Context, request incusprovider.ProvisionNodeEnvironmentRequest) (incusprovider.NodeEnvironmentObservation, error) {
	f.observeCalls++
	f.lastProvisionReq = request
	return f.observation, f.observeErr
}

func (f *fakeNodeProvider) DeleteNodeEnvironment(_ context.Context, _ incusprovider.ProvisionNodeEnvironmentRequest) error {
	f.deleteCalls++
	return f.deleteErr
}

func (f *fakeNodeProvider) ExecNode(_ context.Context, request incusprovider.ExecNodeRequest) (incusprovider.ExecNodeResult, error) {
	f.execCalls++
	f.lastExecRequest = request
	return f.execResult, f.execErr
}

func TestNodeVerificationEnvironmentBecomesReadyWithoutAutomaticChecks(t *testing.T) {
	environment := validNodeEnvironment("node-verification", breakfixv1.EnvironmentPurposeVerification)
	provider := readyNodeProvider()
	reconciler, kubeClient := newNodeTestReconciler(t, environment, provider)

	reconcileNodeTimes(t, reconciler, environment.Name, 3)
	current := getNodeEnvironment(t, kubeClient, environment.Name)
	if current.Status.Environment.Phase != breakfixv1.EnvironmentReady {
		t.Fatalf("phase = %q, want Ready", current.Status.Environment.Phase)
	}
	if provider.execCalls != 0 {
		t.Fatalf("verification environment executed %d automatic checks", provider.execCalls)
	}
	if provider.lastProvisionReq.ImageFingerprint != environment.Spec.Runtime.ImageFingerprint {
		t.Fatalf("provider image fingerprint = %q", provider.lastProvisionReq.ImageFingerprint)
	}
}

func TestNodeLearningEnvironmentCompletesFromNodeCheckpoint(t *testing.T) {
	environment := validNodeEnvironment("node-learning", breakfixv1.EnvironmentPurposeLearning)
	provider := readyNodeProvider()
	provider.execResult = incusprovider.ExecNodeResult{Stdout: `{"checks":[{"id":"proxy-ready","passed":true,"summary":"proxy is ready"}]}`}
	reconciler, kubeClient := newNodeTestReconciler(t, environment, provider)

	reconcileNodeTimes(t, reconciler, environment.Name, 3)
	current := getNodeEnvironment(t, kubeClient, environment.Name)
	if current.Status.Environment.Phase != breakfixv1.EnvironmentCompleted {
		t.Fatalf("phase = %q, want Completed", current.Status.Environment.Phase)
	}
	if provider.execCalls != 1 || provider.lastExecRequest.LogicalName != "proxy" {
		t.Fatalf("checkpoint exec = %d on %q", provider.execCalls, provider.lastExecRequest.LogicalName)
	}
	wantCommand := []string{"/bin/bash", "/opt/breakfix/challenge/nodes/proxy/checks.sh"}
	if len(provider.lastExecRequest.Command) != len(wantCommand) || provider.lastExecRequest.Command[0] != wantCommand[0] || provider.lastExecRequest.Command[1] != wantCommand[1] {
		t.Fatalf("checkpoint command = %v", provider.lastExecRequest.Command)
	}
}

func TestNodeRuntimeInitializationFailureIsArtifactFailure(t *testing.T) {
	environment := validNodeEnvironment("node-init-failed", breakfixv1.EnvironmentPurposeVerification)
	provider := readyNodeProvider()
	provider.observation.Ready = false
	provider.observation.Nodes[0].Initialization = incusprovider.NodeInitialization{Failed: true, ExitCode: 23, Message: "generate failed"}
	reconciler, kubeClient := newNodeTestReconciler(t, environment, provider)

	reconcileNodeTimes(t, reconciler, environment.Name, 3)
	current := getNodeEnvironment(t, kubeClient, environment.Name)
	if current.Status.Environment.Phase != breakfixv1.EnvironmentFailed || current.Status.Environment.Failure == nil {
		t.Fatalf("expected failed status, got %#v", current.Status.Environment)
	}
	if current.Status.Environment.Failure.Class != breakfixv1.EnvironmentFailureArtifact || current.Status.Environment.Failure.Reason != "RuntimeInitializationFailed" {
		t.Fatalf("failure = %#v", current.Status.Environment.Failure)
	}
}

func TestNodeProviderUnavailableRemainsRetryableInfrastructureState(t *testing.T) {
	environment := validNodeEnvironment("node-provider-failed", breakfixv1.EnvironmentPurposeVerification)
	provider := readyNodeProvider()
	provider.provisionErr = fmt.Errorf("dial Incus: %w", incusprovider.ErrUnavailable)
	reconciler, kubeClient := newNodeTestReconciler(t, environment, provider)

	reconcileNodeTimes(t, reconciler, environment.Name, 3)
	current := getNodeEnvironment(t, kubeClient, environment.Name)
	if current.Status.Environment.Phase != breakfixv1.EnvironmentProvisioning || current.Status.Environment.Failure == nil {
		t.Fatalf("expected retryable provisioning state, got %#v", current.Status.Environment)
	}
	if current.Status.Environment.Failure.Class != breakfixv1.EnvironmentFailureInfrastructure {
		t.Fatalf("failure class = %q", current.Status.Environment.Failure.Class)
	}
}

func TestNodePreflightStopsProvisioningWhenProviderIsUnavailable(t *testing.T) {
	environment := validNodeEnvironment("node-preflight-failed", breakfixv1.EnvironmentPurposeVerification)
	provider := readyNodeProvider()
	provider.preflightErr = fmt.Errorf("dial provider: %w", incusprovider.ErrUnavailable)
	reconciler, kubeClient := newNodeTestReconciler(t, environment, provider)

	reconcileNodeTimes(t, reconciler, environment.Name, 3)
	current := getNodeEnvironment(t, kubeClient, environment.Name)
	if provider.preflightCalls != 1 || provider.provisionCalls != 0 {
		t.Fatalf("preflight/provision calls = %d/%d, want 1/0", provider.preflightCalls, provider.provisionCalls)
	}
	if current.Status.Environment.Phase != breakfixv1.EnvironmentProvisioning || current.Status.Environment.Failure == nil {
		t.Fatalf("expected retryable preflight failure, got %#v", current.Status.Environment)
	}
	if current.Status.Environment.Failure.Reason != "ProviderUnavailable" {
		t.Fatalf("failure = %#v", current.Status.Environment.Failure)
	}
}

func TestReadyNodeEnvironmentRemainsReadyWhenProviderIsTemporarilyUnavailable(t *testing.T) {
	environment := validNodeEnvironment("node-ready-provider-unavailable", breakfixv1.EnvironmentPurposeVerification)
	provider := readyNodeProvider()
	reconciler, kubeClient := newNodeTestReconciler(t, environment, provider)

	reconcileNodeTimes(t, reconciler, environment.Name, 3)
	provider.observeErr = fmt.Errorf("dial provider: %w", incusprovider.ErrUnavailable)
	reconcileNodeTimes(t, reconciler, environment.Name, 1)

	current := getNodeEnvironment(t, kubeClient, environment.Name)
	if current.Status.Environment.Phase != breakfixv1.EnvironmentReady || current.Status.Environment.Failure != nil {
		t.Fatalf("ready environment was downgraded by a transient provider failure: %#v", current.Status.Environment)
	}
	if provider.provisionCalls != 1 || provider.observeCalls != 1 {
		t.Fatalf("provision/observe calls = %d/%d, want 1/1", provider.provisionCalls, provider.observeCalls)
	}
}

func TestNodeDeletionRunsExactProviderCleanupBeforeRemovingFinalizer(t *testing.T) {
	environment := validNodeEnvironment("node-delete", breakfixv1.EnvironmentPurposeVerification)
	environment.Finalizers = []string{nodeEnvironmentFinalizer}
	now := metav1.NewTime(time.Now().UTC())
	environment.DeletionTimestamp = &now
	provider := readyNodeProvider()
	provider.preflightErr = fmt.Errorf("base image unavailable: %w", incusprovider.ErrUnavailable)
	reconciler, kubeClient := newNodeTestReconciler(t, environment, provider)

	reconcileNodeTimes(t, reconciler, environment.Name, 1)
	if provider.deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want 1", provider.deleteCalls)
	}
	if provider.preflightCalls != 0 {
		t.Fatalf("deletion preflight calls = %d, want 0", provider.preflightCalls)
	}
	if err := kubeClient.Get(context.Background(), client.ObjectKey{Name: environment.Name, Namespace: environment.Namespace}, &breakfixv1.NodeEnvironment{}); err == nil {
		t.Fatal("environment should disappear after provider cleanup and finalizer removal")
	}
}

func TestValidateNodeEnvironmentRejectsIncompleteRuntimeSnapshot(t *testing.T) {
	environment := validNodeEnvironment("node-invalid", breakfixv1.EnvironmentPurposeVerification)
	environment.Spec.Runtime.ImageFingerprint = "latest"
	if err := validateNodeEnvironmentSpec(environment); err == nil {
		t.Fatal("expected incomplete Incus fingerprint to be rejected")
	}
}

func validNodeEnvironment(name string, purpose breakfixv1.EnvironmentPurpose) *breakfixv1.NodeEnvironment {
	sourceKind := breakfixv1.EnvironmentSourceCandidate
	if purpose == breakfixv1.EnvironmentPurposeLearning {
		sourceKind = breakfixv1.EnvironmentSourcePublished
	}
	return &breakfixv1.NodeEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "breakfix-system", UID: types.UID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
			CreationTimestamp: metav1.NewTime(time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)),
		},
		Spec: breakfixv1.NodeEnvironmentSpec{
			Environment: breakfixv1.EnvironmentSpec{
				Purpose:     purpose,
				Source:      breakfixv1.EnvironmentSourceSpec{Kind: sourceKind, Ref: "source-one", Revision: "sha256:revision"},
				Checkpoints: []breakfixv1.EnvironmentCheckpointSpec{{ID: "proxy-ready", Node: "proxy"}},
				Lifecycle:   breakfixv1.EnvironmentLifecycleSpec{},
			},
			Runtime: breakfixv1.NodeRuntimeSnapshot{
				ImageFingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				ProfileRevision:  "node-profile-v1", NetworkPolicyRevision: "node-network-v1",
				Nodes:     []breakfixv1.NodeRuntimeNodeSpec{{Name: "client", Title: "Client"}, {Name: "proxy", Title: "Proxy"}},
				Resources: breakfixv1.NodeResourceSnapshot{CPU: "1", Memory: "512MiB", Processes: 512, RootDisk: "5GiB"},
			},
		},
	}
}

func readyNodeProvider() *fakeNodeProvider {
	identity := incusprovider.NodeEnvironmentIdentity{
		Project: "bf-project", Network: "bf-network", ACL: "bf-acl", Profile: "bf-profile",
		Nodes: []incusprovider.NodeIdentity{
			{LogicalName: "client", InstanceName: "bf-client", Address: "10.1.1.10"},
			{LogicalName: "proxy", InstanceName: "bf-proxy", Address: "10.1.1.11"},
		},
	}
	return &fakeNodeProvider{
		identity: identity,
		observation: incusprovider.NodeEnvironmentObservation{
			Identity: identity, Ready: true,
			Nodes: []incusprovider.NodeObservation{
				{Node: identity.Nodes[0], Running: true, Initialization: incusprovider.NodeInitialization{Complete: true}},
				{Node: identity.Nodes[1], Running: true, Initialization: incusprovider.NodeInitialization{Complete: true}},
			},
		},
	}
}

func newNodeTestReconciler(t *testing.T, environment *breakfixv1.NodeEnvironment, provider *fakeNodeProvider) (*NodeEnvironmentReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := breakfixv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&breakfixv1.NodeEnvironment{}).WithObjects(environment).Build()
	reconciler := &NodeEnvironmentReconciler{
		Client: kubeClient, Provider: provider,
		Now: func() time.Time { return time.Date(2026, 7, 30, 2, 0, 0, 0, time.UTC) },
	}
	return reconciler, kubeClient
}

func reconcileNodeTimes(t *testing.T, reconciler *NodeEnvironmentReconciler, name string, count int) {
	t.Helper()
	for range count {
		if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "breakfix-system"}}); err != nil {
			t.Fatal(err)
		}
	}
}

func getNodeEnvironment(t *testing.T, kubeClient client.Client, name string) *breakfixv1.NodeEnvironment {
	t.Helper()
	var environment breakfixv1.NodeEnvironment
	if err := kubeClient.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "breakfix-system"}, &environment); err != nil {
		t.Fatal(err)
	}
	return &environment
}
