package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//nolint:gosec // This is an HTTP header name, not a credential value.
const DebugCredentialHeader = "X-Breakfix-Debug-Key"

// DebugCredentialMiddleware protects the opt-in remote debugging routes. Its
// dedicated header keeps this boundary independent from browser JWTs and
// internal worker identities.
func DebugCredentialMiddleware(credential string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.TrimSpace(credential) == "" {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "debug routes are unavailable"})
			return
		}
		presented := c.GetHeader(DebugCredentialHeader)
		if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(credential)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid debug credential"})
			return
		}
		c.Next()
	}
}
