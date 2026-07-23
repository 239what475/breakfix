package gateway

import (
	"context"
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
	h.StartAuthoringReconciler(context.Background())
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

	// The catalog is public read-only. Starting, viewing full content, and every
	// environment operation below remain bound to an authenticated user.
	router.GET("/api/challenges", h.ListChallenges)
	router.POST("/api/challenges/:id/start", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.StartChallenge(c, c.Param("id"))
		}
	})
	router.GET("/api/challenges/:id/content", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.GetChallengeContent(c, c.Param("id"))
		}
	})
	router.GET("/api/challenges/:id/progress", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.GetChallengeProgress(c, c.Param("id"))
		}
	})
	router.POST("/api/challenges/:id/reset", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.ResetChallenge(c, c.Param("id"))
		}
	})
	router.POST("/api/challenges/:id/stop", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.StopChallenge(c, c.Param("id"))
		}
	})
	router.POST("/api/authoring/sessions", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.CreateAuthoringSession(c)
		}
	})
	router.GET("/api/authoring/sessions/current", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.GetCurrentAuthoringSession(c)
		}
	})
	router.GET("/api/authoring/sessions/:id", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.GetAuthoringSession(c, c.Param("id"))
		}
	})
	router.POST("/api/authoring/sessions/:id/messages", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.SendAuthoringMessage(c, c.Param("id"))
		}
	})
	router.POST("/api/authoring/sessions/:id/generate", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.ConfirmAuthoringGeneration(c, c.Param("id"))
		}
	})
	router.POST("/api/authoring/sessions/:id/publish", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.PublishAuthoringRevision(c, c.Param("id"))
		}
	})
	router.POST("/api/internal/generations/:id/artifact", h.UploadGenerationArtifact)
	router.GET("/api/internal/verify-submissions/:id/artifact", h.DownloadVerifySubmissionArtifact)

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
	router.DELETE("/api/challenges/:id/terminals/:window", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.CloseTerminalWindow(c, c.Param("id"), c.Param("window"))
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
