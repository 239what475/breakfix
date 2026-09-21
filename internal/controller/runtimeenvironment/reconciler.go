package runtimeenvironment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	finalizer               = "breakfix.dev/runtime-environment-cleanup"
	resetNonceAnnotation    = "breakfix.dev/observed-reset-nonce"
	provisionRequeue        = 2 * time.Second
	providerRetryRequeue    = 5 * time.Second
	reapStatusRequeue       = 2 * time.Second
	statusStepRequeue       = 2 * time.Second
	maxConcurrentReconciles = 4
)

// RevisionResolver retrieves only an immutable complete runnable revision.
// The Controller never receives a content aggregate or mutable artifact input.
type RevisionResolver interface {
	ResolveRunnableRevision(context.Context, string, string) (runnable.RunnableRevision, error)
}

// BlankSource resolves the installed runtime definition for one blank runtime
// provider. Blank environments name no runnable revision; their runtime
// inputs come from controller configuration alone.
type BlankSource interface {
	BlankPlan(provider string) (runnable.BlankRuntimePlan, error)
}

// Provider creates and resets generic runtime resources. Concrete
// implementations decide node or k8s behavior from revision.RuntimeProfile.
// Release is intentionally owned by Reaper so reconciliation never blocks
// validation or lifecycle projection on provider cleanup.
type Provider interface {
	Provision(context.Context, Binding) (Observation, error)
	Reset(context.Context, Binding) (Observation, error)
}

type Binding = runnable.EnvironmentBinding

type Observation struct {
	Ready        bool
	ResourceRefs []runtimev2.ResourceReference
	EndpointRefs []runtimev2.EndpointReference
}

type Reconciler struct {
	client.Client
	Resolver RevisionResolver
	Provider Provider
	Reaps    runnable.ReapQueue
	Blank    BlankSource
	Now      func() time.Time
}

