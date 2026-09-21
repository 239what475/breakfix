package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	"github.com/breakfix/breakfix/internal/content/scenario"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
)

// blankScenarioProvider is the only blank runtime offered today; the CRD
// provider enum and the controller's installed plans both key off it.
const blankScenarioProvider = scenario.RuntimeK8s

// blankScenarioToken derives the deterministic content identity of a reader's
// blank scenario from the deployment-pinned library: the same user on the same
// pinned library always addresses the same environment, on every page.
func blankScenarioToken(sourceID, commit, language string) string {
	sum := sha256.Sum256([]byte(sourceID + "\x00" + commit + "\x00" + language))
	return hex.EncodeToString(sum[:16])
}

// blankScenarioTarget pins the user's library-scoped blank practice
// environment. The token rides both the content-id and content-revision
// labels: the binding is stable for the lifetime of the pinned library.
func (h *Handler) blankScenarioTarget(c *gin.Context) (environmentContentTarget, bool) {
	if h.documentationLibrary == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "documentation library is unavailable"})
		return environmentContentTarget{}, false
	}
	pinned := h.documentationLibrary.PinnedContext()
	token := blankScenarioToken(pinned.SourceID, pinned.Commit, pinned.Language)
	return environmentContentTarget{
		kind: environmentContentDocumentationBlank, id: token, revisionID: token,
		runtime: blankScenarioProvider, title: "Documentation practice", blank: true,
	}, true
}

// findBlankScenarioEnvironment resolves the reader's blank session by its
// deterministic name. The label-based findEnvironment cannot see a session
// while it is still provisioning: the runtime provider projection is empty
// until the controller's first observation, and the shared list filters on
// it. The name is the ownership fence, so a direct read plus an identity
// check is exact.
func (h *Handler) findBlankScenarioEnvironment(ctx context.Context, userID string, target environmentContentTarget) (*activeEnvironment, error) {
	adapter, err := h.environmentRuntimeAdapter(target.runtime)
	if err != nil {
		return nil, err
	}
	env, err := adapter.get(ctx, learningEnvironmentName(userID, target))
	if apierrors.IsNotFound(err) {
		return nil, errNoMatchingEnvironment
	}
	if err != nil {
		return nil, err
	}
	if env.Deleting || !isLiveEnvironmentPhase(env.Phase) ||
		env.UserID != userID || env.ScenarioRef != target.id || env.SourceRevision != target.revisionID || !env.Blank {
		return nil, errNoMatchingEnvironment
	}
	return env, nil
}

// blankScenarioState projects the session state machine the toolbar renders:
// none → creating → ready, reset returning to creating, failure staying
// retryable, and lifecycle reclamation reading as none.
func blankScenarioState(env *activeEnvironment) string {
	if env == nil {
		return "none"
	}
	if env.Operation == runtimev2.OperationResetting {
		return "creating"
	}
	switch env.Phase {
	case runtimev2.PhaseReady:
		return "ready"
	case runtimev2.PhaseFailed:
		return "failed"
	case runtimev2.PhasePending, runtimev2.PhaseProvisioning:
		return "creating"
	default:
		// Draining and Released are lifecycle reclamation: the session is over.
		return "none"
	}
}

func blankScenarioResponse(env *activeEnvironment) api.DocumentationScenarioEnvironment {
	state := api.DocumentationScenarioEnvironmentState(blankScenarioState(env))
	response := api.DocumentationScenarioEnvironment{State: state}
	if env != nil {
		environmentID, runtime := env.UID, env.Runtime
		response.EnvironmentId = &environmentID
		response.Runtime = &runtime
	}
	return response
}

// GetDocumentationScenario reports the reader's blank scenario session so the
// toolbar can render and the panel can poll Pending→Provisioning→Ready.
func (h *Handler) GetDocumentationScenario(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	target, ok := h.blankScenarioTarget(c)
	if !ok {
		return
	}
	env, err := h.findBlankScenarioEnvironment(c.Request.Context(), user.ID, target)
	if err != nil && !errors.Is(err, errNoMatchingEnvironment) {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("find blank scenario: %v", err)})
		return
	}
	// A reader polling a still-preparing session is actively watching it:
	// renew the idle lease so slow provisioning cannot starve the session
	// before its first readiness. The adapter resolves from the target's
	// runtime because an unprovisioned environment projects no runtime
	// provider yet. Ready sessions renew only through real use (terminal
	// attach or an explicit start), keeping the idle TTL meaningful. A failed
	// renewal never fails the read.
	if env != nil && (env.Phase == runtimev2.PhasePending || env.Phase == runtimev2.PhaseProvisioning || env.Operation == runtimev2.OperationResetting) {
		if adapter, adapterErr := h.environmentRuntimeAdapter(target.runtime); adapterErr == nil {
			_ = adapter.renewActivity(c.Request.Context(), env.Name, nowActivity())
		}
	}
	c.JSON(http.StatusOK, blankScenarioResponse(env))
}

