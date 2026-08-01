package incus

import (
	"context"
	"errors"
	"fmt"

	"github.com/breakfix/breakfix/internal/domain/environment"
)

// NodeEnvironmentProvider adapts the Incus client to the environment domain
// port consumed by the NodeEnvironment reconciler.
type NodeEnvironmentProvider struct {
	client *ReconnectableClient
}

func NewNodeEnvironmentProvider(client *ReconnectableClient) *NodeEnvironmentProvider {
	return &NodeEnvironmentProvider{client: client}
}

func (p *NodeEnvironmentProvider) Preflight(ctx context.Context) error {
	if p == nil || p.client == nil {
		return environment.ErrProviderUnavailable
	}
	_, err := p.client.Preflight(ctx)
	return mapNodeProviderError(err)
}

func (p *NodeEnvironmentProvider) Identity(environmentUID string, logicalNames []string) (environment.NodeEnvironmentIdentity, error) {
	if p == nil || p.client == nil {
		return environment.NodeEnvironmentIdentity{}, environment.ErrProviderUnavailable
	}
	identity, err := p.client.NodeEnvironmentIdentity(environmentUID, logicalNames)
	if err != nil {
		return environment.NodeEnvironmentIdentity{}, mapNodeProviderError(err)
	}
	return nodeIdentityToDomain(identity), nil
}

func (p *NodeEnvironmentProvider) Provision(ctx context.Context, request environment.NodeProvisionRequest) (environment.NodeEnvironmentObservation, error) {
	if p == nil || p.client == nil {
		return environment.NodeEnvironmentObservation{}, environment.ErrProviderUnavailable
	}
	observation, err := p.client.ProvisionNodeEnvironment(ctx, nodeProvisionRequestFromDomain(request))
	if err != nil {
		return environment.NodeEnvironmentObservation{}, mapNodeProviderError(err)
	}
	return nodeObservationToDomain(observation), nil
}

func (p *NodeEnvironmentProvider) Observe(ctx context.Context, request environment.NodeProvisionRequest) (environment.NodeEnvironmentObservation, error) {
	if p == nil || p.client == nil {
		return environment.NodeEnvironmentObservation{}, environment.ErrProviderUnavailable
	}
	observation, err := p.client.ObserveNodeEnvironment(ctx, nodeProvisionRequestFromDomain(request))
	if err != nil {
		return environment.NodeEnvironmentObservation{}, mapNodeProviderError(err)
	}
	return nodeObservationToDomain(observation), nil
}

func (p *NodeEnvironmentProvider) Delete(ctx context.Context, request environment.NodeProvisionRequest) error {
	if p == nil || p.client == nil {
		return environment.ErrProviderUnavailable
	}
	return mapNodeProviderError(p.client.DeleteNodeEnvironment(ctx, nodeProvisionRequestFromDomain(request)))
}

func (p *NodeEnvironmentProvider) Execute(ctx context.Context, request environment.NodeExecutionRequest) (environment.ExecutionResult, error) {
	if p == nil || p.client == nil {
		return environment.ExecutionResult{}, environment.ErrProviderUnavailable
	}
	result, err := p.client.ExecNode(ctx, ExecNodeRequest{
		EnvironmentUID: request.EnvironmentUID,
		Revision:       request.Revision,
		Identity:       nodeIdentityFromDomain(request.Identity),
		LogicalName:    request.LogicalName,
		Command:        append([]string(nil), request.Command...),
	})
	if err != nil {
		return environment.ExecutionResult{}, mapNodeProviderError(err)
	}
	return environment.ExecutionResult{ExitCode: result.ExitCode, Stdout: result.Stdout, Stderr: result.Stderr}, nil
}

func nodeProvisionRequestFromDomain(request environment.NodeProvisionRequest) ProvisionNodeEnvironmentRequest {
	return ProvisionNodeEnvironmentRequest{
		EnvironmentUID:        request.EnvironmentUID,
		Revision:              request.Revision,
		ImageFingerprint:      request.ImageFingerprint,
		ProfileRevision:       request.ProfileRevision,
		NetworkPolicyRevision: request.NetworkPolicyRevision,
		Identity:              nodeIdentityFromDomain(request.Identity),
		Resources: NodeEnvironmentResources{
			CPU: request.Resources.CPU, Memory: request.Resources.Memory,
			Processes: request.Resources.Processes, RootDisk: request.Resources.RootDisk,
		},
	}
}

func nodeIdentityFromDomain(identity environment.NodeEnvironmentIdentity) NodeEnvironmentIdentity {
	result := NodeEnvironmentIdentity{
		Project: identity.Project, Network: identity.Network, ACL: identity.ACL, Profile: identity.Profile,
		Nodes: make([]NodeIdentity, len(identity.Nodes)),
	}
	for index, node := range identity.Nodes {
		result.Nodes[index] = NodeIdentity{LogicalName: node.LogicalName, InstanceName: node.InstanceName, Address: node.Address}
	}
	return result
}

func nodeIdentityToDomain(identity NodeEnvironmentIdentity) environment.NodeEnvironmentIdentity {
	result := environment.NodeEnvironmentIdentity{
		Project: identity.Project, Network: identity.Network, ACL: identity.ACL, Profile: identity.Profile,
		Nodes: make([]environment.NodeIdentity, len(identity.Nodes)),
	}
	for index, node := range identity.Nodes {
		result.Nodes[index] = environment.NodeIdentity{LogicalName: node.LogicalName, InstanceName: node.InstanceName, Address: node.Address}
	}
	return result
}

func nodeObservationToDomain(observation NodeEnvironmentObservation) environment.NodeEnvironmentObservation {
	result := environment.NodeEnvironmentObservation{
		Identity: nodeIdentityToDomain(observation.Identity),
		Nodes:    make([]environment.NodeObservation, len(observation.Nodes)),
		Ready:    observation.Ready,
	}
	for index, node := range observation.Nodes {
		result.Nodes[index] = environment.NodeObservation{
			Node:    environment.NodeIdentity{LogicalName: node.Node.LogicalName, InstanceName: node.Node.InstanceName, Address: node.Node.Address},
			Running: node.Running,
			Initialization: environment.InitializationObservation{
				Complete: node.Initialization.Complete, Failed: node.Initialization.Failed,
				ExitCode: node.Initialization.ExitCode, Message: node.Initialization.Message,
			},
		}
	}
	return result
}

func mapNodeProviderError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrUnavailable):
		return fmt.Errorf("%w: %v", environment.ErrProviderUnavailable, err)
	case errors.Is(err, ErrNotFound):
		return fmt.Errorf("%w: %v", environment.ErrProviderNotFound, err)
	case errors.Is(err, ErrConflict):
		return fmt.Errorf("%w: %v", environment.ErrProviderConflict, err)
	default:
		return err
	}
}
