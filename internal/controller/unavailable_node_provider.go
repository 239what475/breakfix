package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/breakfix/breakfix/internal/incusprovider"
)

// UnavailableNodeProvider keeps the Controller and VK8s reconciler available
// when Incus cannot be reached. NodeEnvironment reconciliation records the
// returned ErrUnavailable as a retryable ProviderUnavailable condition.
func UnavailableNodeProvider(cause error) NodeEnvironmentProvider {
	if cause == nil {
		cause = errors.New("incus node provider is not configured")
	}
	return unavailableNodeProvider{cause: cause}
}

type unavailableNodeProvider struct {
	cause error
}

func (p unavailableNodeProvider) unavailable() error {
	if errors.Is(p.cause, incusprovider.ErrUnavailable) {
		return p.cause
	}
	return fmt.Errorf("%w: %v", incusprovider.ErrUnavailable, p.cause)
}

func (p unavailableNodeProvider) Preflight(context.Context) (incusprovider.PreflightResult, error) {
	return incusprovider.PreflightResult{}, p.unavailable()
}

func (p unavailableNodeProvider) NodeEnvironmentIdentity(string, []string) (incusprovider.NodeEnvironmentIdentity, error) {
	return incusprovider.NodeEnvironmentIdentity{}, p.unavailable()
}

func (p unavailableNodeProvider) ProvisionNodeEnvironment(context.Context, incusprovider.ProvisionNodeEnvironmentRequest) (incusprovider.NodeEnvironmentObservation, error) {
	return incusprovider.NodeEnvironmentObservation{}, p.unavailable()
}

func (p unavailableNodeProvider) ObserveNodeEnvironment(context.Context, incusprovider.ProvisionNodeEnvironmentRequest) (incusprovider.NodeEnvironmentObservation, error) {
	return incusprovider.NodeEnvironmentObservation{}, p.unavailable()
}

func (p unavailableNodeProvider) DeleteNodeEnvironment(context.Context, incusprovider.ProvisionNodeEnvironmentRequest) error {
	return p.unavailable()
}

func (p unavailableNodeProvider) ExecNode(context.Context, incusprovider.ExecNodeRequest) (incusprovider.ExecNodeResult, error) {
	return incusprovider.ExecNodeResult{}, p.unavailable()
}
