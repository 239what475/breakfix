package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/runtimeprofile"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	vk8sEnvironmentFinalizer               = "breakfix.dev/vk8s-environment-cleanup"
	vk8sEnvironmentMaxConcurrentReconciles = 2
	vk8sProvisioningInterval               = 2 * time.Second
	vk8sProviderRetryInterval              = 5 * time.Second
)

var immutableImagePattern = regexp.MustCompile(`^[^[:space:]]+@sha256:[0-9a-f]{64}$`)

type VK8sEnvironmentReconciler struct {
	client.Client
	Provider VK8sEnvironmentProvider
	Now      func() time.Time
}

func (r *VK8sEnvironmentReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var environment breakfixv1.VK8sEnvironment
	if err := r.Get(ctx, request.NamespacedName, &environment); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if r.Provider == nil {
		return ctrl.Result{}, fmt.Errorf("VK8s environment provider is required")
	}

	if !environment.DeletionTimestamp.IsZero() {
		return r.reconcileDeletion(ctx, &environment)
	}
	if !controllerutil.ContainsFinalizer(&environment, vk8sEnvironmentFinalizer) {
		before := environment.DeepCopy()
		controllerutil.AddFinalizer(&environment, vk8sEnvironmentFinalizer)
		if err := r.Patch(ctx, &environment, client.MergeFrom(before)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	statusBefore := environment.DeepCopy()
	if err := validateVK8sEnvironmentSpec(&environment); err != nil {
		setVK8sEnvironmentFailure(&environment, breakfixv1.EnvironmentFailureArtifact, "InvalidSnapshot", err.Error(), r.now())
		return ctrl.Result{}, r.patchStatus(ctx, statusBefore, &environment)
	}
	identity, changed, err := r.ensureIdentity(&environment)
	if err != nil {
		setVK8sEnvironmentFailure(&environment, breakfixv1.EnvironmentFailureInfrastructure, "InvalidProviderIdentity", err.Error(), r.now())
		return ctrl.Result{}, r.patchStatus(ctx, statusBefore, &environment)
	}
	if changed {
		return ctrl.Result{Requeue: true}, r.patchStatus(ctx, statusBefore, &environment)
	}
	providerRequest := vk8sProviderRequest(&environment, identity)

	if shouldDestroyVK8sEnvironment(&environment, r.now()) {
		return r.reconcileDrain(ctx, statusBefore, &environment, providerRequest)
	}
	if environment.Status.Environment.Phase == breakfixv1.EnvironmentDestroyed {
		return ctrl.Result{}, nil
	}

	observation, err := r.Provider.Provision(ctx, providerRequest)
	if err != nil {
		return r.handleProviderError(ctx, statusBefore, &environment, err)
	}
	applyVK8sObservation(&environment, observation)
	if observation.ControlPlaneReady {
		setRuntimeEnvironmentCondition(&environment.Status.Environment, breakfixv1.ConditionProvisioned, metav1.ConditionTrue, "ControlPlaneReady", "virtual Kubernetes control plane is ready", environment.Generation)
	}
	if observation.KubeconfigReady {
		setRuntimeEnvironmentCondition(&environment.Status.Environment, breakfixv1.ConditionKubeconfigReady, metav1.ConditionTrue, "KubeconfigReady", "management terminal kubeconfig is ready", environment.Generation)
	}
	if observation.Initialization.Failed {
		message := observation.Initialization.Message
		if strings.TrimSpace(message) == "" {
			message = fmt.Sprintf("runtime initialization exited with %d", observation.Initialization.ExitCode)
		}
		setVK8sEnvironmentFailure(&environment, breakfixv1.EnvironmentFailureArtifact, "RuntimeInitializationFailed", message, r.now())
		return ctrl.Result{}, r.patchStatus(ctx, statusBefore, &environment)
	}
	if !observation.ControlPlaneReady || !observation.KubeconfigReady || !observation.TerminalReady || !observation.Initialization.Complete {
		markVK8sProvisioning(&environment, "RuntimeInitializing", "waiting for virtual cluster and management terminal initialization", r.now())
		return ctrl.Result{RequeueAfter: vk8sProvisioningInterval}, r.patchStatus(ctx, statusBefore, &environment)
	}

	markVK8sReady(&environment, r.now())
	if environment.Spec.Environment.Purpose == breakfixv1.EnvironmentPurposeLearning {
		results, checkErr := r.runCheckpoints(ctx, &environment, providerRequest)
		recordRuntimeCheckpointStatus(&environment.Status.Environment, results, checkErr, r.now())
		if checkErr == nil && checkpointsPassed(results) {
			markRuntimeEnvironmentCompleted(&environment.Status.Environment, environment.Generation, r.now())
		}
	}
	if err := r.patchStatus(ctx, statusBefore, &environment); err != nil {
		return ctrl.Result{}, err
	}
	return vk8sEnvironmentRequeue(&environment, r.now()), nil
}

func (r *VK8sEnvironmentReconciler) reconcileDeletion(ctx context.Context, environment *breakfixv1.VK8sEnvironment) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(environment, vk8sEnvironmentFinalizer) {
		return ctrl.Result{}, nil
	}
	identity, err := r.Provider.EnvironmentIdentity(string(environment.UID))
	if err != nil {
		return ctrl.Result{}, err
	}
	done, err := r.Provider.Delete(ctx, vk8sProviderRequest(environment, identity))
	if err != nil {
		return ctrl.Result{RequeueAfter: vk8sProviderRetryInterval}, err
	}
	if !done {
		return ctrl.Result{RequeueAfter: vk8sProvisioningInterval}, nil
	}
	before := environment.DeepCopy()
	controllerutil.RemoveFinalizer(environment, vk8sEnvironmentFinalizer)
	if err := r.Patch(ctx, environment, client.MergeFrom(before)); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{}, nil
}

func (r *VK8sEnvironmentReconciler) reconcileDrain(ctx context.Context, before *breakfixv1.VK8sEnvironment, environment *breakfixv1.VK8sEnvironment, request VK8sProvisionRequest) (ctrl.Result, error) {
	now := r.now()
	condition := apiMeta.FindStatusCondition(environment.Status.Environment.Conditions, breakfixv1.ConditionDraining)
	if environment.Status.Environment.Phase != breakfixv1.EnvironmentDraining || condition == nil || condition.Status != metav1.ConditionTrue {
		environment.Status.Environment.Phase = breakfixv1.EnvironmentDraining
		setRuntimeEnvironmentCondition(&environment.Status.Environment, breakfixv1.ConditionDraining, metav1.ConditionTrue, "LifecycleExpired", "environment lifecycle expired", environment.Generation)
		setRuntimeEnvironmentCondition(&environment.Status.Environment, breakfixv1.ConditionReady, metav1.ConditionFalse, "LifecycleExpired", "environment is draining", environment.Generation)
		grace := drainGracePeriod(environment.Spec.Environment.Lifecycle)
		if err := r.patchStatus(ctx, before, environment); err != nil {
			return ctrl.Result{}, err
		}
		if grace > 0 {
			return ctrl.Result{RequeueAfter: grace}, nil
		}
		before = environment.DeepCopy()
		condition = apiMeta.FindStatusCondition(environment.Status.Environment.Conditions, breakfixv1.ConditionDraining)
	}
	grace := drainGracePeriod(environment.Spec.Environment.Lifecycle)
	if condition != nil && now.Before(condition.LastTransitionTime.Add(grace)) {
		return ctrl.Result{RequeueAfter: condition.LastTransitionTime.Add(grace).Sub(now)}, nil
	}
	done, err := r.Provider.Delete(ctx, request)
	if err != nil {
		return ctrl.Result{RequeueAfter: vk8sProviderRetryInterval}, err
	}
	if !done {
		return ctrl.Result{RequeueAfter: vk8sProvisioningInterval}, nil
	}
	destroyed := metav1.NewTime(now)
	environment.Status.Environment.Phase = breakfixv1.EnvironmentDestroyed
	environment.Status.Environment.DestroyedAt = &destroyed
	setRuntimeEnvironmentCondition(&environment.Status.Environment, breakfixv1.ConditionCleanedUp, metav1.ConditionTrue, "ResourcesDeleted", "VK8s resources are deleted", environment.Generation)
	setRuntimeEnvironmentCondition(&environment.Status.Environment, breakfixv1.ConditionDraining, metav1.ConditionFalse, "ResourcesDeleted", "environment cleanup completed", environment.Generation)
	return ctrl.Result{}, r.patchStatus(ctx, before, environment)
}

func (r *VK8sEnvironmentReconciler) handleProviderError(ctx context.Context, before *breakfixv1.VK8sEnvironment, environment *breakfixv1.VK8sEnvironment, err error) (ctrl.Result, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ctrl.Result{}, err
	}
	markVK8sProvisioning(environment, "ProviderUnavailable", err.Error(), r.now())
	environment.Status.Environment.Failure = &breakfixv1.EnvironmentFailureStatus{
		Class: breakfixv1.EnvironmentFailureInfrastructure, Component: "vk8s-runtime", Reason: "ProviderUnavailable", Message: truncate(err.Error(), 4000), At: metav1.NewTime(r.now()),
	}
	if patchErr := r.patchStatus(ctx, before, environment); patchErr != nil {
		return ctrl.Result{}, patchErr
	}
	return ctrl.Result{RequeueAfter: vk8sProviderRetryInterval}, nil
}

