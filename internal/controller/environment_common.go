package controller

import (
	"context"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func setEnvironmentProvisioning(status *breakfixv1.CommonEnvironmentStatus, msg string) {
	status.Phase = breakfixv1.EnvironmentProvisioning
	status.Message = truncate(msg, 4000)
}

func setEnvironmentReady(status *breakfixv1.CommonEnvironmentStatus, msg string) {
	status.Phase = breakfixv1.EnvironmentReady
	status.Message = msg
	now := metav1.Now()
	status.StartedAt = &now
	if status.ExpiresAt == nil || status.ExpiresAt.Before(&now) {
		expires := metav1.NewTime(now.Add(10 * time.Minute))
		status.ExpiresAt = &expires
	}
}

func setEnvironmentSubmitted(status *breakfixv1.CommonEnvironmentStatus, exitCode int, output string) {
	now := metav1.Now()
	status.SubmitResult = &breakfixv1.SubmitResult{
		Passed:      exitCode == 0,
		ExitCode:    exitCode,
		Output:      truncate(output, 4000),
		SubmittedAt: &now,
	}
	status.Phase = breakfixv1.EnvironmentDestroyed
	status.Message = "environment submitted"
}

func shouldDestroyEnvironment(status *breakfixv1.CommonEnvironmentStatus) (bool, time.Duration) {
	if status.ExpiresAt == nil || time.Now().After(status.ExpiresAt.Time) {
		return true, 0
	}
	remaining := time.Until(status.ExpiresAt.Time)
	if remaining > 30*time.Second {
		remaining = 30 * time.Second
	}
	return false, remaining
}

func cleanupCommonEnvironment(k8sClient *k8s.Client, status *breakfixv1.CommonEnvironmentStatus) {
	if status.WorkspacePodName != "" {
		_ = k8sClient.DeletePod(status.Namespace, status.WorkspacePodName)
	}
	if status.Namespace != "" {
		_ = k8sClient.DeleteNamespace(status.Namespace)
	}
}

func finalizeCommonEnvironment(ctx context.Context, k8sClient *k8s.Client, namespace string) (bool, error) {
	if namespace == "" {
		return true, nil
	}
	ns, err := k8sClient.GetNamespace(namespace)
	if err == nil && ns != nil {
		return false, nil
	}
	return true, nil
}

func requestEnvironmentDeletion(ctx context.Context, kubeClient client.Client, env client.Object) (ctrl.Result, error) {
	if env.GetDeletionTimestamp() != nil {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err := kubeClient.Delete(ctx, env); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}
