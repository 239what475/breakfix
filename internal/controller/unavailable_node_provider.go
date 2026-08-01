package controller

import (
	"context"
	"errors"
	"fmt"

	"github.com/breakfix/breakfix/internal/adapter/incus"
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
	if errors.Is(p.cause, incus.ErrUnavailable) {
		return p.cause
	}
	return fmt.Errorf("%w: %v", incus.ErrUnavailable, p.cause)
}

func (p unavailableNodeProvider) Preflight(context.Context) (incus.PreflightResult, error) {
	return incus.PreflightResult{}, p.unavailable()
}

func (p unavailableNodeProvider) NodeEnvironmentIdentity(string, []string) (incus.NodeEnvironmentIdentity, error) {
	return incus.NodeEnvironmentIdentity{}, p.unavailable()
}

func (p unavailableNodeProvider) ProvisionNodeEnvironment(context.Context, incus.ProvisionNodeEnvironmentRequest) (incus.NodeEnvironmentObservation, error) {
	return incus.NodeEnvironmentObservation{}, p.unavailable()
}

func (p unavailableNodeProvider) ObserveNodeEnvironment(context.Context, incus.ProvisionNodeEnvironmentRequest) (incus.NodeEnvironmentObservation, error) {
	return incus.NodeEnvironmentObservation{}, p.unavailable()
}

func (p unavailableNodeProvider) DeleteNodeEnvironment(context.Context, incus.ProvisionNodeEnvironmentRequest) error {
	return p.unavailable()
}

func (p unavailableNodeProvider) ExecNode(context.Context, incus.ExecNodeRequest) (incus.ExecNodeResult, error) {
	return incus.ExecNodeResult{}, p.unavailable()
}
