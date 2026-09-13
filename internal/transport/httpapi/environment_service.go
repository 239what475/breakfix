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
	"github.com/breakfix/breakfix/internal/content/scenario"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var errNoActiveAssistantEnvironment = errors.New("no active environment for assistant run")

type activeEnvironment struct {
	UID            string
	UserID         string
	Runtime        string
	Name           string
	ScenarioRef    string
	SourceRevision string
	Purpose        breakfixv1.EnvironmentPurpose
	Namespace      string
	WorkspacePod   string
	NodeIdentity   incus.NodeEnvironmentIdentity
	Nodes          []breakfixv1.NodeRuntimeNodeSpec
	Phase          breakfixv1.EnvironmentPhase
	Deleting       bool
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
		UID: string(environment.UID), Runtime: scenario.RuntimeNode, Name: environment.Name,
		UserID:      environment.Spec.Environment.UserRef,
		ScenarioRef: environment.Spec.Environment.Source.Ref, SourceRevision: environment.Spec.Environment.Source.Revision,
		Purpose: environment.Spec.Environment.Purpose, NodeIdentity: identity,
		Nodes: append([]breakfixv1.NodeRuntimeNodeSpec(nil), environment.Spec.Runtime.Nodes...),
		Phase: environment.Status.Environment.Phase, Deleting: environment.DeletionTimestamp != nil, ExpiresAt: environment.Status.Environment.ExpiresAt,
		Checkpoints: environment.Status.Environment.Checkpoints, Failure: environment.Status.Environment.Failure,
		Lifecycle: environment.Spec.Environment.Lifecycle,
	}
}

func environmentFromVK8s(environment *breakfixv1.VK8sEnvironment) *activeEnvironment {
	if environment == nil {
		return nil
	}
	return &activeEnvironment{
		UID: string(environment.UID), UserID: environment.Spec.Environment.UserRef, Runtime: scenario.RuntimeK8s, Name: environment.Name,
		ScenarioRef: environment.Spec.Environment.Source.Ref, SourceRevision: environment.Spec.Environment.Source.Revision,
		Purpose:   environment.Spec.Environment.Purpose,
		Namespace: environment.Status.Runtime.Namespace, WorkspacePod: environment.Status.Runtime.TerminalPodName,
		Phase: environment.Status.Environment.Phase, Deleting: environment.DeletionTimestamp != nil, ExpiresAt: environment.Status.Environment.ExpiresAt,
		Checkpoints: environment.Status.Environment.Checkpoints, Failure: environment.Status.Environment.Failure,
		Lifecycle: environment.Spec.Environment.Lifecycle,
	}
}

