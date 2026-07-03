package gateway

import (
	"net/http/httptest"
	"testing"

	"github.com/breakfix/breakfix/internal/config"
)

func TestStopRouteRegistered(t *testing.T) {
	router := SetupRouter(nil, nil, config.Config{}, nil)

	found := false
	for _, route := range router.Routes() {
		if route.Method == "POST" && route.Path == "/api/challenges/:id/stop" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("stop route not registered")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/challenges/demo/stop", nil)
	router.ServeHTTP(rec, req)
	if rec.Code == 200 && rec.Body.String() != "" && rec.Header().Get("Content-Type") == "text/html; charset=utf-8" {
		t.Fatalf("stop route fell through to SPA handler: %s", rec.Body.String())
	}
}
