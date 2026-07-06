package gateway

import (
	"context"
	"testing"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMarkDrainingSkipsSubmittedEnvironment(t *testing.T) {
	spec := breakfixv1.CommonEnvironmentSpec{Submit: true}
	status := breakfixv1.CommonEnvironmentStatus{Phase: breakfixv1.EnvironmentReady}
	adapter := &environmentRuntimeAdapter{
		updateSessionStatus: func(_ context.Context, _ string, mutate func(*breakfixv1.CommonEnvironmentSpec, *breakfixv1.CommonEnvironmentStatus)) error {
			mutate(&spec, &status)
			return nil
		},
	}

	expiresAt := metav1.NewTime(time.Now().Add(5 * time.Minute))
	if err := adapter.markDraining(context.Background(), "demo", expiresAt); err != nil {
		t.Fatal(err)
	}
	if status.Phase != breakfixv1.EnvironmentReady {
		t.Fatalf("expected phase to remain Ready, got %s", status.Phase)
	}
	if status.ExpiresAt != nil {
		t.Fatalf("expected expiresAt to remain nil, got %v", status.ExpiresAt)
	}
}

func TestRenewLeaseSkipsWhenSubmitResultAlreadyPresent(t *testing.T) {
	spec := breakfixv1.CommonEnvironmentSpec{}
	status := breakfixv1.CommonEnvironmentStatus{
		Phase: breakfixv1.EnvironmentDraining,
		SubmitResult: &breakfixv1.SubmitResult{
			Passed: true,
		},
	}
	adapter := &environmentRuntimeAdapter{
		updateSessionStatus: func(_ context.Context, _ string, mutate func(*breakfixv1.CommonEnvironmentSpec, *breakfixv1.CommonEnvironmentStatus)) error {
			mutate(&spec, &status)
			return nil
		},
	}

	expiresAt := metav1.NewTime(time.Now().Add(5 * time.Minute))
	if err := adapter.renewLease(context.Background(), "demo", expiresAt); err != nil {
		t.Fatal(err)
	}
	if status.Phase != breakfixv1.EnvironmentDraining {
		t.Fatalf("expected phase to remain Draining, got %s", status.Phase)
	}
	if status.ExpiresAt != nil {
		t.Fatalf("expected expiresAt to remain nil, got %v", status.ExpiresAt)
	}
}
