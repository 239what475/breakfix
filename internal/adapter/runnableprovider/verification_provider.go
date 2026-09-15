package runnableprovider

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	runnableworker "github.com/breakfix/breakfix/internal/worker/runnable"
)

type RuntimeEnvironmentClient interface {
	GetRuntimeEnvironment(context.Context, string, string) (*runtimev2.RuntimeEnvironment, error)
	ExecInPodStreamsContext(context.Context, string, string, int, ...string) (kubernetes.PodExecResult, error)
}

// VerificationEnvironmentCreator is the Server-owned control-plane boundary.
// The Runtime Worker may observe and execute in an environment, but it never
// writes RuntimeEnvironment spec fields itself.
type VerificationEnvironmentCreator interface {
	CreateVerificationEnvironment(context.Context, runnable.VerifyRequest) (*runtimev2.RuntimeEnvironment, error)
}

type NodeRuntimeExecutor interface {
	ExecNode(context.Context, incus.ExecNodeRequest) (incus.ExecNodeResult, error)
}

// VerificationProvider adapts the v2 RuntimeEnvironment API and provider
// execution clients to the public runnable verifier boundary.
type VerificationProvider struct {
	client    RuntimeEnvironmentClient
	creator   VerificationEnvironmentCreator
	node      NodeRuntimeExecutor
	namespace string
	pollEvery time.Duration
	mu        sync.Mutex
	retained  map[string]*runtimev2.RuntimeEnvironment
}

func NewVerificationProvider(environments RuntimeEnvironmentClient, creator VerificationEnvironmentCreator, node NodeRuntimeExecutor, namespace string) (*VerificationProvider, error) {
	if environments == nil || creator == nil || strings.TrimSpace(namespace) == "" {
		return nil, errors.New("runnable verification provider requires RuntimeEnvironment client, Server creator, and namespace")
	}
	return &VerificationProvider{client: environments, creator: creator, node: node, namespace: namespace, pollEvery: time.Second, retained: make(map[string]*runtimev2.RuntimeEnvironment)}, nil
}

func (p *VerificationProvider) CreateVerificationEnvironment(ctx context.Context, request runnable.VerifyRequest) (runnable.EnvironmentIdentity, error) {
	if err := request.Validate(); err != nil {
		return runnable.EnvironmentIdentity{}, err
	}
	profileDigest, err := request.RunnableRevision.Spec.RuntimeProfile.Digest()
	if err != nil {
		return runnable.EnvironmentIdentity{}, err
	}
	created, err := p.creator.CreateVerificationEnvironment(ctx, request)
	if err != nil {
		return runnable.EnvironmentIdentity{}, fmt.Errorf("create runnable verification environment: %w", err)
	}
	if created == nil || created.UID == "" {
		return runnable.EnvironmentIdentity{}, errors.New("created RuntimeEnvironment has no UID")
	}
	if created.Spec.RunnableRevisionRef.ID != request.RunnableRevisionRef.ID || created.Spec.RunnableRevisionRef.Digest != request.RunnableRevisionDigest || created.Spec.Purpose != runtimev2.PurposeVerification {
		return runnable.EnvironmentIdentity{}, errors.New("existing RuntimeEnvironment is bound to another runnable revision")
	}
	for {
		current, err := p.client.GetRuntimeEnvironment(ctx, p.namespace, created.Name)
		if err != nil {
			return runnable.EnvironmentIdentity{}, fmt.Errorf("observe runnable verification environment: %w", err)
		}
		if current.UID != created.UID {
			return runnable.EnvironmentIdentity{}, errors.New("RuntimeEnvironment UID changed during verification")
		}
		switch current.Status.Phase {
		case runtimev2.PhaseReady:
			if current.Status.Runtime.ProfileDigest != profileDigest {
				return runnable.EnvironmentIdentity{}, errors.New("RuntimeEnvironment returned another profile")
			}
			if current.Status.Runtime.Provider != string(request.RunnableRevision.Spec.RuntimeProfile.Runtime) {
				return runnable.EnvironmentIdentity{}, errors.New("RuntimeEnvironment returned another provider")
			}
			identity := runnable.EnvironmentIdentity{ID: string(current.UID), Provider: current.Status.Runtime.Provider, ProfileDigest: profileDigest}
			p.mu.Lock()
			p.retained[identity.ID] = current.DeepCopy()
			p.mu.Unlock()
			return identity, nil
		case runtimev2.PhaseFailed:
			if current.Status.Failure == nil {
				return runnable.EnvironmentIdentity{}, errors.New("runnable verification environment failed without a failure detail")
			}
			return runnable.EnvironmentIdentity{}, fmt.Errorf("runnable verification environment failed: %s", current.Status.Failure.Message)
		}
		select {
		case <-ctx.Done():
			return runnable.EnvironmentIdentity{}, ctx.Err()
		case <-time.After(p.pollEvery):
		}
	}
}

