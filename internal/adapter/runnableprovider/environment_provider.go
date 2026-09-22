package runnableprovider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/controller/runtimeenvironment"
	"github.com/breakfix/breakfix/internal/domain/environment"
	"github.com/breakfix/breakfix/internal/domain/runnable"
)

// NodeEnvironmentProvider is the provider-specific Node lifecycle capability
// used by the public RuntimeEnvironment adapter.
type NodeEnvironmentProvider interface {
	NodeEnvironmentIdentity(string, []string) (incus.NodeEnvironmentIdentity, error)
	ProvisionNodeEnvironment(context.Context, incus.ProvisionNodeEnvironmentRequest) (incus.NodeEnvironmentObservation, error)
	DeleteNodeEnvironment(context.Context, incus.ProvisionNodeEnvironmentRequest) error
}

// K8sEnvironmentProvider is the provider-specific virtual Kubernetes
// lifecycle capability used by the public RuntimeEnvironment adapter.
type K8sEnvironmentProvider interface {
	Identity(string) (environment.VK8sEnvironmentIdentity, error)
	Provision(context.Context, environment.VK8sProvisionRequest) (environment.VK8sEnvironmentObservation, error)
	Delete(context.Context, environment.VK8sProvisionRequest) (bool, error)
}

// NodeEnvironmentConfig is the immutable platform profile selected by a
// public RuntimeProfile.ProfileRevision. The adapter rejects a revision whose
// frozen public profile does not select this installed profile.
type NodeEnvironmentConfig struct {
	ProfileRevision       string
	NetworkPolicyRevision string
	Resources             incus.NodeEnvironmentResources
}

// EnvironmentProviderConfig provides installed provider profiles. The
// RunnableRevision remains authoritative for its selected profile revision,
// artifact, and lifecycle policy; these values supply only the concrete SDK
// representation for an installed platform profile.
type EnvironmentProviderConfig struct {
	Node NodeEnvironmentConfig
	K8s  environment.VK8sRuntime
}

// EnvironmentProvider implements the public RuntimeEnvironment provider
// port. It contains no content aggregate, workflow, or publication state.
type EnvironmentProvider struct {
	node   NodeEnvironmentProvider
	k8s    K8sEnvironmentProvider
	config EnvironmentProviderConfig
}

func NewEnvironmentProvider(node NodeEnvironmentProvider, k8s K8sEnvironmentProvider, config EnvironmentProviderConfig) (*EnvironmentProvider, error) {
	if node == nil || k8s == nil {
		return nil, errors.New("runnable environment provider requires Node and K8s providers")
	}
	if strings.TrimSpace(config.Node.ProfileRevision) == "" || strings.TrimSpace(config.Node.NetworkPolicyRevision) == "" || strings.TrimSpace(config.K8s.ProfileRevision) == "" {
		return nil, errors.New("runnable environment provider requires installed runtime profile revisions")
	}
	return &EnvironmentProvider{node: node, k8s: k8s, config: config}, nil
}

func (p *EnvironmentProvider) Provision(ctx context.Context, binding runtimeenvironment.Binding) (runtimeenvironment.Observation, error) {
	if binding.BlankRuntime == nil {
		if err := binding.RunnableRevision.Validate(); err != nil {
			return runtimeenvironment.Observation{}, runnable.NewArtifactFailure("runnable-revision-invalid", err.Error())
		}
	}
	switch p.bindingRuntime(binding) {
	case runnable.RuntimeNode:
		return p.provisionNode(ctx, binding)
	case runnable.RuntimeK8s:
		return p.provisionK8s(ctx, binding)
	default:
		return runtimeenvironment.Observation{}, runnable.NewArtifactFailure("runtime-unsupported", "runnable runtime is unsupported")
	}
}

