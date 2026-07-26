package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/db"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type activeEnvironment struct {
	UID                 string
	Runtime             string
	Name                string
	ChallengeRef        string
	Namespace           string
	WorkspacePod        string
	Phase               breakfixv1.EnvironmentPhase
	ExpiresAt           *metav1.Time
	Checkpoints         *breakfixv1.CheckpointStatus
	Message             string
	Reason              string
	LastError           *breakfixv1.EnvironmentErrorStatus
	ReadyTimeoutSeconds *int64
	IdleTTLSeconds      *int64
	DrainGraceSeconds   *int64
}

func environmentFromContainer(env *breakfixv1.ContainerEnvironment) *activeEnvironment {
	if env == nil {
		return nil
	}
	runtime := challenge.NormalizeRuntime(env.Spec.Runtime)
	if strings.TrimSpace(env.Spec.Runtime) == "" {
		runtime = challenge.RuntimeContainer
	}
	return &activeEnvironment{
		UID:                 string(env.UID),
		Runtime:             runtime,
		Name:                env.Name,
		ChallengeRef:        env.Spec.ChallengeRef,
		Namespace:           env.Status.Namespace,
		WorkspacePod:        env.Status.WorkspacePodName,
		Phase:               env.Status.Phase,
		ExpiresAt:           env.Status.ExpiresAt,
		Checkpoints:         env.Status.Checkpoints,
		Message:             env.Status.Message,
		Reason:              env.Status.Reason,
		LastError:           env.Status.LastError,
		ReadyTimeoutSeconds: env.Spec.Timeouts.ReadyTimeoutSeconds,
		IdleTTLSeconds:      env.Spec.Timeouts.IdleTTLSeconds,
		DrainGraceSeconds:   env.Spec.Timeouts.DrainGracePeriodSeconds,
	}
}

func environmentFromVCluster(env *breakfixv1.VClusterEnvironment) *activeEnvironment {
	if env == nil {
		return nil
	}
	runtime := challenge.NormalizeRuntime(env.Spec.Runtime)
	if strings.TrimSpace(env.Spec.Runtime) == "" {
		runtime = challenge.RuntimeVCluster
	}
	return &activeEnvironment{
		UID:                 string(env.UID),
		Runtime:             runtime,
		Name:                env.Name,
		ChallengeRef:        env.Spec.ChallengeRef,
		Namespace:           env.Status.Namespace,
		WorkspacePod:        env.Status.WorkspacePodName,
		Phase:               env.Status.Phase,
		ExpiresAt:           env.Status.ExpiresAt,
		Checkpoints:         env.Status.Checkpoints,
		Message:             env.Status.Message,
		Reason:              env.Status.Reason,
		LastError:           env.Status.LastError,
		ReadyTimeoutSeconds: env.Spec.Timeouts.ReadyTimeoutSeconds,
		IdleTTLSeconds:      env.Spec.Timeouts.IdleTTLSeconds,
		DrainGraceSeconds:   env.Spec.Timeouts.DrainGracePeriodSeconds,
	}
}

func (h *Handler) listActiveEnvironments(ctx context.Context, userID string) ([]activeEnvironment, error) {
	selector := fmt.Sprintf("breakfix.dev/user=%s", userID)
	result := make([]activeEnvironment, 0, 4)
	for _, runtime := range []string{challenge.RuntimeContainer, challenge.RuntimeVCluster} {
		adapter, err := h.environmentRuntimeAdapter(runtime)
		if err != nil {
			return nil, err
		}
		envs, err := adapter.list(ctx, selector)
		if err != nil {
			return nil, err
		}
		result = append(result, envs...)
	}
	return result, nil
}

func (h *Handler) findEnvironment(ctx context.Context, userID string, challengeEntry *challenge.Entry) (*activeEnvironment, error) {
	selector := fmt.Sprintf("breakfix.dev/user=%s,breakfix.dev/challenge=%s", userID, challengeEntry.ID)
	adapter, err := h.environmentRuntimeAdapter(challengeEntry.Runtime)
	if err != nil {
		return nil, err
	}
	envs, err := adapter.list(ctx, selector)
	if err != nil {
		return nil, err
	}
	for i := range envs {
		env := envs[i]
		if isLiveEnvironmentPhase(env.Phase) {
			return &env, nil
		}
	}
	return nil, errors.New("no active environment")
}

func (h *Handler) findActiveEnvironmentByUID(ctx context.Context, userID, environmentUID string) (*activeEnvironment, error) {
	environments, err := h.listActiveEnvironments(ctx, userID)
	if err != nil {
		return nil, err
	}
	for index := range environments {
		environment := environments[index]
		if environment.UID == environmentUID && isLiveEnvironmentPhase(environment.Phase) {
			return &environment, nil
		}
	}
	return nil, errors.New("no active environment for assistant run")
}

