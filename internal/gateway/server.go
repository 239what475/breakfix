package gateway

import (
	"io/fs"
	"net/http"

	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/auth"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/gin-gonic/gin"
)

func SetupRouter(database *db.DB, k8sClient *k8s.Client, cfg config.Config, frontendFS fs.FS) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	h := NewHandler(database, k8sClient, cfg)
	jwtSecret := []byte(cfg.JWTSecret)
	jwtMW := auth.JWTMiddleware(jwtSecret)

	// Public routes
	router.POST("/api/auth/register", h.Register)
	router.POST("/api/auth/login", h.Login)

	// OpenAPI spec
	router.GET("/api/openapi.json", func(c *gin.Context) {
		spec, _ := api.GetSpecJSON()
		c.Data(http.StatusOK, "application/json", spec)
	})

	// Protected routes (JWT via Authorization header)
	protected := router.Group("/api")
	protected.Use(jwtMW)
	{
		protected.GET("/challenges", h.ListChallenges)
		protected.POST("/challenges/:id/start", func(c *gin.Context) { h.StartChallenge(c, c.Param("id")) })
		protected.POST("/challenges/:id/submit", func(c *gin.Context) { h.SubmitChallenge(c, c.Param("id")) })
		protected.POST("/challenges/:id/reset", func(c *gin.Context) { h.ResetChallenge(c, c.Param("id")) })
		protected.POST("/generate", h.GenerateChallenge)
	}

	// Terminal WebSocket (supports query param token for browser API)
	router.GET("/api/challenges/:id/terminal", func(c *gin.Context) {
		if tok := c.Query("token"); tok != "" {
			c.Request.Header.Set("Authorization", "Bearer "+tok)
		}
		jwtMW(c)
		if !c.IsAborted() {
			h.HandleTerminal(c)
		}
	})

	// Serve embedded frontend SPA
	if frontendFS != nil {
		spaFS := &spaFallbackFS{fs: frontendFS}
		router.NoRoute(func(c *gin.Context) {
			f, err := spaFS.Open(c.Request.URL.Path)
			if err != nil {
				// Serve index.html for SPA client-side routing
				http.ServeFileFS(c.Writer, c.Request, frontendFS, "index.html")
				return
			}
			f.Close()
			http.FileServerFS(frontendFS).ServeHTTP(c.Writer, c.Request)
		})
	}

	return router
}

type spaFallbackFS struct {
	fs fs.FS
}

func (s *spaFallbackFS) Open(name string) (fs.File, error) {
	f, err := s.fs.Open(name)
	if err != nil {
		return s.fs.Open("index.html")
	}
	return f, nil
}
