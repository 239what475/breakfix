package httpapi

import (
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/breakfix/breakfix/internal/bootstrap/config"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/breakfix/breakfix/internal/transport/httpapi/middleware"
	"github.com/gin-gonic/gin"
)

// SetupRouter only registers HTTP routes. Server bootstrap owns Handler
// construction, startup recovery, and every process-scoped background service.
func SetupRouter(h *Handler, cfg config.Config, frontendFS fs.FS) (*gin.Engine, error) {
	if h == nil {
		return nil, fmt.Errorf("HTTP handler is required")
	}
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())
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
	// Public routes
	router.POST("/api/auth/register", h.Register)
	router.POST("/api/auth/login", h.Login)
	catalogRoutes := router.Group("/")
	catalogRoutes.Use(h.requireCatalogReady)
	catalogRoutes.GET("/api/me/space", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.GetMySpace(c)
		}
	})
	catalogRoutes.GET("/api/me/space/learning", func(c *gin.Context) {
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

	// Operations scenarios are public read-only. Starting, viewing full content,
	// and every environment operation below remain bound to an authenticated
	// user. Documentation uses its own /api/documentation namespace once its
	// source-sync and reading flow exists; it is intentionally not routed here.
	catalogRoutes.GET("/api/operations/scenarios", optionalJWTMW, h.ListScenarios)
	catalogRoutes.POST("/api/operations/scenarios/:id/start", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.StartScenario(c, c.Param("id"))
		}
	})
	catalogRoutes.GET("/api/operations/scenarios/:id/content", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.GetScenarioContent(c, c.Param("id"))
		}
	})
	catalogRoutes.GET("/api/operations/scenarios/:id/progress", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.GetScenarioProgress(c, c.Param("id"))
		}
	})
	catalogRoutes.GET("/api/operations/scenarios/:id/assistant", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.GetScenarioAssistant(c, c.Param("id"))
		}
	})
	catalogRoutes.POST("/api/operations/scenarios/:id/assistant/messages", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.SendScenarioAssistantMessage(c, c.Param("id"))
		}
	})
	catalogRoutes.POST("/api/operations/scenarios/:id/reset", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.ResetScenario(c, c.Param("id"))
		}
	})
	catalogRoutes.POST("/api/operations/scenarios/:id/stop", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.StopScenario(c, c.Param("id"))
		}
	})
	catalogRoutes.POST("/api/operations/scenarios/:id/terminal-ticket", func(c *gin.Context) {
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
	catalogRoutes.POST("/api/authoring/scenarios/:id/revisions", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.CreateAuthoringScenarioRevision(c, c.Param("id"))
		}
	})
	catalogRoutes.POST("/api/authoring/scenarios/:id/deprecate", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.DeprecateAuthoringScenario(c, c.Param("id"))
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
	generatorRoutes := router.Group("/api/generator")
	generatorRoutes.Use(jwtMW)
	generatorRoutes.POST("/plans", h.SetGenerationPlan)
	generatorRoutes.GET("/workflows", h.ListActiveGenerations)
	generatorRoutes.POST("/workflows", h.ConfirmGeneration)
	generatorRoutes.GET("/workflows/:workflow_id", func(c *gin.Context) { h.GetGeneration(c, c.Param("workflow_id")) })
	generatorRoutes.GET("/workflows/:workflow_id/review-bundle", func(c *gin.Context) {
		kind := api.GetGeneratorReviewBundleParamsKind(c.Query("kind"))
		h.GetGeneratorReviewBundle(c, c.Param("workflow_id"), api.GetGeneratorReviewBundleParams{Kind: kind})
	})
	generatorRoutes.POST("/workflows/:workflow_id/workspace/turn", func(c *gin.Context) { h.StartGeneratorWorkspaceTurn(c, c.Param("workflow_id")) })
	generatorRoutes.POST("/workflows/:workflow_id/workspace/turn/end", func(c *gin.Context) { h.EndGeneratorWorkspaceTurn(c, c.Param("workflow_id")) })
	generatorRoutes.GET("/workflows/:workflow_id/workspace/files", func(c *gin.Context) {
		h.ListGeneratorWorkspaceFiles(c, c.Param("workflow_id"), api.ListGeneratorWorkspaceFilesParams{TurnId: c.Query("turn_id")})
	})
	generatorRoutes.PUT("/workflows/:workflow_id/workspace/files", func(c *gin.Context) { h.WriteGeneratorWorkspaceFile(c, c.Param("workflow_id")) })
	generatorRoutes.GET("/workflows/:workflow_id/workspace/file", func(c *gin.Context) {
		params := api.ReadGeneratorWorkspaceFileParams{TurnId: c.Query("turn_id"), Path: c.Query("path")}
		if raw := c.Query("offset"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "invalid workspace file offset"})
				return
			}
			params.Offset = &value
		}
		if raw := c.Query("limit"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "invalid workspace file limit"})
				return
			}
			params.Limit = &value
		}
		h.ReadGeneratorWorkspaceFile(c, c.Param("workflow_id"), params)
	})
	generatorRoutes.POST("/workflows/:workflow_id/workspace/commands", func(c *gin.Context) { h.RunGeneratorWorkspaceCommand(c, c.Param("workflow_id")) })
	generatorRoutes.POST("/workflows/:workflow_id/candidate", func(c *gin.Context) { h.SubmitGeneratorCandidate(c, c.Param("workflow_id")) })
	generatorRoutes.POST("/workflows/:workflow_id/content/confirm", func(c *gin.Context) { h.ConfirmGeneratorContent(c, c.Param("workflow_id")) })
	generatorRoutes.POST("/workflows/:workflow_id/content/changes", func(c *gin.Context) { h.RequestGeneratorContentChanges(c, c.Param("workflow_id")) })
	generatorRoutes.POST("/workflows/:workflow_id/cancel", func(c *gin.Context) { h.CancelGeneration(c, c.Param("workflow_id")) })
	router.POST("/api/internal/runtime-actions/claim", h.InternalClaimRuntimeAction)
	router.POST("/api/internal/runtime-resource-reaps/claim", h.InternalClaimRuntimeResourceReap)
	router.POST("/api/internal/runtime-resource-reaps/complete", h.InternalCompleteRuntimeResourceReap)
	router.POST("/api/internal/runtime-actions/:id/renew", h.InternalRenewRuntimeAction)
	router.POST("/api/internal/runtime-actions/:id/source/archive", h.InternalDownloadRuntimeArchive)
	router.POST("/api/internal/runtime-actions/:id/build/complete", h.InternalCompleteRuntimeBuild)
	router.POST("/api/internal/runtime-actions/:id/artifact-publish/complete", h.InternalCompleteRuntimeArtifactPublish)
	router.POST("/api/internal/runtime-actions/:id/verification/environment", h.InternalRecordRuntimeVerificationEnvironment)
	router.POST("/api/internal/runtime-actions/:id/verification/complete", h.InternalCompleteRuntimeVerification)
	router.POST("/api/internal/runtime-actions/:id/scenario-publish/complete", h.InternalRecordRuntimeScenarioPublication)
	router.POST("/api/internal/runtime-actions/:id/failure/infrastructure", h.InternalReportRuntimeInfrastructureFailure)
	router.POST("/api/internal/runtime-actions/:id/failure/artifact", h.InternalReportRuntimeArtifactFailure)
	router.POST("/api/internal/runnable-actions/claim", h.InternalClaimRunnableAction)
	router.POST("/api/internal/runnable-actions/renew", h.InternalRenewRunnableAction)
	router.POST("/api/internal/runnable-actions/source", h.InternalDownloadRunnableSource)
	router.POST("/api/internal/runnable-actions/output", h.InternalStoreRunnableExecutionOutput)
	router.POST("/api/internal/runnable-actions/materialization/complete", h.InternalCompleteRunnableMaterialization)
	router.POST("/api/internal/runnable-actions/verification/complete", h.InternalCompleteRunnableVerification)
	router.POST("/api/internal/runnable-actions/failure", h.InternalReportRunnableActionFailure)

	// Terminal WebSocket
	catalogRoutes.GET("/api/operations/scenarios/:id/terminal", h.HandleTerminalTicket)
	catalogRoutes.DELETE("/api/operations/scenarios/:id/terminals/:window", func(c *gin.Context) {
		jwtMW(c)
		if !c.IsAborted() {
			h.CloseTerminalWindow(c, c.Param("id"), c.Param("window"))
		}
	})

	// Serve embedded frontend SPA
	if frontendFS != nil {
		router.NoRoute(func(c *gin.Context) {
			if c.Request.URL.Path == "/internal/debug" || strings.HasPrefix(c.Request.URL.Path, "/internal/debug/") {
				c.Status(http.StatusNotFound)
				return
			}
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
