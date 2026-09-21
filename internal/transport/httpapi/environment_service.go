package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var errNoActiveAssistantEnvironment = errors.New("no active environment for assistant run")

const (
	// environmentContentOperations is the content-kind label of environments
	// pinned to Operations scenarios; environmentContentDocumentationPractice
	// matches the runnable Kind of published documentation practices;
	// environmentContentDocumentationBlank pins the library-scoped blank
	// practice environment each reader may run alongside the documentation.
	environmentContentOperations            = "operations"
	environmentContentDocumentationPractice = "documentation-practice"
	environmentContentDocumentationBlank    = "documentation-blank"
)

// environmentContentTarget is the content identity one learning environment is
// pinned to, regardless of kind. Operations scenarios build it from their
// catalog entry; published documentation practices build it from their
// immutable revision. Both kinds share the find-or-create path and differ
// only in labels and in how their runnable revision binding resolves.
type environmentContentTarget struct {
	kind       string
	id         string
	revisionID string
	runtime    string
	title      string
	// blank marks a content-free environment: no runnable revision resolves
	// and creation names the system blank runtime instead.
	blank bool
	// resolveBinding supplies the immutable runnable revision the environment
	// runs. Catalog-backed content resolves lazily through its revision
	// binding; a published practice already carries its reference.
	resolveBinding func(context.Context) (runnable.RevisionReference, error)
}

// operationsEnvironmentTarget pins an environment to one Operations scenario
// revision without changing the Operations label vocabulary.
func (h *Handler) operationsEnvironmentTarget(entry *scenario.Entry) environmentContentTarget {
	return environmentContentTarget{
		kind: environmentContentOperations, id: entry.ID, revisionID: entry.RevisionID,
		runtime: entry.Runtime, title: entry.Title,
		resolveBinding: func(ctx context.Context) (runnable.RevisionReference, error) {
			if h.runnableBindings == nil {
				return runnable.RevisionReference{}, errors.New("runnable revision store is unavailable")
			}
			return h.runnableBindings.ResolveOperationsRevisionBinding(ctx, entry.RevisionID)
		},
	}
}

// These are HTTP projections, not CRD or domain types. They retain only the
// public lifecycle and Operations display data needed by this transport.
type activeNode struct {
	Name  string
	Title string
}

type checkpointResult struct {
	ID            string
	Passed        bool
	FirstPassedAt *metav1.Time
	Summary       string
	Details       string
}

type checkpointStatus struct {
	Results   []checkpointResult
	CheckedAt *metav1.Time
	Error     string
}

type activeLifecycle struct {
	IdleTTLSeconds *int64
}

type activeEnvironment struct {
	UID                    string
	UserID                 string
	Runtime                string
	Name                   string
	ScenarioRef            string
	SourceRevision         string
	RunnableRevisionID     string
	RunnableRevisionDigest string
	Blank                  bool
	Purpose                runtimev2.EnvironmentPurpose
	Namespace              string
	WorkspacePod           string
	NodeIdentity           incus.NodeEnvironmentIdentity
	Nodes                  []activeNode
	Phase                  runtimev2.EnvironmentPhase
	Operation              runtimev2.EnvironmentOperation
	Deleting               bool
	ReadyAt                *metav1.Time
	ExpiresAt              *metav1.Time
	Checkpoints            *checkpointStatus
	Failure                *runtimev2.EnvironmentFailure
	Lifecycle              activeLifecycle
}

func environmentFromRuntime(environment *runtimev2.RuntimeEnvironment) *activeEnvironment {
	if environment == nil {
		return nil
	}
	runtimeName := environment.Status.Runtime.Provider
	phase := environment.Status.Phase
	if phase == "" {
		phase = runtimev2.PhasePending
	}
	labels := environment.Labels
	active := &activeEnvironment{
		UID: string(environment.UID), Runtime: runtimeName, Name: environment.Name,
		UserID: labels["breakfix.dev/user"], ScenarioRef: labels["breakfix.dev/content-id"], SourceRevision: labels["breakfix.dev/content-revision"],
		RunnableRevisionID: environment.Spec.RunnableRevisionRef.ID, RunnableRevisionDigest: environment.Spec.RunnableRevisionRef.Digest,
		Blank:   environment.Spec.BlankRuntime != nil,
		Purpose: environment.Spec.Purpose, Phase: phase, Operation: environment.Status.Operation, Deleting: environment.DeletionTimestamp != nil,
	}
	if environment.Status.Lifecycle.ExpiresAt != nil {
		expires := environment.Status.Lifecycle.ExpiresAt.DeepCopy()
		active.ExpiresAt = expires
	}
	switch runtimeName {
	case scenario.RuntimeNode:
		active.NodeIdentity = nodeIdentityFromRuntime(environment)
		active.Nodes = nodeSpecsFromRuntime(environment)
	case scenario.RuntimeK8s:
		active.Namespace, active.WorkspacePod = runtimeK8sTerminal(environment)
	}
	if environment.Status.Failure != nil {
		failure := *environment.Status.Failure
		active.Failure = &failure
	}
	return active
}