func (r *Reconciler) SetupWithManager(manager ctrl.Manager) error {
	if err := runtimev2.AddToScheme(manager.GetScheme()); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(manager).
		For(&runtimev2.RuntimeEnvironment{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: maxConcurrentReconciles}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	if r.Resolver == nil || r.Provider == nil || r.Reaps == nil {
		return ctrl.Result{}, errors.New("runtime environment reconciler requires revision resolver, provider, and reap queue")
	}
	var environment runtimev2.RuntimeEnvironment
	if err := r.Get(ctx, request.NamespacedName, &environment); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !environment.DeletionTimestamp.IsZero() {
		return r.reconcileDeletion(ctx, &environment)
	}
	if !controllerutil.ContainsFinalizer(&environment, finalizer) {
		before := environment.DeepCopy()
		controllerutil.AddFinalizer(&environment, finalizer)
		if err := r.Patch(ctx, &environment, client.MergeFrom(before)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: statusStepRequeue}, nil
	}

	plan, err := r.resolvePlan(ctx, &environment)
	if err != nil {
		if errors.Is(err, errBlankRuntimeNotInstalled) {
			return r.fail(ctx, &environment, runnable.FailureArtifact, "runtime-environment", "blank-runtime-not-installed", err)
		}
		result, updateErr := r.infrastructureFailure(ctx, &environment, "revision-resolver", "revision-unavailable", err)
		if updateErr != nil {
			return result, updateErr
		}
		return ctrl.Result{RequeueAfter: providerRetryRequeue}, nil //nolint:nilerr // requeue is the controller idiom for retryable handoffs
	}
	if err := ValidateSpec(environment, plan); err != nil {
		return r.fail(ctx, &environment, runnable.FailureArtifact, "runtime-environment", "invalid-revision-binding", err)
	}
	observedReset := parseObservedResetNonce(environment.Annotations)
	decision, err := Decide(environment, plan, r.now(), observedReset)
	if err != nil {
		return r.fail(ctx, &environment, runnable.FailureArtifact, "runtime-environment", "invalid-lifecycle", err)
	}
	if decision == DecisionNone {
		return ctrl.Result{}, nil
	}
	if decision == DecisionDrain {
		if err := r.updateStatus(ctx, &environment, func(status *runtimev2.RuntimeEnvironmentStatus) {
			status.Phase = runtimev2.PhaseDraining
			status.Operation = runtimev2.OperationNone
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: statusStepRequeue}, nil
	}
	binding := environmentBinding(&environment, plan)
	switch decision {
	case DecisionProvision:
		if environment.Status.Phase == "" || environment.Status.Phase == runtimev2.PhasePending {
			if err := r.updateStatus(ctx, &environment, func(status *runtimev2.RuntimeEnvironmentStatus) {
				status.Phase = runtimev2.PhaseProvisioning
				status.Operation = runtimev2.OperationNone
			}); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: statusStepRequeue}, nil
		}
		timeoutSeconds := plan.Lifecycle().CreateTimeoutSeconds
		if environment.Status.Operation == runtimev2.OperationResetting {
			timeoutSeconds = plan.Lifecycle().ResetTimeoutSeconds
		}
		operationCtx, cancel := withLifecycleTimeout(ctx, timeoutSeconds)
		defer cancel()
		observation, err := r.Provider.Provision(operationCtx, binding)
		if err != nil {
			return r.providerFailure(ctx, &environment, err)
		}
		return r.applyObservation(ctx, &environment, plan, observation, environment.Status.Operation)
	case DecisionReset:
		if environment.Annotations == nil {
			environment.Annotations = make(map[string]string)
		}
		before := environment.DeepCopy()
		environment.Annotations[resetNonceAnnotation] = fmt.Sprintf("%d", environment.Spec.ResetNonce)
		if err := r.Patch(ctx, &environment, client.MergeFrom(before)); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.updateStatus(ctx, &environment, func(status *runtimev2.RuntimeEnvironmentStatus) {
			status.Operation = runtimev2.OperationResetting
		}); err != nil {
			return ctrl.Result{}, err
		}
		operationCtx, cancel := withLifecycleTimeout(ctx, plan.Lifecycle().ResetTimeoutSeconds)
		defer cancel()
		observation, err := r.Provider.Reset(operationCtx, binding)
		if err != nil {
			return r.providerFailure(ctx, &environment, err)
		}
		return r.applyObservation(ctx, &environment, plan, observation, runtimev2.OperationResetting)
	case DecisionReap:
		return r.reconcileReap(ctx, &environment, binding)
	default:
		return ctrl.Result{}, nil
	}
}

// errBlankRuntimeNotInstalled marks a blank environment whose provider has no
// installed plan; it is a deployment configuration error, not retryable
// infrastructure noise.
var errBlankRuntimeNotInstalled = errors.New("blank runtime has no installed plan")

// resolvePlan resolves the runtime definition for one environment: the
// immutable runnable revision for content-bound environments, the installed
// blank plan for blank ones.
func (r *Reconciler) resolvePlan(ctx context.Context, environment *runtimev2.RuntimeEnvironment) (Plan, error) {
	if environment.Spec.BlankRuntime == nil {
		ref := environment.Spec.RunnableRevisionRef
		if ref == nil {
			return Plan{}, errBlankRuntimeNotInstalled
		}
		revision, err := r.Resolver.ResolveRunnableRevision(ctx, ref.ID, ref.Digest)
		if err != nil {
			return Plan{}, err
		}
		return Plan{Revision: revision}, nil
	}
	plan, err := r.blankPlan(environment.Spec.BlankRuntime.Provider)
	if err != nil {
		return Plan{}, err
	}
	return Plan{Blank: &plan}, nil
}

func (r *Reconciler) blankPlan(provider string) (runnable.BlankRuntimePlan, error) {
	if r.Blank == nil {
		return runnable.BlankRuntimePlan{}, fmt.Errorf("%w: %s", errBlankRuntimeNotInstalled, provider)
	}
	plan, err := r.Blank.BlankPlan(provider)
	if err != nil {
		return runnable.BlankRuntimePlan{}, fmt.Errorf("%w: %s: %w", errBlankRuntimeNotInstalled, provider, err)
	}
	return plan, nil
}

// environmentBinding builds the provider binding for one environment. Blank
// deletion must stay resolvable without an installed plan, so the deletion
// path may pass a zero plan and rely on the provider's k8s-only fallback.
func environmentBinding(environment *runtimev2.RuntimeEnvironment, plan Plan) Binding {
	binding := Binding{Namespace: environment.Namespace, Name: environment.Name, UID: string(environment.UID), Purpose: runnable.EnvironmentPurpose(environment.Spec.Purpose), RunnableRevision: plan.Revision}
	if plan.Blank != nil {
		binding.BlankRuntime = plan.Blank
	}
	return binding
}

func (r *Reconciler) applyObservation(ctx context.Context, environment *runtimev2.RuntimeEnvironment, plan Plan, observation Observation, operation runtimev2.EnvironmentOperation) (ctrl.Result, error) {
	profileDigest, err := plan.Profile().Digest()
	if err != nil {
		return ctrl.Result{}, err
	}
	phase := environment.Status.Phase
	if phase == "" || phase == runtimev2.PhasePending {
		phase = runtimev2.PhaseProvisioning
	}
	if observation.Ready {
		phase = runtimev2.PhaseReady
		operation = runtimev2.OperationNone
	}
	expiresAt, err := ExpiresAt(environment.CreationTimestamp.Time, environment.Spec.Lease, plan.Lifecycle())
	if err != nil {
		return ctrl.Result{}, err
	}
	expires := metav1.NewTime(expiresAt)
	if err := r.updateStatus(ctx, environment, func(status *runtimev2.RuntimeEnvironmentStatus) {
		status.Phase = phase
		status.Operation = operation
		status.Runtime.Provider = string(plan.Runtime())
		status.Runtime.ProfileDigest = profileDigest
		status.Runtime.ResourceRefs = append([]runtimev2.ResourceReference(nil), observation.ResourceRefs...)
		status.Runtime.EndpointRefs = append([]runtimev2.EndpointReference(nil), observation.EndpointRefs...)
		status.Lifecycle.ExpiresAt = &expires
		status.Failure = nil
	}); err != nil {
		return ctrl.Result{}, err
	}
	if observation.Ready {
		return ctrl.Result{RequeueAfter: expiresAt.Sub(r.now())}, nil
	}
	return ctrl.Result{RequeueAfter: provisionRequeue}, nil
}

func (r *Reconciler) reconcileDeletion(ctx context.Context, environment *runtimev2.RuntimeEnvironment) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(environment, finalizer) {
		return ctrl.Result{}, nil
	}
	// Deletion must still use a resolved definition so Reaper never receives
	// an unverified artifact reference from mutable CRD state. A blank
	// environment deletes through its installed plan, exactly like a
	// content-bound environment deletes through its immutable revision.
	plan, err := r.resolvePlan(ctx, environment)
	if err != nil {
		return ctrl.Result{}, err
	}
	return r.reconcileReap(ctx, environment, environmentBinding(environment, plan))
}

