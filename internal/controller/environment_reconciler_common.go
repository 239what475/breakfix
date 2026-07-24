package controller

import (
	"context"

	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type commonEnvironmentObject interface {
	client.Object
	CommonSpec() *breakfixv1.CommonEnvironmentSpec
	CommonStatus() *breakfixv1.CommonEnvironmentStatus
}

type commonEnvironmentRuntime interface {
	provision(context.Context, commonEnvironmentObject) (ctrl.Result, error)
	waitReady(context.Context, commonEnvironmentObject) (ctrl.Result, error)
	evaluateCheckpoints(context.Context, commonEnvironmentObject) (ctrl.Result, error)
	handleDraining(context.Context, commonEnvironmentObject) (ctrl.Result, error)
	cleanup(context.Context, commonEnvironmentObject) (ctrl.Result, error)
	finalCleanup(context.Context, commonEnvironmentObject) (ctrl.Result, error)
	requestDeletion(context.Context, commonEnvironmentObject) (ctrl.Result, error)
}

func reconcileCommonEnvironment(ctx context.Context, env commonEnvironmentObject, runtime commonEnvironmentRuntime) (ctrl.Result, error) {
	status := env.CommonStatus()
	if status.ObservedGeneration != env.GetGeneration() {
		status.ObservedGeneration = env.GetGeneration()
	}
	if env.GetDeletionTimestamp() != nil {
		return runtime.finalCleanup(ctx, env)
	}

	switch status.Phase {
	case "":
		return runtime.provision(ctx, env)
	case breakfixv1.EnvironmentPending, breakfixv1.EnvironmentProvisioning:
		return runtime.waitReady(ctx, env)
	case breakfixv1.EnvironmentReady:
		return runtime.evaluateCheckpoints(ctx, env)
	case breakfixv1.EnvironmentDraining:
		return runtime.evaluateCheckpoints(ctx, env)
	case breakfixv1.EnvironmentCompleted:
		return runtime.handleDraining(ctx, env)
	case breakfixv1.EnvironmentDestroyed:
		return runtime.requestDeletion(ctx, env)
	case breakfixv1.EnvironmentFailed:
		if env.CommonSpec().ForceCleanupOnFailureOr(false) {
			return runtime.requestDeletion(ctx, env)
		}
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}
