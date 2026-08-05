package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/transport/httpapi/middleware"
)

func TestEnvironmentAndAssistantRoutesRegistered(t *testing.T) {
	router, err := SetupRouter(context.Background(), nil, nil, config.Config{Registry: config.RegistryConfig{Repository: "registry.example.com/breakfix"}}, nil, Dependencies{})
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
		"POST /api/internal/runtime-actions/claim",
		"POST /api/internal/runtime-actions/:id/build/complete",
		"GET /readyz",
		"GET /capabilities/node-provider",
	} {
		if !routes[expected] {
			t.Fatalf("route not registered: %s", expected)
		}
	}
	for _, forbidden := range []string{
		"POST /internal/debug/roadmap-maintenance",
		"GET /internal/debug/roadmap-revisions/:revision_id/export",
	} {
		if routes[forbidden] {
			t.Fatalf("debug route registered by default: %s", forbidden)
		}
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/challenges/demo/stop", nil)
	router.ServeHTTP(rec, req)
	if rec.Code == 200 && rec.Body.String() != "" && rec.Header().Get("Content-Type") == "text/html; charset=utf-8" {
		t.Fatalf("stop route fell through to SPA handler: %s", rec.Body.String())
	}
}

//nolint:gosec // Test-only values exercise the opt-in debug route boundary.
func TestDebugRoutesRegisterOnlyWhenExplicitlyEnabled(t *testing.T) {
	cfg := config.Config{
		Registry:        config.RegistryConfig{Repository: "registry.example.com/breakfix"},
		JWTSecret:       "user-jwt-secret",
		InternalWorkers: config.InternalWorkerKeys{Runtime: "runtime-worker-key"},
		Debug:           config.DebugConfig{Enabled: true, CredentialEnv: "BREAKFIX_DEBUG_CREDENTIAL", Credential: "debug-only-credential"},
	}
	router, err := SetupRouter(context.Background(), nil, nil, cfg, nil, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}

	routes := map[string]bool{}
	for _, route := range router.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, expected := range []string{
		"POST /internal/debug/roadmap-maintenance",
		"GET /internal/debug/roadmap-revisions/:revision_id/export",
	} {
		if !routes[expected] {
			t.Fatalf("enabled debug route not registered: %s", expected)
		}
	}

	for name, applyCredential := range map[string]func(*http.Request){
		"user JWT": func(request *http.Request) {
			token, tokenErr := middleware.GenerateJWT("user-a", "Alice", []byte(cfg.JWTSecret))
			if tokenErr != nil {
				t.Fatal(tokenErr)
			}
			request.Header.Set("Authorization", "Bearer "+token)
		},
		"runtime worker identity": func(request *http.Request) {
			request.Header.Set("X-Breakfix-Internal-Key", cfg.InternalWorkers.Runtime)
		},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/internal/debug/roadmap-maintenance", nil)
			applyCredential(request)
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
			}
		})
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/internal/debug/roadmap-maintenance", nil)
	request.Header.Set(middleware.DebugCredentialHeader, cfg.Debug.Credential)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("valid credential did not reach the debug handler: status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestDisabledDebugPathDoesNotFallThroughToFrontend(t *testing.T) {
	router, err := SetupRouter(context.Background(), nil, nil, config.Config{Registry: config.RegistryConfig{Repository: "registry.example.com/breakfix"}}, fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("frontend")},
	}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/debug/roadmap-revisions/test/export", nil)
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("disabled debug path status = %d, want %d: %s", recorder.Code, http.StatusNotFound, recorder.Body.String())
	}
}
