package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appcatalog "github.com/breakfix/breakfix/internal/application/catalog"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/content/challenge"
	testpostgres "github.com/breakfix/breakfix/internal/testkit/postgres"
	"github.com/breakfix/breakfix/internal/transport/httpapi/middleware"
)

//nolint:gosec // Test-only values exercise separation from user and worker credentials.
func TestDebugRoutesRequireIndependentCredentialAndKeepRoadmapToolsAvailable(t *testing.T) {
	root := t.TempDir()
	writeTestChallenge(t, root)
	database := testpostgres.New(t)
	cfg := config.Config{
		DataDir:         root,
		JWTSecret:       "user-jwt-secret",
		InternalWorkers: config.InternalWorkerKeys{Runtime: "runtime-worker-key"},
		Debug:           config.DebugConfig{Enabled: true, CredentialEnv: "BREAKFIX_DEBUG_CREDENTIAL", Credential: "debug-only-credential"},
	}
	if err := seedTestRoadmap(database, cfg); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	router, err := SetupRouter(runCtx, database, nil, cfg, nil, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := database.Roadmap.CurrentRoadmap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	userJWT, err := middleware.GenerateJWT("user-a", "Alice", []byte(cfg.JWTSecret))
	if err != nil {
		t.Fatal(err)
	}

	for name, applyCredential := range map[string]func(*http.Request){
		"user JWT": func(request *http.Request) {
			request.Header.Set("Authorization", "Bearer "+userJWT)
		},
		"runtime worker identity": func(request *http.Request) {
			request.Header.Set("X-Breakfix-Internal-Key", cfg.InternalWorkers.Runtime)
		},
	} {
		for _, endpoint := range []struct {
			method string
			path   string
		}{
			{method: http.MethodPost, path: "/internal/debug/roadmap-maintenance"},
			{method: http.MethodGet, path: "/internal/debug/roadmap-revisions/" + revision.Revision + "/export"},
		} {
			t.Run(name+" cannot call "+endpoint.path, func(t *testing.T) {
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(endpoint.method, endpoint.path, nil)
				applyCredential(request)
				router.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
				}
			})
		}
	}

	maintenance := httptest.NewRecorder()
	maintenanceRequest := httptest.NewRequest(http.MethodPost, "/internal/debug/roadmap-maintenance", nil)
	maintenanceRequest.Header.Set(middleware.DebugCredentialHeader, cfg.Debug.Credential)
	router.ServeHTTP(maintenance, maintenanceRequest)
	if maintenance.Code != http.StatusAccepted {
		t.Fatalf("maintenance status = %d, want %d: %s", maintenance.Code, http.StatusAccepted, maintenance.Body.String())
	}

	export := httptest.NewRecorder()
	exportRequest := httptest.NewRequest(http.MethodGet, "/internal/debug/roadmap-revisions/"+revision.Revision+"/export", nil)
	exportRequest.Header.Set(middleware.DebugCredentialHeader, cfg.Debug.Credential)
	router.ServeHTTP(export, exportRequest)
	if export.Code != http.StatusOK {
		t.Fatalf("export status = %d, want %d: %s", export.Code, http.StatusOK, export.Body.String())
	}
	if export.Header().Get("Content-Type") != "application/gzip" || !strings.Contains(export.Header().Get("Content-Disposition"), "breakfix-roadmap-r"+revision.Revision+".tar.gz") {
		t.Fatalf("unexpected export headers: %#v", export.Header())
	}
	extracted := filepath.Join(root, "exported")
	if err := os.MkdirAll(extracted, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := challenge.ExtractTarGz(extracted, export.Body); err != nil {
		t.Fatalf("extract debug export: %v", err)
	}
	if _, err := appcatalog.LoadPortableSource(extracted); err != nil {
		t.Fatalf("load debug export: %v", err)
	}
}
