package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"log/slog"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/challenge"
	environmentdomain "github.com/breakfix/breakfix/internal/domain/environment"
	"github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
)

func (h *Handler) StartChallenge(c *gin.Context, id string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}

	challengeEntry, err := h.publishedChallenge(id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}

	existing, _ := h.findEnvironment(c.Request.Context(), user.ID, challengeEntry)
	if existing != nil {
		if existing.Phase == breakfixv1.EnvironmentDraining {
			if err := h.resumeEnvironment(c.Request.Context(), existing); err != nil {
				c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("resume environment: %v", err)})
				return
			}
		}
		slog.Info("resuming existing environment", "environment", existing.Name, "challenge", id, "runtime", existing.Runtime)
		c.JSON(http.StatusOK, api.StartResponse{ChallengeTitle: challengeEntry.Title})
		return
	}

	env, err := h.createEnvironment(c.Request.Context(), user, challengeEntry)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}

	slog.Info("environment started", "environment", env.Name, "user", user.ID, "challenge", challengeEntry.ID, "runtime", challengeEntry.Runtime)
	c.JSON(http.StatusOK, api.StartResponse{ChallengeTitle: challengeEntry.Title})
}

func (h *Handler) ResetChallenge(c *gin.Context, id string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}

	challengeEntry, err := h.publishedChallenge(id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}

	existing, _ := h.findProgressEnvironment(c.Request.Context(), user.ID, challengeEntry)
	if existing != nil {
		if err := h.db.Environment.FinishChallengeAttempt(c.Request.Context(), existing.UID, postgres.AttemptReset, time.Now().UTC()); err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("record reset attempt: %v", err)})
			return
		}
		if err := h.assistant.DeleteEnvironment(c.Request.Context(), existing.UID); err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("clear assistant session: %v", err)})
			return
		}
		if err := h.destroyEnvironment(c.Request.Context(), existing); err != nil {
			slog.Error("failed to destroy old environment", "err", err)
		}
		slog.Info("old environment deletion requested", "environment", existing.Name, "runtime", existing.Runtime)
	}

	env, err := h.createEnvironment(c.Request.Context(), user, challengeEntry)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}

	slog.Info("environment reset", "environment", env.Name, "user", user.ID, "challenge", challengeEntry.ID)
	c.JSON(http.StatusOK, api.ResetResponse{ChallengeTitle: challengeEntry.Title})
}

func (h *Handler) StopChallenge(c *gin.Context, id string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}

	challengeEntry, err := h.publishedChallenge(id)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}

	env, err := h.findProgressEnvironment(c.Request.Context(), user.ID, challengeEntry)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active environment for this challenge"})
		return
	}

	if err := h.db.Environment.FinishChallengeAttempt(c.Request.Context(), env.UID, postgres.AttemptStopped, time.Now().UTC()); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("record stopped attempt: %v", err)})
		return
	}
	if err := h.assistant.DeleteEnvironment(c.Request.Context(), env.UID); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("clear assistant session: %v", err)})
		return
	}
	if err := h.destroyEnvironment(c.Request.Context(), env); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("stop environment: %v", err)})
		return
	}

	c.JSON(http.StatusOK, api.StopResponse{
		Stopped:        true,
		ChallengeTitle: challengeEntry.Title,
	})
}

func (h *Handler) CreateTerminalTicket(c *gin.Context, challengeID string) {
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
	challengeEntry, err := h.publishedChallenge(challengeID)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}
	env, err := h.findEnvironment(c.Request.Context(), user.ID, challengeEntry)
	if err != nil || env.UID == "" || !terminalEnvironmentReady(env, h.nodeTerminal) {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active environment for this challenge"})
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
		ChallengeID:    challengeID,
		NodeName:       nodeName,
		WindowName:     windowName,
		ExpiresAt:      now.Add(terminalTicketTTL),
	}, now); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("create terminal ticket: %v", err)})
		return
	}
	c.JSON(http.StatusOK, api.TerminalTicketResponse{Ticket: ticket})
}

