package server

import (
	"context"
	"fmt"

	"github.com/breakfix/breakfix/internal/challenge"
)

// validateStartup keeps the catalog in strict mode: a malformed published
// directory prevents Server startup instead of making unrelated catalog
// entries disappear or failing individual requests unpredictably.
func (h *Handler) validateStartup() error {
	if h == nil {
		return fmt.Errorf("server handler is not configured")
	}
	if h.startupErr != nil {
		return h.startupErr
	}
	if _, err := challenge.List(h.challengesDir); err != nil {
		return fmt.Errorf("validate challenge catalog: %w", err)
	}
	return nil
}

// validateReadiness defines the Server's core Kubernetes readiness boundary.
// A live Node provider is deliberately not part of it: Incus being unavailable
// must not withdraw catalog, authoring, or VK8s traffic from the Service.
func (h *Handler) validateReadiness(_ context.Context) error {
	return h.validateStartup()
}

// validateNodeProviderCapability exposes the live Node provider contract
// without turning that optional runtime into a process-wide readiness gate.
func (h *Handler) validateNodeProviderCapability(ctx context.Context) error {
	if h.nodeProviderReady == nil {
		return fmt.Errorf("node provider readiness is not configured")
	}
	if _, err := h.nodeProviderReady.Preflight(ctx); err != nil {
		return fmt.Errorf("preflight node provider: %w", err)
	}
	return nil
}
