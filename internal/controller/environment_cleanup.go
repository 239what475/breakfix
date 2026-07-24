package controller

import (
	"context"
	"log/slog"
	"strings"

	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

func cleanupStaleEnvironments(ctx context.Context, k8sClient *k8s.Client, crdNamespace string) error {
	containerEnvs, err := k8sClient.ListContainerEnvironments(ctx, crdNamespace, "")
	if err != nil {
		return err
	}
	vclusterEnvs, err := k8sClient.ListVClusterEnvironments(ctx, crdNamespace, "")
	if err != nil {
		return err
	}

	claimedNamespaces := map[string]struct{}{}
	for i := range containerEnvs.Items {
		env := &containerEnvs.Items[i]
		if ns := strings.TrimSpace(env.Status.Namespace); ns != "" {
			claimedNamespaces[ns] = struct{}{}
		}
		if staleCommonEnvironment(k8sClient, &env.Status) {
			slog.Info("cleanup deleting stale container environment", "name", env.Name, "namespace", env.Status.Namespace)
			_, _ = k8sClient.UpdateContainerEnvironmentStatus(ctx, crdNamespace, markContainerDestroyed(env))
		}
	}
	for i := range vclusterEnvs.Items {
		env := &vclusterEnvs.Items[i]
		if ns := strings.TrimSpace(env.Status.Namespace); ns != "" {
			claimedNamespaces[ns] = struct{}{}
		}
		if staleCommonEnvironment(k8sClient, &env.Status.CommonEnvironmentStatus) {
			slog.Info("cleanup deleting stale vcluster environment", "name", env.Name, "namespace", env.Status.Namespace)
			_, _ = k8sClient.UpdateVClusterEnvironmentStatus(ctx, crdNamespace, markVClusterDestroyed(env))
		}
	}

	namespaces, err := k8sClient.ListNamespaces("")
	if err != nil {
		return err
	}
	for _, ns := range namespaces {
		name := ns.Name
		if !strings.HasPrefix(name, "breakfix-u-") && !strings.HasPrefix(name, "breakfix-debug-") {
			continue
		}
		if _, ok := claimedNamespaces[name]; ok {
			continue
		}
		slog.Info("cleanup deleting orphan breakfix namespace", "namespace", name)
		_ = k8sClient.DeleteNamespace(name)
	}
	return nil
}

func staleCommonEnvironment(k8sClient *k8s.Client, status *breakfixv1.CommonEnvironmentStatus) bool {
	if status == nil {
		return false
	}
	if status.Phase == breakfixv1.EnvironmentCompleted || status.Phase == breakfixv1.EnvironmentDestroyed || status.Phase == breakfixv1.EnvironmentFailed {
		return false
	}
	ns := strings.TrimSpace(status.Namespace)
	podName := strings.TrimSpace(status.WorkspacePodName)
	if ns == "" || podName == "" {
		return false
	}
	pod, err := k8sClient.GetPod(ns, podName)
	if err != nil {
		return k8serrors.IsNotFound(err)
	}
	if pod == nil {
		return true
	}
	if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
		return true
	}
	if pod.Status.Phase == corev1.PodUnknown {
		return true
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != "challenge" {
			continue
		}
		if cs.Ready {
			return false
		}
		if waiting := cs.State.Waiting; waiting != nil {
			switch waiting.Reason {
			case "CreateContainerConfigError", "CreateContainerError", "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "RunContainerError":
				return true
			}
		}
		if cs.State.Terminated != nil || cs.LastTerminationState.Terminated != nil {
			return true
		}
	}
	return false
}

func markContainerDestroyed(env *breakfixv1.ContainerEnvironment) *breakfixv1.ContainerEnvironment {
	copy := env.DeepCopy()
	markEnvironmentDestroyed(&copy.Status, "CleanupMarkedStale", "cleanup marked stale environment destroyed")
	return copy
}

func markVClusterDestroyed(env *breakfixv1.VClusterEnvironment) *breakfixv1.VClusterEnvironment {
	copy := env.DeepCopy()
	markEnvironmentDestroyed(&copy.Status.CommonEnvironmentStatus, "CleanupMarkedStale", "cleanup marked stale environment destroyed")
	return copy
}
