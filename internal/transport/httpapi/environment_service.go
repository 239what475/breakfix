package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/content/challenge"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type activeEnvironment struct {
	UID            string
	Runtime        string
	Name           string
	ChallengeRef   string
	SourceRevision string
	Purpose        breakfixv1.EnvironmentPurpose
	Namespace      string
	WorkspacePod   string
	NodeIdentity   incus.NodeEnvironmentIdentity
	Nodes          []breakfixv1.NodeRuntimeNodeSpec
	Phase          breakfixv1.EnvironmentPhase
	ExpiresAt      *metav1.Time
	Checkpoints    *breakfixv1.CheckpointStatus
	Failure        *breakfixv1.EnvironmentFailureStatus
	Lifecycle      breakfixv1.EnvironmentLifecycleSpec
}

func environmentFromNode(environment *breakfixv1.NodeEnvironment) *activeEnvironment {
	if environment == nil {
		return nil
	}
	identity := incus.NodeEnvironmentIdentity{
		Project: environment.Status.Runtime.Project, Network: environment.Status.Runtime.Network,
		ACL: environment.Status.Runtime.ACL, Profile: environment.Status.Runtime.Profile,
		Nodes: make([]incus.NodeIdentity, len(environment.Status.Runtime.Nodes)),
	}
	for index, node := range environment.Status.Runtime.Nodes {
		identity.Nodes[index] = incus.NodeIdentity{LogicalName: node.Name, InstanceName: node.InstanceName, Address: node.Address}
	}
	return &activeEnvironment{
		UID: string(environment.UID), Runtime: challenge.RuntimeNode, Name: environment.Name,
		ChallengeRef: environment.Spec.Environment.Source.Ref, SourceRevision: environment.Spec.Environment.Source.Revision,
		Purpose: environment.Spec.Environment.Purpose, NodeIdentity: identity,
		Nodes: append([]breakfixv1.NodeRuntimeNodeSpec(nil), environment.Spec.Runtime.Nodes...),
		Phase: environment.Status.Environment.Phase, ExpiresAt: environment.Status.Environment.ExpiresAt,
		Checkpoints: environment.Status.Environment.Checkpoints, Failure: environment.Status.Environment.Failure,
		Lifecycle: environment.Spec.Environment.Lifecycle,
	}
}

func environmentFromVK8s(environment *breakfixv1.VK8sEnvironment) *activeEnvironment {
	if environment == nil {
		return nil
	}
	return &activeEnvironment{
		UID: string(environment.UID), Runtime: challenge.RuntimeK8s, Name: environment.Name,
		ChallengeRef: environment.Spec.Environment.Source.Ref, SourceRevision: environment.Spec.Environment.Source.Revision,
		Purpose:   environment.Spec.Environment.Purpose,
		Namespace: environment.Status.Runtime.Namespace, WorkspacePod: environment.Status.Runtime.TerminalPodName,
		Phase: environment.Status.Environment.Phase, ExpiresAt: environment.Status.Environment.ExpiresAt,
		Checkpoints: environment.Status.Environment.Checkpoints, Failure: environment.Status.Environment.Failure,
		Lifecycle: environment.Spec.Environment.Lifecycle,
	}
}

func (h *Handler) listActiveEnvironments(ctx context.Context, userID string) ([]activeEnvironment, error) {
	selector := fmt.Sprintf("breakfix.dev/user=%s", userID)
	result := make([]activeEnvironment, 0, 4)
	for _, runtime := range []string{challenge.RuntimeNode, challenge.RuntimeK8s} {
		adapter, err := h.environmentRuntimeAdapter(runtime)
		if err != nil {
			return nil, err
		}
		environments, err := adapter.list(ctx, selector)
		if err != nil {
			return nil, err
		}
		result = append(result, environments...)
	}
	return result, nil
}

