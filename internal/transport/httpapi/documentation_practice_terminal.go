package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

// CreatePracticeTerminalTicket mints the one-time ticket a browser exchanges
// for the practice terminal WebSocket. It mirrors the operations endpoint and
// reuses the same ticket storage; the practice identifier rides the generic
// content column.
func (h *Handler) CreatePracticeTerminalTicket(c *gin.Context) {
	target, ok := h.resolvePracticeTarget(c, c.Param("id"))
	if !ok {
		return
	}
	h.createContentTerminalTicket(c, target, c.Param("id"))
}

// HandlePracticeTerminalTicket upgrades the one-time ticket to a terminal
// WebSocket with the same origin allowlist, resize/data/ready protocol, and
// connection-scoped lease renewal as the operations terminal.
func (h *Handler) HandlePracticeTerminalTicket(c *gin.Context) {
	target, ok := h.resolvePracticeTarget(c, c.Param("id"))
	if !ok {
		return
	}
	h.handleContentTerminalTicket(c, target, c.Param("id"))
}

// createContentTerminalTicket mints the one-time ticket for whichever content
// target owns the environment: a published practice or the reader's blank
// documentation scenario.
func (h *Handler) createContentTerminalTicket(c *gin.Context, target environmentContentTarget, contentID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	if h.db == nil || h.k8s == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "terminal dependencies are not configured"})
		return
	}
	var request api.TerminalTicketRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: "invalid terminal ticket request"})
		return
	}
	windowName, err := parseTerminalWindow(request.Window)
	if err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	env, err := h.findEnvironment(c.Request.Context(), user.ID, target)
	if err != nil || env == nil || env.UID == "" || !terminalEnvironmentReady(env, h.nodeTerminal) {
		if err == nil || errors.Is(err, errNoMatchingEnvironment) {
			c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active environment for this content"})
			return
		}
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("find environment: %v", err)})
		return
	}
	nodeName, err := terminalNodeName(env, request.Node)
	if err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	ticket, err := newTerminalTicket()
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	now := time.Now().UTC()
	if err := h.db.Environment.CreateTerminalTicket(c.Request.Context(), postgres.TerminalTicket{
		TokenHash:      terminalTicketHash(ticket),
		UserID:         user.ID,
		EnvironmentUID: env.UID,
		ScenarioID:     contentID,
		NodeName:       nodeName,
		WindowName:     windowName,
		ExpiresAt:      now.Add(terminalTicketTTL),
	}, now); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("create terminal ticket: %v", err)})
		return
	}
	c.JSON(http.StatusOK, api.TerminalTicketResponse{Ticket: ticket})
}

// handleContentTerminalTicket upgrades the one-time ticket to a terminal
// WebSocket for the content that minted it, with the same origin allowlist,
// resize/data/ready protocol, and connection-scoped lease renewal as the
// operations terminal.
func (h *Handler) handleContentTerminalTicket(c *gin.Context, target environmentContentTarget, contentID string) {
	if h.db == nil || h.k8s == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "terminal dependencies are not configured"})
		return
	}
	if !terminalOriginAllowed(c.GetHeader("Origin"), h.uiOrigin) {
		c.JSON(http.StatusForbidden, api.ErrorResponse{Error: "terminal origin is not allowed"})
		return
	}
	ticket, err := h.db.Environment.ClaimTerminalTicket(c.Request.Context(), terminalTicketHash(c.Query("ticket")), contentID, c.Query("window"), time.Now().UTC())
	if err != nil {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: "terminal ticket is invalid or expired"})
		return
	}
	env, err := h.findActiveEnvironmentByUID(c.Request.Context(), ticket.UserID, ticket.EnvironmentUID)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active environment for this content"})
		return
	}
	if env.UID != ticket.EnvironmentUID || env.ScenarioRef != contentID || env.SourceRevision != target.revisionID {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "terminal environment has changed"})
		return
	}
	if !terminalEnvironmentReady(env, h.nodeTerminal) {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "terminal runtime is not ready"})
		return
	}
	windowName := ticket.WindowName
	connectionID, err := newTerminalConnectionID()
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}

	runtimeAdapter, err := h.environmentRuntimeAdapter(env.Runtime)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	if env.Phase == runtimev2.PhaseDraining {
		if err := h.resumeEnvironment(c.Request.Context(), env); err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("resume environment: %v", err)})
			return
		}
		env.Phase = runtimev2.PhaseReady
	}
	stream, err := h.terminalStream(env, ticket.NodeName, windowName)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: err.Error()})
		return
	}

	slog.Info("terminal session started", "content", contentID, "user", ticket.UserID)
	key := env.Runtime + "/" + env.Name
	wsUpgrade(c.Writer, c.Request, h.uiOrigin, env, runtimeAdapter, h.cooldownMin, stream, terminalSocketLifecycle{
		open: func() error {
			if err := h.db.Environment.OpenTerminalConnection(c.Request.Context(), postgres.TerminalConnection{
				ID:               connectionID,
				EnvironmentUID:   env.UID,
				UserID:           ticket.UserID,
				ScenarioID:       contentID,
				ServerInstanceID: h.serverInstance,
				ConnectedAt:      time.Now().UTC(),
			}); err != nil {
				return err
			}
			h.terminals.open(key)
			return nil
		},
		heartbeat: func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := h.db.Environment.TouchTerminalConnection(ctx, connectionID, time.Now().UTC()); err != nil {
				slog.Warn("touch terminal connection", "err", err, "environment", env.Name)
			}
		},
		close: func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := h.db.Environment.CloseTerminalConnection(ctx, connectionID, time.Now().UTC())
			cancel()
			if err != nil {
				slog.Error("close terminal connection", "err", err, "environment", env.Name)
			}
			h.terminals.close(key, func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				closed, err := h.db.Environment.FinishTerminalUsageSession(ctx, env.UID, time.Now().UTC())
				cancel()
				if err != nil {
					slog.Error("finish terminal usage session", "err", err, "environment", env.Name)
					return
				}
				if !closed {
					return
				}
				slog.Info("terminal activity ended", "environment", env.Name)
			})
		},
	})
}
