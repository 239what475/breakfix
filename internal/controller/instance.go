package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"log/slog"
)

const instanceFinalizer = "breakfix.dev/instance-cleanup"

// InstanceReconciler watches Instance CRDs and manages challenge Pods.
type InstanceReconciler struct {
	client.Client
	K8s           *k8s.Client
	RegistryAddr  string
	NS            string        // base namespace for user namespaces
	CRDNamespace  string        // where Instance CRDs live
	Cooldown      time.Duration // drain duration before auto-destroy
}

func (r *InstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	var inst breakfixv1.Instance
	if err := r.Get(ctx, req.NamespacedName, &inst); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	var result ctrl.Result
	var err error
	switch inst.Status.Phase {
	case "":
		result, err = r.createPod(ctx, &inst)
	case breakfixv1.InstancePending:
		result, err = r.waitForPod(ctx, &inst)
	case breakfixv1.InstanceRunning:
		if inst.Spec.Submit {
			result, err = r.submit(ctx, &inst)
		}
	case breakfixv1.InstanceDraining:
		if inst.Spec.Submit {
			result, err = r.submit(ctx, &inst)
		} else {
			result, err = r.checkCooldown(ctx, &inst)
		}
	case breakfixv1.InstanceDestroyed:
		result, err = r.finalCleanup(ctx, &inst)
	}

	slog.Info("reconcile done", "resource", "instance", "name", inst.Name, "phase", inst.Status.Phase, "duration", time.Since(start))
	return result, err
}

func (r *InstanceReconciler) createPod(ctx context.Context, inst *breakfixv1.Instance) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(inst, instanceFinalizer) {
		controllerutil.AddFinalizer(inst, instanceFinalizer)
		if err := r.Update(ctx, inst); err != nil {
			return ctrl.Result{}, err
		}
	}

	ns := k8s.UserNamespace(r.NS, inst.Spec.UserRef)
	podName := "challenge-" + inst.Name

	if err := r.K8s.EnsureNamespace(ns); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure namespace: %w", err)
	}

	imageURL := inst.Spec.Image
	if r.RegistryAddr != "" && !looksLikeURL(imageURL) {
		imageURL = r.RegistryAddr + "/" + imageURL
	}

	if err := r.K8s.CreatePod(ns, podName, k8s.CreatePodOpts{
		Image:       imageURL,
		ChallengeID: inst.Spec.ChallengeRef,
		InstanceID:  inst.Name,
	}); err != nil {
		slog.Error("failed to create pod", "err", err, "instance", inst.Name)
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}

	inst.Status.Phase = breakfixv1.InstancePending
	inst.Status.PodName = podName
	inst.Status.Namespace = ns

	if err := r.Status().Update(ctx, inst); err != nil {
		return ctrl.Result{}, err
	}

	slog.Info("pod created", "instance", inst.Name, "pod", podName, "namespace", ns)
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func (r *InstanceReconciler) waitForPod(ctx context.Context, inst *breakfixv1.Instance) (ctrl.Result, error) {
	if err := r.K8s.WaitForPod(inst.Status.Namespace, inst.Status.PodName, "challenge"); err != nil {
		slog.Debug("pod not ready yet", "instance", inst.Name, "err", err)
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	inst.Status.Phase = breakfixv1.InstanceRunning
	now := metav1.Now()
	inst.Status.StartedAt = &now

	if err := r.Status().Update(ctx, inst); err != nil {
		return ctrl.Result{}, err
	}

	slog.Info("instance running", "instance", inst.Name, "pod", inst.Status.PodName)
	return ctrl.Result{}, nil
}

func (r *InstanceReconciler) submit(ctx context.Context, inst *breakfixv1.Instance) (ctrl.Result, error) {
	slog.Info("submitting instance", "instance", inst.Name)

	// Execute verify.sh baked into the challenge image
	exitCode, output, err := r.K8s.ExecInPod(inst.Status.Namespace, inst.Status.PodName, "/verify.sh")
	if err != nil {
		slog.Error("exec verify.sh", "err", err, "instance", inst.Name)
	}

	passed := exitCode == 0
	result := &breakfixv1.SubmitResult{
		Passed:   passed,
		ExitCode: exitCode,
		Output:   truncate(output, 4000),
	}
	now := metav1.Now()
	result.SubmittedAt = &now

	inst.Status.SubmitResult = result
	inst.Status.Phase = breakfixv1.InstanceDestroyed

	if err := r.Status().Update(ctx, inst); err != nil {
		slog.Error("update instance status", "err", err)
		return ctrl.Result{}, err
	}

	resultStr := "FAILED"
	if passed {
		resultStr = "PASSED"
	}
	slog.Info("submit result", "instance", inst.Name, "result", resultStr, "exit", exitCode)

	return r.cleanupPod(ctx, inst)
}

func (r *InstanceReconciler) checkCooldown(ctx context.Context, inst *breakfixv1.Instance) (ctrl.Result, error) {
	if inst.Status.CooldownUntil == nil {
		inst.Status.Phase = breakfixv1.InstanceDestroyed
		if err := r.Status().Update(ctx, inst); err != nil {
			return ctrl.Result{}, err
		}
		return r.cleanupPod(ctx, inst)
	}

	if time.Now().After(inst.Status.CooldownUntil.Time) {
		slog.Info("cooldown expired", "instance", inst.Name)
		inst.Status.Phase = breakfixv1.InstanceDestroyed
		if err := r.Status().Update(ctx, inst); err != nil {
			return ctrl.Result{}, err
		}
		return r.cleanupPod(ctx, inst)
	}

	remaining := time.Until(inst.Status.CooldownUntil.Time)
	if remaining > 30*time.Second {
		remaining = 30 * time.Second
	}
	return ctrl.Result{RequeueAfter: remaining}, nil
}

func (r *InstanceReconciler) cleanupPod(ctx context.Context, inst *breakfixv1.Instance) (ctrl.Result, error) {
	if inst.Status.PodName != "" {
		if err := r.K8s.DeletePod(inst.Status.Namespace, inst.Status.PodName); err != nil {
			slog.Debug("cleanup pod", "err", err)
		}
	}
	if inst.Status.Namespace != "" {
		if err := r.K8s.DeleteNamespace(inst.Status.Namespace); err != nil {
			slog.Debug("cleanup namespace", "err", err)
		}
	}
	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *InstanceReconciler) finalCleanup(ctx context.Context, inst *breakfixv1.Instance) (ctrl.Result, error) {
	if inst.Status.PodName != "" {
		r.K8s.DeletePod(inst.Status.Namespace, inst.Status.PodName) //nolint:errcheck
	}
	if inst.Status.Namespace != "" {
		r.K8s.DeleteNamespace(inst.Status.Namespace) //nolint:errcheck
	}

	if r.K8s.EnsureNamespace(inst.Status.Namespace) == nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	controllerutil.RemoveFinalizer(inst, instanceFinalizer)
	if err := r.Update(ctx, inst); err != nil {
		return ctrl.Result{}, err
	}

	slog.Info("instance destroyed", "instance", inst.Name)
	return ctrl.Result{}, nil
}

func (r *InstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&breakfixv1.Instance{}).
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