func (h *Handler) findEnvironment(ctx context.Context, userID string, entry *challenge.Entry) (*activeEnvironment, error) {
	selector := fmt.Sprintf("breakfix.dev/user=%s,breakfix.dev/challenge=%s", userID, entry.ID)
	adapter, err := h.environmentRuntimeAdapter(entry.Runtime)
	if err != nil {
		return nil, err
	}
	environments, err := adapter.list(ctx, selector)
	if err != nil {
		return nil, err
	}
	for index := range environments {
		if isLiveEnvironmentPhase(environments[index].Phase) {
			return &environments[index], nil
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
		if environments[index].UID == environmentUID && isLiveEnvironmentPhase(environments[index].Phase) {
			return &environments[index], nil
		}
	}
	return nil, errors.New("no active environment for assistant run")
}

func (h *Handler) findProgressEnvironment(ctx context.Context, userID string, entry *challenge.Entry) (*activeEnvironment, error) {
	selector := fmt.Sprintf("breakfix.dev/user=%s,breakfix.dev/challenge=%s", userID, entry.ID)
	adapter, err := h.environmentRuntimeAdapter(entry.Runtime)
	if err != nil {
		return nil, err
	}
	environments, err := adapter.list(ctx, selector)
	if err != nil {
		return nil, err
	}
	for index := range environments {
		if isLiveEnvironmentPhase(environments[index].Phase) {
			return &environments[index], nil
		}
	}
	for index := range environments {
		if environments[index].Phase == breakfixv1.EnvironmentCompleted {
			return &environments[index], nil
		}
	}
	return nil, errors.New("no environment with checkpoint status")
}

func (h *Handler) createEnvironment(ctx context.Context, user *postgres.User, entry *challenge.Entry) (*activeEnvironment, error) {
	adapter, err := h.environmentRuntimeAdapter(entry.Runtime)
	if err != nil {
		return nil, err
	}
	name, err := adapter.create(ctx, user, entry)
	if err != nil {
		return nil, err
	}
	return h.waitEnvironmentReady(ctx, adapter.runtime, name, adapter.readyTimeout)
}

func (h *Handler) resumeEnvironment(ctx context.Context, environment *activeEnvironment) error {
	adapter, err := h.environmentRuntimeAdapter(environment.Runtime)
	if err != nil {
		return err
	}
	return adapter.renewActivity(ctx, environment.Name, nowActivity())
}

func (h *Handler) destroyEnvironment(ctx context.Context, environment *activeEnvironment) error {
	adapter, err := h.environmentRuntimeAdapter(environment.Runtime)
	if err != nil {
		return err
	}
	return adapter.requestDeletion(ctx, environment.Name)
}

func (h *Handler) waitEnvironmentReady(ctx context.Context, runtime, name string, timeout time.Duration) (*activeEnvironment, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		environment, err := h.getEnvironment(ctx, runtime, name)
		if err != nil {
			return nil, err
		}
		if environment.Phase == breakfixv1.EnvironmentReady {
			return environment, nil
		}
		if environment.Phase == breakfixv1.EnvironmentDestroyed || environment.Phase == breakfixv1.EnvironmentFailed {
			return nil, environmentUnavailableError(environment)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, fmt.Errorf("timeout waiting for %s environment %q to become ready", runtime, name)
		case <-ticker.C:
		}
	}
}

func (h *Handler) getEnvironment(ctx context.Context, runtime, name string) (*activeEnvironment, error) {
	adapter, err := h.environmentRuntimeAdapter(runtime)
	if err != nil {
		return nil, err
	}
	return adapter.get(ctx, name)
}

func isLiveEnvironmentPhase(phase breakfixv1.EnvironmentPhase) bool {
	return phase == "" || phase == breakfixv1.EnvironmentPending || phase == breakfixv1.EnvironmentProvisioning || phase == breakfixv1.EnvironmentReady || phase == breakfixv1.EnvironmentDraining
}

func environmentIdleTTL(environment *activeEnvironment, fallback time.Duration) time.Duration {
	if environment == nil || environment.Lifecycle.IdleTTLSeconds == nil || *environment.Lifecycle.IdleTTLSeconds <= 0 {
		return fallback
	}
	return time.Duration(*environment.Lifecycle.IdleTTLSeconds) * time.Second
}

func environmentUnavailableError(environment *activeEnvironment) error {
	if environment == nil {
		return errors.New("environment became unavailable before ready")
	}
	if environment.Failure != nil {
		// Provider diagnostics stay on the controller-facing CRD status. The
		// browser only receives stable runtime-level failures, never transport
		// details or implementation names.
		if environment.Failure.Class == breakfixv1.EnvironmentFailureInfrastructure {
			return errors.New("learning environment is temporarily unavailable; please try again")
		}
		if message := strings.TrimSpace(environment.Failure.Message); message != "" {
			return errors.New(message)
		}
		if reason := strings.TrimSpace(environment.Failure.Reason); reason != "" {
			return fmt.Errorf("environment became unavailable before ready: %s", reason)
		}
	}
	return errors.New("environment became unavailable before ready")
}
