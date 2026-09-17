package middleware

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	UserID   string `json:"uid"`
	UserName string `json:"name"`
	Role     string `json:"role,omitempty"`
	jwt.RegisteredClaims
}

// GenerateJWT embeds the durable role claim. Tokens issued before the role
// model lack the field and therefore parse as an ordinary user; no legacy
// branch exists because roles only change with a schema reset.
func GenerateJWT(userID, userName, role string, secret []byte) (string, error) {
	claims := Claims{
		UserID:   userID,
		UserName: userName,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

func JWTMiddleware(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		claims, err := claimsFromAuthorization(authHeader, secret)
		if err != nil {
			if authHeader == "" {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token claims"})
			return
		}
		setClaims(c, claims)
		c.Next()
	}
}

// OptionalJWTMiddleware attaches an authenticated identity when a valid bearer
// token is present, while preserving public access for anonymous catalog reads.
// Invalid optional credentials are treated as anonymous because the route has
// no privileged behavior without a verified identity.
func OptionalJWTMiddleware(secret []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" {
			if claims, err := claimsFromAuthorization(authHeader, secret); err == nil {
				setClaims(c, claims)
			}
		}
		c.Next()
	}
}

func claimsFromAuthorization(authHeader string, secret []byte) (*Claims, error) {
	if authHeader == "" {
		return nil, fmt.Errorf("missing authorization header")
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return nil, fmt.Errorf("invalid authorization format")
	}
	token, err := jwt.ParseWithClaims(parts[1], &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("invalid or expired token")
	}
	claims, ok := token.Claims.(*Claims)
	if !ok {
		return nil, fmt.Errorf("invalid token claims")
	}
	return claims, nil
}

func setClaims(c *gin.Context, claims *Claims) {
	c.Set("user_id", claims.UserID)
	c.Set("user_name", claims.UserName)
	c.Set("user_role", claims.Role)
}

func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		role, _ := c.Get("user_role")
		if role != "admin" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin role required"})
			return
		}
		c.Next()
	}
}
