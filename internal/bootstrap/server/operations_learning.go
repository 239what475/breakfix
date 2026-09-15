package server

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	applearning "github.com/breakfix/breakfix/internal/application/learning"
	"github.com/breakfix/breakfix/internal/application/operations"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

type operationsLearningEvaluator interface {
	Evaluate(context.Context, runtimev2.RuntimeEnvironment) ([]applearning.Checkpoint, error)
}

type runnableRevisionResolver interface {
	ResolveRunnableRevision(context.Context, string, string) (runnable.RunnableRevision, error)
}

type operationsNodeExecutor interface {
	ExecNode(context.Context, incus.ExecNodeRequest) (incus.ExecNodeResult, error)
}

type operationsPodExecutor interface {
	ExecInPodStreamsContext(context.Context, string, string, int, ...string) (kubernetes.PodExecResult, error)
}

// operationsLearningCheckpointEvaluator binds Operations checkpoint polling to
// a Ready RuntimeEnvironment. It never changes the Environment and receives
// its executable paths solely from the bound immutable RunnableRevision.
type operationsLearningCheckpointEvaluator struct {
	revisions runnableRevisionResolver
	node      operationsNodeExecutor
	pods      operationsPodExecutor
	now       func() time.Time
}

func (e operationsLearningCheckpointEvaluator) Evaluate(ctx context.Context, environment runtimev2.RuntimeEnvironment) ([]applearning.Checkpoint, error) {
	if e.revisions == nil {
		return nil, errors.New("Operations runnable revision resolver is required")
	}
	if environment.Spec.Purpose != runtimev2.PurposeLearning || environment.Labels["breakfix.dev/content-kind"] != "operations" || environment.UID == "" {
		return nil, errors.New("runtime environment is not an Operations learning environment")
	}
	if environment.Status.Phase != runtimev2.PhaseReady {
		return nil, errors.New("learning environment is not ready for checkpoint evaluation")
	}
	reference := runnable.RevisionReference{ID: environment.Spec.RunnableRevisionRef.ID, Digest: environment.Spec.RunnableRevisionRef.Digest}
	if err := reference.Validate(); err != nil {
		return nil, fmt.Errorf("learning environment runnable revision reference: %w", err)
	}
	revision, err := e.revisions.ResolveRunnableRevision(ctx, reference.ID, reference.Digest)
	if err != nil {
		return nil, fmt.Errorf("resolve Operations runnable revision: %w", err)
	}
	actualDigest, err := revision.Digest()
	if err != nil {
		return nil, fmt.Errorf("digest resolved Operations runnable revision: %w", err)
	}
	if actualDigest != reference.Digest {
		return nil, errors.New("resolved Operations runnable revision does not match the environment reference")
	}
	identity := runnable.EnvironmentIdentity{
		ID: string(environment.UID), Provider: environment.Status.Runtime.Provider,
		ProfileDigest: environment.Status.Runtime.ProfileDigest,
	}
	results, err := operations.EvaluateLearningCheckpoints(ctx, revision, identity, operationsEnvironmentAssertionExecutor{environment: environment, node: e.node, pods: e.pods})
	if err != nil {
		return nil, err
	}
	checkedAt := time.Now().UTC()
	if e.now != nil {
		checkedAt = e.now().UTC()
	}
	checkpoints := make([]applearning.Checkpoint, 0, len(results))
	for _, result := range results {
		checkpoint := applearning.Checkpoint{ID: result.ID, Passed: result.Satisfied, Summary: result.Summary}
		if result.Satisfied {
			firstPassedAt := checkedAt
			checkpoint.FirstPassedAt = &firstPassedAt
		}
		checkpoints = append(checkpoints, checkpoint)
	}
	return checkpoints, nil
}

type operationsEnvironmentAssertionExecutor struct {
	environment runtimev2.RuntimeEnvironment
	node        operationsNodeExecutor
	pods        operationsPodExecutor
}