// Reset removes only the stable resource set bound to this revision, then
// recreates it from the same immutable artifact. A K8s namespace may be
// asynchronous to delete, in which case the Controller will retry Reset; the
// request carries the reset generation so a retry adopts the rebuild it
// started instead of deleting it again.
func (p *EnvironmentProvider) Reset(ctx context.Context, binding runtimeenvironment.Binding) (runtimeenvironment.Observation, error) {
	switch p.bindingRuntime(binding) {
	case runnable.RuntimeNode:
		request, err := p.nodeRequest(binding)
		if err != nil {
			return runtimeenvironment.Observation{}, err
		}
		request.ResetNonce = binding.ResetNonce
		if err := p.node.DeleteNodeEnvironment(ctx, request); err != nil {
			if errors.Is(err, environment.ErrProviderNotFound) {
				return p.provisionNodeRequest(ctx, request)
			}
			return runtimeenvironment.Observation{}, fmt.Errorf("reset Node environment: %w", err)
		}
		return p.provisionNodeRequest(ctx, request)
	case runnable.RuntimeK8s:
		request, err := p.k8sRequest(binding)
		if err != nil {
			return runtimeenvironment.Observation{}, err
		}
		request.ResetNonce = binding.ResetNonce
		done, err := p.k8s.Delete(ctx, request)
		if err != nil {
			return runtimeenvironment.Observation{}, fmt.Errorf("reset K8s environment: %w", err)
		}
		if !done {
			return runtimeenvironment.Observation{}, nil
		}
		return p.provisionK8sRequest(ctx, request)
	default:
		return runtimeenvironment.Observation{}, runnable.NewArtifactFailure("runtime-unsupported", "runnable runtime is unsupported")
	}
}

// Stop is deliberately idempotent. These isolated resource types have no
// separate durable stop state; Release below is the authoritative teardown.
func (p *EnvironmentProvider) Stop(_ context.Context, binding runtimeenvironment.Binding) (bool, error) {
	if binding.BlankRuntime == nil {
		if err := binding.RunnableRevision.Validate(); err != nil {
			return false, runnable.NewArtifactFailure("runnable-revision-invalid", err.Error())
		}
	}
	return true, nil
}

func (p *EnvironmentProvider) Release(ctx context.Context, binding runtimeenvironment.Binding) (bool, error) {
	switch p.bindingRuntime(binding) {
	case runnable.RuntimeNode:
		request, err := p.nodeRequest(binding)
		if err != nil {
			return false, err
		}
		if err := p.node.DeleteNodeEnvironment(ctx, request); err != nil {
			if errors.Is(err, environment.ErrProviderNotFound) {
				return true, nil
			}
			return false, fmt.Errorf("release Node environment: %w", err)
		}
		return true, nil
	case runnable.RuntimeK8s:
		request, err := p.k8sReleaseRequest(binding)
		if err != nil {
			return false, err
		}
		done, err := p.k8s.Delete(ctx, request)
		if err != nil {
			return false, fmt.Errorf("release K8s environment: %w", err)
		}
		return done, nil
	default:
		return false, runnable.NewArtifactFailure("runtime-unsupported", "runnable runtime is unsupported")
	}
}

func (p *EnvironmentProvider) bindingRuntime(binding runtimeenvironment.Binding) runnable.Runtime {
	if binding.BlankRuntime != nil {
		return binding.BlankRuntime.Profile.Runtime
	}
	return binding.RunnableRevision.Spec.RuntimeProfile.Runtime
}

func (p *EnvironmentProvider) provisionNode(ctx context.Context, binding runtimeenvironment.Binding) (runtimeenvironment.Observation, error) {
	request, err := p.nodeRequest(binding)
	if err != nil {
		return runtimeenvironment.Observation{}, err
	}
	return p.provisionNodeRequest(ctx, request)
}

func (p *EnvironmentProvider) provisionNodeRequest(ctx context.Context, request incus.ProvisionNodeEnvironmentRequest) (runtimeenvironment.Observation, error) {
	observation, err := p.node.ProvisionNodeEnvironment(ctx, request)
	if err != nil {
		return runtimeenvironment.Observation{}, fmt.Errorf("provision Node environment: %w", err)
	}
	refs := []runtimev2.ResourceReference{
		{Provider: "incus", Kind: "project", ID: observation.Identity.Project},
		{Provider: "incus", Kind: "network", ID: observation.Identity.Network},
		{Provider: "incus", Kind: "acl", ID: observation.Identity.ACL},
		{Provider: "incus", Kind: "profile", ID: observation.Identity.Profile},
	}
	endpoints := make([]runtimev2.EndpointReference, 0, len(observation.Identity.Nodes))
	for _, node := range observation.Identity.Nodes {
		refs = append(refs, runtimev2.ResourceReference{Provider: "incus", Kind: "instance:" + node.LogicalName, ID: node.InstanceName})
		if strings.TrimSpace(node.Address) != "" {
			endpoints = append(endpoints, runtimev2.EndpointReference{Name: node.LogicalName, Ref: node.Address})
		}
	}
	return runtimeenvironment.Observation{Ready: observation.Ready, ResourceRefs: refs, EndpointRefs: endpoints}, nil
}

