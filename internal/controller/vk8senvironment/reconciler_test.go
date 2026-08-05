package vk8senvironment

import (
	"context"
	"errors"
	"testing"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	environmentdomain "github.com/breakfix/breakfix/internal/domain/environment"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testImageDigest = "registry.example/breakfix/candidate@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeVK8sProvider struct {
	identity         environmentdomain.VK8sEnvironmentIdentity
	observation      environmentdomain.VK8sEnvironmentObservation
	provisionErr     error
	deleteDone       bool
	deleteErr        error
	execResult       environmentdomain.ExecutionResult
	execErr          error
	provisionCalls   int
	deleteCalls      int
	execCalls        int
	lastExecCommand  []string
	lastProvisionReq environmentdomain.VK8sProvisionRequest
}

func (f *fakeVK8sProvider) Identity(string) (environmentdomain.VK8sEnvironmentIdentity, error) {
	return f.identity, nil
}

func (f *fakeVK8sProvider) Provision(_ context.Context, request environmentdomain.VK8sProvisionRequest) (environmentdomain.VK8sEnvironmentObservation, error) {
	f.provisionCalls++
	f.lastProvisionReq = request
	return f.observation, f.provisionErr
}

func (f *fakeVK8sProvider) Delete(_ context.Context, _ environmentdomain.VK8sProvisionRequest) (bool, error) {
	f.deleteCalls++
	return f.deleteDone, f.deleteErr
}

func (f *fakeVK8sProvider) ExecuteTerminal(_ context.Context, _ environmentdomain.VK8sProvisionRequest, command []string) (environmentdomain.ExecutionResult, error) {
	f.execCalls++
	f.lastExecCommand = append([]string(nil), command...)
	return f.execResult, f.execErr
}

func TestVK8sVerificationEnvironmentBecomesReadyWithoutAutomaticChecks(t *testing.T) {
	environment := validVK8sEnvironment("verification-ready", breakfixv1.EnvironmentPurposeVerification)
	provider := readyVK8sProvider()
	reconciler, kubeClient := newVK8sTestReconciler(t, environment, provider)

	reconcileVK8sTimes(t, reconciler, environment.Name, 3)
	current := getVK8sEnvironment(t, kubeClient, environment.Name)
	if current.Status.Environment.Phase != breakfixv1.EnvironmentReady {
		t.Fatalf("phase = %q, want Ready", current.Status.Environment.Phase)
	}
	if !current.Status.Runtime.Initialized {
		t.Fatal("runtime should be initialized")
	}
	if provider.execCalls != 0 {
		t.Fatalf("verification environment executed %d automatic checks", provider.execCalls)
	}
	if provider.lastProvisionReq.Purpose != environmentdomain.PurposeVerification {
		t.Fatalf("provider purpose = %q", provider.lastProvisionReq.Purpose)
	}
}

func TestVK8sLearningEnvironmentCompletesFromCheckpointReport(t *testing.T) {
	environment := validVK8sEnvironment("learning-complete", breakfixv1.EnvironmentPurposeLearning)
	provider := readyVK8sProvider()
	provider.execResult = environmentdomain.ExecutionResult{Stdout: `{"checks":[{"id":"workload-ready","passed":true,"summary":"workload is ready"}]}`}
	reconciler, kubeClient := newVK8sTestReconciler(t, environment, provider)

	reconcileVK8sTimes(t, reconciler, environment.Name, 3)
	current := getVK8sEnvironment(t, kubeClient, environment.Name)
	if current.Status.Environment.Phase != breakfixv1.EnvironmentCompleted {
		t.Fatalf("phase = %q, want Completed", current.Status.Environment.Phase)
	}
	if provider.execCalls != 1 {
		t.Fatalf("checkpoint exec calls = %d, want 1", provider.execCalls)
	}
	if len(provider.lastExecCommand) != 2 || provider.lastExecCommand[0] != "/bin/bash" || provider.lastExecCommand[1] != vk8sCheckpointRoot+"/checks.sh" {
		t.Fatalf("checkpoint command = %v", provider.lastExecCommand)
	}
	if current.Status.Environment.Checkpoints == nil || len(current.Status.Environment.Checkpoints.Results) != 1 || current.Status.Environment.Checkpoints.Results[0].FirstPassedAt == nil {
		t.Fatalf("checkpoint status was not persisted: %#v", current.Status.Environment.Checkpoints)
	}
}

