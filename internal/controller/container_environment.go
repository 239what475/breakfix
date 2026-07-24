package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/k8s"
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
	ChallengesDir      string
	NS                 string
	CRDNamespace       string
	Cooldown           time.Duration
	CompletionRecorder CompletionRecorder
	AttemptRecorder    AttemptRecorder
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
	if !controllerutil.ContainsFinalizer(env, containerEnvironmentFinalizer) {
		controllerutil.AddFinalizer(env, containerEnvironmentFinalizer)
		if err := r.Update(ctx, env); err != nil {
			return ctrl.Result{}, err
		}
	}

	ns := k8s.UserNamespace(r.NS, env.Spec.UserRef) + "-" + env.Name
	podName := "challenge-" + env.Name

	if err := r.K8s.EnsureNamespace(ns); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure namespace: %w", err)
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
		Image:         imageURL,
		ChallengeID:   env.Spec.ChallengeRef,
		EnvironmentID: env.Name,
		Resources:     resources,
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

	if err := setEnvironmentReadyAndRecordAttempt(ctx, r.AttemptRecorder, env, "container", "WorkspaceReady", "environment ready"); err != nil {
		return ctrl.Result{}, fmt.Errorf("record environment attempt: %w", err)
	}

	if err := r.Status().Update(ctx, env); err != nil {
		return ctrl.Result{}, err
	}

	slog.Info("container environment ready", "environment", env.Name, "pod", env.Status.WorkspacePodName)
	return ctrl.Result{}, nil
}

func (r *ContainerEnvironmentReconciler) evaluateCheckpoints(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	if readyEnvironmentLeaseExpired(&env.Spec, &env.Status) {
		return r.checkCooldown(ctx, env)
	}
	report, checkErr := runCheckpointEvaluation(ctx, r.K8s, r.ChallengesDir, env.Spec.ChallengeRef, env.Status.Namespace, env.Status.WorkspacePodName)
	changed := recordCheckpointStatus(&env.Status, report, checkErr)
	if checkErr != nil {
		if changed {
			if err := r.Status().Update(ctx, env); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: checkpointInterval}, nil
	}
	if report.Passed() {
		slog.Info("environment checkpoints completed", "environment", env.Name)
		completedAt := time.Now().UTC()
		if err := recordEnvironmentCompletion(ctx, r.CompletionRecorder, env, completedAt); err != nil {
			return ctrl.Result{}, fmt.Errorf("record environment completion: %w", err)
		}
		setEnvironmentCompletedAt(&env.Status, completedAt)
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
		return r.requestDeletion(ctx, env)
	}
	return ctrl.Result{RequeueAfter: remaining}, nil
}

func (r *ContainerEnvironmentReconciler) requestDeletion(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	slog.Info("requesting container environment deletion", "environment", env.Name, "namespace", env.Status.Namespace)
	return requestEnvironmentDeletion(ctx, r.Client, env)
}

func (r *ContainerEnvironmentReconciler) finalCleanup(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	if err := finishEnvironmentAttempt(ctx, r.AttemptRecorder, env, "expired", time.Now().UTC()); err != nil {
		return ctrl.Result{}, fmt.Errorf("finish environment attempt: %w", err)
	}
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
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(env, containerEnvironmentFinalizer)
	if err := r.Update(ctx, env); err != nil {
		return ctrl.Result{}, err
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
	if err := recordEnvironmentCompletion(ctx, rt.r.CompletionRecorder, env, completionTime(env.CommonStatus())); err != nil {
		return ctrl.Result{}, fmt.Errorf("record environment completion: %w", err)
	}
	return rt.r.checkCooldown(ctx, env.(*breakfixv1.ContainerEnvironment))
}

func (rt containerEnvironmentRuntime) cleanup(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.cleanup(ctx, env.(*breakfixv1.ContainerEnvironment))
}

func (rt containerEnvironmentRuntime) finalCleanup(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.finalCleanup(ctx, env.(*breakfixv1.ContainerEnvironment))
}

func (rt containerEnvironmentRuntime) requestDeletion(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.requestDeletion(ctx, env.(*breakfixv1.ContainerEnvironment))
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