func (h *Handler) findProgressEnvironment(ctx context.Context, userID string, challengeEntry *challenge.Entry) (*activeEnvironment, error) {
	selector := fmt.Sprintf("breakfix.dev/user=%s,breakfix.dev/challenge=%s", userID, challengeEntry.ID)
	adapter, err := h.environmentRuntimeAdapter(challengeEntry.Runtime)
	if err != nil {
		return nil, err
	}
	envs, err := adapter.list(ctx, selector)
	if err != nil {
		return nil, err
	}
	for i := range envs {
		env := envs[i]
		if isLiveEnvironmentPhase(env.Phase) {
			return &env, nil
		}
	}
	for i := range envs {
		env := envs[i]
		if env.Phase == breakfixv1.EnvironmentCompleted {
			return &env, nil
		}
	}
	return nil, errors.New("no environment with checkpoint status")
}

func (h *Handler) createEnvironment(ctx context.Context, user *db.User, challengeEntry *challenge.Entry) (*activeEnvironment, error) {
	adapter, err := h.environmentRuntimeAdapter(challengeEntry.Runtime)
	if err != nil {
		return nil, err
	}
	name, err := adapter.create(ctx, user, challengeEntry)
	if err != nil {
		return nil, err
	}
	return h.waitEnvironmentReady(ctx, adapter.runtime, name, time.Duration(adapter.readyTimeoutDuration())*time.Second)
}

func (h *Handler) resumeEnvironment(ctx context.Context, env *activeEnvironment) error {
	adapter, err := h.environmentRuntimeAdapter(env.Runtime)
	if err != nil {
		return err
	}
	return adapter.renewActivity(ctx, env.Name, nowActivity())
}

func (h *Handler) destroyEnvironment(ctx context.Context, env *activeEnvironment) error {
	adapter, err := h.environmentRuntimeAdapter(env.Runtime)
	if err != nil {
		return err
	}
	if adapter.requestDeletion == nil {
		return fmt.Errorf("runtime %q does not support deletion", env.Runtime)
	}
	return adapter.requestDeletion(ctx, env.Name)
}

func (h *Handler) waitEnvironmentReady(ctx context.Context, runtime, name string, timeout time.Duration) (*activeEnvironment, error) {
	effectiveTimeout := timeout
	deadline := time.Now().Add(effectiveTimeout)
	for time.Now().Before(deadline) {
		env, err := h.getEnvironment(ctx, runtime, name)
		if err != nil {
			return nil, err
		}
		if readyTimeout := environmentReadyTimeout(env, effectiveTimeout); readyTimeout > 0 && readyTimeout != effectiveTimeout {
			effectiveTimeout = readyTimeout
			deadline = time.Now().Add(effectiveTimeout)
		}
		if env.Phase == breakfixv1.EnvironmentReady {
			return env, nil
		}
		if env.Phase == breakfixv1.EnvironmentDestroyed || env.Phase == breakfixv1.EnvironmentFailed {
			return nil, environmentUnavailableError(env)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("timeout waiting for environment ready")
}

func (h *Handler) getEnvironment(ctx context.Context, runtime, name string) (*activeEnvironment, error) {
	adapter, err := h.environmentRuntimeAdapter(runtime)
	if err != nil {
		return nil, err
	}
	return adapter.get(ctx, name)
}

func isLiveEnvironmentPhase(phase breakfixv1.EnvironmentPhase) bool {
	return phase == "" ||
		phase == breakfixv1.EnvironmentPending ||
		phase == breakfixv1.EnvironmentProvisioning ||
		phase == breakfixv1.EnvironmentReady ||
		phase == breakfixv1.EnvironmentDraining
}

func environmentReadyTimeout(env *activeEnvironment, fallback time.Duration) time.Duration {
	if env == nil || env.ReadyTimeoutSeconds == nil || *env.ReadyTimeoutSeconds <= 0 {
		return fallback
	}
	return time.Duration(*env.ReadyTimeoutSeconds) * time.Second
}

func environmentIdleTTL(env *activeEnvironment, fallback time.Duration) time.Duration {
	if env == nil || env.IdleTTLSeconds == nil || *env.IdleTTLSeconds <= 0 {
		return fallback
	}
	return time.Duration(*env.IdleTTLSeconds) * time.Second
}

func environmentDrainGracePeriod(env *activeEnvironment, fallback time.Duration) time.Duration {
	if env == nil || env.DrainGraceSeconds == nil || *env.DrainGraceSeconds <= 0 {
		return fallback
	}
	return time.Duration(*env.DrainGraceSeconds) * time.Second
}

func environmentUnavailableError(env *activeEnvironment) error {
	if env == nil {
		return errors.New("environment became unavailable before ready")
	}
	if env.LastError != nil && strings.TrimSpace(env.LastError.Message) != "" {
		return errors.New(env.LastError.Message)
	}
	if strings.TrimSpace(env.Message) != "" {
		return errors.New(env.Message)
	}
	if strings.TrimSpace(env.Reason) != "" {
		return fmt.Errorf("environment became unavailable before ready: %s", env.Reason)
	}
	return errors.New("environment became unavailable before ready")
}
