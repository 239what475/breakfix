package gateway

import (
	"net/http"

	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/auth"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/gin-gonic/gin"
)

func SetupRouter(database *db.DB, k8sClient *k8s.Client, cfg config.Config) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	h := NewHandler(database, k8sClient, cfg)
	jwtSecret := []byte(cfg.JWTSecret)

	// Public routes
	router.POST("/api/auth/register", h.Register)
	router.POST("/api/auth/login", h.Login)

	// OpenAPI spec
	router.GET("/api/openapi.json", func(c *gin.Context) {
		spec, _ := api.GetSpecJSON()
		c.Data(http.StatusOK, "application/json", spec)
	})

	// Protected routes
	protected := router.Group("/api")
	protected.Use(auth.JWTMiddleware(jwtSecret))
	{
		protected.GET("/challenges", h.ListChallenges)
		protected.POST("/challenges/:id/start", func(c *gin.Context) { h.StartChallenge(c, c.Param("id")) })
		protected.POST("/challenges/:id/submit", func(c *gin.Context) { h.SubmitChallenge(c, c.Param("id")) })
		protected.POST("/challenges/:id/reset", func(c *gin.Context) { h.ResetChallenge(c, c.Param("id")) })
		protected.POST("/generate", h.GenerateChallenge)
		protected.GET("/challenges/:id/terminal", h.HandleTerminal)
	}

	return router
}
