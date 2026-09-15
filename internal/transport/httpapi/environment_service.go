package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/content/scenario"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

var errNoActiveAssistantEnvironment = errors.New("no active environment for assistant run")

type activeEnvironment struct {
	UID                      string
	UserID                   string
	Runtime                  string
	Name                     string
	ScenarioRef              string
	SourceRevision           string
	RunnableRevisionID       string
	RunnableRevisionDigest   string
	VerificationReportID     string
	VerificationReportDigest string
	Purpose                  breakfixv1.EnvironmentPurpose
	Namespace                string
	WorkspacePod             string
	NodeIdentity             incus.NodeEnvironmentIdentity
	Nodes                    []breakfixv1.NodeRuntimeNodeSpec
	Phase                    breakfixv1.EnvironmentPhase
	Deleting                 bool
	ReadyAt                  *metav1.Time
	ExpiresAt                *metav1.Time
	Checkpoints              *breakfixv1.CheckpointStatus
	Failure                  *breakfixv1.EnvironmentFailureStatus
	Lifecycle                breakfixv1.EnvironmentLifecycleSpec
}

func environmentFromRuntime(environment *runtimev2.RuntimeEnvironment) *activeEnvironment {
	if environment == nil {
		return nil
	}
	runtimeName := string(environment.Status.Runtime.Provider)
	purpose := breakfixv1.EnvironmentPurposeLearning
	if environment.Spec.Purpose == runtimev2.PurposeVerification {
		purpose = breakfixv1.EnvironmentPurposeVerification
	}
	phase := breakfixv1.EnvironmentProvisioning
	switch environment.Status.Phase {
	case runtimev2.PhaseReady:
		phase = breakfixv1.EnvironmentReady
	case runtimev2.PhaseDraining:
		phase = breakfixv1.EnvironmentDraining
	case runtimev2.PhaseReleased:
		phase = breakfixv1.EnvironmentDestroyed
	case runtimev2.PhaseFailed:
		phase = breakfixv1.EnvironmentFailed
	}
	labels := environment.Labels
	active := &activeEnvironment{
		UID: string(environment.UID), Runtime: runtimeName, Name: environment.Name,
		UserID: labels["breakfix.dev/user"], ScenarioRef: labels["breakfix.dev/content-id"], SourceRevision: labels["breakfix.dev/content-revision"],
		RunnableRevisionID: environment.Spec.RunnableRevisionRef.ID, RunnableRevisionDigest: environment.Spec.RunnableRevisionRef.Digest,
		VerificationReportID: environment.Status.Progress.ReportRef.ID, VerificationReportDigest: environment.Status.Progress.ReportRef.Digest,
		Purpose: purpose, Phase: phase, Deleting: environment.DeletionTimestamp != nil,
	}
	if environment.Status.Lifecycle.ExpiresAt != nil {
		expires := environment.Status.Lifecycle.ExpiresAt.DeepCopy()
		active.ExpiresAt = expires
	}
	if runtimeName == scenario.RuntimeNode {
		active.NodeIdentity = nodeIdentityFromRuntime(environment)
		active.Nodes = nodeSpecsFromRuntime(environment)
	} else if runtimeName == scenario.RuntimeK8s {
		active.Namespace, active.WorkspacePod = runtimeK8sTerminal(environment)
	}
	if environment.Status.Failure != nil {
		active.Failure = &breakfixv1.EnvironmentFailureStatus{Class: breakfixv1.EnvironmentFailureClass(environment.Status.Failure.Class), Reason: environment.Status.Failure.Reason, Message: environment.Status.Failure.Message}
	}
	return active
}

// checkpointStatus projects only the Operations conclusion assertions from a
// referenced immutable report. Checkpoint data is deliberately not copied to
// RuntimeEnvironment status.
func (h *Handler) checkpointStatus(ctx context.Context, environment *activeEnvironment, entry *scenario.Entry) (*breakfixv1.CheckpointStatus, error) {
	if environment == nil || entry == nil || environment.VerificationReportID == "" || environment.VerificationReportDigest == "" {
		return nil, nil
	}
	if h == nil || h.runnableReports == nil || environment.RunnableRevisionID == "" || environment.RunnableRevisionDigest == "" {
		return nil, errors.New("verification report reader is unavailable")
	}
	revision, err := h.runnableReports.ResolveRunnableRevision(ctx, environment.RunnableRevisionID, environment.RunnableRevisionDigest)
	if err != nil {
		return nil, fmt.Errorf("resolve runnable revision for environment report: %w", err)
	}
	report, err := h.runnableReports.ResolveVerificationReport(ctx, environment.VerificationReportID, environment.VerificationReportDigest, revision)
	if err != nil {
		return nil, fmt.Errorf("resolve environment verification report: %w", err)
	}
	status := &breakfixv1.CheckpointStatus{Results: make([]breakfixv1.CheckpointResultStatus, 0, len(entry.Checkpoints))}
	checkedAt := metav1.NewTime(report.CreatedAt.UTC())
	status.CheckedAt = &checkedAt
	if report.Failure != nil {
		status.Error = report.Failure.Message
		return status, nil
	}
	declared := make(map[string]struct{}, len(entry.Checkpoints))
	for _, checkpoint := range entry.Checkpoints {
		declared[checkpoint.ID] = struct{}{}
	}
	for _, phase := range report.Phases {
		for _, assertion := range phase.Assertions {
			if _, exists := declared[assertion.ID]; !exists {
				continue
			}
			firstPassedAt := (*metav1.Time)(nil)
			if assertion.Satisfied {
				value := checkedAt
				firstPassedAt = &value
			}
			status.Results = append(status.Results, breakfixv1.CheckpointResultStatus{ID: assertion.ID, Passed: assertion.Satisfied, FirstPassedAt: firstPassedAt, Summary: assertion.Summary, Details: assertion.Details})
		}
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

func nodeSpecsFromRuntime(environment *runtimev2.RuntimeEnvironment) []breakfixv1.NodeRuntimeNodeSpec {
	identity := nodeIdentityFromRuntime(environment)
	result := make([]breakfixv1.NodeRuntimeNodeSpec, 0, len(identity.Nodes))
	for _, node := range identity.Nodes {
		result = append(result, breakfixv1.NodeRuntimeNodeSpec{Name: node.LogicalName, Title: node.LogicalName})
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

func (h *Handler) findEnvironment(ctx context.Context, userID string, entry *scenario.Entry) (*activeEnvironment, error) {
	if entry == nil {
		return nil, errNoMatchingEnvironment
	}
	selector := fmt.Sprintf("breakfix.dev/user=%s,breakfix.dev/content-kind=operations,breakfix.dev/content-id=%s", userID, entry.ID)
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
	selector := fmt.Sprintf("breakfix.dev/user=%s,breakfix.dev/content-kind=operations,breakfix.dev/content-id=%s", userID, entry.ID)
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
