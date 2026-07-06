package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/k8s"
	"log/slog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const containerEnvironmentFinalizer = "breakfix.dev/container-environment-cleanup"
const breakfixInitSentinel = "/var/lib/breakfix/.initialized"

type ContainerEnvironmentReconciler struct {
	client.Client
	K8s          *k8s.Client
	RegistryAddr string
	NS           string
	CRDNamespace string
	Cooldown     time.Duration
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

	if err := r.K8s.CreatePod(ns, podName, k8s.CreatePodOpts{
		Image:         imageURL,
		ChallengeID:   env.Spec.ChallengeRef,
		EnvironmentID: env.Name,
	}); err != nil {
		slog.Error("failed to create pod", "err", err, "environment", env.Name)
		setEnvironmentProvisioning(&env.Status, err.Error())
		_, _ = r.K8s.UpdateContainerEnvironmentStatus(ctx, r.CRDNamespace, env)
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	env.Status.Phase = breakfixv1.EnvironmentProvisioning
	env.Status.WorkspacePodName = podName
	env.Status.Namespace = ns
	env.Status.Message = "workspace pod created"

	if err := r.Status().Update(ctx, env); err != nil {
		return ctrl.Result{}, err
	}

	slog.Info("workspace pod created", "environment", env.Name, "pod", podName, "namespace", ns)
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func (r *ContainerEnvironmentReconciler) waitForPod(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	if err := r.K8s.WaitForPod(env.Status.Namespace, env.Status.WorkspacePodName, "challenge"); err != nil {
		slog.Debug("pod not ready yet", "environment", env.Name, "err", err)
		setEnvironmentProvisioning(&env.Status, err.Error())
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err := r.K8s.WaitForFileInPod(env.Status.Namespace, env.Status.WorkspacePodName, breakfixInitSentinel, 2*time.Minute); err != nil {
		slog.Debug("pod initialized sentinel not ready yet", "environment", env.Name, "err", err)
		setEnvironmentProvisioning(&env.Status, err.Error())
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	setEnvironmentReady(&env.Status, "environment ready")

	if err := r.Status().Update(ctx, env); err != nil {
		return ctrl.Result{}, err
	}

	slog.Info("container environment ready", "environment", env.Name, "pod", env.Status.WorkspacePodName)
	return ctrl.Result{}, nil
}

func (r *ContainerEnvironmentReconciler) submit(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	slog.Info("submitting environment", "environment", env.Name)

	exitCode, output, err := r.K8s.ExecInPod(env.Status.Namespace, env.Status.WorkspacePodName, "/verify.sh")
	if err != nil {
		slog.Error("exec verify.sh", "err", err, "environment", env.Name)
	}

	setEnvironmentSubmitted(&env.Status, exitCode, output)

	if err := r.Status().Update(ctx, env); err != nil {
		return ctrl.Result{}, err
	}

	return r.cleanup(ctx, env)
}

func (r *ContainerEnvironmentReconciler) cleanup(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	cleanupCommonEnvironment(r.K8s, &env.Status)
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *ContainerEnvironmentReconciler) checkCooldown(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	destroy, remaining := shouldDestroyEnvironment(&env.Status)
	if destroy {
		slog.Info("environment expired", "environment", env.Name)
		env.Status.Phase = breakfixv1.EnvironmentDestroyed
		if err := r.Status().Update(ctx, env); err != nil {
			return ctrl.Result{}, err
		}
		return r.cleanup(ctx, env)
	}
	return ctrl.Result{RequeueAfter: remaining}, nil
}

func (r *ContainerEnvironmentReconciler) requestDeletion(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	slog.Info("requesting container environment deletion", "environment", env.Name, "namespace", env.Status.Namespace)
	return requestEnvironmentDeletion(ctx, r.Client, env)
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

	controllerutil.RemoveFinalizer(env, containerEnvironmentFinalizer)
	if err := r.Update(ctx, env); err != nil {
		return ctrl.Result{}, err
	}

	slog.Info("container environment destroyed", "environment", env.Name)
	return ctrl.Result{}, nil
}

func (r *ContainerEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
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

func (rt containerEnvironmentRuntime) submit(ctx context.Context, env commonEnvironmentObject) (ctrl.Result, error) {
	return rt.r.submit(ctx, env.(*breakfixv1.ContainerEnvironment))
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
