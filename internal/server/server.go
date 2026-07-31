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

func SetupRouter(runCtx context.Context, database *db.DB, k8sClient *k8s.Client, cfg config.Config, frontendFS fs.FS, dependencies Dependencies) (*gin.Engine, error) {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	h := NewHandlerWithDependencies(database, k8sClient, cfg, dependencies)
	if h.startupErr != nil {
		return nil, h.startupErr
	}
	if err := h.RecoverCandidatePublications(runCtx); err != nil {
		return nil, err
	}
	if err := h.RecoverExpiredWork(runCtx); err != nil {
		return nil, err
	}
	if err := h.validateStartup(); err != nil {
		return nil, err
	}
	if h.taxonomyWorkflow != nil {
		h.taxonomyWorkflow.Start(runCtx)
	}
	h.StartLearningCleanup(runCtx)
	h.StartEnvironmentStatusProjector(runCtx)
	h.StartAssistantEnvironmentLeaseMaintainer(runCtx)
	h.StartGeneratorWorkspaceCleanup(runCtx)
	h.StartCandidatePublicationRecovery(runCtx)
	h.StartWorkDeadlineRecovery(runCtx)
	jwtSecret := []byte(cfg.JWTSecret)
	jwtMW := auth.JWTMiddleware(jwtSecret)
	optionalJWTMW := auth.OptionalJWTMiddleware(jwtSecret)
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
	router.GET("/metrics", h.WorklistMetrics)

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
	router.POST("/api/authoring/sessions/:id/publish", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.PublishAuthoringRevision(c, c.Param("id"))
		}
	})
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
	router.POST("/api/internal/agent-runs/:id/generator/archive", h.InternalGeneratorArchiveWorkspace)
	router.POST("/api/internal/agent-runs/:id/generator/finalize", h.InternalGeneratorFinalizeCandidate)
	router.POST("/api/internal/work-items/agent/claim", h.InternalClaimAgentWork)
	router.POST("/api/internal/work-items/inspect", h.InternalInspectWorkItems)
	router.POST("/api/internal/work-items/:kind/claim", h.InternalClaimCandidateWork)
	router.POST("/api/internal/work-items/:kind/:id/renew", h.InternalRenewCandidateWork)
	router.POST("/api/internal/work-items/:kind/:id/requeue", h.InternalRequeueCandidateWork)
	router.POST("/api/internal/work-items/:kind/:id/candidate/archive", h.InternalDownloadCandidateArchive)
	router.POST("/api/internal/work-items/:kind/:id/k8s/base", h.InternalDownloadCandidateK8sBase)
	router.POST("/api/internal/work-items/:kind/:id/build/archive", h.InternalDownloadCandidateBuildArchive)
	router.POST("/api/internal/work-items/:kind/:id/complete/build", h.InternalCompleteCandidateBuild)
	router.POST("/api/internal/work-items/:kind/:id/complete/artifact-publish", h.InternalCompleteCandidateArtifactPublish)
	router.POST("/api/internal/work-items/:kind/:id/verify/environment", h.InternalRecordCandidateVerificationEnvironment)
	router.POST("/api/internal/work-items/:kind/:id/complete/verify", h.InternalCompleteCandidateVerification)
	router.POST("/api/internal/work-items/:kind/:id/fail/artifact", h.InternalFailCandidateArtifact)
	router.POST("/api/internal/work-items/:kind/:id/complete/cleanup", h.InternalCompleteCandidateCleanup)
	router.POST("/api/internal/work-items/:kind/:id/complete/challenge-publish", h.InternalCompleteCandidateChallengePublish)
	router.POST("/api/internal/agent-runs/:id/status", h.InternalAgentRunStatus)
	router.POST("/api/internal/agent-runs/:id/renew", h.InternalRenewAgentWork)
	router.POST("/api/internal/agent-runs/:id/requeue", h.InternalRequeueAgentWork)
	router.POST("/api/internal/agent-runs/:id/complete", h.InternalCompleteAgentWork)
	router.POST("/api/internal/agent-runs/:id/fail", h.InternalFailAgentWork)

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