func (r *VK8sEnvironmentReconciler) ensureIdentity(environment *breakfixv1.VK8sEnvironment) (VK8sEnvironmentIdentity, bool, error) {
	expected, err := r.Provider.EnvironmentIdentity(string(environment.UID))
	if err != nil {
		return VK8sEnvironmentIdentity{}, false, err
	}
	status := &environment.Status.Runtime
	empty := status.Namespace == "" && status.VClusterName == "" && status.KubeconfigSecretName == "" && status.TerminalPodName == ""
	if empty {
		status.Namespace = expected.Namespace
		status.VClusterName = expected.VClusterName
		status.KubeconfigSecretName = expected.KubeconfigSecretName
		status.TerminalPodName = expected.TerminalPodName
		if environment.Status.Environment.Phase == "" {
			environment.Status.Environment.Phase = breakfixv1.EnvironmentPending
		}
		now := metav1.NewTime(r.now())
		if environment.Status.Environment.StartedAt == nil {
			environment.Status.Environment.StartedAt = &now
		}
		environment.Status.Environment.ObservedGeneration = environment.Generation
		return expected, true, nil
	}
	if status.Namespace != expected.Namespace || status.VClusterName != expected.VClusterName || status.KubeconfigSecretName != expected.KubeconfigSecretName || status.TerminalPodName != expected.TerminalPodName {
		return VK8sEnvironmentIdentity{}, false, fmt.Errorf("recorded VK8s identity differs from Environment UID")
	}
	return expected, false, nil
}

