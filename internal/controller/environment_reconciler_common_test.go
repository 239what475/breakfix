package controller

import (
	"context"
	"testing"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

type fakeCommonRuntime struct {
	requestDeletionCalls int
}

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

func (f *fakeCommonRuntime) requestDeletion(context.Context, commonEnvironmentObject) (ctrl.Result, error) {
	f.requestDeletionCalls++
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
	if runtime.requestDeletionCalls != 0 {
		t.Fatalf("expected completed environment to be retained for idle cleanup, got %d delete calls", runtime.requestDeletionCalls)
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
	if runtime.requestDeletionCalls != 0 {
		t.Fatalf("expected failed environment to be retained, got %d delete calls", runtime.requestDeletionCalls)
	}
}

func TestReconcileCommonEnvironmentDeletesFailedWhenForced(t *testing.T) {
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
	if runtime.requestDeletionCalls != 1 {
		t.Fatalf("expected failed environment to be deleted when forced, got %d delete calls", runtime.requestDeletionCalls)
	}
}
