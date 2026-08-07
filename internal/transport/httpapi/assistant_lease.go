package httpapi

import (
	"context"
	"errors"
	"fmt"
)

// RenewAssistantEnvironmentLease implements the Assistant application port.
// A missing Environment is expected when deletion concurrently fences a run;
// provider failures still return an error to the lifecycle service.
func (h *Handler) RenewAssistantEnvironmentLease(ctx context.Context, userID, environmentUID string) error {
	if h == nil || h.k8s == nil {
		return fmt.Errorf("assistant environment lease renewer is not configured")
	}
	environment, err := h.findActiveEnvironmentByUID(ctx, userID, environmentUID)
	if errors.Is(err, errNoActiveAssistantEnvironment) {
		return nil
	}
	if err != nil {
		return err
	}
	adapter, err := h.environmentRuntimeAdapter(environment.Runtime)
	if err != nil {
		return err
	}
	if err := adapter.renewActivity(ctx, environment.Name, nowActivity()); err != nil {
		return err
	}
	return nil
}