func (e operationsEnvironmentAssertionExecutor) ExecuteReadOnly(ctx context.Context, assertion runnable.AssertionSpec, boundary runnable.ExecutionBoundary) (operations.AssertionExecutionOutput, error) {
	if boundary.Permission != runnable.PermissionReadOnly || boundary.Target != assertion.Target {
		return operations.AssertionExecutionOutput{}, errors.New("learning assertion execution exceeds its read-only boundary")
	}
	entrypoint, err := runnableEntrypoint(assertion.Entrypoint)
	if err != nil {
		return operations.AssertionExecutionOutput{}, err
	}
	switch e.environment.Status.Runtime.Provider {
	case string(runnable.RuntimeNode):
		if e.node == nil || assertion.Target.Kind != "node" {
			return operations.AssertionExecutionOutput{}, errors.New("Node learning assertion target is unavailable")
		}
		result, err := e.node.ExecNode(ctx, incus.ExecNodeRequest{
			EnvironmentUID: string(e.environment.UID), Revision: e.environment.Spec.RunnableRevisionRef.Digest,
			Identity: runtimeNodeIdentity(e.environment), LogicalName: assertion.Target.ID,
			Command: []string{"/bin/bash", entrypoint},
		})
		if err != nil {
			return operations.AssertionExecutionOutput{}, err
		}
		return operations.AssertionExecutionOutput{ExitCode: result.ExitCode, Stdout: []byte(result.Stdout), Stderr: []byte(result.Stderr)}, nil
	case string(runnable.RuntimeK8s):
		if e.pods == nil || assertion.Target.Kind != "management" {
			return operations.AssertionExecutionOutput{}, errors.New("Kubernetes learning assertion target is unavailable")
		}
		terminal := runtimeEndpoint(e.environment.Status.Runtime.EndpointRefs, "terminal")
		namespace, pod, ok := strings.Cut(terminal, "/")
		if !ok || namespace == "" || pod == "" {
			return operations.AssertionExecutionOutput{}, errors.New("Kubernetes learning environment has no terminal endpoint")
		}
		result, err := e.pods.ExecInPodStreamsContext(ctx, namespace, pod, runnable.MaxAssertionOutputBytes, "/bin/bash", entrypoint)
		if err != nil {
			return operations.AssertionExecutionOutput{}, err
		}
		return operations.AssertionExecutionOutput{ExitCode: result.ExitCode, Stdout: []byte(result.Stdout), Stderr: []byte(result.Stderr)}, nil
	default:
		return operations.AssertionExecutionOutput{}, fmt.Errorf("unsupported learning runtime %q", e.environment.Status.Runtime.Provider)
	}
}

func runnableEntrypoint(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || path.IsAbs(value) || value == "." || strings.HasPrefix(value, "../") || strings.Contains(value, "/../") || strings.Contains(value, "\\") {
		return "", errors.New("learning assertion entrypoint must be a safe relative path")
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("learning assertion entrypoint escapes the runnable archive")
	}
	return path.Join("/opt/breakfix/runnable", clean), nil
}

func runtimeNodeIdentity(environment runtimev2.RuntimeEnvironment) incus.NodeEnvironmentIdentity {
	identity := incus.NodeEnvironmentIdentity{
		Project: runtimeResource(environment.Status.Runtime.ResourceRefs, "project"), Network: runtimeResource(environment.Status.Runtime.ResourceRefs, "network"),
		ACL: runtimeResource(environment.Status.Runtime.ResourceRefs, "acl"), Profile: runtimeResource(environment.Status.Runtime.ResourceRefs, "profile"),
	}
	for _, resource := range environment.Status.Runtime.ResourceRefs {
		if !strings.HasPrefix(resource.Kind, "instance:") {
			continue
		}
		logicalName := strings.TrimPrefix(resource.Kind, "instance:")
		identity.Nodes = append(identity.Nodes, incus.NodeIdentity{LogicalName: logicalName, InstanceName: resource.ID, Address: runtimeEndpoint(environment.Status.Runtime.EndpointRefs, logicalName)})
	}
	return identity
}

func runtimeResource(values []runtimev2.ResourceReference, kind string) string {
	for _, value := range values {
		if value.Kind == kind {
			return value.ID
		}
	}
	return ""
}

func runtimeEndpoint(values []runtimev2.EndpointReference, name string) string {
	for _, value := range values {
		if value.Name == name {
			return value.Ref
		}
	}
	return ""
}
