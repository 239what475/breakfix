package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type commonServerEnvironment interface {
	CommonSpec() *breakfixv1.CommonEnvironmentSpec
}

func (h *Handler) newCommonEnvironmentSpec(userID string, challengeEntry *challenge.Entry) (breakfixv1.CommonEnvironmentSpec, error) {
	if challengeEntry == nil {
		return breakfixv1.CommonEnvironmentSpec{}, fmt.Errorf("challenge is required")
	}
	runtime := challenge.NormalizeRuntime(challengeEntry.Runtime)
	if runtime != challenge.RuntimeContainer && runtime != challenge.RuntimeVCluster {
		return breakfixv1.CommonEnvironmentSpec{}, fmt.Errorf("challenge %q has unsupported runtime %q", challengeEntry.ID, challengeEntry.Runtime)
	}
	if strings.TrimSpace(challengeEntry.Revision) == "" {
		return breakfixv1.CommonEnvironmentSpec{}, fmt.Errorf("challenge %q has no published revision", challengeEntry.ID)
	}
	checkpointIDs := make([]string, 0, len(challengeEntry.Checkpoints))
	seen := make(map[string]struct{}, len(challengeEntry.Checkpoints))
	for _, checkpoint := range challengeEntry.Checkpoints {
		id := strings.TrimSpace(checkpoint.ID)
		if id == "" {
			return breakfixv1.CommonEnvironmentSpec{}, fmt.Errorf("challenge %q has an empty checkpoint id", challengeEntry.ID)
		}
		if _, exists := seen[id]; exists {
			return breakfixv1.CommonEnvironmentSpec{}, fmt.Errorf("challenge %q has duplicate checkpoint id %q", challengeEntry.ID, id)
		}
		seen[id] = struct{}{}
		checkpointIDs = append(checkpointIDs, id)
	}
	if len(checkpointIDs) == 0 {
		return breakfixv1.CommonEnvironmentSpec{}, fmt.Errorf("challenge %q has no checkpoints", challengeEntry.ID)
	}

	idleTTLSeconds := int64(h.cooldownMin * 60)
	activityAt := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	return breakfixv1.CommonEnvironmentSpec{
		ChallengeRef:      challengeEntry.ID,
		ChallengeRevision: challengeEntry.Revision,
		UserRef:           userID,
		Runtime:           runtime,
		Image:             challengeEntry.Image,
		CheckpointIDs:     checkpointIDs,
		ActivityAt:        &activityAt,
		Timeouts: breakfixv1.EnvironmentTimeoutsSpec{
			IdleTTLSeconds: &idleTTLSeconds,
		},
	}, nil
}

type environmentRuntimeAdapter struct {
	runtime         string
	readyTimeout    func() int64
	list            func(context.Context, string) ([]activeEnvironment, error)
	get             func(context.Context, string) (*activeEnvironment, error)
	create          func(context.Context, *db.User, *challenge.Entry) (string, error)
	updateSpec      func(context.Context, string, func(*breakfixv1.CommonEnvironmentSpec)) error
	requestDeletion func(context.Context, string) error
}

func (a *environmentRuntimeAdapter) readyTimeoutDuration() int64 {
	if a.readyTimeout == nil {
		return 60
	}
	return a.readyTimeout()
}

func (a *environmentRuntimeAdapter) renewActivity(ctx context.Context, name string, activityAt metav1.Time) error {
	if a.updateSpec == nil {
		return fmt.Errorf("runtime %q does not support activity updates", a.runtime)
	}
	return a.updateSpec(ctx, name, func(spec *breakfixv1.CommonEnvironmentSpec) {
		if spec == nil {
			return
		}
		activityAt = metav1.NewTime(activityAt.UTC().Truncate(time.Second))
		if spec.ActivityAt == nil || activityAt.After(spec.ActivityAt.Time) {
			next := activityAt
			spec.ActivityAt = &next
		}
	})
}

func collectActiveEnvironments[T any](items []T, toActive func(T) *activeEnvironment) []activeEnvironment {
	result := make([]activeEnvironment, 0, len(items))
	for _, item := range items {
		result = append(result, *toActive(item))
	}
	return result
}

func mutateEnvironmentSpec[T commonServerEnvironment](ctx context.Context, name string, get func(context.Context, string) (T, error), update func(context.Context, T) error, mutate func(*breakfixv1.CommonEnvironmentSpec)) error {
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
					if env.DeletionTimestamp == nil {
						items = append(items, &env)
					}
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
				spec, err := h.newCommonEnvironmentSpec(user.ID, challengeEntry)
				if err != nil {
					return "", err
				}
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
					Spec: spec,
				}
				if _, err := h.k8s.CreateContainerEnvironment(ctx, h.crdNamespace, env); err != nil {
					return "", fmt.Errorf("create container environment: %w", err)
				}
				return name, nil
			},
			updateSpec: func(ctx context.Context, name string, mutate func(*breakfixv1.CommonEnvironmentSpec)) error {
				return mutateEnvironmentSpec(ctx, name, getContainer, func(ctx context.Context, env *breakfixv1.ContainerEnvironment) error {
					_, err := h.k8s.UpdateContainerEnvironment(ctx, h.crdNamespace, env)
					return err
				}, mutate)
			},
			requestDeletion: func(ctx context.Context, name string) error {
				return h.k8s.DeleteContainerEnvironment(ctx, h.crdNamespace, name)
			},
		}, nil
	case challenge.RuntimeVCluster:
		getVCluster := func(ctx context.Context, name string) (*breakfixv1.VClusterEnvironment, error) {
			return h.k8s.GetVClusterEnvironment(ctx, h.crdNamespace, name)
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
					if env.DeletionTimestamp == nil {
						items = append(items, &env)
					}
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
				spec, err := h.newCommonEnvironmentSpec(user.ID, challengeEntry)
				if err != nil {
					return "", err
				}
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
					Spec: breakfixv1.VClusterEnvironmentSpec{CommonEnvironmentSpec: spec},
				}
				if _, err := h.k8s.CreateVClusterEnvironment(ctx, h.crdNamespace, env); err != nil {
					return "", fmt.Errorf("create vcluster environment: %w", err)
				}
				return name, nil
			},
			updateSpec: func(ctx context.Context, name string, mutate func(*breakfixv1.CommonEnvironmentSpec)) error {
				return mutateEnvironmentSpec(ctx, name, getVCluster, func(ctx context.Context, env *breakfixv1.VClusterEnvironment) error {
					_, err := h.k8s.UpdateVClusterEnvironment(ctx, h.crdNamespace, env)
					return err
				}, mutate)
			},
			requestDeletion: func(ctx context.Context, name string) error {
				return h.k8s.DeleteVClusterEnvironment(ctx, h.crdNamespace, name)
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported runtime %q", runtime)
	}
}

func nowActivity() metav1.Time {
	return metav1.NewTime(time.Now().UTC())
}
