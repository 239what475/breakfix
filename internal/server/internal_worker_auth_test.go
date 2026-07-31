package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/worklist"
	"github.com/gin-gonic/gin"
)

func testInternalWorkerKeys() config.InternalWorkerKeys {
	return config.InternalWorkerKeys{
		Agent:     "agent-test-key",
		Builder:   "builder-test-key",
		Publisher: "publisher-test-key",
		Verifier:  "verifier-test-key",
	}
}

func TestInternalWorkerAuthorizationSeparatesRoles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(nil, nil, config.Config{InternalWorkers: testInternalWorkerKeys()})
	router := gin.New()
	router.POST("/agent", func(c *gin.Context) {
		var request struct{}
		if handler.decodeInternalAgentRequest(c, &request) {
			c.Status(http.StatusNoContent)
		}
	})
	router.POST("/candidate/:kind", func(c *gin.Context) {
		kind, ok := candidateWorkKind(c.Param("kind"))
		if !ok {
			c.Status(http.StatusNotFound)
			return
		}
		var request struct{}
		if handler.decodeCandidateWorkerRequest(c, kind, &request) {
			c.Status(http.StatusNoContent)
		}
	})

	for _, test := range []struct {
		name   string
		path   string
		key    string
		status int
	}{
		{name: "agent work accepts agent", path: "/agent", key: "agent-test-key", status: http.StatusNoContent},
		{name: "builder cannot call agent work", path: "/agent", key: "builder-test-key", status: http.StatusForbidden},
		{name: "unknown key is rejected", path: "/agent", key: "unknown-test-key", status: http.StatusUnauthorized},
		{name: "builder claims build", path: "/candidate/" + string(worklist.KindBuild), key: "builder-test-key", status: http.StatusNoContent},
		{name: "agent cannot claim build", path: "/candidate/" + string(worklist.KindBuild), key: "agent-test-key", status: http.StatusForbidden},
		{name: "publisher publishes artifact", path: "/candidate/" + string(worklist.KindArtifactPublish), key: "publisher-test-key", status: http.StatusNoContent},
		{name: "verifier verifies", path: "/candidate/" + string(worklist.KindVerify), key: "verifier-test-key", status: http.StatusNoContent},
		{name: "publisher cannot verify", path: "/candidate/" + string(worklist.KindVerify), key: "publisher-test-key", status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(`{}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Breakfix-Internal-Key", test.key)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}
