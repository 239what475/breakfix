package controller

import (
	"context"
	"testing"

	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

type fakeCommonRuntime struct{}

func (f *fakeCommonRuntime) provision(context.Context, commonEnvironmentObject) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

func (f *fakeCommonRuntime) waitReady(context.Context, commonEnvironmentObject) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

func (f *fakeCommonRuntime) evaluateCheckpoints(context.Context, commonEnvironmentObject) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

func (f *fakeCommonRuntime) handleDraining(context.Context, commonEnvironmentObject) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

func (f *fakeCommonRuntime) cleanup(context.Context, commonEnvironmentObject) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

func (f *fakeCommonRuntime) finalCleanup(context.Context, commonEnvironmentObject) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

func TestReconcileCommonEnvironmentRetainsCompletedEnvironmentForIdleCleanup(t *testing.T) {
	env := &breakfixv1.ContainerEnvironment{
		Status: breakfixv1.CommonEnvironmentStatus{
			Phase: breakfixv1.EnvironmentCompleted,
		},
	}
	runtime := &fakeCommonRuntime{}

	if _, err := reconcileCommonEnvironment(context.Background(), env, runtime); err != nil {
		t.Fatalf("reconcileCommonEnvironment: %v", err)
	}
}

func TestReconcileCommonEnvironmentKeepsFailedByDefault(t *testing.T) {
	env := &breakfixv1.ContainerEnvironment{
		Status: breakfixv1.CommonEnvironmentStatus{
			Phase: breakfixv1.EnvironmentFailed,
		},
	}
	runtime := &fakeCommonRuntime{}

	if _, err := reconcileCommonEnvironment(context.Background(), env, runtime); err != nil {
		t.Fatalf("reconcileCommonEnvironment: %v", err)
	}
}

func TestReconcileCommonEnvironmentRetainsForcedFailureForServerProjection(t *testing.T) {
	yes := true
	env := &breakfixv1.ContainerEnvironment{
		Spec: breakfixv1.CommonEnvironmentSpec{
			CleanupPolicy: breakfixv1.CleanupPolicySpec{
				ForceCleanupOnFailure: &yes,
			},
		},
		Status: breakfixv1.CommonEnvironmentStatus{
			Phase: breakfixv1.EnvironmentFailed,
		},
	}
	runtime := &fakeCommonRuntime{}

	if _, err := reconcileCommonEnvironment(context.Background(), env, runtime); err != nil {
		t.Fatalf("reconcileCommonEnvironment: %v", err)
	}
}