func TestVK8sRuntimeInitializationFailureIsArtifactFailure(t *testing.T) {
	environment := validVK8sEnvironment("initialization-failed", breakfixv1.EnvironmentPurposeVerification)
	provider := readyVK8sProvider()
	provider.observation.TerminalReady = false
	provider.observation.Initialization = environmentdomain.InitializationObservation{Failed: true, ExitCode: 17, Message: "generate failed"}
	reconciler, kubeClient := newVK8sTestReconciler(t, environment, provider)

	reconcileVK8sTimes(t, reconciler, environment.Name, 3)
	current := getVK8sEnvironment(t, kubeClient, environment.Name)
	if current.Status.Environment.Phase != breakfixv1.EnvironmentFailed || current.Status.Environment.Failure == nil {
		t.Fatalf("expected failed status, got %#v", current.Status.Environment)
	}
	if current.Status.Environment.Failure.Class != breakfixv1.EnvironmentFailureArtifact || current.Status.Environment.Failure.Reason != "RuntimeInitializationFailed" {
		t.Fatalf("failure = %#v", current.Status.Environment.Failure)
	}
}

func TestVK8sProviderFailureRemainsRetryableInfrastructureState(t *testing.T) {
	environment := validVK8sEnvironment("provider-failed", breakfixv1.EnvironmentPurposeVerification)
	provider := readyVK8sProvider()
	provider.provisionErr = errors.New("Kubernetes API unavailable")
	reconciler, kubeClient := newVK8sTestReconciler(t, environment, provider)

	reconcileVK8sTimes(t, reconciler, environment.Name, 3)
	current := getVK8sEnvironment(t, kubeClient, environment.Name)
	if current.Status.Environment.Phase != breakfixv1.EnvironmentProvisioning || current.Status.Environment.Failure == nil {
		t.Fatalf("expected retryable provisioning state, got %#v", current.Status.Environment)
	}
	if current.Status.Environment.Failure.Class != breakfixv1.EnvironmentFailureInfrastructure {
		t.Fatalf("failure class = %q", current.Status.Environment.Failure.Class)
	}
}

func TestVK8sDeletionWaitsForProviderCleanup(t *testing.T) {
	environment := validVK8sEnvironment("delete-runtime", breakfixv1.EnvironmentPurposeVerification)
	environment.Finalizers = []string{vk8sEnvironmentFinalizer}
	now := metav1.NewTime(time.Now().UTC())
	environment.DeletionTimestamp = &now
	provider := readyVK8sProvider()
	provider.deleteDone = false
	reconciler, kubeClient := newVK8sTestReconciler(t, environment, provider)

	reconcileVK8sTimes(t, reconciler, environment.Name, 1)
	current := getVK8sEnvironment(t, kubeClient, environment.Name)
	if !containsString(current.Finalizers, vk8sEnvironmentFinalizer) {
		t.Fatal("finalizer removed before provider cleanup completed")
	}
	provider.deleteDone = true
	reconcileVK8sTimes(t, reconciler, environment.Name, 1)
	if err := kubeClient.Get(context.Background(), client.ObjectKey{Name: environment.Name, Namespace: environment.Namespace}, &breakfixv1.VK8sEnvironment{}); err == nil {
		t.Fatal("environment should disappear after its finalizer is removed")
	}
	if provider.deleteCalls != 2 {
		t.Fatalf("delete calls = %d, want 2", provider.deleteCalls)
	}
}

func TestValidateVK8sEnvironmentRejectsMutableImageReference(t *testing.T) {
	environment := validVK8sEnvironment("mutable-image", breakfixv1.EnvironmentPurposeVerification)
	environment.Spec.Runtime.ImageDigest = "registry.example/breakfix/candidate:latest"
	if err := validateVK8sEnvironmentSpec(environment); err == nil {
		t.Fatal("expected mutable image reference to be rejected")
	}
}

