package server

import (
	"context"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/auth"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/db"
	"github.com/breakfix/breakfix/internal/k8s"
	"github.com/gin-gonic/gin"
)

func SetupRouter(runCtx context.Context, database *db.DB, k8sClient *k8s.Client, cfg config.Config, frontendFS fs.FS) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	h := NewHandler(database, k8sClient, cfg)
	h.StartAuthoringReconciler(runCtx)
	if h.taxonomyWorkflow != nil {
		h.taxonomyWorkflow.Start(runCtx)
	}
	h.StartLearningCleanup(runCtx)
	h.StartEnvironmentStatusProjector(runCtx)
	h.StartAssistantEnvironmentLeaseMaintainer(runCtx)
	h.StartGeneratorWorkspaceCleanup(runCtx)
	jwtSecret := []byte(cfg.JWTSecret)
	jwtMW := auth.JWTMiddleware(jwtSecret)
	optionalJWTMW := auth.OptionalJWTMiddleware(jwtSecret)

	// Public routes
	router.POST("/api/auth/register", h.Register)
	router.POST("/api/auth/login", h.Login)
	router.GET("/api/me/space", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.GetMySpace(c)
		}
	})
	router.GET("/api/me/space/learning", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			limit := 0
			params := api.GetMySpaceLearningParams{}
			if raw := c.Query("limit"); raw != "" {
				parsed, err := strconv.Atoi(raw)
				if err != nil {
					c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "invalid learning history limit"})
					return
				}
				limit = parsed
				params.Limit = &limit
			}
			if raw := c.Query("cursor"); raw != "" {
				params.Cursor = &raw
			}
			if raw := c.Query("state"); raw != "" {
				state := api.GetMySpaceLearningParamsState(raw)
				params.State = &state
			}
			if raw := c.Query("runtime"); raw != "" {
				runtime := api.GetMySpaceLearningParamsRuntime(raw)
				params.Runtime = &runtime
			}
			h.GetMySpaceLearning(c, params)
		}
	})

	// OpenAPI spec
	router.GET("/api/openapi.json", func(c *gin.Context) {
		spec, _ := api.GetSpecJSON()
		c.Data(http.StatusOK, "application/json", spec)
	})

	// The catalog is public read-only. Starting, viewing full content, and every
	// environment operation below remain bound to an authenticated user.
	router.GET("/api/challenges", optionalJWTMW, h.ListChallenges)
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
	router.GET("/api/challenges/:id/assistant", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.GetChallengeAssistant(c, c.Param("id"))
		}
	})
	router.POST("/api/challenges/:id/assistant/messages", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.SendChallengeAssistantMessage(c, c.Param("id"))
		}
	})
	router.GET("/api/challenges/:id/assistant/turns/:turnID/events", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.StreamChallengeAssistantTurn(c, c.Param("id"), c.Param("turnID"))
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
	router.POST("/api/internal/agent-runs/:id/assistant/context", h.InternalAssistantContext)
	router.POST("/api/internal/agent-runs/:id/assistant/tools/:tool", h.InternalAssistantTool)
	router.POST("/api/internal/agent-runs/:id/assistant/events", h.InternalAssistantEvent)
	router.POST("/api/internal/agent-runs/:id/authoring/context", h.InternalAuthoringContext)
	router.POST("/api/internal/agent-runs/:id/authoring/stage", h.InternalAuthoringStage)
	router.POST("/api/internal/agent-runs/:id/authoring/finalize", h.InternalAuthoringFinalize)
	router.POST("/api/internal/agent-runs/:id/taxonomy/context", h.InternalTaxonomyContext)
	router.POST("/api/internal/agent-runs/:id/taxonomy/mapper/finalize", h.InternalTaxonomyFinalizeMapper)
	router.POST("/api/internal/agent-runs/:id/taxonomy/review/finalize", h.InternalTaxonomyFinalizeReviewPair)
	router.POST("/api/internal/agent-runs/:id/generator/context", h.InternalGeneratorContext)
	router.POST("/api/internal/agent-runs/:id/generator/files/read", h.InternalGeneratorReadFile)
	router.POST("/api/internal/agent-runs/:id/generator/files/write", h.InternalGeneratorWriteFile)
	router.POST("/api/internal/agent-runs/:id/generator/execute", h.InternalGeneratorExecute)

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
