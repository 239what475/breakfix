package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"log/slog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const containerEnvironmentFinalizer = "breakfix.dev/container-environment-cleanup"
const breakfixInitSentinel = "/var/lib/breakfix/.initialized"

type ContainerEnvironmentReconciler struct {
	client.Client
	K8s                *k8s.Client
	RegistryAddr       string
	RegistryPullSecret string
	NS                 string
	CRDNamespace       string
	Cooldown           time.Duration
}

func (r *ContainerEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	var env breakfixv1.ContainerEnvironment
	if err := r.Get(ctx, req.NamespacedName, &env); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	result, err := reconcileCommonEnvironment(ctx, &env, containerEnvironmentRuntime{r: r})

	slog.Info("reconcile done", "resource", "containerEnvironment", "name", env.Name, "phase", env.Status.Phase, "duration", time.Since(start))
	return result, err
}

func (r *ContainerEnvironmentReconciler) createPod(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	if err := validateEnvironmentSnapshot(&env.Spec, "container"); err != nil {
		markEnvironmentFailed(&env.Status, "environment", "invalid_execution_snapshot", "InvalidExecutionSnapshot", err.Error(), false)
		if updateErr := r.Status().Update(ctx, env); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(env, containerEnvironmentFinalizer) {
		controllerutil.AddFinalizer(env, containerEnvironmentFinalizer)
		if err := r.Update(ctx, env); err != nil {
			return ctrl.Result{}, err
		}
	}

	ns := k8s.EnvironmentNamespace(r.NS, env.Spec.UserRef, env.Name)
	podName := k8s.DNSLabelName("challenge", env.Name)

	if err := r.K8s.EnsureNamespace(ns); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure namespace: %w", err)
	}
	if err := r.K8s.EnsureImagePullSecret(r.CRDNamespace, ns, r.RegistryPullSecret); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure registry pull secret: %w", err)
	}
	if err := r.K8s.EnsureVerifierWorkspaceExecAccess(ns, r.CRDNamespace, "breakfix-verifier"); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure verifier workspace access: %w", err)
	}

	imageURL := env.Spec.Image
	if r.RegistryAddr != "" && !looksLikeURL(imageURL) {
		imageURL = r.RegistryAddr + "/" + imageURL
	}
	resources, err := workspaceResourceRequirements(&env.Spec)
	if err != nil {
		markEnvironmentFailed(&env.Status, "workspace", "invalid_workspace_resources", "InvalidWorkspaceResources", err.Error(), false)
		_, _ = r.K8s.UpdateContainerEnvironmentStatus(ctx, r.CRDNamespace, env)
		return ctrl.Result{}, nil
	}

	if err := r.K8s.CreatePod(ns, podName, k8s.CreatePodOpts{
		Image:            imageURL,
		ChallengeID:      env.Spec.ChallengeRef,
		EnvironmentID:    env.Name,
		Resources:        resources,
		ImagePullSecrets: []corev1.LocalObjectReference{{Name: r.RegistryPullSecret}},
	}); err != nil {
		slog.Error("failed to create pod", "err", err, "environment", env.Name)
		setEnvironmentProvisioning(&env.Status, "CreateWorkspacePodFailed", err.Error())
		_, _ = r.K8s.UpdateContainerEnvironmentStatus(ctx, r.CRDNamespace, env)
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	env.Status.WorkspacePodName = podName
	env.Status.Namespace = ns
	setEnvironmentProvisioning(&env.Status, "WorkspacePodCreated", "workspace pod created")
	setEnvironmentCondition(&env.Status, breakfixv1.ConditionProvisioned, metav1.ConditionTrue, "WorkspacePodCreated", "workspace pod created")

	if err := r.Status().Update(ctx, env); err != nil {
		return ctrl.Result{}, err
	}

	slog.Info("workspace pod created", "environment", env.Name, "pod", podName, "namespace", ns)
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func (r *ContainerEnvironmentReconciler) waitForPod(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	// A transient create failure leaves no durable workspace identity. Retry the
	// provision path instead of waiting forever on empty status fields.
	if strings.TrimSpace(env.Status.Namespace) == "" || strings.TrimSpace(env.Status.WorkspacePodName) == "" {
		return r.createPod(ctx, env)
	}
	if err := r.K8s.WaitForPod(env.Status.Namespace, env.Status.WorkspacePodName, "challenge"); err != nil {
		slog.Debug("pod not ready yet", "environment", env.Name, "err", err)
		setEnvironmentProvisioning(&env.Status, "WaitingForWorkspacePod", err.Error())
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err := r.K8s.WaitForFileInPod(env.Status.Namespace, env.Status.WorkspacePodName, breakfixInitSentinel, 2*time.Minute); err != nil {
		slog.Debug("pod initialized sentinel not ready yet", "environment", env.Name, "err", err)
		setEnvironmentProvisioning(&env.Status, "WaitingForInitSentinel", err.Error())
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	setEnvironmentReady(&env.Spec, &env.Status, "WorkspaceReady", "environment ready")

	if err := r.Status().Update(ctx, env); err != nil {
		return ctrl.Result{}, err
	}

	slog.Info("container environment ready", "environment", env.Name, "pod", env.Status.WorkspacePodName)
	return ctrl.Result{}, nil
}

func (r *ContainerEnvironmentReconciler) evaluateCheckpoints(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	leaseChanged, evaluate, requeueAfter := advanceEnvironmentLease(&env.Spec, &env.Status)
	if !evaluate {
		if leaseChanged {
			if err := r.Status().Update(ctx, env); err != nil {
				return ctrl.Result{}, err
			}
		}
		if env.Status.Phase == breakfixv1.EnvironmentDestroyed {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	results, checkErr := runCheckpointEvaluation(ctx, r.K8s, env.Spec.CheckpointIDs, env.Status.Namespace, env.Status.WorkspacePodName)
	changed := leaseChanged || recordCheckpointStatus(&env.Status, results, checkErr)
	if checkErr != nil {
		if changed {
			if err := r.Status().Update(ctx, env); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: checkpointInterval}, nil
	}
	if checkpointsPassed(results) {
		slog.Info("environment checkpoints completed", "environment", env.Name)
		setEnvironmentCompleted(&env.Status)
		if err := r.Status().Update(ctx, env); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if changed {
		if err := r.Status().Update(ctx, env); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: checkpointInterval}, nil
}

func (r *ContainerEnvironmentReconciler) cleanup(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	cleanupCommonEnvironment(r.K8s, &env.Status)
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *ContainerEnvironmentReconciler) checkCooldown(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	if !env.Spec.AutoDestroyAfterIdleOr(true) {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	destroy, remaining := shouldDestroyEnvironment(&env.Status)
	if destroy {
		slog.Info("environment expired", "environment", env.Name)
		markEnvironmentDestroyed(&env.Status, "IdleTTLExpired", "environment expired")
		if err := r.Status().Update(ctx, env); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: remaining}, nil
}

func (r *ContainerEnvironmentReconciler) finalCleanup(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	cleanupCommonEnvironment(r.K8s, &env.Status)
	done, err := finalizeCommonEnvironment(ctx, r.K8s, env.Status.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !done {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	markEnvironmentDestroyed(&env.Status, "CleanupCompleted", "container environment destroyed")
	if err := r.Status().Update(ctx, env); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	controllerutil.RemoveFinalizer(env, containerEnvironmentFinalizer)
	if err := r.Update(ctx, env); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	slog.Info("container environment destroyed", "environment", env.Name)
	return ctrl.Result{}, nil
}

func (r *ContainerEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: containerEnvironmentMaxConcurrentReconciles}).
		For(&breakfixv1.ContainerEnvironment{}).
		Complete(r)
}

type containerEnvironmentRuntime struct {
	r *ContainerEnvironmentReconciler
}

func (rt containerEnvironmentRuntime) provision(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.createPod(ctx, env.(*breakfixv1.ContainerEnvironment))
}

func (rt containerEnvironmentRuntime) waitReady(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.waitForPod(ctx, env.(*breakfixv1.ContainerEnvironment))
}

func (rt containerEnvironmentRuntime) evaluateCheckpoints(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.evaluateCheckpoints(ctx, env.(*breakfixv1.ContainerEnvironment))
}

func (rt containerEnvironmentRuntime) handleDraining(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.checkCooldown(ctx, env.(*breakfixv1.ContainerEnvironment))
}

func (rt containerEnvironmentRuntime) cleanup(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.cleanup(ctx, env.(*breakfixv1.ContainerEnvironment))
}

func (rt containerEnvironmentRuntime) finalCleanup(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.finalCleanup(ctx, env.(*breakfixv1.ContainerEnvironment))
}

func looksLikeURL(s string) bool {
	return len(s) > 0 && (s[0] == '.' || s[0] == '/' || (len(s) > 4 && s[:4] == "http") || strings.Contains(s, "/"))
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
