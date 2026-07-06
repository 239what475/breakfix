package gateway

import (
	"context"
	"fmt"

	breakfixv1 "github.com/breakfix/breakfix/apis/breakfix/v1"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type environmentRuntimeAdapter struct {
	runtime      string
	readyTimeout func() int64
	list         func(context.Context, string) ([]activeEnvironment, error)
	get          func(context.Context, string) (*activeEnvironment, error)
	create       func(context.Context, *db.User, *challenge.Entry) (string, error)
	updateStatus func(context.Context, string, func(*breakfixv1.CommonEnvironmentStatus)) error
	updateSpec   func(context.Context, string, func(*breakfixv1.CommonEnvironmentSpec)) error
}

func (a *environmentRuntimeAdapter) readyTimeoutDuration() int64 {
	if a.readyTimeout == nil {
		return 60
	}
	return a.readyTimeout()
}

func (h *Handler) environmentRuntimeAdapter(runtime string) (*environmentRuntimeAdapter, error) {
	switch challenge.NormalizeRuntime(runtime) {
	case challenge.RuntimeContainer:
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
				result := make([]activeEnvironment, 0, len(envs.Items))
				for i := range envs.Items {
					env := envs.Items[i]
					if env.DeletionTimestamp != nil {
						continue
					}
					result = append(result, *environmentFromContainer(&env))
				}
				return result, nil
			},
			get: func(ctx context.Context, name string) (*activeEnvironment, error) {
				env, err := h.k8s.GetContainerEnvironment(ctx, h.crdNamespace, name)
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
				env, err := h.k8s.GetContainerEnvironment(ctx, h.crdNamespace, name)
				if err != nil {
					return err
				}
				mutate(&env.Status)
				_, err = h.k8s.UpdateContainerEnvironmentStatus(ctx, h.crdNamespace, env)
				return err
			},
			updateSpec: func(ctx context.Context, name string, mutate func(*breakfixv1.CommonEnvironmentSpec)) error {
				env, err := h.k8s.GetContainerEnvironment(ctx, h.crdNamespace, name)
				if err != nil {
					return err
				}
				mutate(&env.Spec)
				_, err = h.k8s.UpdateContainerEnvironment(ctx, h.crdNamespace, env)
				return err
			},
		}, nil
	case challenge.RuntimeVCluster:
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
				result := make([]activeEnvironment, 0, len(envs.Items))
				for i := range envs.Items {
					env := envs.Items[i]
					if env.DeletionTimestamp != nil {
						continue
					}
					result = append(result, *environmentFromVCluster(&env))
				}
				return result, nil
			},
			get: func(ctx context.Context, name string) (*activeEnvironment, error) {
				env, err := h.k8s.GetVClusterEnvironment(ctx, h.crdNamespace, name)
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
				env, err := h.k8s.GetVClusterEnvironment(ctx, h.crdNamespace, name)
				if err != nil {
					return err
				}
				mutate(&env.Status.CommonEnvironmentStatus)
				_, err = h.k8s.UpdateVClusterEnvironmentStatus(ctx, h.crdNamespace, env)
				return err
			},
			updateSpec: func(ctx context.Context, name string, mutate func(*breakfixv1.CommonEnvironmentSpec)) error {
				env, err := h.k8s.GetVClusterEnvironment(ctx, h.crdNamespace, name)
				if err != nil {
					return err
				}
				mutate(&env.Spec.CommonEnvironmentSpec)
				_, err = h.k8s.UpdateVClusterEnvironment(ctx, h.crdNamespace, env)
				return err
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported runtime %q", runtime)
	}
}
