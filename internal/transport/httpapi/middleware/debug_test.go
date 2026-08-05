package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDebugCredentialMiddlewareAcceptsOnlyDedicatedCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/internal/debug/test", DebugCredentialMiddleware("debug-credential"), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	for name, prepare := range map[string]func(*http.Request){
		"missing credential": func(*http.Request) {},
		"user bearer token": func(request *http.Request) {
			request.Header.Set("Authorization", "Bearer user-jwt")
		},
		"runtime worker credential": func(request *http.Request) {
			request.Header.Set("X-Breakfix-Internal-Key", "runtime-worker-key")
		},
		"wrong debug credential": func(request *http.Request) {
			request.Header.Set(DebugCredentialHeader, "wrong")
		},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/internal/debug/test", nil)
			prepare(request)
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusUnauthorized, recorder.Body.String())
			}
		})
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/internal/debug/test", nil)
	request.Header.Set(DebugCredentialHeader, "debug-credential")
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusNoContent, recorder.Body.String())
	}
}
