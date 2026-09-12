package nodeenvironment

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
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

var incusImageFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const (
	nodeEnvironmentFinalizer               = "breakfix.dev/node-environment-cleanup"
	nodeEnvironmentMaxConcurrentReconciles = 4
	nodeProvisioningInterval               = 2 * time.Second
	nodeProviderRetryInterval              = 5 * time.Second
	checkpointInterval                     = 4 * time.Second
)

type NodeEnvironmentReconciler struct {
	client.Client
	Provider environmentdomain.NodeProvider
	Now      func() time.Time
}

func (r *NodeEnvironmentReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var environment breakfixv1.NodeEnvironment
	if err := r.Get(ctx, request.NamespacedName, &environment); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !environment.DeletionTimestamp.IsZero() {
		return r.reconcileDeletion(ctx, &environment)
	}
	if !controllerutil.ContainsFinalizer(&environment, nodeEnvironmentFinalizer) {
		before := environment.DeepCopy()
		controllerutil.AddFinalizer(&environment, nodeEnvironmentFinalizer)
		if err := r.Patch(ctx, &environment, client.MergeFrom(before)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	statusBefore := environment.DeepCopy()
	if err := validateNodeEnvironmentSpec(&environment); err != nil {
		setNodeEnvironmentFailure(&environment, breakfixv1.EnvironmentFailureArtifact, "InvalidSnapshot", err.Error(), r.now())
		return ctrl.Result{}, r.patchNodeStatus(ctx, statusBefore, &environment)
	}
	identity, changed, err := r.ensureNodeIdentity(&environment)
	if err != nil {
		if errors.Is(err, environmentdomain.ErrProviderUnavailable) {
			return r.handleProviderError(ctx, statusBefore, &environment, err)
		}
		setNodeEnvironmentFailure(&environment, breakfixv1.EnvironmentFailureInfrastructure, "InvalidProviderIdentity", err.Error(), r.now())
		return ctrl.Result{}, r.patchNodeStatus(ctx, statusBefore, &environment)
	}
	if changed {
		return ctrl.Result{Requeue: true}, r.patchNodeStatus(ctx, statusBefore, &environment)
	}
	providerRequest := nodeProviderRequest(&environment, identity)

	if shouldDestroyNodeEnvironment(&environment, r.now()) {
		result, err := r.reconcileDrain(ctx, statusBefore, &environment, providerRequest)
		return result, err
	}
	if environment.Status.Environment.Phase == breakfixv1.EnvironmentDestroyed {
		return ctrl.Result{}, nil
	}
	if environment.Status.Environment.Phase == breakfixv1.EnvironmentFailed {
		return nodeEnvironmentRequeue(&environment, r.now()), nil
	}

	stable := environment.Status.Environment.Phase == breakfixv1.EnvironmentReady || environment.Status.Environment.Phase == breakfixv1.EnvironmentCompleted
	if !stable {
		if err := r.nodeProvider().Preflight(ctx); err != nil {
			return r.handleProviderError(ctx, statusBefore, &environment, err)
		}
	}
	var observation environmentdomain.NodeEnvironmentObservation
	if stable {
		observation, err = r.nodeProvider().Observe(ctx, providerRequest)
	} else {
		observation, err = r.nodeProvider().Provision(ctx, providerRequest)
	}
	if err != nil {
		// A transient provider outage must not invalidate an already usable
		// environment. Its next observation can reconnect without making the
		// user restart the scenario.
		if stable && errors.Is(err, environmentdomain.ErrProviderUnavailable) {
			return ctrl.Result{RequeueAfter: nodeProviderRetryInterval}, nil
		}
		return r.handleProviderError(ctx, statusBefore, &environment, err)
	}
	applyNodeObservation(&environment, observation)
	setRuntimeEnvironmentCondition(&environment.Status.Environment, breakfixv1.ConditionProvisioned, metav1.ConditionTrue, "ResourcesProvisioned", "Incus resources are provisioned", environment.Generation)
	for _, node := range observation.Nodes {
		if node.Initialization.Failed {
			message := node.Initialization.Message
			if message == "" {
				message = fmt.Sprintf("node %s runtime initialization exited with %d", node.Node.LogicalName, node.Initialization.ExitCode)
			}
			setNodeEnvironmentFailure(&environment, breakfixv1.EnvironmentFailureArtifact, "RuntimeInitializationFailed", message, r.now())
			return ctrl.Result{}, r.patchNodeStatus(ctx, statusBefore, &environment)
		}
	}
	if !observation.Ready {
		markNodeProvisioning(&environment, "RuntimeInitializing", "waiting for all node runtime initializers", r.now())
		return ctrl.Result{RequeueAfter: nodeProvisioningInterval}, r.patchNodeStatus(ctx, statusBefore, &environment)
	}

	markNodeReady(&environment, r.now())
	if environment.Spec.Environment.Purpose == breakfixv1.EnvironmentPurposeLearning {
		report, checkErr := r.runNodeCheckpoints(ctx, &environment, identity)
		recordNodeCheckpointStatus(&environment.Status.Environment, report, checkErr, r.now())
		if checkErr == nil && report.Passed() {
			markRuntimeEnvironmentCompleted(&environment.Status.Environment, environment.Generation, r.now())
		}
	}
	if err := r.patchNodeStatus(ctx, statusBefore, &environment); err != nil {
		return ctrl.Result{}, err
	}
	return nodeEnvironmentRequeue(&environment, r.now()), nil
}

func (r *NodeEnvironmentReconciler) reconcileDeletion(ctx context.Context, environment *breakfixv1.NodeEnvironment) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(environment, nodeEnvironmentFinalizer) {
		return ctrl.Result{}, nil
	}
	if err := validateNodeEnvironmentSpec(environment); err != nil {
		return ctrl.Result{}, err
	}
	identity, _, err := r.ensureNodeIdentity(environment)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.nodeProvider().Delete(ctx, nodeProviderRequest(environment, identity)); err != nil {
		return ctrl.Result{RequeueAfter: nodeProviderRetryInterval}, err
	}
	before := environment.DeepCopy()
	controllerutil.RemoveFinalizer(environment, nodeEnvironmentFinalizer)
	if err := r.Patch(ctx, environment, client.MergeFrom(before)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *NodeEnvironmentReconciler) reconcileDrain(ctx context.Context, before *breakfixv1.NodeEnvironment, environment *breakfixv1.NodeEnvironment, request environmentdomain.NodeProvisionRequest) (ctrl.Result, error) {
	now := r.now()
	condition := apiMeta.FindStatusCondition(environment.Status.Environment.Conditions, breakfixv1.ConditionDraining)
	if environment.Status.Environment.Phase != breakfixv1.EnvironmentDraining || condition == nil || condition.Status != metav1.ConditionTrue {
		environment.Status.Environment.Phase = breakfixv1.EnvironmentDraining
		setRuntimeEnvironmentCondition(&environment.Status.Environment, breakfixv1.ConditionDraining, metav1.ConditionTrue, "LifecycleExpired", "environment lifecycle expired", environment.Generation)
		setRuntimeEnvironmentCondition(&environment.Status.Environment, breakfixv1.ConditionReady, metav1.ConditionFalse, "LifecycleExpired", "environment is draining", environment.Generation)
		grace := drainGracePeriod(environment.Spec.Environment.Lifecycle)
		if err := r.patchNodeStatus(ctx, before, environment); err != nil {
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
	if err := r.nodeProvider().Delete(ctx, request); err != nil {
		return ctrl.Result{RequeueAfter: nodeProviderRetryInterval}, err
	}
	completed := metav1.NewTime(now)
	environment.Status.Environment.Phase = breakfixv1.EnvironmentDestroyed
	environment.Status.Environment.DestroyedAt = &completed
	setRuntimeEnvironmentCondition(&environment.Status.Environment, breakfixv1.ConditionCleanedUp, metav1.ConditionTrue, "ResourcesDeleted", "Incus resources are deleted", environment.Generation)
	setRuntimeEnvironmentCondition(&environment.Status.Environment, breakfixv1.ConditionDraining, metav1.ConditionFalse, "ResourcesDeleted", "environment cleanup completed", environment.Generation)
	return ctrl.Result{}, r.patchNodeStatus(ctx, before, environment)
}

func (r *NodeEnvironmentReconciler) handleProviderError(ctx context.Context, before *breakfixv1.NodeEnvironment, environment *breakfixv1.NodeEnvironment, err error) (ctrl.Result, error) {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return ctrl.Result{}, err
	case errors.Is(err, environmentdomain.ErrProviderUnavailable), errors.Is(err, environmentdomain.ErrProviderNotFound), errors.Is(err, environmentdomain.ErrProviderConflict):
		markNodeProvisioning(environment, "ProviderUnavailable", err.Error(), r.now())
		environment.Status.Environment.Failure = &breakfixv1.EnvironmentFailureStatus{
			Class: breakfixv1.EnvironmentFailureInfrastructure, Component: "incus", Reason: "ProviderUnavailable", Message: truncate(err.Error(), 4000), At: metav1.NewTime(r.now()),
		}
		if patchErr := r.patchNodeStatus(ctx, before, environment); patchErr != nil {
			return ctrl.Result{}, patchErr
		}
		return ctrl.Result{RequeueAfter: nodeProviderRetryInterval}, nil
	default:
		setNodeEnvironmentFailure(environment, breakfixv1.EnvironmentFailureInfrastructure, "ProviderInvariant", err.Error(), r.now())
		return ctrl.Result{}, r.patchNodeStatus(ctx, before, environment)
	}
}

func (r *NodeEnvironmentReconciler) ensureNodeIdentity(environment *breakfixv1.NodeEnvironment) (environmentdomain.NodeEnvironmentIdentity, bool, error) {
	logicalNames := make([]string, 0, len(environment.Spec.Runtime.Nodes))
	for _, node := range environment.Spec.Runtime.Nodes {
		logicalNames = append(logicalNames, node.Name)
	}
	expected, err := r.nodeProvider().Identity(string(environment.UID), logicalNames)
	if err != nil {
		return environmentdomain.NodeEnvironmentIdentity{}, false, err
	}
	status := &environment.Status.Runtime
	empty := status.Project == "" && status.Network == "" && status.ACL == "" && status.Profile == "" && len(status.Nodes) == 0
	if empty {
		status.Project = expected.Project
		status.Network = expected.Network
		status.ACL = expected.ACL
		status.Profile = expected.Profile
		status.ImageFingerprint = environment.Spec.Runtime.ImageFingerprint
		status.Nodes = make([]breakfixv1.NodeInstanceStatus, len(expected.Nodes))
		for index, node := range expected.Nodes {
			status.Nodes[index] = breakfixv1.NodeInstanceStatus{Name: node.LogicalName, InstanceName: node.InstanceName}
		}
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
	if status.Project != expected.Project || status.Network != expected.Network || status.ACL != expected.ACL || status.Profile != expected.Profile || status.ImageFingerprint != environment.Spec.Runtime.ImageFingerprint || len(status.Nodes) != len(expected.Nodes) {
		return environmentdomain.NodeEnvironmentIdentity{}, false, fmt.Errorf("recorded Incus identity differs from Environment UID or snapshot")
	}
	identity := expected
	for index := range expected.Nodes {
		if status.Nodes[index].Name != expected.Nodes[index].LogicalName || status.Nodes[index].InstanceName != expected.Nodes[index].InstanceName {
			return environmentdomain.NodeEnvironmentIdentity{}, false, fmt.Errorf("recorded Incus node identity differs from Environment UID")
		}
		identity.Nodes[index].Address = status.Nodes[index].Address
	}
	return identity, false, nil
}

func nodeProviderRequest(environment *breakfixv1.NodeEnvironment, identity environmentdomain.NodeEnvironmentIdentity) environmentdomain.NodeProvisionRequest {
	return environmentdomain.NodeProvisionRequest{
		EnvironmentUID:        string(environment.UID),
		Revision:              environment.Spec.Environment.Source.Revision,
		ImageFingerprint:      environment.Spec.Runtime.ImageFingerprint,
		ProfileRevision:       environment.Spec.Runtime.ProfileRevision,
		NetworkPolicyRevision: environment.Spec.Runtime.NetworkPolicyRevision,
		Identity:              identity,
		Resources: environmentdomain.NodeResources{
			CPU: environment.Spec.Runtime.Resources.CPU, Memory: environment.Spec.Runtime.Resources.Memory,
			Processes: environment.Spec.Runtime.Resources.Processes, RootDisk: environment.Spec.Runtime.Resources.RootDisk,
		},
	}
}

func validateNodeEnvironmentSpec(environment *breakfixv1.NodeEnvironment) error {
	if environment.UID == "" {
		return fmt.Errorf("environment UID is required")
	}
	spec := environment.Spec.Environment
	if err := commonSpec(spec).Validate(); err != nil {
		return err
	}
	if len(environment.Spec.Runtime.Nodes) == 0 {
		return fmt.Errorf("node runtime requires nodes")
	}
	if !incusImageFingerprintPattern.MatchString(strings.TrimSpace(environment.Spec.Runtime.ImageFingerprint)) {
		return fmt.Errorf("node runtime requires a full Incus image fingerprint")
	}
	if strings.TrimSpace(environment.Spec.Runtime.ProfileRevision) == "" || strings.TrimSpace(environment.Spec.Runtime.NetworkPolicyRevision) == "" {
		return fmt.Errorf("node profile and network policy revisions are required")
	}
	cpu, err := strconv.ParseInt(strings.TrimSpace(environment.Spec.Runtime.Resources.CPU), 10, 64)
	if err != nil || cpu <= 0 {
		return fmt.Errorf("node CPU must be a positive integer")
	}
	for field, value := range map[string]string{
		"memory":    environment.Spec.Runtime.Resources.Memory,
		"root disk": environment.Spec.Runtime.Resources.RootDisk,
	} {
		if err := environmentdomain.ValidatePositiveByteSize(value); err != nil {
			return fmt.Errorf("node %s must be a positive size", field)
		}
	}
	if environment.Spec.Runtime.Resources.Processes <= 0 {
		return fmt.Errorf("node process limit must be positive")
	}
	knownNodes := make(map[string]struct{}, len(environment.Spec.Runtime.Nodes))
	for _, node := range environment.Spec.Runtime.Nodes {
		if strings.TrimSpace(node.Name) == "" || strings.TrimSpace(node.Title) == "" {
			return fmt.Errorf("node name and title are required")
		}
		if _, duplicate := knownNodes[node.Name]; duplicate {
			return fmt.Errorf("duplicate node %q", node.Name)
		}
		knownNodes[node.Name] = struct{}{}
	}
	for _, checkpoint := range spec.Checkpoints {
		if _, ok := knownNodes[checkpoint.Node]; !ok {
			return fmt.Errorf("checkpoint %q references unknown node %q", checkpoint.ID, checkpoint.Node)
		}
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

func applyNodeObservation(environment *breakfixv1.NodeEnvironment, observation environmentdomain.NodeEnvironmentObservation) {
	environment.Status.Runtime.Project = observation.Identity.Project
	environment.Status.Runtime.Network = observation.Identity.Network
	environment.Status.Runtime.ACL = observation.Identity.ACL
	environment.Status.Runtime.Profile = observation.Identity.Profile
	environment.Status.Runtime.ImageFingerprint = environment.Spec.Runtime.ImageFingerprint
	environment.Status.Runtime.Nodes = make([]breakfixv1.NodeInstanceStatus, len(observation.Nodes))
	for index, node := range observation.Nodes {
		environment.Status.Runtime.Nodes[index] = breakfixv1.NodeInstanceStatus{
			Name: node.Node.LogicalName, InstanceName: node.Node.InstanceName, Address: node.Node.Address,
			Initialized: node.Initialization.Complete && !node.Initialization.Failed,
		}
	}
}

func markNodeProvisioning(environment *breakfixv1.NodeEnvironment, reason, message string, now time.Time) {
	status := &environment.Status.Environment
	status.Phase = breakfixv1.EnvironmentProvisioning
	status.ObservedGeneration = environment.Generation
	if status.StartedAt == nil {
		started := metav1.NewTime(now)
		status.StartedAt = &started
	}
	setRuntimeEnvironmentCondition(status, breakfixv1.ConditionReady, metav1.ConditionFalse, reason, message, environment.Generation)
}

func markNodeReady(environment *breakfixv1.NodeEnvironment, now time.Time) {
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
	setRuntimeEnvironmentCondition(status, breakfixv1.ConditionReady, metav1.ConditionTrue, "RuntimeReady", "all nodes completed runtime initialization", environment.Generation)
	setRuntimeEnvironmentCondition(status, breakfixv1.ConditionFailed, metav1.ConditionFalse, "RuntimeReady", "environment has no failure", environment.Generation)
}

func setNodeEnvironmentFailure(environment *breakfixv1.NodeEnvironment, class breakfixv1.EnvironmentFailureClass, reason, message string, now time.Time) {
	status := &environment.Status.Environment
	status.Phase = breakfixv1.EnvironmentFailed
	status.ObservedGeneration = environment.Generation
	status.Failure = &breakfixv1.EnvironmentFailureStatus{
		Class: class, Component: "node-runtime", Reason: reason, Message: truncate(message, 4000), At: metav1.NewTime(now),
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

func shouldDestroyNodeEnvironment(environment *breakfixv1.NodeEnvironment, now time.Time) bool {
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

func drainGracePeriod(lifecycle breakfixv1.EnvironmentLifecycleSpec) time.Duration {
	if lifecycle.DrainGracePeriodSeconds == nil || *lifecycle.DrainGracePeriodSeconds <= 0 {
		return 0
	}
	return time.Duration(*lifecycle.DrainGracePeriodSeconds) * time.Second
}

func nodeEnvironmentRequeue(environment *breakfixv1.NodeEnvironment, now time.Time) ctrl.Result {
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

func (r *NodeEnvironmentReconciler) runNodeCheckpoints(ctx context.Context, environment *breakfixv1.NodeEnvironment, identity environmentdomain.NodeEnvironmentIdentity) (checkpoint.Report, error) {
	byNode := make(map[string][]string)
	for _, checkpoint := range environment.Spec.Environment.Checkpoints {
		byNode[checkpoint.Node] = append(byNode[checkpoint.Node], checkpoint.ID)
	}
	all := make(map[string]checkpoint.Result, len(environment.Spec.Environment.Checkpoints))
	for _, node := range environment.Spec.Runtime.Nodes {
		expected := byNode[node.Name]
		if len(expected) == 0 {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		result, err := r.nodeProvider().Execute(checkCtx, environmentdomain.NodeExecutionRequest{
			EnvironmentUID: string(environment.UID), Revision: environment.Spec.Environment.Source.Revision,
			Identity: identity, LogicalName: node.Name,
			Command: []string{"/bin/bash", "/opt/breakfix/scenario/nodes/" + node.Name + "/checks.sh"},
		})
		cancel()
		if err != nil {
			return checkpoint.Report{}, fmt.Errorf("execute checkpoints on node %s: %w", node.Name, err)
		}
		if result.ExitCode != 0 {
			return checkpoint.Report{}, fmt.Errorf("checkpoint runner on node %s exited with %d: %s", node.Name, result.ExitCode, strings.TrimSpace(result.Stderr))
		}
		report, err := checkpoint.Parse(result.Stdout, expected)
		if err != nil {
			return checkpoint.Report{}, fmt.Errorf("invalid checkpoint report from node %s: %w", node.Name, err)
		}
		for _, item := range report.Checks {
			all[item.ID] = item
		}
	}
	ordered := make([]checkpoint.Result, 0, len(environment.Spec.Environment.Checkpoints))
	for _, expected := range environment.Spec.Environment.Checkpoints {
		result, ok := all[expected.ID]
		if !ok {
			return checkpoint.Report{}, fmt.Errorf("checkpoint %q has no result", expected.ID)
		}
		ordered = append(ordered, result)
	}
	return checkpoint.Report{Checks: ordered}, nil
}

func (r *NodeEnvironmentReconciler) nodeProvider() environmentdomain.NodeProvider {
	if r != nil && r.Provider != nil {
		return r.Provider
	}
	return UnavailableProvider(nil)
}

func recordNodeCheckpointStatus(status *breakfixv1.EnvironmentStatus, report checkpoint.Report, checkErr error, now time.Time) {
	next, changed := environmentdomain.RecordCheckpointStatus(nodeCheckpointStatusFromAPI(status.Checkpoints), report, checkErr, now)
	if !changed {
		return
	}
	status.Checkpoints = nodeCheckpointStatusToAPI(next)
}

func nodeCheckpointStatusFromAPI(status *breakfixv1.CheckpointStatus) *environmentdomain.CheckpointStatus {
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

func nodeCheckpointStatusToAPI(status *environmentdomain.CheckpointStatus) *breakfixv1.CheckpointStatus {
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

func (r *NodeEnvironmentReconciler) patchNodeStatus(ctx context.Context, before, environment *breakfixv1.NodeEnvironment) error {
	if reflect.DeepEqual(before.Status, environment.Status) {
		return nil
	}
	return r.Status().Patch(ctx, environment, client.MergeFrom(before))
}

func (r *NodeEnvironmentReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *NodeEnvironmentReconciler) SetupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).
		For(&breakfixv1.NodeEnvironment{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: nodeEnvironmentMaxConcurrentReconciles}).
		Complete(r)
}
