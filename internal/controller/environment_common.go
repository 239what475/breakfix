package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	defaultEnvironmentIdleTTL = 10 * time.Minute
)

func validateEnvironmentSnapshot(spec *breakfixv1.CommonEnvironmentSpec, expectedRuntime string) error {
	if spec == nil {
		return fmt.Errorf("environment spec is required")
	}
	for name, value := range map[string]string{
		"challengeRef":      spec.ChallengeRef,
		"challengeRevision": spec.ChallengeRevision,
		"userRef":           spec.UserRef,
		"runtime":           spec.Runtime,
		"image":             spec.Image,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("environment spec %s is required", name)
		}
	}
	if spec.Runtime != expectedRuntime {
		return fmt.Errorf("environment runtime %q does not match %s resource", spec.Runtime, expectedRuntime)
	}
	if len(spec.CheckpointIDs) == 0 {
		return fmt.Errorf("environment spec checkpointIDs is required")
	}
	seen := make(map[string]struct{}, len(spec.CheckpointIDs))
	for _, rawID := range spec.CheckpointIDs {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return fmt.Errorf("environment spec checkpoint id is empty")
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("environment spec checkpoint id %q is duplicated", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

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
	if status.ReadyAt == nil {
		status.ReadyAt = &now
	}
	status.Phase = breakfixv1.EnvironmentReady
	status.Reason = reason
	status.Message = msg
	applyEnvironmentActivity(spec, status, status.ReadyAt)
	status.LastError = nil
	setEnvironmentCondition(status, breakfixv1.ConditionProvisioned, metav1.ConditionTrue, reason, msg)
	setEnvironmentCondition(status, breakfixv1.ConditionWorkspaceReady, metav1.ConditionTrue, reason, msg)
	setEnvironmentCondition(status, breakfixv1.ConditionReady, metav1.ConditionTrue, reason, msg)
	setEnvironmentCondition(status, breakfixv1.ConditionDraining, metav1.ConditionFalse, "", "")
	setEnvironmentCondition(status, breakfixv1.ConditionCompleted, metav1.ConditionFalse, "", "")
	setEnvironmentCondition(status, breakfixv1.ConditionFailed, metav1.ConditionFalse, "", "")
}

// applyEnvironmentActivity converts the Server-owned desired activity input
// into Controller-owned lease status. A stale Server write can never shorten a
// lease that has already observed newer activity.
func applyEnvironmentActivity(spec *breakfixv1.CommonEnvironmentSpec, status *breakfixv1.CommonEnvironmentStatus, fallback *metav1.Time) bool {
	if spec == nil || status == nil {
		return false
	}
	activity := fallback
	if spec.ActivityAt != nil && !spec.ActivityAt.IsZero() {
		activity = spec.ActivityAt
	}
	if activity == nil || activity.IsZero() {
		return false
	}
	// metav1.Time serializes through Kubernetes at whole-second precision. Keep
	// the desired activity and observed status in that same precision so the
	// same spec value cannot repeatedly revive an expired draining lease.
	activityTime := activity.UTC().Truncate(time.Second)
	if spec.ActivityAt == nil && status.LastActivityAt == nil && status.ExpiresAt != nil && status.ExpiresAt.After(time.Now()) {
		// Preserve a lease written by the pre-split runtime while it remains
		// active. New Server activity will replace it through spec.activityAt.
		nextActivity := metav1.NewTime(activityTime)
		status.LastActivityAt = &nextActivity
		return true
	}
	if status.LastActivityAt != nil && !activityTime.After(status.LastActivityAt.UTC().Truncate(time.Second)) {
		return false
	}
	nextActivity := metav1.NewTime(activityTime)
	status.LastActivityAt = &nextActivity
	expires := metav1.NewTime(nextActivity.Add(spec.IdleTTLOr(defaultEnvironmentIdleTTL)))
	status.ExpiresAt = &expires
	if status.Phase == breakfixv1.EnvironmentDraining {
		status.Phase = breakfixv1.EnvironmentReady
		status.Reason = "ActivityResumed"
		status.Message = "environment lease renewed"
		setEnvironmentCondition(status, breakfixv1.ConditionReady, metav1.ConditionTrue, "ActivityResumed", "environment lease renewed")
		setEnvironmentCondition(status, breakfixv1.ConditionDraining, metav1.ConditionFalse, "ActivityResumed", "environment lease renewed")
	}
	return true
}

// advanceEnvironmentLease owns every Ready-to-Draining-to-Destroyed
// transition. Server activity arrives only through spec.activityAt.
func advanceEnvironmentLease(spec *breakfixv1.CommonEnvironmentSpec, status *breakfixv1.CommonEnvironmentStatus) (changed, evaluate bool, requeueAfter time.Duration) {
	if spec == nil || status == nil {
		return false, true, 0
	}
	changed = applyEnvironmentActivity(spec, status, status.ReadyAt)
	if !spec.AutoDestroyAfterIdleOr(true) {
		return changed, true, 0
	}
	switch status.Phase {
	case breakfixv1.EnvironmentReady:
		destroy, _ := shouldDestroyEnvironment(status)
		if !destroy {
			return changed, true, 0
		}
		grace := spec.DrainGracePeriodOr(spec.IdleTTLOr(defaultEnvironmentIdleTTL))
		expiresAt := metav1.NewTime(time.Now().Add(grace))
		setEnvironmentDraining(status, expiresAt, "IdleTTLExpired", "environment idle lease expired")
		return true, false, grace
	case breakfixv1.EnvironmentDraining:
		if status.Phase == breakfixv1.EnvironmentReady {
			return true, true, 0
		}
		destroy, remaining := shouldDestroyEnvironment(status)
		if !destroy {
			return changed, false, remaining
		}
		markEnvironmentDestroyed(status, "IdleDrainCompleted", "environment idle drain completed")
		return true, false, 0
	default:
		return changed, true, 0
	}
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
// environment when Server has no opportunity to transition it to Draining,
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
