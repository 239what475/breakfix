package vk8senvironment

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/domain/checkpoint"
	environmentdomain "github.com/breakfix/breakfix/internal/domain/environment"
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
	checkpointInterval                     = 4 * time.Second
	vk8sCheckpointRoot                     = "/opt/breakfix/scenario/k8s"
)

var immutableImagePattern = regexp.MustCompile(`^[^[:space:]]+@sha256:[0-9a-f]{64}$`)

type VK8sEnvironmentReconciler struct {
	client.Client
	Provider environmentdomain.VK8sProvider
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
	if environment.Spec.Environment.Purpose == breakfixv1.EnvironmentPurposeLearning && len(environment.Spec.Environment.Checkpoints) > 0 {
		report, checkErr := r.runCheckpoints(ctx, &environment, providerRequest)
		recordVK8sCheckpointStatus(&environment.Status.Environment, report, checkErr, r.now())
		if checkErr == nil && report.Passed() {
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
	identity, err := r.Provider.Identity(string(environment.UID))
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

func (r *VK8sEnvironmentReconciler) reconcileDrain(ctx context.Context, before *breakfixv1.VK8sEnvironment, environment *breakfixv1.VK8sEnvironment, request environmentdomain.VK8sProvisionRequest) (ctrl.Result, error) {
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

func (r *VK8sEnvironmentReconciler) ensureIdentity(environment *breakfixv1.VK8sEnvironment) (environmentdomain.VK8sEnvironmentIdentity, bool, error) {
	expected, err := r.Provider.Identity(string(environment.UID))
	if err != nil {
		return environmentdomain.VK8sEnvironmentIdentity{}, false, err
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
		return environmentdomain.VK8sEnvironmentIdentity{}, false, fmt.Errorf("recorded VK8s identity differs from Environment UID")
	}
	return expected, false, nil
}

func vk8sProviderRequest(environment *breakfixv1.VK8sEnvironment, identity environmentdomain.VK8sEnvironmentIdentity) environmentdomain.VK8sProvisionRequest {
	return environmentdomain.VK8sProvisionRequest{
		EnvironmentUID: string(environment.UID), Revision: environment.Spec.Environment.Source.Revision,
		Purpose: environmentdomain.Purpose(environment.Spec.Environment.Purpose), Identity: identity,
		Runtime: environmentdomain.VK8sRuntime{
			ImageDigest: environment.Spec.Runtime.ImageDigest, ProfileRevision: environment.Spec.Runtime.ProfileRevision,
			Version: environment.Spec.Runtime.Version, ManagementTerminalImage: environment.Spec.Runtime.ManagementTerminalImage,
			Resources: environmentdomain.VK8sRuntimeResources{
				ControlPlaneCPU: environment.Spec.Runtime.Resources.ControlPlaneCPU, ControlPlaneMemory: environment.Spec.Runtime.Resources.ControlPlaneMemory,
				ControlPlaneEphemeralStorage: environment.Spec.Runtime.Resources.ControlPlaneEphemeralStorage,
				WorkloadCPU:                  environment.Spec.Runtime.Resources.WorkloadCPU, WorkloadMemory: environment.Spec.Runtime.Resources.WorkloadMemory,
				WorkloadEphemeralStorage: environment.Spec.Runtime.Resources.WorkloadEphemeralStorage,
				QuotaCPU:                 environment.Spec.Runtime.Resources.QuotaCPU, QuotaMemory: environment.Spec.Runtime.Resources.QuotaMemory,
				QuotaEphemeralStorage: environment.Spec.Runtime.Resources.QuotaEphemeralStorage,
			},
			Network: environmentdomain.VK8sNetwork{
				PublicEgressCIDR: environment.Spec.Runtime.Network.PublicEgressCIDR,
				ProtectedCIDRs:   append([]string(nil), environment.Spec.Runtime.Network.ProtectedCIDRs...),
			},
		},
	}
}

func validateVK8sEnvironmentSpec(environment *breakfixv1.VK8sEnvironment) error {
	if environment.UID == "" {
		return fmt.Errorf("environment UID is required")
	}
	if err := commonSpec(environment.Spec.Environment).Validate(); err != nil {
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
	if err := (environmentdomain.VK8sNetwork{
		PublicEgressCIDR: runtime.Network.PublicEgressCIDR,
		ProtectedCIDRs:   runtime.Network.ProtectedCIDRs,
	}).Validate(); err != nil {
		return fmt.Errorf("VK8s network: %w", err)
	}
	if err := (environmentdomain.VK8sResources{
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

func commonSpec(spec breakfixv1.EnvironmentSpec) environmentdomain.Spec {
	checkpoints := make([]environmentdomain.Checkpoint, len(spec.Checkpoints))
	for index, checkpoint := range spec.Checkpoints {
		checkpoints[index] = environmentdomain.Checkpoint{ID: checkpoint.ID, Node: checkpoint.Node}
	}
	return environmentdomain.Spec{
		Purpose: environmentdomain.Purpose(spec.Purpose),
		Source: environmentdomain.Source{
			Kind: environmentdomain.SourceKind(spec.Source.Kind), Ref: spec.Source.Ref, Revision: spec.Source.Revision,
		},
		Checkpoints: checkpoints,
	}
}

func applyVK8sObservation(environment *breakfixv1.VK8sEnvironment, observation environmentdomain.VK8sEnvironmentObservation) {
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

func setRuntimeEnvironmentCondition(status *breakfixv1.EnvironmentStatus, conditionType string, conditionStatus metav1.ConditionStatus, reason, message string, generation int64) {
	apiMeta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type: conditionType, Status: conditionStatus, Reason: reason, Message: message, ObservedGeneration: generation,
	})
}

func markRuntimeEnvironmentCompleted(status *breakfixv1.EnvironmentStatus, generation int64, now time.Time) {
	status.Phase = breakfixv1.EnvironmentCompleted
	if status.CompletedAt == nil {
		completed := metav1.NewTime(now)
		status.CompletedAt = &completed
	}
	setRuntimeEnvironmentCondition(status, breakfixv1.ConditionCompleted, metav1.ConditionTrue, "CheckpointsCompleted", "all checkpoints passed", generation)
	setRuntimeEnvironmentCondition(status, breakfixv1.ConditionReady, metav1.ConditionFalse, "CheckpointsCompleted", "scenario completed", generation)
}

func drainGracePeriod(lifecycle breakfixv1.EnvironmentLifecycleSpec) time.Duration {
	if lifecycle.DrainGracePeriodSeconds == nil || *lifecycle.DrainGracePeriodSeconds <= 0 {
		return 0
	}
	return time.Duration(*lifecycle.DrainGracePeriodSeconds) * time.Second
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
	if environment.Status.Environment.Phase == breakfixv1.EnvironmentReady && environment.Spec.Environment.Purpose == breakfixv1.EnvironmentPurposeLearning && len(environment.Spec.Environment.Checkpoints) > 0 {
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

func (r *VK8sEnvironmentReconciler) runCheckpoints(ctx context.Context, environment *breakfixv1.VK8sEnvironment, request environmentdomain.VK8sProvisionRequest) (checkpoint.Report, error) {
	expected := make([]string, 0, len(environment.Spec.Environment.Checkpoints))
	for _, checkpoint := range environment.Spec.Environment.Checkpoints {
		expected = append(expected, checkpoint.ID)
	}
	checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := r.Provider.ExecuteTerminal(checkCtx, request, []string{"/bin/bash", vk8sCheckpointRoot + "/checks.sh"})
	if err != nil {
		return checkpoint.Report{}, fmt.Errorf("execute VK8s checkpoints: %w", err)
	}
	if result.ExitCode != 0 {
		return checkpoint.Report{}, fmt.Errorf("VK8s checkpoint runner exited with %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	report, err := checkpoint.Parse(result.Stdout, expected)
	if err != nil {
		return checkpoint.Report{}, fmt.Errorf("invalid VK8s checkpoint report: %w", err)
	}
	return report, nil
}

func recordVK8sCheckpointStatus(status *breakfixv1.EnvironmentStatus, report checkpoint.Report, checkErr error, now time.Time) {
	next, changed := environmentdomain.RecordCheckpointStatus(vk8sCheckpointStatusFromAPI(status.Checkpoints), report, checkErr, now)
	if !changed {
		return
	}
	status.Checkpoints = vk8sCheckpointStatusToAPI(next)
}

func vk8sCheckpointStatusFromAPI(status *breakfixv1.CheckpointStatus) *environmentdomain.CheckpointStatus {
	if status == nil {
		return nil
	}
	result := &environmentdomain.CheckpointStatus{Error: status.Error, Results: make([]environmentdomain.RecordedCheckpointResult, len(status.Results))}
	if status.CheckedAt != nil {
		checked := status.CheckedAt.Time
		result.CheckedAt = &checked
	}
	for index, value := range status.Results {
		recorded := environmentdomain.RecordedCheckpointResult{Result: checkpoint.Result{
			ID: value.ID, Passed: value.Passed, Summary: value.Summary, Details: value.Details,
		}}
		if value.FirstPassedAt != nil {
			firstPassed := value.FirstPassedAt.Time
			recorded.FirstPassedAt = &firstPassed
		}
		result.Results[index] = recorded
	}
	return result
}

func vk8sCheckpointStatusToAPI(status *environmentdomain.CheckpointStatus) *breakfixv1.CheckpointStatus {
	if status == nil {
		return nil
	}
	result := &breakfixv1.CheckpointStatus{Error: status.Error, Results: make([]breakfixv1.CheckpointResultStatus, len(status.Results))}
	if status.CheckedAt != nil {
		checked := metav1.NewTime(*status.CheckedAt)
		result.CheckedAt = &checked
	}
	for index, checkpoint := range status.Results {
		recorded := breakfixv1.CheckpointResultStatus{
			ID: checkpoint.ID, Passed: checkpoint.Passed, Summary: checkpoint.Summary, Details: checkpoint.Details,
		}
		if checkpoint.FirstPassedAt != nil {
			firstPassed := metav1.NewTime(*checkpoint.FirstPassedAt)
			recorded.FirstPassedAt = &firstPassed
		}
		result.Results[index] = recorded
	}
	return result
}

func truncate(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	return value[:limit]
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