func (r *Reconciler) reconcileReap(ctx context.Context, environment *runtimev2.RuntimeEnvironment, binding Binding) (ctrl.Result, error) {
	revisionDigest, err := binding.Digest()
	if err != nil {
		return ctrl.Result{}, err
	}
	request := runnable.ReapRequest{Namespace: binding.Namespace, Name: binding.Name, UID: binding.UID, Revision: revisionDigest, Binding: binding}
	if err := r.Reaps.Enqueue(ctx, request); err != nil {
		// Cleanup diagnostics remain internal to Reaper. Keeping Draining and
		// retrying the handoff cannot invalidate completed verification work.
		return ctrl.Result{RequeueAfter: providerRetryRequeue}, nil //nolint:nilerr // requeue is the controller idiom for retryable handoffs
	}
	record, err := r.Reaps.Get(ctx, request.Key())
	if err != nil {
		return ctrl.Result{RequeueAfter: providerRetryRequeue}, nil //nolint:nilerr // requeue is the controller idiom for retryable handoffs
	}
	if record.State != runnable.ReapSucceeded {
		return ctrl.Result{RequeueAfter: reapStatusRequeue}, nil
	}
	if environment.DeletionTimestamp.IsZero() {
		now := metav1.NewTime(r.now())
		if err := r.updateStatus(ctx, environment, func(status *runtimev2.RuntimeEnvironmentStatus) {
			status.Phase = runtimev2.PhaseReleased
			status.Operation = runtimev2.OperationNone
			status.Lifecycle.ReleasedAt = &now
		}); err != nil {
			return ctrl.Result{}, err
		}
		// Retain the finalizer until the Reaper has durably reported success,
		// then request CRD deletion. The deletion reconciliation below removes
		// that finalizer only after observing the completed reap record.
		return ctrl.Result{}, client.IgnoreNotFound(r.Delete(ctx, environment))
	}
	before := environment.DeepCopy()
	controllerutil.RemoveFinalizer(environment, finalizer)
	return ctrl.Result{}, r.Patch(ctx, environment, client.MergeFrom(before))
}