func (p *EnvironmentProvider) provisionK8s(ctx context.Context, binding runtimeenvironment.Binding) (runtimeenvironment.Observation, error) {
	request, err := p.k8sRequest(binding)
	if err != nil {
		return runtimeenvironment.Observation{}, err
	}
	return p.provisionK8sRequest(ctx, request)
}

func (p *EnvironmentProvider) provisionK8sRequest(ctx context.Context, request environment.VK8sProvisionRequest) (runtimeenvironment.Observation, error) {
	observation, err := p.k8s.Provision(ctx, request)
	if err != nil {
		return runtimeenvironment.Observation{}, fmt.Errorf("provision K8s environment: %w", err)
	}
	refs := []runtimev2.ResourceReference{
		{Provider: "k8s", Kind: "namespace", ID: request.Identity.Namespace},
		{Provider: "k8s", Kind: "vcluster", ID: request.Identity.VClusterName},
		{Provider: "k8s", Kind: "pod", ID: request.Identity.Namespace + "/" + request.Identity.TerminalPodName},
	}
	endpoints := []runtimev2.EndpointReference{{Name: "kubeconfig", Ref: request.Identity.Namespace + "/" + request.Identity.KubeconfigSecretName}, {Name: "terminal", Ref: request.Identity.Namespace + "/" + request.Identity.TerminalPodName}}
	return runtimeenvironment.Observation{Ready: observation.ControlPlaneReady && observation.KubeconfigReady && observation.TerminalReady, ResourceRefs: refs, EndpointRefs: endpoints}, nil
}

func (p *EnvironmentProvider) nodeRequest(binding runtimeenvironment.Binding) (incus.ProvisionNodeEnvironmentRequest, error) {
	revision := binding.RunnableRevision
	profile := revision.Spec.RuntimeProfile
	if profile.Runtime != runnable.RuntimeNode || profile.ProfileRevision != p.config.Node.ProfileRevision {
		return incus.ProvisionNodeEnvironmentRequest{}, runnable.NewArtifactFailure("node-profile", "Node runnable profile is not installed")
	}
	if revision.Artifact.Runtime != runnable.RuntimeNode || !runnable.ValidDigest(revision.Artifact.ArtifactDigest) {
		return incus.ProvisionNodeEnvironmentRequest{}, runnable.NewArtifactFailure("node-artifact", "Node runnable artifact is invalid")
	}
	names, err := nodeNames(profile)
	if err != nil {
		return incus.ProvisionNodeEnvironmentRequest{}, err
	}
	identity, err := p.node.NodeEnvironmentIdentity(binding.UID, names)
	if err != nil {
		return incus.ProvisionNodeEnvironmentRequest{}, fmt.Errorf("derive Node environment identity: %w", err)
	}
	revisionDigest, err := revision.Digest()
	if err != nil {
		return incus.ProvisionNodeEnvironmentRequest{}, err
	}
	return incus.ProvisionNodeEnvironmentRequest{
		EnvironmentUID: binding.UID, Revision: revisionDigest,
		ImageFingerprint: strings.TrimPrefix(revision.Artifact.ArtifactDigest, "sha256:"),
		ProfileRevision:  profile.ProfileRevision, NetworkPolicyRevision: p.config.Node.NetworkPolicyRevision,
		Identity: identity, Resources: p.config.Node.Resources,
	}, nil
}

