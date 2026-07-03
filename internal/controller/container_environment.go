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

	if env.DeletionTimestamp != nil {
		result, err := r.finalCleanup(ctx, &env)
		slog.Info("reconcile done", "resource", "containerEnvironment", "name", env.Name, "phase", env.Status.Phase, "duration", time.Since(start))
		return result, err
	}

	var result ctrl.Result
	var err error
	switch env.Status.Phase {
	case "":
		result, err = r.createPod(ctx, &env)
	case breakfixv1.EnvironmentPending, breakfixv1.EnvironmentProvisioning:
		result, err = r.waitForPod(ctx, &env)
	case breakfixv1.EnvironmentReady:
		if env.Spec.Submit {
			result, err = r.submit(ctx, &env)
		}
	case breakfixv1.EnvironmentDraining:
		if env.Spec.Submit {
			result, err = r.submit(ctx, &env)
		} else {
			result, err = r.checkCooldown(ctx, &env)
		}
	case breakfixv1.EnvironmentDestroyed, breakfixv1.EnvironmentFailed:
		result, err = r.requestDeletion(ctx, &env)
	}

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
		env.Status.Phase = breakfixv1.EnvironmentProvisioning
		env.Status.Message = truncate(err.Error(), 4000)
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
		env.Status.Phase = breakfixv1.EnvironmentProvisioning
		env.Status.Message = truncate(err.Error(), 4000)
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err := r.K8s.WaitForFileInPod(env.Status.Namespace, env.Status.WorkspacePodName, breakfixInitSentinel, 2*time.Minute); err != nil {
		slog.Debug("pod initialized sentinel not ready yet", "environment", env.Name, "err", err)
		env.Status.Phase = breakfixv1.EnvironmentProvisioning
		env.Status.Message = truncate(err.Error(), 4000)
		_ = r.Status().Update(ctx, env)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	env.Status.Phase = breakfixv1.EnvironmentReady
	env.Status.Message = "environment ready"
	now := metav1.Now()
	env.Status.StartedAt = &now

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

	passed := exitCode == 0
	result := &breakfixv1.SubmitResult{
		Passed:   passed,
		ExitCode: exitCode,
		Output:   truncate(output, 4000),
	}
	now := metav1.Now()
	result.SubmittedAt = &now

	env.Status.SubmitResult = result
	env.Status.Phase = breakfixv1.EnvironmentDestroyed
	env.Status.Message = "environment submitted"

	if err := r.Status().Update(ctx, env); err != nil {
		return ctrl.Result{}, err
	}

	return r.cleanup(ctx, env)
}

func (r *ContainerEnvironmentReconciler) checkCooldown(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	if env.Status.ExpiresAt == nil {
		env.Status.Phase = breakfixv1.EnvironmentDestroyed
		if err := r.Status().Update(ctx, env); err != nil {
			return ctrl.Result{}, err
		}
		return r.cleanup(ctx, env)
	}

	if time.Now().After(env.Status.ExpiresAt.Time) {
		slog.Info("environment expired", "environment", env.Name)
		env.Status.Phase = breakfixv1.EnvironmentDestroyed
		if err := r.Status().Update(ctx, env); err != nil {
			return ctrl.Result{}, err
		}
		return r.cleanup(ctx, env)
	}

	remaining := time.Until(env.Status.ExpiresAt.Time)
	if remaining > 30*time.Second {
		remaining = 30 * time.Second
	}
	return ctrl.Result{RequeueAfter: remaining}, nil
}

func (r *ContainerEnvironmentReconciler) cleanup(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	if env.Status.WorkspacePodName != "" {
		if err := r.K8s.DeletePod(env.Status.Namespace, env.Status.WorkspacePodName); err != nil {
			slog.Debug("cleanup pod", "err", err)
		}
	}
	if env.Status.Namespace != "" {
		if err := r.K8s.DeleteNamespace(env.Status.Namespace); err != nil {
			slog.Debug("cleanup namespace", "err", err)
		}
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *ContainerEnvironmentReconciler) requestDeletion(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	if env.DeletionTimestamp != nil {
		slog.Info("container environment deletion already requested", "environment", env.Name)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	slog.Info("requesting container environment deletion", "environment", env.Name, "namespace", env.Status.Namespace)
	if err := r.Delete(ctx, env); err != nil {
		slog.Error("request container environment deletion failed", "environment", env.Name, "err", err)
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func (r *ContainerEnvironmentReconciler) finalCleanup(ctx context.Context, env *breakfixv1.ContainerEnvironment) (ctrl.Result, error) {
	if env.Status.WorkspacePodName != "" {
		r.K8s.DeletePod(env.Status.Namespace, env.Status.WorkspacePodName) //nolint:errcheck
	}
	if env.Status.Namespace != "" {
		r.K8s.DeleteNamespace(env.Status.Namespace) //nolint:errcheck
	}

	if env.Status.Namespace != "" {
		ns, err := r.K8s.GetNamespace(env.Status.Namespace)
		if err == nil && ns != nil {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
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

func looksLikeURL(s string) bool {
	return len(s) > 0 && (s[0] == '.' || s[0] == '/' || (len(s) > 4 && s[:4] == "http") || strings.Contains(s, "/"))
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
