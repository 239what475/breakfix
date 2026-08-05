package httpapi

import (
	"context"
	"fmt"
)

// validateStartup keeps the catalog in strict mode: a Roadmap binding that no
// longer matches its materialized source prevents Server startup instead of
// making the damaged challenge silently disappear from reads.
func (h *Handler) validateStartup() error {
	if h == nil {
		return fmt.Errorf("server handler is not configured")
	}
	if h.db == nil {
		return nil
	}
	if h.catalog == nil {
		return fmt.Errorf("catalog service is not configured")
	}
	if err := h.catalog.CheckIntegrity(context.Background()); err != nil {
		return fmt.Errorf("validate challenge catalog: %w", err)
	}
	return nil
}

// validateReadiness defines the Server's core Kubernetes readiness boundary.
// It reuses the full materialized Catalog integrity check without waiting for a
// configured release to become available, so a first Catalog bootstrap cannot
// deadlock Server and Runtime Worker startup. A live Node provider is likewise
// not part of this boundary.
func (h *Handler) validateReadiness(ctx context.Context) error {
	if h == nil {
		return fmt.Errorf("server handler is not configured")
	}
	if h.db == nil {
		return nil
	}
	if h.catalog == nil {
		return fmt.Errorf("catalog service is not configured")
	}
	if err := h.catalog.Readiness(ctx); err != nil {
		return fmt.Errorf("validate challenge catalog readiness: %w", err)
	}
	return nil
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