func validVK8sEnvironment(name string, purpose breakfixv1.EnvironmentPurpose) *breakfixv1.VK8sEnvironment {
	sourceKind := breakfixv1.EnvironmentSourceCandidate
	if purpose == breakfixv1.EnvironmentPurposeLearning {
		sourceKind = breakfixv1.EnvironmentSourcePublished
	}
	return &breakfixv1.VK8sEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "breakfix-system", UID: types.UID("11111111-2222-3333-4444-555555555555"),
			CreationTimestamp: metav1.NewTime(time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)),
		},
		Spec: breakfixv1.VK8sEnvironmentSpec{
			Environment: breakfixv1.EnvironmentSpec{
				Purpose:     purpose,
				Source:      breakfixv1.EnvironmentSourceSpec{Kind: sourceKind, Ref: "source-one", Revision: "sha256:revision"},
				Checkpoints: []breakfixv1.EnvironmentCheckpointSpec{{ID: "workload-ready"}},
				Lifecycle:   breakfixv1.EnvironmentLifecycleSpec{},
			},
			Runtime: breakfixv1.VK8sRuntimeSnapshot{
				ImageDigest: testImageDigest, ProfileRevision: "vk8s-profile-v1", Version: "v1.35.0",
				ManagementTerminalImage: "registry.example/breakfix/k8s-base@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				Resources: breakfixv1.VK8sResourceSnapshot{
					ControlPlaneCPU: "250m", ControlPlaneMemory: "512Mi", ControlPlaneEphemeralStorage: "4Gi",
					WorkloadCPU: "250m", WorkloadMemory: "512Mi", WorkloadEphemeralStorage: "2Gi",
					QuotaCPU: "2", QuotaMemory: "2Gi", QuotaEphemeralStorage: "8Gi",
				},
				Network: breakfixv1.VK8sNetworkSnapshot{
					PublicEgressCIDR: "0.0.0.0/0",
					ProtectedCIDRs:   []string{"10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16"},
				},
			},
		},
	}
}

func readyVK8sProvider() *fakeVK8sProvider {
	return &fakeVK8sProvider{
		//nolint:gosec // Kubernetes Secret object name, not credential material.
		identity: environmentdomain.VK8sEnvironmentIdentity{
			Namespace: "breakfix-vk8s-test", VClusterName: "vc-test",
			KubeconfigSecretName: "breakfix-vk8s-kubeconfig", TerminalPodName: "terminal",
		},
		observation: environmentdomain.VK8sEnvironmentObservation{
			ControlPlaneReady: true, KubeconfigReady: true, TerminalReady: true,
			Initialization: environmentdomain.InitializationObservation{Complete: true},
		},
		deleteDone: true,
	}
}

func newVK8sTestReconciler(t *testing.T, environment *breakfixv1.VK8sEnvironment, provider *fakeVK8sProvider) (*VK8sEnvironmentReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := breakfixv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&breakfixv1.VK8sEnvironment{}).WithObjects(environment).Build()
	reconciler := &VK8sEnvironmentReconciler{
		Client: kubeClient, Provider: provider,
		Now: func() time.Time { return time.Date(2026, 7, 30, 2, 0, 0, 0, time.UTC) },
	}
	return reconciler, kubeClient
}

func reconcileVK8sTimes(t *testing.T, reconciler *VK8sEnvironmentReconciler, name string, count int) {
	t.Helper()
	for range count {
		if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "breakfix-system"}}); err != nil {
			t.Fatal(err)
		}
	}
}

func getVK8sEnvironment(t *testing.T, kubeClient client.Client, name string) *breakfixv1.VK8sEnvironment {
	t.Helper()
	var environment breakfixv1.VK8sEnvironment
	if err := kubeClient.Get(context.Background(), client.ObjectKey{Name: name, Namespace: "breakfix-system"}, &environment); err != nil {
		t.Fatal(err)
	}
	return &environment
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
