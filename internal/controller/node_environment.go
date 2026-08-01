package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/incusprovider"
	"github.com/lxc/incus/v7/shared/units"
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

type NodeEnvironmentProvider interface {
	Preflight(context.Context) (incusprovider.PreflightResult, error)
	NodeEnvironmentIdentity(environmentUID string, logicalNames []string) (incusprovider.NodeEnvironmentIdentity, error)
	ProvisionNodeEnvironment(context.Context, incusprovider.ProvisionNodeEnvironmentRequest) (incusprovider.NodeEnvironmentObservation, error)
	ObserveNodeEnvironment(context.Context, incusprovider.ProvisionNodeEnvironmentRequest) (incusprovider.NodeEnvironmentObservation, error)
	DeleteNodeEnvironment(context.Context, incusprovider.ProvisionNodeEnvironmentRequest) error
	ExecNode(context.Context, incusprovider.ExecNodeRequest) (incusprovider.ExecNodeResult, error)
}

type NodeEnvironmentReconciler struct {
	client.Client
	Provider NodeEnvironmentProvider
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
		if errors.Is(err, incusprovider.ErrUnavailable) {
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
		if _, err := r.nodeProvider().Preflight(ctx); err != nil {
			return r.handleProviderError(ctx, statusBefore, &environment, err)
		}
	}
	var observation incusprovider.NodeEnvironmentObservation
	if stable {
		observation, err = r.nodeProvider().ObserveNodeEnvironment(ctx, providerRequest)
	} else {
		observation, err = r.nodeProvider().ProvisionNodeEnvironment(ctx, providerRequest)
	}
	if err != nil {
		// A transient provider outage must not invalidate an already usable
		// environment. Its next observation can reconnect without making the
		// user restart the challenge.
		if stable && errors.Is(err, incusprovider.ErrUnavailable) {
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
		results, checkErr := r.runNodeCheckpoints(ctx, &environment, identity)
		recordRuntimeCheckpointStatus(&environment.Status.Environment, results, checkErr, r.now())
		if checkErr == nil && checkpointsPassed(results) {
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
	if err := r.nodeProvider().DeleteNodeEnvironment(ctx, nodeProviderRequest(environment, identity)); err != nil {
		return ctrl.Result{RequeueAfter: nodeProviderRetryInterval}, err
	}
	before := environment.DeepCopy()
	controllerutil.RemoveFinalizer(environment, nodeEnvironmentFinalizer)
	if err := r.Patch(ctx, environment, client.MergeFrom(before)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *NodeEnvironmentReconciler) reconcileDrain(ctx context.Context, before *breakfixv1.NodeEnvironment, environment *breakfixv1.NodeEnvironment, request incusprovider.ProvisionNodeEnvironmentRequest) (ctrl.Result, error) {
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
	if err := r.nodeProvider().DeleteNodeEnvironment(ctx, request); err != nil {
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
	case errors.Is(err, incusprovider.ErrUnavailable), errors.Is(err, incusprovider.ErrNotFound), errors.Is(err, incusprovider.ErrConflict):
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

func (r *NodeEnvironmentReconciler) ensureNodeIdentity(environment *breakfixv1.NodeEnvironment) (incusprovider.NodeEnvironmentIdentity, bool, error) {
	logicalNames := make([]string, 0, len(environment.Spec.Runtime.Nodes))
	for _, node := range environment.Spec.Runtime.Nodes {
		logicalNames = append(logicalNames, node.Name)
	}
	expected, err := r.nodeProvider().NodeEnvironmentIdentity(string(environment.UID), logicalNames)
	if err != nil {
		return incusprovider.NodeEnvironmentIdentity{}, false, err
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
		return incusprovider.NodeEnvironmentIdentity{}, false, fmt.Errorf("recorded Incus identity differs from Environment UID or snapshot")
	}
	identity := expected
	for index := range expected.Nodes {
		if status.Nodes[index].Name != expected.Nodes[index].LogicalName || status.Nodes[index].InstanceName != expected.Nodes[index].InstanceName {
			return incusprovider.NodeEnvironmentIdentity{}, false, fmt.Errorf("recorded Incus node identity differs from Environment UID")
		}
		identity.Nodes[index].Address = status.Nodes[index].Address
	}
	return identity, false, nil
}

func nodeProviderRequest(environment *breakfixv1.NodeEnvironment, identity incusprovider.NodeEnvironmentIdentity) incusprovider.ProvisionNodeEnvironmentRequest {
	return incusprovider.ProvisionNodeEnvironmentRequest{
		EnvironmentUID:        string(environment.UID),
		Revision:              environment.Spec.Environment.Source.Revision,
		ImageFingerprint:      environment.Spec.Runtime.ImageFingerprint,
		ProfileRevision:       environment.Spec.Runtime.ProfileRevision,
		NetworkPolicyRevision: environment.Spec.Runtime.NetworkPolicyRevision,
		Identity:              identity,
		Resources: incusprovider.NodeEnvironmentResources{
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
	if err := validateEnvironmentSpec(spec); err != nil {
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
		bytes, err := units.ParseByteSizeString(strings.TrimSpace(value))
		if err != nil || bytes <= 0 {
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

func applyNodeObservation(environment *breakfixv1.NodeEnvironment, observation incusprovider.NodeEnvironmentObservation) {
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
	setRuntimeEnvironmentCondition(status, breakfixv1.ConditionReady, metav1.ConditionFalse, "CheckpointsCompleted", "challenge completed", generation)
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

func (r *NodeEnvironmentReconciler) runNodeCheckpoints(ctx context.Context, environment *breakfixv1.NodeEnvironment, identity incusprovider.NodeEnvironmentIdentity) ([]breakfixv1.CheckpointResultStatus, error) {
	byNode := make(map[string][]string)
	for _, checkpoint := range environment.Spec.Environment.Checkpoints {
		byNode[checkpoint.Node] = append(byNode[checkpoint.Node], checkpoint.ID)
	}
	all := make(map[string]breakfixv1.CheckpointResultStatus, len(environment.Spec.Environment.Checkpoints))
	for _, node := range environment.Spec.Runtime.Nodes {
		expected := byNode[node.Name]
		if len(expected) == 0 {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		result, err := r.nodeProvider().ExecNode(checkCtx, incusprovider.ExecNodeRequest{
			EnvironmentUID: string(environment.UID), Revision: environment.Spec.Environment.Source.Revision,
			Identity: identity, LogicalName: node.Name,
			Command: []string{"/bin/bash", "/opt/breakfix/challenge/nodes/" + node.Name + "/checks.sh"},
		})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("execute checkpoints on node %s: %w", node.Name, err)
		}
		if result.ExitCode != 0 {
			return nil, fmt.Errorf("checkpoint runner on node %s exited with %d: %s", node.Name, result.ExitCode, strings.TrimSpace(result.Stderr))
		}
		parsed, err := parseCheckpointReport(result.Stdout, expected)
		if err != nil {
			return nil, fmt.Errorf("invalid checkpoint report from node %s: %w", node.Name, err)
		}
		for _, item := range parsed {
			all[item.ID] = item
		}
	}
	ordered := make([]breakfixv1.CheckpointResultStatus, 0, len(environment.Spec.Environment.Checkpoints))
	for _, checkpoint := range environment.Spec.Environment.Checkpoints {
		result, ok := all[checkpoint.ID]
		if !ok {
			return nil, fmt.Errorf("checkpoint %q has no result", checkpoint.ID)
		}
		ordered = append(ordered, result)
	}
	return ordered, nil
}

func (r *NodeEnvironmentReconciler) nodeProvider() NodeEnvironmentProvider {
	if r != nil && r.Provider != nil {
		return r.Provider
	}
	return UnavailableNodeProvider(nil)
}

func recordRuntimeCheckpointStatus(status *breakfixv1.EnvironmentStatus, results []breakfixv1.CheckpointResultStatus, checkErr error, now time.Time) {
	next := &breakfixv1.CheckpointStatus{}
	if checkErr != nil {
		next.Error = truncate(checkErr.Error(), 4000)
		if status.Checkpoints != nil {
			next.Results = append([]breakfixv1.CheckpointResultStatus(nil), status.Checkpoints.Results...)
		}
	} else {
		firstPassed := make(map[string]*metav1.Time)
		if status.Checkpoints != nil {
			for _, result := range status.Checkpoints.Results {
				if result.FirstPassedAt != nil {
					firstPassed[result.ID] = result.FirstPassedAt
				}
			}
		}
		next.Results = append([]breakfixv1.CheckpointResultStatus(nil), results...)
		for index := range next.Results {
			if recorded := firstPassed[next.Results[index].ID]; recorded != nil {
				next.Results[index].FirstPassedAt = recorded
			} else if next.Results[index].Passed {
				passed := metav1.NewTime(now)
				next.Results[index].FirstPassedAt = &passed
			}
		}
	}
	if status.Checkpoints != nil && status.Checkpoints.Error == next.Error && slices.Equal(status.Checkpoints.Results, next.Results) {
		return
	}
	checked := metav1.NewTime(now)
	next.CheckedAt = &checked
	status.Checkpoints = next
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