// checkpointStatus projects only durable learning facts. The Server's
// background Operations evaluator is the sole writer; RuntimeEnvironment
// status deliberately retains no copied assertion tree or learning report.
func (h *Handler) checkpointStatus(ctx context.Context, environment *activeEnvironment, entry *scenario.Entry) (*checkpointStatus, error) {
	if environment == nil || entry == nil {
		return nil, nil
	}
	if h == nil || h.db == nil {
		return nil, errors.New("learning progress store is unavailable")
	}
	events, err := h.db.Environment.ListCheckpointFirstPasses(ctx, []string{environment.UID})
	if err != nil {
		return nil, fmt.Errorf("read checkpoint first-pass events: %w", err)
	}
	status := &checkpointStatus{Results: make([]checkpointResult, 0, len(entry.Checkpoints))}
	passed := make(map[string]postgres.CheckpointFirstPassEvent, len(events[environment.UID]))
	for _, event := range events[environment.UID] {
		passed[event.CheckpointID] = event
	}
	for _, checkpoint := range entry.Checkpoints {
		result := checkpointResult{ID: checkpoint.ID}
		if event, exists := passed[checkpoint.ID]; exists {
			firstPassedAt := metav1.NewTime(event.FirstPassedAt.UTC())
			result.Passed, result.FirstPassedAt, result.Summary = true, &firstPassedAt, event.Summary
			status.CheckedAt = &firstPassedAt
		}
		status.Results = append(status.Results, result)
	}
	return status, nil
}

func resourceID(values []runtimev2.ResourceReference, kind string) string {
	for _, value := range values {
		if value.Kind == kind {
			return value.ID
		}
	}
	return ""
}

func endpointID(values []runtimev2.EndpointReference, name string) string {
	for _, value := range values {
		if value.Name == name {
			return value.Ref
		}
	}
	return ""
}

func runtimeK8sTerminal(environment *runtimev2.RuntimeEnvironment) (string, string) {
	terminal := endpointID(environment.Status.Runtime.EndpointRefs, "terminal")
	if terminal == "" {
		terminal = resourceID(environment.Status.Runtime.ResourceRefs, "pod")
	}
	if namespace, pod, ok := strings.Cut(terminal, "/"); ok && namespace != "" && pod != "" {
		return namespace, pod
	}
	return resourceID(environment.Status.Runtime.ResourceRefs, "namespace"), ""
}

func nodeIdentityFromRuntime(environment *runtimev2.RuntimeEnvironment) incus.NodeEnvironmentIdentity {
	identity := incus.NodeEnvironmentIdentity{Project: resourceID(environment.Status.Runtime.ResourceRefs, "project"), Network: resourceID(environment.Status.Runtime.ResourceRefs, "network"), ACL: resourceID(environment.Status.Runtime.ResourceRefs, "acl"), Profile: resourceID(environment.Status.Runtime.ResourceRefs, "profile")}
	for _, resource := range environment.Status.Runtime.ResourceRefs {
		if !strings.HasPrefix(resource.Kind, "instance:") {
			continue
		}
		logical := strings.TrimPrefix(resource.Kind, "instance:")
		address := ""
		for _, endpoint := range environment.Status.Runtime.EndpointRefs {
			if endpoint.Name == logical {
				address = endpoint.Ref
			}
		}
		identity.Nodes = append(identity.Nodes, incus.NodeIdentity{LogicalName: logical, InstanceName: resource.ID, Address: address})
	}
	return identity
}