func (h *Handler) HandleTerminalTicket(c *gin.Context) {
	challengeID := c.Param("id")
	if h.db == nil || h.k8s == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "terminal dependencies are not configured"})
		return
	}
	if !terminalOriginAllowed(c.GetHeader("Origin"), h.uiOrigin) {
		c.JSON(http.StatusForbidden, api.ErrorResponse{Error: "terminal origin is not allowed"})
		return
	}
	challengeEntry, err := h.publishedChallenge(challengeID)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}
	ticket, err := h.db.Environment.ClaimTerminalTicket(c.Request.Context(), terminalTicketHash(c.Query("ticket")), challengeID, c.Query("window"), time.Now().UTC())
	if err != nil {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: "terminal ticket is invalid or expired"})
		return
	}
	env, err := h.findEnvironment(c.Request.Context(), ticket.UserID, challengeEntry)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active environment for this challenge"})
		return
	}
	if env.UID != ticket.EnvironmentUID {
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
	if env.Phase == breakfixv1.EnvironmentDraining {
		if err := h.resumeEnvironment(c.Request.Context(), env); err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("resume environment: %v", err)})
			return
		}
		env.Phase = breakfixv1.EnvironmentReady
	}
	stream, err := h.terminalStream(env, ticket.NodeName, windowName)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: err.Error()})
		return
	}

	slog.Info("terminal session started", "challenge", challengeID, "user", ticket.UserID)
	key := env.Runtime + "/" + env.Name
	wsUpgrade(c.Writer, c.Request, h.uiOrigin, env, runtimeAdapter, h.cooldownMin, stream, terminalSocketLifecycle{
		open: func() error {
			if err := h.db.Environment.OpenTerminalConnection(c.Request.Context(), postgres.TerminalConnection{
				ID:               connectionID,
				EnvironmentUID:   env.UID,
				UserID:           ticket.UserID,
				ChallengeID:      challengeID,
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

func (h *Handler) CloseTerminalWindow(c *gin.Context, challengeID, windowName string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	windowName, err := parseTerminalWindow(windowName)
	if err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	entry, err := h.publishedChallenge(challengeID)
	if err != nil {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "challenge not found"})
		return
	}
	env, err := h.findEnvironment(c.Request.Context(), user.ID, entry)
	if err != nil || !terminalEnvironmentReady(env, h.nodeTerminal) {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active environment for this challenge"})
		return
	}
	var requestedNode *string
	if raw := strings.TrimSpace(c.Query("node")); raw != "" {
		requestedNode = &raw
	}
	nodeName, err := terminalNodeName(env, requestedNode)
	if err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	if err := h.closeTerminalWindow(c.Request.Context(), env, nodeName, windowName); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("close terminal window: %v", err)})
		return
	}
	c.JSON(http.StatusOK, api.TerminalWindowCloseResponse{Closed: true})
}

func terminalEnvironmentReady(environment *activeEnvironment, nodeProvider NodeTerminalProvider) bool {
	if environment == nil || environment.UID == "" {
		return false
	}
	switch environment.Runtime {
	case challenge.RuntimeNode:
		return nodeProvider != nil && environment.NodeIdentity.Project != "" && len(environment.NodeIdentity.Nodes) == len(environment.Nodes)
	case challenge.RuntimeK8s:
		return environment.Namespace != "" && environment.WorkspacePod != ""
	default:
		return false
	}
}

func terminalNodeName(environment *activeEnvironment, requested *string) (string, error) {
	raw := ""
	if requested != nil {
		raw = strings.TrimSpace(*requested)
	}
	if environment.Runtime == challenge.RuntimeK8s {
		if raw != "" {
			return "", fmt.Errorf("k8s environments have a single management terminal")
		}
		return "", nil
	}
	if environment.Runtime != challenge.RuntimeNode {
		return "", fmt.Errorf("unsupported environment runtime %q", environment.Runtime)
	}
	if raw == "" {
		return "", fmt.Errorf("node is required for a node environment terminal")
	}
	for _, node := range environment.Nodes {
		if node.Name == raw {
			return raw, nil
		}
	}
	return "", fmt.Errorf("environment has no node %q", raw)
}

func (h *Handler) terminalStream(environment *activeEnvironment, nodeName, windowName string) (terminalStream, error) {
	sessionName := terminalSessionName(environment.UID)
	switch environment.Runtime {
	case challenge.RuntimeNode:
		if h.nodeTerminal == nil {
			return nil, fmt.Errorf("node terminal provider is unavailable")
		}
		return func(ctx context.Context, stdin io.Reader, stdout io.Writer, resize <-chan environmentdomain.Size) error {
			return h.nodeTerminal.ExecNodePTY(ctx, incus.ExecNodePTYRequest{
				EnvironmentUID: environment.UID, Revision: environment.SourceRevision, Identity: environment.NodeIdentity,
				LogicalName: nodeName, SessionName: sessionName, WindowName: windowName,
				Stdin: stdin, Stdout: stdout, Resize: resize,
			})
		}, nil
	case challenge.RuntimeK8s:
		return func(ctx context.Context, stdin io.Reader, stdout io.Writer, resize <-chan environmentdomain.Size) error {
			return h.k8s.ExecPTY(ctx, stdin, stdout, stdout, resize, environment.Namespace, environment.WorkspacePod, sessionName, windowName)
		}, nil
	default:
		return nil, fmt.Errorf("unsupported environment runtime %q", environment.Runtime)
	}
}

func (h *Handler) closeTerminalWindow(ctx context.Context, environment *activeEnvironment, nodeName, windowName string) error {
	sessionName := terminalSessionName(environment.UID)
	if environment.Runtime == challenge.RuntimeNode {
		return h.nodeTerminal.CloseNodePTYWindow(ctx, incus.CloseNodePTYWindowRequest{
			EnvironmentUID: environment.UID, Revision: environment.SourceRevision, Identity: environment.NodeIdentity,
			LogicalName: nodeName, SessionName: sessionName, WindowName: windowName,
		})
	}
	return h.k8s.ClosePTYWindow(environment.Namespace, environment.WorkspacePod, sessionName, windowName)
}
