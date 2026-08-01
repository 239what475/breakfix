package nodeenvironment

import (
	"context"
	"errors"
	"fmt"

	"github.com/breakfix/breakfix/internal/domain/environment"
)

// UnavailableProvider lets the controller start when Incus is unavailable.
// Reconciliation records the condition and retries after the provider recovers.
func UnavailableProvider(cause error) environment.NodeProvider {
	if cause == nil {
		cause = errors.New("incus node provider is not configured")
	}
	return unavailableNodeProvider{cause: cause}
}

type unavailableNodeProvider struct {
	cause error
}

func (p unavailableNodeProvider) unavailable() error {
	if errors.Is(p.cause, environment.ErrProviderUnavailable) {
		return p.cause
	}
	return fmt.Errorf("%w: %v", environment.ErrProviderUnavailable, p.cause)
}

func (p unavailableNodeProvider) Preflight(context.Context) error {
	return p.unavailable()
}

func (p unavailableNodeProvider) Identity(string, []string) (environment.NodeEnvironmentIdentity, error) {
	return environment.NodeEnvironmentIdentity{}, p.unavailable()
}

func (p unavailableNodeProvider) Provision(context.Context, environment.NodeProvisionRequest) (environment.NodeEnvironmentObservation, error) {
	return environment.NodeEnvironmentObservation{}, p.unavailable()
}

func (p unavailableNodeProvider) Observe(context.Context, environment.NodeProvisionRequest) (environment.NodeEnvironmentObservation, error) {
	return environment.NodeEnvironmentObservation{}, p.unavailable()
}

func (p unavailableNodeProvider) Delete(context.Context, environment.NodeProvisionRequest) error {
	return p.unavailable()
}

func (p unavailableNodeProvider) Execute(context.Context, environment.NodeExecutionRequest) (environment.ExecutionResult, error) {
	return environment.ExecutionResult{}, p.unavailable()
}
