package server

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/breakfix/breakfix/internal/config"
)

func TestEnvironmentAndAssistantRoutesRegistered(t *testing.T) {
	router, err := SetupRouter(context.Background(), nil, nil, config.Config{}, nil)
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
		"GET /api/challenges/:id/assistant/turns/:turnID/events",
		"POST /api/internal/agent-runs/:id/assistant/context",
		"POST /api/internal/agent-runs/:id/assistant/tools/:tool",
		"POST /api/internal/agent-runs/:id/assistant/events",
		"POST /api/internal/agent-runs/:id/authoring/context",
		"POST /api/internal/agent-runs/:id/authoring/stage",
		"POST /api/internal/agent-runs/:id/authoring/finalize",
		"GET /readyz",
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

func TestSetupRouterRefusesMalformedChallengeCatalog(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "challenges", "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "challenges", "broken", "challenge.yaml"), []byte("id: broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SetupRouter(context.Background(), nil, nil, config.Config{DataDir: root}, nil); err == nil {
		t.Fatal("SetupRouter accepted a malformed challenge catalog")
	}
}