// StartDocumentationScenario creates or adopts the reader's blank scenario.
// Creation returns as soon as the environment is named; readiness arrives
// through polling, never through a blocking request.
func (h *Handler) StartDocumentationScenario(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	target, ok := h.blankScenarioTarget(c)
	if !ok {
		return
	}
	env, err := h.findBlankScenarioEnvironment(c.Request.Context(), user.ID, target)
	if err != nil && !errors.Is(err, errNoMatchingEnvironment) {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("find blank scenario: %v", err)})
		return
	}
	if env != nil {
		switch env.Phase {
		case runtimev2.PhaseReady:
			// An explicit start is user activity: renew the idle lease.
			if err := h.resumeEnvironment(c.Request.Context(), env); err != nil {
				c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("resume environment: %v", err)})
				return
			}
		case runtimev2.PhaseDraining:
			// A drained environment occupies the deterministic name; clear it
			// so the same request can recreate below.
			if err := h.destroyEnvironmentAndWait(c.Request.Context(), env); err != nil {
				c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("stop drained environment: %v", err)})
				return
			}
			env = nil
		default:
			// Pending, Provisioning, or an in-flight reset: report the
			// session as it stands; creation is already in progress.
		}
	}
	if env == nil {
		env, err = h.adoptOrCreateBlankScenario(c.Request.Context(), user, target)
		if err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("start blank scenario: %v", err)})
			return
		}
		slog.Info("blank scenario started", "environment", env.Name, "user", user.ID, "runtime", env.Runtime)
	}
	c.JSON(http.StatusOK, blankScenarioResponse(env))
}

// adoptOrCreateBlankScenario creates the environment under its deterministic
// name and adopts a concurrent creation instead of racing it. Failed remains
// from an earlier attempt are cleared first: a retry means a fresh start.
func (h *Handler) adoptOrCreateBlankScenario(ctx context.Context, user *postgres.User, target environmentContentTarget) (*activeEnvironment, error) {
	adapter, err := h.environmentRuntimeAdapter(target.runtime)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < environmentCreateAttempts; attempt++ {
		name, createErr := adapter.create(ctx, user, target)
		if createErr == nil {
			return h.getEnvironment(ctx, target.runtime, name)
		}
		if !apierrors.IsAlreadyExists(createErr) {
			return nil, createErr
		}
		existing, getErr := adapter.get(ctx, learningEnvironmentName(user.ID, target))
		if apierrors.IsNotFound(getErr) {
			continue
		}
		if getErr != nil {
			return nil, fmt.Errorf("read concurrently created environment: %w", getErr)
		}
		if existing.Deleting || existing.Phase == runtimev2.PhaseFailed {
			if err := h.destroyEnvironmentAndWait(ctx, existing); err != nil {
				return nil, fmt.Errorf("clear previous environment: %w", err)
			}
			continue
		}
		if !environmentMatchesTarget(existing, user.ID, target) {
			return nil, fmt.Errorf("existing environment %q does not match the blank scenario binding", existing.Name)
		}
		return existing, nil
	}
	return nil, fmt.Errorf("environment creation is still racing; retry the request")
}

// ResetDocumentationScenario wipes the blank scenario to a fresh state. It is
// a Ready-session verb: while a reset is already running it stays idempotent,
// and a session that is not ready yet is a conflict.
func (h *Handler) ResetDocumentationScenario(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	target, ok := h.blankScenarioTarget(c)
	if !ok {
		return
	}
	env, err := h.findBlankScenarioEnvironment(c.Request.Context(), user.ID, target)
	if err != nil {
		if errors.Is(err, errNoMatchingEnvironment) {
			c.JSON(http.StatusConflict, api.ErrorResponse{Error: "no active blank scenario to reset"})
			return
		}
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("find blank scenario: %v", err)})
		return
	}
	if env.Operation == runtimev2.OperationResetting {
		c.JSON(http.StatusOK, blankScenarioResponse(env))
		return
	}
	if env.Phase != runtimev2.PhaseReady {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "blank scenario is not ready to reset"})
		return
	}
	adapter, err := h.environmentRuntimeAdapter(env.Runtime)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("select reset runtime: %v", err)})
		return
	}
	if adapter.requestReset == nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "runtime does not support reset"})
		return
	}
	if _, err := adapter.requestReset(c.Request.Context(), env.Name, types.UID(env.UID)); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("request environment reset: %v", err)})
		return
	}
	slog.Info("blank scenario reset requested", "environment", env.Name, "user", user.ID)
	env.Operation = runtimev2.OperationResetting
	c.JSON(http.StatusOK, blankScenarioResponse(env))
}

// StopDocumentationScenario closes the reader's blank scenario and releases
// its resources. Closing without an active session is a successful no-op.
func (h *Handler) StopDocumentationScenario(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	target, ok := h.blankScenarioTarget(c)
	if !ok {
		return
	}
	env, err := h.findBlankScenarioEnvironment(c.Request.Context(), user.ID, target)
	if err != nil {
		if errors.Is(err, errNoMatchingEnvironment) {
			c.JSON(http.StatusOK, api.DocumentationScenarioCloseResponse{Closed: true})
			return
		}
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("find blank scenario: %v", err)})
		return
	}
	if err := h.destroyEnvironmentAndWait(c.Request.Context(), env); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("stop environment: %v", err)})
		return
	}
	slog.Info("blank scenario closed", "environment", env.Name, "user", user.ID)
	c.JSON(http.StatusOK, api.DocumentationScenarioCloseResponse{Closed: true})
}

// CreateBlankScenarioTerminalTicket mints the one-time ticket a browser
// exchanges for the blank scenario terminal WebSocket.
func (h *Handler) CreateBlankScenarioTerminalTicket(c *gin.Context) {
	target, ok := h.blankScenarioTarget(c)
	if !ok {
		return
	}
	h.createContentTerminalTicket(c, target, target.id)
}

// HandleBlankScenarioTerminalTicket upgrades the one-time ticket to the blank
// scenario terminal WebSocket.
func (h *Handler) HandleBlankScenarioTerminalTicket(c *gin.Context) {
	target, ok := h.blankScenarioTarget(c)
	if !ok {
		return
	}
	h.handleContentTerminalTicket(c, target, target.id)
}
