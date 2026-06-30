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

	// Protected routes — inline JWT middleware
	router.GET("/api/challenges", func(c *gin.Context) { jwtMW(c); if !c.IsAborted() { h.ListChallenges(c) } })
	router.POST("/api/challenges/:id/start", func(c *gin.Context) { jwtMW(c); if !c.IsAborted() { h.StartChallenge(c, c.Param("id")) } })
	router.POST("/api/challenges/:id/submit", func(c *gin.Context) { jwtMW(c); if !c.IsAborted() { h.SubmitChallenge(c, c.Param("id")) } })
	router.POST("/api/challenges/:id/reset", func(c *gin.Context) { jwtMW(c); if !c.IsAborted() { h.ResetChallenge(c, c.Param("id")) } })
	router.POST("/api/generate", func(c *gin.Context) { jwtMW(c); if !c.IsAborted() { h.GenerateChallenge(c) } })

	// Terminal WebSocket
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
		router.NoRoute(func(c *gin.Context) {
			path := c.Request.URL.Path
			if len(path) > 0 && path[0] == '/' {
				path = path[1:]
			}
			if path == "" {
				path = "index.html"
			}
			f, err := frontendFS.Open(path)
			if err != nil {
				http.ServeFileFS(c.Writer, c.Request, frontendFS, "index.html")
				return
			}
			f.Close()
			http.FileServerFS(frontendFS).ServeHTTP(c.Writer, c.Request)
		})
	}

	return router
}