func vk8sProviderRequest(environment *breakfixv1.VK8sEnvironment, identity VK8sEnvironmentIdentity) VK8sProvisionRequest {
	return VK8sProvisionRequest{
		EnvironmentUID: string(environment.UID), Revision: environment.Spec.Environment.Source.Revision,
		Purpose: environment.Spec.Environment.Purpose, Identity: identity, Runtime: environment.Spec.Runtime,
	}
}

func validateVK8sEnvironmentSpec(environment *breakfixv1.VK8sEnvironment) error {
	if environment.UID == "" {
		return fmt.Errorf("environment UID is required")
	}
	if err := validateEnvironmentSpec(environment.Spec.Environment); err != nil {
		return err
	}
	for _, checkpoint := range environment.Spec.Environment.Checkpoints {
		if strings.TrimSpace(checkpoint.Node) != "" {
			return fmt.Errorf("VK8s checkpoint %q must not declare a node", checkpoint.ID)
		}
	}
	runtime := environment.Spec.Runtime
	if !immutableImagePattern.MatchString(strings.TrimSpace(runtime.ImageDigest)) {
		return fmt.Errorf("VK8s imageDigest must be an immutable sha256 image reference")
	}
	if !immutableImagePattern.MatchString(strings.TrimSpace(runtime.ManagementTerminalImage)) {
		return fmt.Errorf("VK8s managementTerminalImage must be an immutable sha256 image reference")
	}
	if strings.TrimSpace(runtime.ProfileRevision) == "" || strings.TrimSpace(runtime.Version) == "" {
		return fmt.Errorf("VK8s profile revision and version are required")
	}
	if err := (runtimeprofile.VK8sResources{
		ControlPlaneCPU: runtime.Resources.ControlPlaneCPU, ControlPlaneMemory: runtime.Resources.ControlPlaneMemory,
		ControlPlaneEphemeralStorage: runtime.Resources.ControlPlaneEphemeralStorage,
		WorkloadCPU:                  runtime.Resources.WorkloadCPU, WorkloadMemory: runtime.Resources.WorkloadMemory,
		WorkloadEphemeralStorage: runtime.Resources.WorkloadEphemeralStorage,
		QuotaCPU:                 runtime.Resources.QuotaCPU, QuotaMemory: runtime.Resources.QuotaMemory,
		QuotaEphemeralStorage: runtime.Resources.QuotaEphemeralStorage,
	}).Validate(); err != nil {
		return fmt.Errorf("VK8s resources: %w", err)
	}
	return nil
}

