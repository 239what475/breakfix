package workerhealth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerReportsDependencyReadinessAndMetrics(t *testing.T) {
	readyErr := errors.New("provider unavailable")
	handler := newHandler(Config{
		Component: "verifier",
		Ready:     func(context.Context) error { return readyErr },
		Capabilities: []Capability{{
			Name: "node-provider", Ready: func(context.Context) error { return readyErr },
		}},
	})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), readyErr.Error()) {
		t.Fatalf("ready response = %d %q", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `breakfix_worker_ready{component="verifier"} 0`) || !strings.Contains(response.Body.String(), `breakfix_worker_capability_ready{component="verifier",capability="node-provider"} 0`) {
		t.Fatalf("metrics response = %d %q", response.Code, response.Body.String())
	}
}

func TestCapabilityFailureDoesNotChangeCoreReadiness(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	handler := newHandler(Config{Component: "builder", Capabilities: []Capability{{
		Name: "node-provider", Ready: func(context.Context) error { return providerErr },
	}}})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("core readiness = %d %q", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/capabilities/node-provider", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), providerErr.Error()) {
		t.Fatalf("node provider capability = %d %q", response.Code, response.Body.String())
	}
}
