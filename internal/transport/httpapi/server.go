package httpapi

import (
	"context"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/breakfix/breakfix/internal/adapter/kubernetes"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/breakfix/breakfix/internal/transport/httpapi/middleware"
	"github.com/gin-gonic/gin"
)

func SetupRouter(runCtx context.Context, database *postgres.Store, k8sClient *kubernetes.Client, cfg config.Config, frontendFS fs.FS, dependencies Dependencies) (*gin.Engine, error) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	h, err := NewHandlerWithDependencies(database, k8sClient, cfg, dependencies)
	if err != nil {
		return nil, err
	}
	h.setRuntimeContext(runCtx)
	if err := h.RecoverInteractiveAgentRuns(runCtx); err != nil {
		return nil, err
	}
	if err := h.validateStartup(); err != nil {
		return nil, err
	}
	h.StartLearningCleanup(runCtx)
	h.StartEnvironmentStatusProjector(runCtx)
	h.StartAssistantEnvironmentLeaseMaintainer(runCtx)
	h.StartGenerationPublicationFinalizer(runCtx)
	h.StartRoadmapMaintenance(runCtx)
	jwtSecret := []byte(cfg.JWTSecret)
	jwtMW := middleware.JWTMiddleware(jwtSecret)
	optionalJWTMW := middleware.OptionalJWTMiddleware(jwtSecret)
	router.GET("/healthz", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	router.GET("/readyz", func(c *gin.Context) {
		if err := h.validateReadiness(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: err.Error()})
			return
		}
		c.Status(http.StatusOK)
	})
	router.GET("/capabilities/node-provider", func(c *gin.Context) {
		if err := h.validateNodeProviderCapability(c.Request.Context()); err != nil {
			c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: err.Error()})
			return
		}
		c.Status(http.StatusOK)
	})
	router.GET("/metrics", h.WorkflowMetrics)
	router.POST("/internal/debug/roadmap-maintenance", h.RequestRoadmapMaintenance)
	router.GET("/internal/debug/roadmap-revisions/:revision_id/export", h.ExportRoadmapRevision)

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
	router.POST("/api/challenges/:id/terminal-ticket", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.CreateTerminalTicket(c, c.Param("id"))
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
	router.POST("/api/authoring/sessions/:id/generation/:workflow_id/cancel", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.CancelAuthoringGeneration(c, c.Param("id"), c.Param("workflow_id"))
		}
	})
	router.POST("/api/authoring/sessions/:id/classify", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.ConfirmAuthoringContent(c, c.Param("id"))
		}
	})
	router.POST("/api/authoring/sessions/:id/classification-feedback", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.RequestAuthoringClassificationAdjustment(c, c.Param("id"))
		}
	})
	router.POST("/api/authoring/sessions/:id/publish", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.PublishAuthoringRevision(c, c.Param("id"))
		}
	})
	router.POST("/api/internal/runtime-actions/claim", h.InternalClaimGenerationWorkflow)
	router.POST("/api/internal/runtime-resource-reaps/claim", h.InternalClaimGenerationResourceReap)
	router.POST("/api/internal/runtime-resource-reaps/complete", h.InternalCompleteGenerationResourceReap)
	router.POST("/api/internal/runtime-actions/:id/renew", h.InternalRenewGenerationWorkflow)
	router.POST("/api/internal/runtime-actions/:id/candidate/archive", h.InternalDownloadGenerationCandidateArchive)
	router.POST("/api/internal/runtime-actions/:id/build/complete", h.InternalCompleteGenerationBuild)
	router.POST("/api/internal/runtime-actions/:id/artifact-publish/complete", h.InternalCompleteGenerationArtifactPublish)
	router.POST("/api/internal/runtime-actions/:id/verification/environment", h.InternalRecordGenerationVerificationEnvironment)
	router.POST("/api/internal/runtime-actions/:id/verification/complete", h.InternalCompleteGenerationVerification)
	router.POST("/api/internal/runtime-actions/:id/challenge-publish/complete", h.InternalRecordGenerationChallengePublication)
	router.POST("/api/internal/runtime-actions/:id/failure/infrastructure", h.InternalReportGenerationInfrastructureFailure)
	router.POST("/api/internal/runtime-actions/:id/failure/artifact", h.InternalReportGenerationArtifactFailure)

	// Terminal WebSocket
	router.GET("/api/challenges/:id/terminal", h.HandleTerminalTicket)
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
			_ = f.Close()
			http.FileServerFS(frontendFS).ServeHTTP(c.Writer, c.Request)
		})
	}

	return router, nil
}
