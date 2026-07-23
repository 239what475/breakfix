package gateway

import (
	"context"
	"testing"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMarkDrainingSkipsCompletedEnvironment(t *testing.T) {
	spec := breakfixv1.CommonEnvironmentSpec{}
	status := breakfixv1.CommonEnvironmentStatus{Phase: breakfixv1.EnvironmentCompleted}
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
	if status.Phase != breakfixv1.EnvironmentCompleted {
		t.Fatalf("expected phase to remain Completed, got %s", status.Phase)
	}
	if status.ExpiresAt != nil {
		t.Fatalf("expected expiresAt to remain nil, got %v", status.ExpiresAt)
	}
}

func TestMarkDrainingSetsConditionsAndObservedGeneration(t *testing.T) {
	spec := breakfixv1.CommonEnvironmentSpec{}
	env := &breakfixv1.ContainerEnvironment{}
	env.Generation = 7
	env.Status.Phase = breakfixv1.EnvironmentReady

	adapter := &environmentRuntimeAdapter{
		updateSessionStatus: func(_ context.Context, _ string, mutate func(*breakfixv1.CommonEnvironmentSpec, *breakfixv1.CommonEnvironmentStatus)) error {
			env.Status.ObservedGeneration = env.GetGeneration()
			mutate(&spec, &env.Status)
			return nil
		},
	}

	expiresAt := metav1.NewTime(time.Now().Add(5 * time.Minute))
	if err := adapter.markDraining(context.Background(), "demo", expiresAt); err != nil {
		t.Fatal(err)
	}
	if env.Status.Phase != breakfixv1.EnvironmentDraining {
		t.Fatalf("expected phase Draining, got %s", env.Status.Phase)
	}
	if env.Status.ObservedGeneration != 7 {
		t.Fatalf("expected observedGeneration 7, got %d", env.Status.ObservedGeneration)
	}
	if cond := findGatewayCondition(env.Status.Conditions, breakfixv1.ConditionDraining); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected Draining condition true, got %#v", cond)
	}
	if cond := findGatewayCondition(env.Status.Conditions, breakfixv1.ConditionReady); cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected Ready condition false, got %#v", cond)
	}
}

func TestRenewLeaseSkipsCompletedEnvironment(t *testing.T) {
	spec := breakfixv1.CommonEnvironmentSpec{}
	status := breakfixv1.CommonEnvironmentStatus{
		Phase: breakfixv1.EnvironmentCompleted,
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
	if status.Phase != breakfixv1.EnvironmentCompleted {
		t.Fatalf("expected phase to remain Completed, got %s", status.Phase)
	}
	if status.ExpiresAt != nil {
		t.Fatalf("expected expiresAt to remain nil, got %v", status.ExpiresAt)
	}
}

func TestRenewLeaseRestoresReadyConditions(t *testing.T) {
	spec := breakfixv1.CommonEnvironmentSpec{}
	status := breakfixv1.CommonEnvironmentStatus{
		Phase:              breakfixv1.EnvironmentDraining,
		ObservedGeneration: 9,
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
	if status.Phase != breakfixv1.EnvironmentReady {
		t.Fatalf("expected phase Ready, got %s", status.Phase)
	}
	if cond := findGatewayCondition(status.Conditions, breakfixv1.ConditionReady); cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected Ready condition true, got %#v", cond)
	}
	if cond := findGatewayCondition(status.Conditions, breakfixv1.ConditionDraining); cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected Draining condition false, got %#v", cond)
	}
}

func TestMutateEnvironmentSessionStatusSetsObservedGeneration(t *testing.T) {
	env := &breakfixv1.ContainerEnvironment{}
	env.Generation = 11

	if err := mutateEnvironmentSessionStatus(context.Background(), "demo",
		func(_ context.Context, _ string) (*breakfixv1.ContainerEnvironment, error) {
			return env, nil
		},
		func(_ context.Context, updated *breakfixv1.ContainerEnvironment) error {
			env = updated
			return nil
		},
		func(spec *breakfixv1.CommonEnvironmentSpec, status *breakfixv1.CommonEnvironmentStatus) {
			status.Reason = "Updated"
			_ = spec
		},
	); err != nil {
		t.Fatal(err)
	}

	if env.Status.ObservedGeneration != 11 {
		t.Fatalf("expected observedGeneration 11, got %d", env.Status.ObservedGeneration)
	}
}

func findGatewayCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}