func (h *Handler) listActiveEnvironments(ctx context.Context, userID string) ([]activeEnvironment, error) {
	selector := fmt.Sprintf("breakfix.dev/user=%s", userID)
	result := make([]activeEnvironment, 0, 4)
	for _, runtime := range []string{scenario.RuntimeNode, scenario.RuntimeK8s} {
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

func (h *Handler) findEnvironment(ctx context.Context, userID string, entry *scenario.Entry) (*activeEnvironment, error) {
	if entry == nil {
		return nil, errNoMatchingEnvironment
	}
	selector := fmt.Sprintf("breakfix.dev/user=%s,breakfix.dev/scenario=%s", userID, entry.ID)
	adapter, err := h.environmentRuntimeAdapter(entry.Runtime)
	if err != nil {
		return nil, err
	}
	environments, err := adapter.list(ctx, selector)
	if err != nil {
		return nil, err
	}
	for index := range environments {
		if environments[index].SourceRevision == entry.RevisionID && isLiveEnvironmentPhase(environments[index].Phase) {
			return &environments[index], nil
		}
	}
	return nil, errNoMatchingEnvironment
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
	return nil, errNoActiveAssistantEnvironment
}

func (h *Handler) findProgressEnvironment(ctx context.Context, userID string, entry *scenario.Entry) (*activeEnvironment, error) {
	if entry == nil {
		return nil, errNoMatchingEnvironment
	}
	selector := fmt.Sprintf("breakfix.dev/user=%s,breakfix.dev/scenario=%s", userID, entry.ID)
	adapter, err := h.environmentRuntimeAdapter(entry.Runtime)
	if err != nil {
		return nil, err
	}
	environments, err := adapter.list(ctx, selector)
	if err != nil {
		return nil, err
	}
	for index := range environments {
		if environments[index].SourceRevision == entry.RevisionID && isLiveEnvironmentPhase(environments[index].Phase) {
			return &environments[index], nil
		}
	}
	for index := range environments {
		if environments[index].SourceRevision == entry.RevisionID && environments[index].Phase == breakfixv1.EnvironmentCompleted {
			return &environments[index], nil
		}
	}
	return nil, errNoMatchingEnvironment
}

func (h *Handler) createEnvironment(ctx context.Context, user *postgres.User, entry *scenario.Entry) (*activeEnvironment, error) {
	adapter, err := h.environmentRuntimeAdapter(entry.Runtime)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < environmentCreateAttempts; attempt++ {
		name, createErr := adapter.create(ctx, user, entry)
		if createErr == nil {
			environment, waitErr := h.waitEnvironmentReady(ctx, adapter.runtime, name, adapter.readyTimeout)
			if waitErr == nil {
				return environment, nil
			}
			if cleanupErr := h.cleanupFailedEnvironment(adapter, name); cleanupErr != nil {
				return nil, fmt.Errorf("wait for newly created environment: %w; request cleanup: %v", waitErr, cleanupErr)
			}
			return nil, fmt.Errorf("wait for newly created environment: %w", waitErr)
		}
		if !apierrors.IsAlreadyExists(createErr) {
			return nil, createErr
		}

		existing, getErr := adapter.get(ctx, learningEnvironmentName(user.ID, entry))
		if apierrors.IsNotFound(getErr) {
			continue
		}
		if getErr != nil {
			return nil, fmt.Errorf("read concurrently created environment: %w", getErr)
		}
		if existing.Deleting || !isLiveEnvironmentPhase(existing.Phase) {
			if err := h.destroyEnvironmentAndWait(ctx, existing); err != nil {
				return nil, fmt.Errorf("clear previous environment: %w", err)
			}
			continue
		}
		if !environmentMatchesEntry(existing, user.ID, entry) {
			return nil, fmt.Errorf("existing environment %q does not match the requested scenario revision", existing.Name)
		}
		return h.waitEnvironmentReady(ctx, adapter.runtime, existing.Name, adapter.readyTimeout)
	}
	return nil, fmt.Errorf("environment creation is still racing; retry the request")
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
	return adapter.requestDeletion(ctx, environment.Name, types.UID(environment.UID))
}

func (h *Handler) destroyEnvironmentAndWait(ctx context.Context, environment *activeEnvironment) error {
	if environment == nil {
		return errNoMatchingEnvironment
	}
	if err := h.destroyEnvironment(ctx, environment); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return h.waitEnvironmentDeleted(ctx, environment.Runtime, environment.Name, environmentDeletionTimeout(environment.Runtime))
}

func (h *Handler) cleanupFailedEnvironment(adapter *environmentRuntimeAdapter, name string) error {
	if adapter == nil {
		return errors.New("environment runtime is required")
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), environmentDeletionTimeout(adapter.runtime))
	defer cancel()
	if err := adapter.requestDeletion(cleanupCtx, name, ""); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return h.waitEnvironmentDeleted(cleanupCtx, adapter.runtime, name, environmentDeletionTimeout(adapter.runtime))
}

func (h *Handler) waitEnvironmentDeleted(ctx context.Context, runtime, name string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := h.getEnvironment(ctx, runtime, name)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("get deleting %s environment %q: %w", runtime, name, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timeout waiting for %s environment %q to be deleted", runtime, name)
		case <-ticker.C:
		}
	}
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
		if environment.Deleting {
			return nil, errors.New("learning environment is shutting down; wait for deletion to finish before starting it again")
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

func environmentMatchesEntry(environment *activeEnvironment, userID string, entry *scenario.Entry) bool {
	return environment != nil && entry != nil &&
		environment.UserID == userID &&
		environment.Purpose == breakfixv1.EnvironmentPurposeLearning &&
		environment.ScenarioRef == entry.ID &&
		environment.SourceRevision == entry.RevisionID &&
		environment.Runtime == entry.Runtime
}

func environmentDeletionTimeout(runtime string) time.Duration {
	if scenario.NormalizeRuntime(runtime) == scenario.RuntimeK8s {
		return vk8sEnvironmentReadyTimeout
	}
	return nodeEnvironmentReadyTimeout
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
