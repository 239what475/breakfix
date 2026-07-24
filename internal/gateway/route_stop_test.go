package gateway

import (
	"net/http/httptest"
	"testing"

	"github.com/breakfix/breakfix/internal/config"
)

func TestEnvironmentAndAssistantRoutesRegistered(t *testing.T) {
	router := SetupRouter(nil, nil, config.Config{}, nil)

	routes := map[string]bool{}
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, expected := range []string{
		"POST /api/challenges/:id/stop",
		"GET /api/challenges/:id/assistant",
		"POST /api/challenges/:id/assistant/messages",
		"GET /api/challenges/:id/assistant/turns/:turnID/events",
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