func validateEnvironmentSpec(spec breakfixv1.EnvironmentSpec) error {
	if spec.Purpose != breakfixv1.EnvironmentPurposeLearning && spec.Purpose != breakfixv1.EnvironmentPurposeVerification {
		return fmt.Errorf("unsupported environment purpose %q", spec.Purpose)
	}
	if spec.Source.Kind != breakfixv1.EnvironmentSourcePublished && spec.Source.Kind != breakfixv1.EnvironmentSourceCandidate {
		return fmt.Errorf("unsupported environment source kind %q", spec.Source.Kind)
	}
	if strings.TrimSpace(spec.Source.Ref) == "" || strings.TrimSpace(spec.Source.Revision) == "" {
		return fmt.Errorf("environment source ref and revision are required")
	}
	if spec.Purpose == breakfixv1.EnvironmentPurposeLearning && spec.Source.Kind != breakfixv1.EnvironmentSourcePublished {
		return fmt.Errorf("learning environments require a published source")
	}
	if spec.Purpose == breakfixv1.EnvironmentPurposeVerification && spec.Source.Kind != breakfixv1.EnvironmentSourceCandidate {
		return fmt.Errorf("verification environments require a candidate source")
	}
	checkpointIDs := make(map[string]struct{}, len(spec.Checkpoints))
	for _, checkpoint := range spec.Checkpoints {
		if strings.TrimSpace(checkpoint.ID) == "" {
			return fmt.Errorf("checkpoint ID is required")
		}
		if _, duplicate := checkpointIDs[checkpoint.ID]; duplicate {
			return fmt.Errorf("duplicate checkpoint %q", checkpoint.ID)
		}
		checkpointIDs[checkpoint.ID] = struct{}{}
	}
	if len(checkpointIDs) == 0 {
		return fmt.Errorf("environment checkpoints are required")
	}
	return nil
}

func applyVK8sObservation(environment *breakfixv1.VK8sEnvironment, observation VK8sEnvironmentObservation) {
	environment.Status.Runtime.Initialized = observation.Initialization.Complete && !observation.Initialization.Failed
}

func markVK8sProvisioning(environment *breakfixv1.VK8sEnvironment, reason, message string, now time.Time) {
	status := &environment.Status.Environment
	status.Phase = breakfixv1.EnvironmentProvisioning
	status.ObservedGeneration = environment.Generation
	if status.StartedAt == nil {
		started := metav1.NewTime(now)
		status.StartedAt = &started
	}
	setRuntimeEnvironmentCondition(status, breakfixv1.ConditionReady, metav1.ConditionFalse, reason, truncate(message, 4000), environment.Generation)
}

func markVK8sReady(environment *breakfixv1.VK8sEnvironment, now time.Time) {
	status := &environment.Status.Environment
	if status.Phase != breakfixv1.EnvironmentCompleted {
		status.Phase = breakfixv1.EnvironmentReady
	}
	status.ObservedGeneration = environment.Generation
	if status.ReadyAt == nil {
		ready := metav1.NewTime(now)
		status.ReadyAt = &ready
	}
	status.Failure = nil
	setRuntimeEnvironmentCondition(status, breakfixv1.ConditionReady, metav1.ConditionTrue, "RuntimeReady", "virtual cluster and management terminal are ready", environment.Generation)
	setRuntimeEnvironmentCondition(status, breakfixv1.ConditionFailed, metav1.ConditionFalse, "RuntimeReady", "environment has no failure", environment.Generation)
}

