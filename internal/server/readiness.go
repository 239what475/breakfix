package server

import (
	"fmt"

	"github.com/breakfix/breakfix/internal/challenge"
)

// validateReadiness keeps the catalog in strict mode: a malformed published
// directory prevents Server startup and readiness instead of making unrelated
// catalog entries disappear or failing individual requests unpredictably.
func (h *Handler) validateReadiness() error {
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