func (r *Reconciler) providerFailure(ctx context.Context, environment *runtimev2.RuntimeEnvironment, err error) (ctrl.Result, error) {
	class := runnable.FailureInfrastructure
	reason := "provider-unavailable"
	var artifact *runnable.ArtifactFailure
	if errors.As(err, &artifact) {
		class = runnable.FailureArtifact
		reason = "provider-artifact-failure"
	}
	if class == runnable.FailureArtifact {
		return r.fail(ctx, environment, class, "runtime-provider", reason, err)
	}
	result, updateErr := r.infrastructureFailure(ctx, environment, "runtime-provider", reason, err)
	if updateErr != nil {
		return result, updateErr
	}
	return ctrl.Result{RequeueAfter: providerRetryRequeue}, nil
}

func (r *Reconciler) fail(ctx context.Context, environment *runtimev2.RuntimeEnvironment, class runnable.FailureClass, component, reason string, cause error) (ctrl.Result, error) {
	return ctrl.Result{}, r.updateStatus(ctx, environment, func(status *runtimev2.RuntimeEnvironmentStatus) {
		status.Phase = runtimev2.PhaseFailed
		status.Operation = runtimev2.OperationNone
		status.Failure = &runtimev2.EnvironmentFailure{Class: runtimev2.FailureClass(class), Component: component, Reason: reason, Message: bounded(cause.Error()), At: metav1.NewTime(r.now())}
	})
}

func (r *Reconciler) infrastructureFailure(ctx context.Context, environment *runtimev2.RuntimeEnvironment, component, reason string, cause error) (ctrl.Result, error) {
	return ctrl.Result{}, r.updateStatus(ctx, environment, func(status *runtimev2.RuntimeEnvironmentStatus) {
		if status.Phase == "" || status.Phase == runtimev2.PhasePending {
			status.Phase = runtimev2.PhaseProvisioning
		}
		status.Failure = &runtimev2.EnvironmentFailure{Class: runtimev2.FailureInfrastructure, Component: component, Reason: reason, Message: bounded(cause.Error()), At: metav1.NewTime(r.now())}
	})
}

func (r *Reconciler) updateStatus(ctx context.Context, environment *runtimev2.RuntimeEnvironment, mutate func(*runtimev2.RuntimeEnvironmentStatus)) error {
	before := environment.DeepCopy()
	mutate(&environment.Status)
	if !CanTransition(before.Status.Phase, environment.Status.Phase) {
		return fmt.Errorf("runtime environment cannot transition from %q to %q", before.Status.Phase, environment.Status.Phase)
	}
	environment.Status.ObservedGeneration = environment.Generation
	return r.Status().Patch(ctx, environment, client.MergeFrom(before))
}

func (r *Reconciler) now() time.Time {
	if r.Now == nil {
		return time.Now().UTC()
	}
	return r.Now().UTC()
}

func parseObservedResetNonce(annotations map[string]string) int64 {
	if annotations == nil {
		return 0
	}
	var value int64
	_, _ = fmt.Sscan(strings.TrimSpace(annotations[resetNonceAnnotation]), &value)
	return value
}

func bounded(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 1024 {
		return value[:1024]
	}
	return value
}

func withLifecycleTimeout(ctx context.Context, seconds int64) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
}