func (p *VerificationProvider) Execute(ctx context.Context, identity runnable.EnvironmentIdentity, request runnableworker.ExecutionRequest) (runnableworker.ExecutionOutput, error) {
	if err := identity.Validate(); err != nil {
		return runnableworker.ExecutionOutput{}, err
	}
	if err := request.Target.Validate("execution.target"); err != nil {
		return runnableworker.ExecutionOutput{}, err
	}
	if err := request.Boundary.Validate(); err != nil || request.Boundary.Target != request.Target || request.Timeout <= 0 || request.Timeout > time.Duration(request.Boundary.MaxTimeout)*time.Second || request.ReadOnly != (request.Boundary.Permission == runnable.PermissionReadOnly) {
		return runnableworker.ExecutionOutput{}, errors.New("verification execution request exceeds its approved boundary")
	}
	p.mu.Lock()
	environment := p.retained[identity.ID]
	p.mu.Unlock()
	if environment == nil {
		return runnableworker.ExecutionOutput{}, errors.New("verification environment is not retained by provider")
	}
	entrypoint, err := runtimeEntrypoint(request.Entrypoint)
	if err != nil {
		return runnableworker.ExecutionOutput{}, err
	}
	if request.Target.Kind == "management" && identity.Provider == string(runnable.RuntimeK8s) {
		terminal := endpointRef(environment.Status.Runtime.EndpointRefs, "terminal")
		namespace, pod, ok := strings.Cut(terminal, "/")
		if !ok || namespace == "" || pod == "" {
			return runnableworker.ExecutionOutput{}, errors.New("K8s verification environment has no terminal endpoint")
		}
		result, err := p.client.ExecInPodStreamsContext(ctx, namespace, pod, 256*1024, "/bin/bash", entrypoint)
		if err != nil {
			return runnableworker.ExecutionOutput{}, err
		}
		return runnableworker.ExecutionOutput{ExitCode: result.ExitCode, Summary: strings.TrimSpace(result.Stderr), Raw: []byte(result.Stdout), Stderr: []byte(result.Stderr)}, nil
	}
	if identity.Provider != string(runnable.RuntimeNode) || p.node == nil || request.Target.Kind != "node" {
		return runnableworker.ExecutionOutput{}, errors.New("verification target is not available in this environment")
	}
	project := resourceRef(environment.Status.Runtime.ResourceRefs, "project")
	network := resourceRef(environment.Status.Runtime.ResourceRefs, "network")
	acl := resourceRef(environment.Status.Runtime.ResourceRefs, "acl")
	profile := resourceRef(environment.Status.Runtime.ResourceRefs, "profile")
	instance := resourceRef(environment.Status.Runtime.ResourceRefs, "instance:"+request.Target.ID)
	if instance == "" {
		return runnableworker.ExecutionOutput{}, fmt.Errorf("Node verification environment has no instance for target %q", request.Target.ID)
	}
	nodeIdentity := incus.NodeEnvironmentIdentity{Project: project, Network: network, ACL: acl, Profile: profile, Nodes: []incus.NodeIdentity{{LogicalName: request.Target.ID, InstanceName: instance}}}
	result, err := p.node.ExecNode(ctx, incus.ExecNodeRequest{EnvironmentUID: string(environment.UID), Revision: environment.Spec.RunnableRevisionRef.Digest, Identity: nodeIdentity, LogicalName: request.Target.ID, Command: []string{"/bin/bash", entrypoint}})
	if err != nil {
		return runnableworker.ExecutionOutput{}, err
	}
	return runnableworker.ExecutionOutput{ExitCode: result.ExitCode, Summary: strings.TrimSpace(result.Stderr), Raw: []byte(result.Stdout), Stderr: []byte(result.Stderr)}, nil
}

func runtimeEntrypoint(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || path.IsAbs(value) || value == "." || strings.HasPrefix(value, "../") || strings.Contains(value, "/../") || strings.Contains(value, "\\") {
		return "", errors.New("runnable execution entrypoint must be a safe relative path")
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("runnable execution entrypoint escapes the artifact root")
	}
	return path.Join("/opt/breakfix/runnable", clean), nil
}

func endpointRef(refs []runtimev2.EndpointReference, name string) string {
	for _, ref := range refs {
		if ref.Name == name {
			return ref.Ref
		}
	}
	return ""
}

func resourceRef(refs []runtimev2.ResourceReference, kind string) string {
	for _, ref := range refs {
		if ref.Kind == kind || (kind == "instance" && strings.HasPrefix(ref.Kind, "instance:")) {
			return ref.ID
		}
	}
	return ""
}

var _ runnableworker.EnvironmentProvider = (*VerificationProvider)(nil)