func (p *EnvironmentProvider) k8sRequest(binding runtimeenvironment.Binding) (environment.VK8sProvisionRequest, error) {
	if binding.BlankRuntime != nil {
		return p.blankK8sRequest(binding)
	}
	revision := binding.RunnableRevision
	profile := revision.Spec.RuntimeProfile
	if profile.Runtime != runnable.RuntimeK8s || profile.ProfileRevision != p.config.K8s.ProfileRevision {
		return environment.VK8sProvisionRequest{}, runnable.NewArtifactFailure("k8s-profile", "K8s runnable profile is not installed")
	}
	if revision.Artifact.Runtime != runnable.RuntimeK8s || revision.Artifact.ProviderReference == "" {
		return environment.VK8sProvisionRequest{}, runnable.NewArtifactFailure("k8s-artifact", "K8s runnable artifact is invalid")
	}
	identity, err := p.k8s.Identity(binding.UID)
	if err != nil {
		return environment.VK8sProvisionRequest{}, fmt.Errorf("derive K8s environment identity: %w", err)
	}
	revisionDigest, err := revision.Digest()
	if err != nil {
		return environment.VK8sProvisionRequest{}, err
	}
	runtime := p.config.K8s
	runtime.ImageDigest = revision.Artifact.ProviderReference
	purpose, err := environmentPurpose(binding.Purpose)
	if err != nil {
		return environment.VK8sProvisionRequest{}, err
	}
	return environment.VK8sProvisionRequest{EnvironmentUID: binding.UID, Revision: revisionDigest, Purpose: purpose, Identity: identity, Runtime: runtime}, nil
}

// blankK8sRequest freezes the blank terminal runtime from the installed plan:
// the plan's image replaces the runnable artifact reference and the request is
// marked blank so the provider provisions the terminal without content.
func (p *EnvironmentProvider) blankK8sRequest(binding runtimeenvironment.Binding) (environment.VK8sProvisionRequest, error) {
	plan := binding.BlankRuntime
	if err := plan.Validate(); err != nil {
		return environment.VK8sProvisionRequest{}, runnable.NewArtifactFailure("blank-plan-invalid", err.Error())
	}
	if plan.Profile.Runtime != runnable.RuntimeK8s || plan.Profile.ProfileRevision != p.config.K8s.ProfileRevision {
		return environment.VK8sProvisionRequest{}, runnable.NewArtifactFailure("k8s-profile", "K8s blank profile is not installed")
	}
	identity, err := p.k8s.Identity(binding.UID)
	if err != nil {
		return environment.VK8sProvisionRequest{}, fmt.Errorf("derive K8s environment identity: %w", err)
	}
	planDigest, err := plan.Digest()
	if err != nil {
		return environment.VK8sProvisionRequest{}, err
	}
	runtime := p.config.K8s
	runtime.ImageDigest = plan.Image
	purpose, err := environmentPurpose(binding.Purpose)
	if err != nil {
		return environment.VK8sProvisionRequest{}, err
	}
	return environment.VK8sProvisionRequest{EnvironmentUID: binding.UID, Revision: planDigest, Purpose: purpose, Identity: identity, Runtime: runtime, Blank: true}, nil
}

// k8sReleaseRequest is the identity-driven teardown request. Blank release
// deliberately skips plan validation and the plan digest fence: a plan change
// across a controller upgrade must not block cleanup of live environments.
func (p *EnvironmentProvider) k8sReleaseRequest(binding runtimeenvironment.Binding) (environment.VK8sProvisionRequest, error) {
	if binding.BlankRuntime == nil {
		return p.k8sRequest(binding)
	}
	identity, err := p.k8s.Identity(binding.UID)
	if err != nil {
		return environment.VK8sProvisionRequest{}, fmt.Errorf("derive K8s environment identity: %w", err)
	}
	purpose, err := environmentPurpose(binding.Purpose)
	if err != nil {
		return environment.VK8sProvisionRequest{}, err
	}
	return environment.VK8sProvisionRequest{EnvironmentUID: binding.UID, Purpose: purpose, Identity: identity, Runtime: p.config.K8s, Blank: true}, nil
}

func environmentPurpose(purpose runnable.EnvironmentPurpose) (environment.Purpose, error) {
	switch purpose {
	case runnable.PurposeLearning:
		return environment.PurposeLearning, nil
	case runnable.PurposeVerification:
		return environment.PurposeVerification, nil
	default:
		return "", runnable.NewArtifactFailure("environment-purpose", "runtime environment purpose is invalid")
	}
}

func nodeNames(profile runnable.RuntimeProfile) ([]string, error) {
	seen := make(map[string]struct{})
	for _, boundary := range profile.ExecutionBoundaries {
		if boundary.Target.Kind != "node" {
			continue
		}
		seen[boundary.Target.ID] = struct{}{}
	}
	if len(seen) == 0 {
		return nil, runnable.NewArtifactFailure("node-targets", "Node runnable profile has no node execution targets")
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

var _ runtimeenvironment.Provider = (*EnvironmentProvider)(nil)
var _ runtimeenvironment.ReapProvider = (*EnvironmentProvider)(nil)
