package httpapi

import (
	"context"
	"testing"

	"github.com/breakfix/breakfix/internal/bootstrap/config"
)

func TestSetupRouterDoesNotRecoverAgentRuns(t *testing.T) {
	handler, err := NewHandlerWithDependencies(nil, nil, config.Config{Registry: config.RegistryConfig{Repository: "registry.example.com/breakfix"}}, Dependencies{
		AgentRuntimeContext: context.Background(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetupRouter(handler, config.Config{Registry: config.RegistryConfig{Repository: "registry.example.com/breakfix"}}, nil); err != nil {
		t.Fatal(err)
	}
	// A nil database deliberately makes recovery impossible. Router assembly
	// still succeeds because it does not execute any recovery or background work.
}
