package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestOptionalJWTMiddlewareAddsIdentityOnlyForValidToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := []byte("test-secret")
	token, err := GenerateJWT("user-a", "Alice", secret)
	if err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.GET("/catalog", OptionalJWTMiddleware(secret), func(c *gin.Context) {
		userID, _ := c.Get("user_id")
		if userID == nil {
			c.Status(http.StatusNoContent)
			return
		}
		c.String(http.StatusOK, userID.(string))
	})

	valid := httptest.NewRecorder()
	validRequest := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	validRequest.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(valid, validRequest)
	if valid.Code != http.StatusOK || valid.Body.String() != "user-a" {
		t.Fatalf("valid optional token response = %d %q", valid.Code, valid.Body.String())
	}

	anonymous := httptest.NewRecorder()
	router.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/catalog", nil))
	if anonymous.Code != http.StatusNoContent {
		t.Fatalf("anonymous response = %d", anonymous.Code)
	}

	invalid := httptest.NewRecorder()
	invalidRequest := httptest.NewRequest(http.MethodGet, "/catalog", nil)
	invalidRequest.Header.Set("Authorization", "Bearer invalid")
	router.ServeHTTP(invalid, invalidRequest)
	if invalid.Code != http.StatusNoContent {
		t.Fatalf("invalid optional token response = %d", invalid.Code)
	}
}
