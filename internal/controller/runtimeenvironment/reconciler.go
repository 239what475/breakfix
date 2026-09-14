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
	maxConcurrentReconciles = 4
)

// RevisionResolver retrieves only an immutable complete runnable revision.
// The Controller never receives a content aggregate or mutable artifact input.
type RevisionResolver interface {
	ResolveRunnableRevision(context.Context, string, string) (runnable.RunnableRevision, error)
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
		return ctrl.Result{Requeue: true}, nil
	}

	revision, err := r.Resolver.ResolveRunnableRevision(ctx, environment.Spec.RunnableRevisionRef.ID, environment.Spec.RunnableRevisionRef.Digest)
	if err != nil {
		result, updateErr := r.infrastructureFailure(ctx, &environment, "revision-resolver", "revision-unavailable", err)
		if updateErr != nil {
			return result, updateErr
		}
		return ctrl.Result{RequeueAfter: providerRetryRequeue}, nil
	}
	if err := ValidateSpec(environment, revision); err != nil {
		return r.fail(ctx, &environment, runnable.FailureArtifact, "runtime-environment", "invalid-revision-binding", err)
	}
	observedReset := parseObservedResetNonce(environment.Annotations)
	decision, err := Decide(environment, revision, r.now(), observedReset)
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
		return ctrl.Result{Requeue: true}, nil
	}
	binding := Binding{Namespace: environment.Namespace, Name: environment.Name, UID: string(environment.UID), RunnableRevision: revision}
	switch decision {
	case DecisionProvision:
		if environment.Status.Phase == "" || environment.Status.Phase == runtimev2.PhasePending {
			if err := r.updateStatus(ctx, &environment, func(status *runtimev2.RuntimeEnvironmentStatus) {
				status.Phase = runtimev2.PhaseProvisioning
				status.Operation = runtimev2.OperationNone
			}); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
		timeoutSeconds := revision.Spec.LifecyclePolicy.CreateTimeoutSeconds
		if environment.Status.Operation == runtimev2.OperationResetting {
			timeoutSeconds = revision.Spec.LifecyclePolicy.ResetTimeoutSeconds
		}
		operationCtx, cancel := withLifecycleTimeout(ctx, timeoutSeconds)
		defer cancel()
		observation, err := r.Provider.Provision(operationCtx, binding)
		if err != nil {
			return r.providerFailure(ctx, &environment, err)
		}
		return r.applyObservation(ctx, &environment, revision, observation, environment.Status.Operation)
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
		operationCtx, cancel := withLifecycleTimeout(ctx, revision.Spec.LifecyclePolicy.ResetTimeoutSeconds)
		defer cancel()
		observation, err := r.Provider.Reset(operationCtx, binding)
		if err != nil {
			return r.providerFailure(ctx, &environment, err)
		}
		return r.applyObservation(ctx, &environment, revision, observation, runtimev2.OperationResetting)
	case DecisionReap:
		return r.reconcileReap(ctx, &environment, binding)
	default:
		return ctrl.Result{}, nil
	}
}

func (r *Reconciler) applyObservation(ctx context.Context, environment *runtimev2.RuntimeEnvironment, revision runnable.RunnableRevision, observation Observation, operation runtimev2.EnvironmentOperation) (ctrl.Result, error) {
	profileDigest, err := revision.Spec.RuntimeProfile.Digest()
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
	expiresAt, err := ExpiresAt(environment.CreationTimestamp.Time, environment.Spec.Lease, revision.Spec.LifecyclePolicy)
	if err != nil {
		return ctrl.Result{}, err
	}
	expires := metav1.NewTime(expiresAt)
	if err := r.updateStatus(ctx, environment, func(status *runtimev2.RuntimeEnvironmentStatus) {
		status.Phase = phase
		status.Operation = operation
		status.Runtime.Provider = string(revision.Spec.RuntimeProfile.Runtime)
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
	// Deletion must still use a resolved revision so Reaper never receives an
	// unverified artifact reference from mutable CRD state.
	revision, err := r.Resolver.ResolveRunnableRevision(ctx, environment.Spec.RunnableRevisionRef.ID, environment.Spec.RunnableRevisionRef.Digest)
	if err != nil {
		return ctrl.Result{}, err
	}
	return r.reconcileReap(ctx, environment, Binding{Namespace: environment.Namespace, Name: environment.Name, UID: string(environment.UID), RunnableRevision: revision})
}

func (r *Reconciler) reconcileReap(ctx context.Context, environment *runtimev2.RuntimeEnvironment, binding Binding) (ctrl.Result, error) {
	revisionDigest, err := binding.RunnableRevision.Digest()
	if err != nil {
		return ctrl.Result{}, err
	}
	request := runnable.ReapRequest{Namespace: binding.Namespace, Name: binding.Name, UID: binding.UID, Revision: revisionDigest, Binding: binding}
	if err := r.Reaps.Enqueue(ctx, request); err != nil {
		// Cleanup diagnostics remain internal to Reaper. Keeping Draining and
		// retrying the handoff cannot invalidate completed verification work.
		return ctrl.Result{RequeueAfter: providerRetryRequeue}, nil
	}
	record, err := r.Reaps.Get(ctx, request.Key())
	if err != nil {
		return ctrl.Result{RequeueAfter: providerRetryRequeue}, nil
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
