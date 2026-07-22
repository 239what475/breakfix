package gateway

import (
	"context"
	"fmt"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type commonGatewayEnvironment interface {
	GetGeneration() int64
	CommonSpec() *breakfixv1.CommonEnvironmentSpec
	CommonStatus() *breakfixv1.CommonEnvironmentStatus
}

type environmentRuntimeAdapter struct {
	runtime             string
	readyTimeout        func() int64
	list                func(context.Context, string) ([]activeEnvironment, error)
	get                 func(context.Context, string) (*activeEnvironment, error)
	create              func(context.Context, *db.User, *challenge.Entry) (string, error)
	updateStatus        func(context.Context, string, func(*breakfixv1.CommonEnvironmentStatus)) error
	updateSessionStatus func(context.Context, string, func(*breakfixv1.CommonEnvironmentSpec, *breakfixv1.CommonEnvironmentStatus)) error
	updateSpec          func(context.Context, string, func(*breakfixv1.CommonEnvironmentSpec)) error
	requestDeletion     func(context.Context, string) error
}

func (a *environmentRuntimeAdapter) readyTimeoutDuration() int64 {
	if a.readyTimeout == nil {
		return 60
	}
	return a.readyTimeout()
}

func (a *environmentRuntimeAdapter) markDraining(ctx context.Context, name string, expiresAt metav1.Time) error {
	return a.updateSessionStatus(ctx, name, func(spec *breakfixv1.CommonEnvironmentSpec, status *breakfixv1.CommonEnvironmentStatus) {
		if spec.Submit || status.Phase != breakfixv1.EnvironmentReady || status.SubmitResult != nil {
			return
		}
		setGatewayEnvironmentDraining(status, expiresAt, "SessionDetached", "session disconnected, environment draining")
	})
}

func (a *environmentRuntimeAdapter) renewLease(ctx context.Context, name string, expiresAt metav1.Time) error {
	return a.updateSessionStatus(ctx, name, func(spec *breakfixv1.CommonEnvironmentSpec, status *breakfixv1.CommonEnvironmentStatus) {
		if spec.Submit || status.SubmitResult != nil {
			return
		}
		if status.Phase == breakfixv1.EnvironmentReady || status.Phase == breakfixv1.EnvironmentDraining {
			setGatewayEnvironmentReady(status, expiresAt, "SessionActive", "environment lease renewed")
		}
	})
}

func collectActiveEnvironments[T commonGatewayEnvironment](items []T, toActive func(T) *activeEnvironment) []activeEnvironment {
	result := make([]activeEnvironment, 0, len(items))
	for _, item := range items {
		result = append(result, *toActive(item))
	}
	return result
}

func mutateEnvironmentStatus[T commonGatewayEnvironment](ctx context.Context, name string, get func(context.Context, string) (T, error), update func(context.Context, T) error, mutate func(*breakfixv1.CommonEnvironmentStatus)) error {
	env, err := get(ctx, name)
	if err != nil {
		return err
	}
	status := env.CommonStatus()
	status.ObservedGeneration = env.GetGeneration()
	mutate(status)
	return update(ctx, env)
}

func mutateEnvironmentSessionStatus[T commonGatewayEnvironment](ctx context.Context, name string, get func(context.Context, string) (T, error), update func(context.Context, T) error, mutate func(*breakfixv1.CommonEnvironmentSpec, *breakfixv1.CommonEnvironmentStatus)) error {
	env, err := get(ctx, name)
	if err != nil {
		return err
	}
	status := env.CommonStatus()
	status.ObservedGeneration = env.GetGeneration()
	mutate(env.CommonSpec(), status)
	return update(ctx, env)
}

func mutateEnvironmentSpec[T commonGatewayEnvironment](ctx context.Context, name string, get func(context.Context, string) (T, error), update func(context.Context, T) error, mutate func(*breakfixv1.CommonEnvironmentSpec)) error {
	env, err := get(ctx, name)
	if err != nil {
		return err
	}
	mutate(env.CommonSpec())
	return update(ctx, env)
}

func (h *Handler) environmentRuntimeAdapter(runtime string) (*environmentRuntimeAdapter, error) {
	switch challenge.NormalizeRuntime(runtime) {
	case challenge.RuntimeContainer:
		getContainer := func(ctx context.Context, name string) (*breakfixv1.ContainerEnvironment, error) {
			return h.k8s.GetContainerEnvironment(ctx, h.crdNamespace, name)
		}
		updateContainer := func(ctx context.Context, env *breakfixv1.ContainerEnvironment) error {
			_, err := h.k8s.UpdateContainerEnvironment(ctx, h.crdNamespace, env)
			return err
		}
		updateContainerStatus := func(ctx context.Context, env *breakfixv1.ContainerEnvironment) error {
			_, err := h.k8s.UpdateContainerEnvironmentStatus(ctx, h.crdNamespace, env)
			return err
		}
		return &environmentRuntimeAdapter{
			runtime: challenge.RuntimeContainer,
			readyTimeout: func() int64 {
				return 60
			},
			list: func(ctx context.Context, selector string) ([]activeEnvironment, error) {
				envs, err := h.k8s.ListContainerEnvironments(ctx, h.crdNamespace, selector)
				if err != nil {
					return nil, err
				}
				items := make([]*breakfixv1.ContainerEnvironment, 0, len(envs.Items))
				for i := range envs.Items {
					env := envs.Items[i]
					if env.DeletionTimestamp != nil {
						continue
					}
					items = append(items, &env)
				}
				return collectActiveEnvironments(items, environmentFromContainer), nil
			},
			get: func(ctx context.Context, name string) (*activeEnvironment, error) {
				env, err := getContainer(ctx, name)
				if err != nil {
					return nil, err
				}
				return environmentFromContainer(env), nil
			},
			create: func(ctx context.Context, user *db.User, challengeEntry *challenge.Entry) (string, error) {
				name := k8s.RandomID()
				env := &breakfixv1.ContainerEnvironment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: h.crdNamespace,
						Labels: map[string]string{
							"breakfix.dev/user":      user.ID,
							"breakfix.dev/challenge": challengeEntry.ID,
						},
					},
					Spec: breakfixv1.CommonEnvironmentSpec{
						ChallengeRef: challengeEntry.ID,
						UserRef:      user.ID,
						Image:        challengeEntry.Image,
					},
				}
				if _, err := h.k8s.CreateContainerEnvironment(ctx, h.crdNamespace, env); err != nil {
					return "", fmt.Errorf("create container environment: %w", err)
				}
				return name, nil
			},
			updateStatus: func(ctx context.Context, name string, mutate func(*breakfixv1.CommonEnvironmentStatus)) error {
				return mutateEnvironmentStatus(ctx, name, getContainer, updateContainerStatus, mutate)
			},
			updateSessionStatus: func(ctx context.Context, name string, mutate func(*breakfixv1.CommonEnvironmentSpec, *breakfixv1.CommonEnvironmentStatus)) error {
				return mutateEnvironmentSessionStatus(ctx, name, getContainer, updateContainerStatus, mutate)
			},
			updateSpec: func(ctx context.Context, name string, mutate func(*breakfixv1.CommonEnvironmentSpec)) error {
				return mutateEnvironmentSpec(ctx, name, getContainer, updateContainer, mutate)
			},
			requestDeletion: func(ctx context.Context, name string) error {
				return h.k8s.DeleteContainerEnvironment(ctx, h.crdNamespace, name)
			},
		}, nil
	case challenge.RuntimeVCluster:
		getVCluster := func(ctx context.Context, name string) (*breakfixv1.VClusterEnvironment, error) {
			return h.k8s.GetVClusterEnvironment(ctx, h.crdNamespace, name)
		}
		updateVCluster := func(ctx context.Context, env *breakfixv1.VClusterEnvironment) error {
			_, err := h.k8s.UpdateVClusterEnvironment(ctx, h.crdNamespace, env)
			return err
		}
		updateVClusterStatus := func(ctx context.Context, env *breakfixv1.VClusterEnvironment) error {
			_, err := h.k8s.UpdateVClusterEnvironmentStatus(ctx, h.crdNamespace, env)
			return err
		}
		return &environmentRuntimeAdapter{
			runtime: challenge.RuntimeVCluster,
			readyTimeout: func() int64 {
				return 300
			},
			list: func(ctx context.Context, selector string) ([]activeEnvironment, error) {
				envs, err := h.k8s.ListVClusterEnvironments(ctx, h.crdNamespace, selector)
				if err != nil {
					return nil, err
				}
				items := make([]*breakfixv1.VClusterEnvironment, 0, len(envs.Items))
				for i := range envs.Items {
					env := envs.Items[i]
					if env.DeletionTimestamp != nil {
						continue
					}
					items = append(items, &env)
				}
				return collectActiveEnvironments(items, environmentFromVCluster), nil
			},
			get: func(ctx context.Context, name string) (*activeEnvironment, error) {
				env, err := getVCluster(ctx, name)
				if err != nil {
					return nil, err
				}
				return environmentFromVCluster(env), nil
			},
			create: func(ctx context.Context, user *db.User, challengeEntry *challenge.Entry) (string, error) {
				name := k8s.RandomID()
				env := &breakfixv1.VClusterEnvironment{
					ObjectMeta: metav1.ObjectMeta{
						Name:      name,
						Namespace: h.crdNamespace,
						Labels: map[string]string{
							"breakfix.dev/user":      user.ID,
							"breakfix.dev/challenge": challengeEntry.ID,
						},
					},
					Spec: breakfixv1.VClusterEnvironmentSpec{
						CommonEnvironmentSpec: breakfixv1.CommonEnvironmentSpec{
							ChallengeRef: challengeEntry.ID,
							UserRef:      user.ID,
							Image:        challengeEntry.Image,
						},
					},
				}
				if _, err := h.k8s.CreateVClusterEnvironment(ctx, h.crdNamespace, env); err != nil {
					return "", fmt.Errorf("create vcluster environment: %w", err)
				}
				return name, nil
			},
			updateStatus: func(ctx context.Context, name string, mutate func(*breakfixv1.CommonEnvironmentStatus)) error {
				return mutateEnvironmentStatus(ctx, name, getVCluster, updateVClusterStatus, mutate)
			},
			updateSessionStatus: func(ctx context.Context, name string, mutate func(*breakfixv1.CommonEnvironmentSpec, *breakfixv1.CommonEnvironmentStatus)) error {
				return mutateEnvironmentSessionStatus(ctx, name, getVCluster, updateVClusterStatus, mutate)
			},
			updateSpec: func(ctx context.Context, name string, mutate func(*breakfixv1.CommonEnvironmentSpec)) error {
				return mutateEnvironmentSpec(ctx, name, getVCluster, updateVCluster, mutate)
			},
			requestDeletion: func(ctx context.Context, name string) error {
				return h.k8s.DeleteVClusterEnvironment(ctx, h.crdNamespace, name)
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported runtime %q", runtime)
	}
}

func setGatewayEnvironmentDraining(status *breakfixv1.CommonEnvironmentStatus, expiresAt metav1.Time, reason, msg string) {
	status.Phase = breakfixv1.EnvironmentDraining
	status.Reason = reason
	status.Message = msg
	status.ExpiresAt = &expiresAt
	setGatewayCondition(status, breakfixv1.ConditionDraining, metav1.ConditionTrue, reason, msg)
	setGatewayCondition(status, breakfixv1.ConditionReady, metav1.ConditionFalse, reason, msg)
}

func setGatewayEnvironmentReady(status *breakfixv1.CommonEnvironmentStatus, expiresAt metav1.Time, reason, msg string) {
	status.Phase = breakfixv1.EnvironmentReady
	status.Reason = reason
	status.Message = msg
	status.ExpiresAt = &expiresAt
	status.LastError = nil
	setGatewayCondition(status, breakfixv1.ConditionReady, metav1.ConditionTrue, reason, msg)
	setGatewayCondition(status, breakfixv1.ConditionDraining, metav1.ConditionFalse, "", "")
	setGatewayCondition(status, breakfixv1.ConditionFailed, metav1.ConditionFalse, "", "")
}

func setGatewayCondition(status *breakfixv1.CommonEnvironmentStatus, conditionType string, conditionStatus metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             conditionStatus,
		Reason:             truncate(reason, 1024),
		Message:            truncate(msg, 4000),
		ObservedGeneration: status.ObservedGeneration,
		LastTransitionTime: metav1.NewTime(time.Now()),
	})
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