func setVK8sEnvironmentFailure(environment *breakfixv1.VK8sEnvironment, class breakfixv1.EnvironmentFailureClass, reason, message string, now time.Time) {
	status := &environment.Status.Environment
	status.Phase = breakfixv1.EnvironmentFailed
	status.ObservedGeneration = environment.Generation
	status.Failure = &breakfixv1.EnvironmentFailureStatus{
		Class: class, Component: "vk8s-runtime", Reason: reason, Message: truncate(message, 4000), At: metav1.NewTime(now),
	}
	setRuntimeEnvironmentCondition(status, breakfixv1.ConditionFailed, metav1.ConditionTrue, reason, truncate(message, 4000), environment.Generation)
	setRuntimeEnvironmentCondition(status, breakfixv1.ConditionReady, metav1.ConditionFalse, reason, "environment failed", environment.Generation)
}

func shouldDestroyVK8sEnvironment(environment *breakfixv1.VK8sEnvironment, now time.Time) bool {
	if environment.Status.Environment.Phase == breakfixv1.EnvironmentDestroyed {
		return false
	}
	lifecycle := environment.Spec.Environment.Lifecycle
	if lifecycle.DeadlineAt != nil && !now.Before(lifecycle.DeadlineAt.Time) {
		return true
	}
	if lifecycle.IdleTTLSeconds == nil || *lifecycle.IdleTTLSeconds <= 0 {
		return false
	}
	activity := environment.CreationTimestamp.Time
	if lifecycle.ActivityAt != nil && lifecycle.ActivityAt.After(activity) {
		activity = lifecycle.ActivityAt.Time
	}
	return !now.Before(activity.Add(time.Duration(*lifecycle.IdleTTLSeconds) * time.Second))
}

func vk8sEnvironmentRequeue(environment *breakfixv1.VK8sEnvironment, now time.Time) ctrl.Result {
	if environment.Status.Environment.Phase == breakfixv1.EnvironmentReady && environment.Spec.Environment.Purpose == breakfixv1.EnvironmentPurposeLearning {
		return ctrl.Result{RequeueAfter: checkpointInterval}
	}
	next := time.Duration(0)
	lifecycle := environment.Spec.Environment.Lifecycle
	if lifecycle.DeadlineAt != nil && lifecycle.DeadlineAt.After(now) {
		next = lifecycle.DeadlineAt.Sub(now)
	}
	if lifecycle.IdleTTLSeconds != nil && *lifecycle.IdleTTLSeconds > 0 {
		activity := environment.CreationTimestamp.Time
		if lifecycle.ActivityAt != nil && lifecycle.ActivityAt.After(activity) {
			activity = lifecycle.ActivityAt.Time
		}
		idle := activity.Add(time.Duration(*lifecycle.IdleTTLSeconds) * time.Second).Sub(now)
		if idle > 0 && (next == 0 || idle < next) {
			next = idle
		}
	}
	return ctrl.Result{RequeueAfter: next}
}

func (r *VK8sEnvironmentReconciler) runCheckpoints(ctx context.Context, environment *breakfixv1.VK8sEnvironment, request VK8sProvisionRequest) ([]breakfixv1.CheckpointResultStatus, error) {
	expected := make([]string, 0, len(environment.Spec.Environment.Checkpoints))
	for _, checkpoint := range environment.Spec.Environment.Checkpoints {
		expected = append(expected, checkpoint.ID)
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := r.Provider.ExecTerminal(checkCtx, request, []string{"/bin/bash", vk8sChallengeRoot + "/checks.sh"})
	if err != nil {
		return nil, fmt.Errorf("execute VK8s checkpoints: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("VK8s checkpoint runner exited with %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	results, err := parseCheckpointReport(result.Stdout, expected)
	if err != nil {
		return nil, fmt.Errorf("invalid VK8s checkpoint report: %w", err)
	}
	return results, nil
}

func (r *VK8sEnvironmentReconciler) patchStatus(ctx context.Context, before, environment *breakfixv1.VK8sEnvironment) error {
	if reflect.DeepEqual(before.Status, environment.Status) {
		return nil
	}
	return r.Status().Patch(ctx, environment, client.MergeFrom(before))
}

func (r *VK8sEnvironmentReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *VK8sEnvironmentReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).
		For(&breakfixv1.VK8sEnvironment{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: vk8sEnvironmentMaxConcurrentReconciles}).
		Complete(r)
}
