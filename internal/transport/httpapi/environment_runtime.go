package httpapi

import (
	"context"
	"fmt"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/content/scenario"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	nodeEnvironmentReadyTimeout = 5 * time.Minute
	vk8sEnvironmentReadyTimeout = 10 * time.Minute
	environmentCreateAttempts   = 3
)

type environmentRuntimeAdapter struct {
	runtime         string
	readyTimeout    time.Duration
	list            func(context.Context, string) ([]activeEnvironment, error)
	get             func(context.Context, string) (*activeEnvironment, error)
	create          func(context.Context, *postgres.User, environmentContentTarget) (string, error)
	requestReset    func(context.Context, string, types.UID) (int64, error)
	requestDeletion func(context.Context, string, types.UID) error
	updateLease     func(context.Context, string, metav1.Time) error
}

func (a *environmentRuntimeAdapter) renewActivity(ctx context.Context, name string, activityAt metav1.Time) error {
	if a == nil || a.updateLease == nil {
		return fmt.Errorf("environment runtime does not support activity updates")
	}
	return a.updateLease(ctx, name, activityAt)
}

func (h *Handler) environmentRuntimeAdapter(runtime string) (*environmentRuntimeAdapter, error) {
	provider := scenario.NormalizeRuntime(runtime)
	if provider != scenario.RuntimeNode && provider != scenario.RuntimeK8s {
		return nil, fmt.Errorf("unsupported runtime %q", runtime)
	}
	adapter := &environmentRuntimeAdapter{runtime: provider, readyTimeout: nodeEnvironmentReadyTimeout}
	if provider == scenario.RuntimeK8s {
		adapter.readyTimeout = vk8sEnvironmentReadyTimeout
	}
	adapter.list = func(ctx context.Context, selector string) ([]activeEnvironment, error) {
		items, err := h.k8s.ListRuntimeEnvironments(ctx, h.crdNamespace, selector)
		if err != nil {
			return nil, err
		}
		result := make([]activeEnvironment, 0, len(items.Items))
		for index := range items.Items {
			if items.Items[index].DeletionTimestamp != nil {
				continue
			}
			value := environmentFromRuntime(&items.Items[index])
			if value != nil {
				if value.Runtime == provider {
					result = append(result, *value)
				}
			}
		}
		return result, nil
	}
	adapter.get = func(ctx context.Context, name string) (*activeEnvironment, error) {
		item, err := h.k8s.GetRuntimeEnvironment(ctx, h.crdNamespace, name)
		if err != nil {
			return nil, err
		}
		value := environmentFromRuntime(item)
		if value != nil && value.Runtime == "" {
			value.Runtime = provider
		}
		return value, nil
	}
	adapter.create = func(ctx context.Context, user *postgres.User, target environmentContentTarget) (string, error) {
		if user == nil {
			return "", fmt.Errorf("runnable revision store is unavailable")
		}
		name := learningEnvironmentName(user.ID, target)
		now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
		environment := &runtimev2.RuntimeEnvironment{
			ObjectMeta: environmentObjectMeta(name, h.crdNamespace, user.ID, target.kind, target.id, target.revisionID, runtimev2.PurposeLearning),
			Spec: runtimev2.RuntimeEnvironmentSpec{
				Purpose: runtimev2.PurposeLearning, Lease: runtimev2.LeaseSpec{RenewedAt: now},
			},
		}
		if target.blank {
			environment.Spec.BlankRuntime = &runtimev2.BlankRuntimeSpec{Provider: scenario.NormalizeRuntime(target.runtime)}
		} else {
			if target.resolveBinding == nil {
				return "", fmt.Errorf("runnable revision store is unavailable")
			}
			reference, err := target.resolveBinding(ctx)
			if err != nil {
				return "", fmt.Errorf("resolve runnable revision for environment: %w", err)
			}
			environment.Spec.RunnableRevisionRef = runtimev2.RunnableRevisionReference{ID: reference.ID, Digest: reference.Digest}
		}
		if _, err := h.k8s.CreateRuntimeEnvironment(ctx, h.crdNamespace, environment); err != nil {
			return "", fmt.Errorf("create runtime environment: %w", err)
		}
		return name, nil
	}
	adapter.updateLease = func(ctx context.Context, name string, renewedAt metav1.Time) error {
		item, err := h.k8s.GetRuntimeEnvironment(ctx, h.crdNamespace, name)
		if err != nil {
			return err
		}
		renewedAt = metav1.NewTime(renewedAt.UTC().Truncate(time.Second))
		if !renewedAt.After(item.Spec.Lease.RenewedAt.Time) {
			return nil
		}
		item.Spec.Lease.RenewedAt = renewedAt
		_, err = h.k8s.UpdateRuntimeEnvironment(ctx, h.crdNamespace, item)
		return err
	}
	adapter.requestReset = func(ctx context.Context, name string, uid types.UID) (int64, error) {
		item, err := h.k8s.GetRuntimeEnvironment(ctx, h.crdNamespace, name)
		if err != nil {
			return 0, err
		}
		if uid == "" || item.UID != uid {
			return 0, fmt.Errorf("runtime environment UID no longer matches reset request")
		}
		if item.DeletionTimestamp != nil {
			return 0, fmt.Errorf("runtime environment is already deleting")
		}
		item.Spec.ResetNonce++
		updated, err := h.k8s.UpdateRuntimeEnvironment(ctx, h.crdNamespace, item)
		if err != nil {
			return 0, err
		}
		return updated.Spec.ResetNonce, nil
	}
	adapter.requestDeletion = func(ctx context.Context, name string, uid types.UID) error {
		if uid != "" {
			return h.k8s.DeleteRuntimeEnvironmentWithUID(ctx, h.crdNamespace, name, uid)
		}
		return h.k8s.DeleteRuntimeEnvironment(ctx, h.crdNamespace, name)
	}
	return adapter, nil
}

// learningEnvironmentName gives concurrent Start requests the same CRD name.
// A stable name makes the Kubernetes API the cross-server ownership fence,
// rather than relying on a process-local lock or a best-effort List then Create.
func learningEnvironmentName(userID string, target environmentContentTarget) string {
	if target.id == "" {
		return kubernetes.DNSLabelName("learning", userID)
	}
	return kubernetes.DNSLabelName("learning", userID, target.id, target.revisionID)
}

func environmentObjectMeta(name, namespace, userID, contentKind, contentID, contentRevision string, purpose runtimev2.EnvironmentPurpose) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name: name, Namespace: namespace,
		Labels: map[string]string{
			"breakfix.dev/user": userID, "breakfix.dev/content-kind": contentKind,
			"breakfix.dev/content-id": contentID, "breakfix.dev/content-revision": contentRevision,
			"breakfix.dev/purpose": string(purpose),
		},
	}
}

func nowActivity() metav1.Time {
	return metav1.NewTime(time.Now().UTC())
}
