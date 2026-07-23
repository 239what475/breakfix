package controller

import (
	"testing"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSetEnvironmentReadyAssignsLeaseWhenMissing(t *testing.T) {
	var status breakfixv1.CommonEnvironmentStatus
	var spec breakfixv1.CommonEnvironmentSpec

	setEnvironmentReady(&spec, &status, "WorkspaceReady", "ready")

	if status.Phase != breakfixv1.EnvironmentReady {
		t.Fatalf("expected Ready, got %s", status.Phase)
	}
	if status.ExpiresAt == nil {
		t.Fatal("expected expiresAt to be assigned")
	}
	if status.StartedAt == nil {
		t.Fatal("expected startedAt to be assigned")
	}
	if status.ReadyAt == nil {
		t.Fatal("expected readyAt to be assigned")
	}
	if !status.ExpiresAt.After(status.StartedAt.Time) {
		t.Fatalf("expected expiresAt after startedAt, got startedAt=%v expiresAt=%v", status.StartedAt, status.ExpiresAt)
	}
}

func TestSetEnvironmentReadyPreservesActiveLease(t *testing.T) {
	expires := time.Now().Add(3 * time.Minute)
	status := breakfixv1.CommonEnvironmentStatus{
		ExpiresAt: ptrMetaTime(expires),
	}
	var spec breakfixv1.CommonEnvironmentSpec

	setEnvironmentReady(&spec, &status, "WorkspaceReady", "ready")

	if status.ExpiresAt == nil {
		t.Fatal("expected expiresAt to remain set")
	}
	if got := status.ExpiresAt.Time; !got.Equal(expires) {
		t.Fatalf("expected existing expiresAt %v, got %v", expires, got)
	}
}

func TestSetEnvironmentProvisioningPreservesProvisionedCondition(t *testing.T) {
	status := breakfixv1.CommonEnvironmentStatus{}
	setEnvironmentCondition(&status, breakfixv1.ConditionProvisioned, metav1.ConditionTrue, "WorkspacePodCreated", "workspace pod created")

	setEnvironmentProvisioning(&status, "WaitingForWorkspacePod", "waiting for workspace pod")

	for _, condition := range status.Conditions {
		if condition.Type != breakfixv1.ConditionProvisioned {
			continue
		}
		if condition.Status != metav1.ConditionTrue {
			t.Fatalf("expected Provisioned condition to remain true, got %s", condition.Status)
		}
		return
	}
	t.Fatal("expected Provisioned condition to be retained")
}

func TestSetEnvironmentCompletedUsesCompletedPhase(t *testing.T) {
	var status breakfixv1.CommonEnvironmentStatus

	setEnvironmentCompleted(&status)

	if status.Phase != breakfixv1.EnvironmentCompleted {
		t.Fatalf("expected Completed, got %s", status.Phase)
	}
	if status.CompletedAt == nil {
		t.Fatal("expected completed timestamp")
	}
}

func TestWorkspaceResourceRequirementsUsesSpecLimitsForRequestsAndLimits(t *testing.T) {
	spec := breakfixv1.CommonEnvironmentSpec{
		Resources: breakfixv1.EnvironmentResourcesSpec{
			WorkspaceCPU:              "500m",
			WorkspaceMemory:           "256Mi",
			WorkspaceEphemeralStorage: "1Gi",
		},
	}

	resources, err := workspaceResourceRequirements(&spec)
	if err != nil {
		t.Fatalf("workspaceResourceRequirements: %v", err)
	}
	if got := resources.Requests[corev1.ResourceCPU]; got.String() != "500m" {
		t.Fatalf("expected cpu request 500m, got %s", got.String())
	}
	if got := resources.Limits[corev1.ResourceMemory]; got.String() != "256Mi" {
		t.Fatalf("expected memory limit 256Mi, got %s", got.String())
	}
	if got := resources.Limits[corev1.ResourceEphemeralStorage]; got.String() != "1Gi" {
		t.Fatalf("expected ephemeral storage limit 1Gi, got %s", got.String())
	}
}

func TestCleanupPolicyDefaultsCanBeOverridden(t *testing.T) {
	yes := true
	drain := int64(45)
	destroy := int64(120)

	var empty breakfixv1.CommonEnvironmentSpec
	if !empty.AutoDestroyAfterIdleOr(true) {
		t.Fatalf("expected default idle cleanup to remain true")
	}
	if empty.ForceCleanupOnFailureOr(false) {
		t.Fatalf("expected failure cleanup default to remain false")
	}
	if got := empty.DrainGracePeriodOr(30 * time.Second); got != 30*time.Second {
		t.Fatalf("expected fallback drain grace to remain 30s, got %s", got)
	}
	if got := empty.DestroyTimeoutOr(90 * time.Second); got != 90*time.Second {
		t.Fatalf("expected fallback destroy timeout to remain 90s, got %s", got)
	}

	spec := breakfixv1.CommonEnvironmentSpec{
		Timeouts: breakfixv1.EnvironmentTimeoutsSpec{
			DrainGracePeriodSeconds: &drain,
			DestroyTimeoutSeconds:   &destroy,
		},
		CleanupPolicy: breakfixv1.CleanupPolicySpec{
			AutoDestroyAfterIdle:  &yes,
			ForceCleanupOnFailure: &yes,
		},
	}
	if !spec.AutoDestroyAfterIdleOr(false) {
		t.Fatalf("expected idle cleanup override to be true")
	}
	if !spec.ForceCleanupOnFailureOr(false) {
		t.Fatalf("expected failure cleanup override to be true")
	}
	if got := spec.DrainGracePeriodOr(10 * time.Second); got != 45*time.Second {
		t.Fatalf("expected drain grace override to be 45s, got %s", got)
	}
	if got := spec.DestroyTimeoutOr(10 * time.Second); got != 120*time.Second {
		t.Fatalf("expected destroy timeout override to be 120s, got %s", got)
	}
}

func ptrMetaTime(t time.Time) *metav1.Time {
	mt := metav1.NewTime(t)
	return &mt
}
