package controller

import (
	"context"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	defaultEnvironmentIdleTTL = 10 * time.Minute
)

func setEnvironmentProvisioning(status *breakfixv1.CommonEnvironmentStatus, reason, msg string) {
	now := metav1.Now()
	if status.StartedAt == nil {
		status.StartedAt = &now
	}
	status.Phase = breakfixv1.EnvironmentProvisioning
	status.Reason = reason
	status.Message = truncate(msg, 4000)
	status.LastError = nil
	setEnvironmentCondition(status, breakfixv1.ConditionReady, metav1.ConditionFalse, reason, msg)
	setEnvironmentCondition(status, breakfixv1.ConditionCompleted, metav1.ConditionFalse, "", "")
	setEnvironmentCondition(status, breakfixv1.ConditionFailed, metav1.ConditionFalse, "", "")
}

func setEnvironmentReady(spec *breakfixv1.CommonEnvironmentSpec, status *breakfixv1.CommonEnvironmentStatus, reason, msg string) {
	now := metav1.Now()
	if status.StartedAt == nil {
		status.StartedAt = &now
	}
	status.ReadyAt = &now
	status.Phase = breakfixv1.EnvironmentReady
	status.Reason = reason
	status.Message = msg
	if status.ExpiresAt == nil || status.ExpiresAt.Before(&now) {
		expires := metav1.NewTime(now.Add(spec.IdleTTLOr(defaultEnvironmentIdleTTL)))
		status.ExpiresAt = &expires
	}
	status.LastError = nil
	setEnvironmentCondition(status, breakfixv1.ConditionProvisioned, metav1.ConditionTrue, reason, msg)
	setEnvironmentCondition(status, breakfixv1.ConditionWorkspaceReady, metav1.ConditionTrue, reason, msg)
	setEnvironmentCondition(status, breakfixv1.ConditionReady, metav1.ConditionTrue, reason, msg)
	setEnvironmentCondition(status, breakfixv1.ConditionDraining, metav1.ConditionFalse, "", "")
	setEnvironmentCondition(status, breakfixv1.ConditionCompleted, metav1.ConditionFalse, "", "")
	setEnvironmentCondition(status, breakfixv1.ConditionFailed, metav1.ConditionFalse, "", "")
}

func setEnvironmentCompleted(status *breakfixv1.CommonEnvironmentStatus) {
	setEnvironmentCompletedAt(status, time.Now().UTC())
}

func setEnvironmentCompletedAt(status *breakfixv1.CommonEnvironmentStatus, completedAt time.Time) {
	now := metav1.NewTime(completedAt.UTC())
	status.Phase = breakfixv1.EnvironmentCompleted
	status.CompletedAt = &now
	status.Reason = "CheckpointsCompleted"
	status.Message = "all checkpoints completed"
	status.LastError = nil
	setEnvironmentCondition(status, breakfixv1.ConditionCompleted, metav1.ConditionTrue, "CheckpointsCompleted", "all checkpoints completed")
	setEnvironmentCondition(status, breakfixv1.ConditionReady, metav1.ConditionFalse, "CheckpointsCompleted", "all checkpoints completed")
	setEnvironmentCondition(status, breakfixv1.ConditionDraining, metav1.ConditionFalse, "", "")
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

// readyEnvironmentLeaseExpired applies the existing idle lease to a Ready
// environment when Gateway has no opportunity to transition it to Draining,
// such as after a process restart or a lost WebSocket close frame.
func readyEnvironmentLeaseExpired(spec *breakfixv1.CommonEnvironmentSpec, status *breakfixv1.CommonEnvironmentStatus) bool {
	if spec == nil || status == nil || !spec.AutoDestroyAfterIdleOr(true) {
		return false
	}
	expired, _ := shouldDestroyEnvironment(status)
	return expired
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

func markEnvironmentDestroyed(status *breakfixv1.CommonEnvironmentStatus, reason, msg string) {
	now := metav1.Now()
	status.Phase = breakfixv1.EnvironmentDestroyed
	status.Reason = reason
	status.Message = truncate(msg, 4000)
	status.DestroyedAt = &now
	setEnvironmentCondition(status, breakfixv1.ConditionCleanedUp, metav1.ConditionTrue, reason, msg)
	setEnvironmentCondition(status, breakfixv1.ConditionReady, metav1.ConditionFalse, reason, msg)
	setEnvironmentCondition(status, breakfixv1.ConditionDraining, metav1.ConditionFalse, "", "")
}

func markEnvironmentFailed(status *breakfixv1.CommonEnvironmentStatus, component, code, reason, msg string, retriable bool) {
	now := metav1.Now()
	status.Phase = breakfixv1.EnvironmentFailed
	status.Reason = reason
	status.Message = truncate(msg, 4000)
	status.LastError = &breakfixv1.EnvironmentErrorStatus{
		Component: component,
		Code:      code,
		Message:   truncate(msg, 4000),
		At:        &now,
		Retriable: retriable,
	}
	setEnvironmentCondition(status, breakfixv1.ConditionFailed, metav1.ConditionTrue, reason, msg)
	setEnvironmentCondition(status, breakfixv1.ConditionReady, metav1.ConditionFalse, reason, msg)
}

func setEnvironmentDraining(status *breakfixv1.CommonEnvironmentStatus, expiresAt metav1.Time, reason, msg string) {
	status.Phase = breakfixv1.EnvironmentDraining
	status.Reason = reason
	status.ExpiresAt = &expiresAt
	status.Message = truncate(msg, 4000)
	setEnvironmentCondition(status, breakfixv1.ConditionDraining, metav1.ConditionTrue, reason, msg)
}

func clearEnvironmentFailure(status *breakfixv1.CommonEnvironmentStatus) {
	status.LastError = nil
	setEnvironmentCondition(status, breakfixv1.ConditionFailed, metav1.ConditionFalse, "", "")
}

func setEnvironmentCondition(status *breakfixv1.CommonEnvironmentStatus, conditionType string, conditionStatus metav1.ConditionStatus, reason, msg string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "NotApplicable"
	}
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             conditionStatus,
		Reason:             truncate(reason, 1024),
		Message:            truncate(msg, 4000),
		ObservedGeneration: status.ObservedGeneration,
		LastTransitionTime: metav1.Now(),
	})
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

func workspaceResourceRequirements(spec *breakfixv1.CommonEnvironmentSpec) (corev1.ResourceRequirements, error) {
	if spec == nil {
		return corev1.ResourceRequirements{}, nil
	}
	limits := corev1.ResourceList{}
	if v := spec.Resources.WorkspaceCPU; v != "" {
		q, err := resource.ParseQuantity(v)
		if err != nil {
			return corev1.ResourceRequirements{}, err
		}
		limits[corev1.ResourceCPU] = q
	}
	if v := spec.Resources.WorkspaceMemory; v != "" {
		q, err := resource.ParseQuantity(v)
		if err != nil {
			return corev1.ResourceRequirements{}, err
		}
		limits[corev1.ResourceMemory] = q
	}
	if v := spec.Resources.WorkspaceEphemeralStorage; v != "" {
		q, err := resource.ParseQuantity(v)
		if err != nil {
			return corev1.ResourceRequirements{}, err
		}
		limits[corev1.ResourceEphemeralStorage] = q
	}
	if len(limits) == 0 {
		return corev1.ResourceRequirements{}, nil
	}
	return corev1.ResourceRequirements{
		Requests: limits,
		Limits:   limits,
	}, nil
}
