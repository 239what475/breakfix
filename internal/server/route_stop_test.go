package server

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/breakfix/breakfix/internal/config"
)

func TestEnvironmentAndAssistantRoutesRegistered(t *testing.T) {
	router, err := SetupRouter(context.Background(), nil, nil, config.Config{Registry: config.RegistryConfig{ClientAddress: "registry.example.com"}}, nil, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	routes := map[string]bool{}
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, expected := range []string{
		"POST /api/challenges/:id/stop",
		"POST /api/challenges/:id/terminal-ticket",
		"GET /api/challenges/:id/assistant",
		"POST /api/challenges/:id/assistant/messages",
		"POST /api/internal/generation-workflows/claim",
		"POST /api/internal/generation-workflows/:id/phase",
		"POST /api/internal/taxonomy-workflows/claim",
		"POST /api/internal/taxonomy-workflows/:id/phase",
		"GET /readyz",
		"GET /capabilities/node-provider",
	} {
		if !routes[expected] {
			t.Fatalf("route not registered: %s", expected)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/challenges/demo/stop", nil)
	router.ServeHTTP(rec, req)
	if rec.Code == 200 && rec.Body.String() != "" && rec.Header().Get("Content-Type") == "text/html; charset=utf-8" {
		t.Fatalf("stop route fell through to SPA handler: %s", rec.Body.String())
	}
}