func nodeSpecsFromRuntime(environment *runtimev2.RuntimeEnvironment) []activeNode {
	identity := nodeIdentityFromRuntime(environment)
	result := make([]activeNode, 0, len(identity.Nodes))
	for _, node := range identity.Nodes {
		result = append(result, activeNode{Name: node.LogicalName, Title: node.LogicalName})
	}
	return result
}

func (h *Handler) listActiveEnvironments(ctx context.Context, userID string) ([]activeEnvironment, error) {
	selector := fmt.Sprintf("breakfix.dev/user=%s", userID)
	items, err := h.k8s.ListRuntimeEnvironments(ctx, h.crdNamespace, selector)
	if err != nil {
		return nil, err
	}
	result := make([]activeEnvironment, 0, len(items.Items))
	for index := range items.Items {
		if items.Items[index].DeletionTimestamp != nil {
			continue
		}
		if environment := environmentFromRuntime(&items.Items[index]); environment != nil {
			result = append(result, *environment)
		}
	}
	return result, nil
}

func (h *Handler) findEnvironment(ctx context.Context, userID string, target environmentContentTarget) (*activeEnvironment, error) {
	if target.id == "" {
		return nil, errNoMatchingEnvironment
	}
	selector := fmt.Sprintf("breakfix.dev/user=%s,breakfix.dev/content-kind=%s,breakfix.dev/content-id=%s", userID, target.kind, target.id)
	adapter, err := h.environmentRuntimeAdapter(target.runtime)
	if err != nil {
		return nil, err
	}
	environments, err := adapter.list(ctx, selector)
	if err != nil {
		return nil, err
	}
	for index := range environments {
		if environments[index].SourceRevision == target.revisionID && isLiveEnvironmentPhase(environments[index].Phase) {
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

func (h *Handler) findProgressEnvironment(ctx context.Context, userID string, target environmentContentTarget) (*activeEnvironment, error) {
	if target.id == "" {
		return nil, errNoMatchingEnvironment
	}
	selector := fmt.Sprintf("breakfix.dev/user=%s,breakfix.dev/content-kind=%s,breakfix.dev/content-id=%s", userID, target.kind, target.id)
	adapter, err := h.environmentRuntimeAdapter(target.runtime)
	if err != nil {
		return nil, err
	}
	environments, err := adapter.list(ctx, selector)
	if err != nil {
		return nil, err
	}
	for index := range environments {
		if environments[index].SourceRevision == target.revisionID && isLiveEnvironmentPhase(environments[index].Phase) {
			return &environments[index], nil
		}
	}
	for index := range environments {
		if environments[index].SourceRevision == target.revisionID && environments[index].Phase == runtimev2.PhaseReleased {
			return &environments[index], nil
		}
	}
	return nil, errNoMatchingEnvironment
}

func (h *Handler) createEnvironment(ctx context.Context, user *postgres.User, target environmentContentTarget) (*activeEnvironment, error) {
	adapter, err := h.environmentRuntimeAdapter(target.runtime)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < environmentCreateAttempts; attempt++ {
		name, createErr := adapter.create(ctx, user, target)
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

		existing, getErr := adapter.get(ctx, learningEnvironmentName(user.ID, target))
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
		if !environmentMatchesTarget(existing, user.ID, target) {
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
		if environment.Phase == runtimev2.PhaseReady {
			return environment, nil
		}
		if environment.Phase == runtimev2.PhaseReleased || environment.Phase == runtimev2.PhaseFailed {
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

func environmentMatchesTarget(environment *activeEnvironment, userID string, target environmentContentTarget) bool {
	return environment != nil && target.id != "" &&
		environment.UserID == userID &&
		environment.Purpose == runtimev2.PurposeLearning &&
		environment.ScenarioRef == target.id &&
		environment.SourceRevision == target.revisionID &&
		environment.Runtime == target.runtime &&
		environment.Blank == target.blank
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

func isLiveEnvironmentPhase(phase runtimev2.EnvironmentPhase) bool {
	return phase == "" || phase == runtimev2.PhasePending || phase == runtimev2.PhaseProvisioning || phase == runtimev2.PhaseReady || phase == runtimev2.PhaseDraining
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
		if environment.Failure.Class == runtimev2.FailureInfrastructure {
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
