package httpapi

import (
	"context"
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

// playgroundProvider is the only blank runtime offered today; the CRD provider
// enum and the controller's installed plans both key off it.
const playgroundProvider = scenario.RuntimeK8s

// playgroundContentID rides the terminal tickets as the scenario identifier:
// the playground has no content identity, so a fixed word keeps the ticket
// rows and logs self-describing.
const playgroundContentID = "playground"

// playgroundTarget pins the user's personal practice environment. The binding
// is the user alone — no library, page, or content identity participates — so
// the same session is carried across the whole site.
func (h *Handler) playgroundTarget() environmentContentTarget {
	return environmentContentTarget{
		kind: environmentContentPlayground, runtime: playgroundProvider,
		title: "Playground", blank: true,
	}
}

// findPlaygroundEnvironment resolves the user's session by its deterministic
// name. The label-based findEnvironment cannot see a session while it is still
// provisioning: the runtime provider projection is empty until the
// controller's first observation, and the shared list filters on it. The name
// is the ownership fence, so a direct read plus an identity check is exact.
func (h *Handler) findPlaygroundEnvironment(ctx context.Context, userID string, target environmentContentTarget) (*activeEnvironment, error) {
	adapter, err := h.environmentRuntimeAdapter(target.runtime)
	if err != nil {
		return nil, err
	}
	env, err := adapter.get(ctx, playgroundEnvironmentName(userID))
	if apierrors.IsNotFound(err) {
		return nil, errNoMatchingEnvironment
	}
	if err != nil {
		return nil, err
	}
	if env.Deleting || !isLiveEnvironmentPhase(env.Phase) ||
		env.UserID != userID || env.ScenarioRef != "" || env.SourceRevision != "" || !env.Blank {
		return nil, errNoMatchingEnvironment
	}
	return env, nil
}

// playgroundState projects the session state machine the floating ball
// renders: none → creating → ready, reset returning to creating, failure
// staying retryable, and lifecycle reclamation reading as none.
func playgroundState(env *activeEnvironment) string {
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

func playgroundResponse(env *activeEnvironment) api.PlaygroundEnvironment {
	state := api.PlaygroundEnvironmentState(playgroundState(env))
	response := api.PlaygroundEnvironment{State: state}
	if env != nil {
		environmentID, runtime := env.UID, env.Runtime
		response.EnvironmentId = &environmentID
		response.Runtime = &runtime
	}
	return response
}

// GetPlayground reports the user's playground session so the floating ball
// can render and the panel can poll Pending→Provisioning→Ready.
func (h *Handler) GetPlayground(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	target := h.playgroundTarget()
	env, err := h.findPlaygroundEnvironment(c.Request.Context(), user.ID, target)
	if err != nil && !errors.Is(err, errNoMatchingEnvironment) {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("find playground: %v", err)})
		return
	}
	// A user polling a still-preparing session is actively watching it:
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
	c.JSON(http.StatusOK, playgroundResponse(env))
}

// playgroundAtCapacity reports whether the site-wide playground fleet already
// holds the configured number of active sessions. The counting口径 matches
// findPlaygroundEnvironment — content-kind label, no content identity, live
// phase, not deleting — so everything the ball would call "a session" occupies
// capacity, including Draining environments whose resources the Reaper has not
// reclaimed yet. The gate is deliberately soft: it reads one list without a
// lock, so concurrent creates may briefly overshoot; the TTLs stay the hard
// bound on fleet size.
func (h *Handler) playgroundAtCapacity(ctx context.Context) (bool, error) {
	selector := fmt.Sprintf("breakfix.dev/content-kind=%s", environmentContentPlayground)
	items, err := h.k8s.ListRuntimeEnvironments(ctx, h.crdNamespace, selector)
	if err != nil {
		return false, err
	}
	active := 0
	for index := range items.Items {
		environment := &items.Items[index]
		if environment.DeletionTimestamp != nil {
			continue
		}
		if environment.Spec.BlankRuntime == nil ||
			environment.Labels["breakfix.dev/content-id"] != "" ||
			environment.Labels["breakfix.dev/content-revision"] != "" ||
			!isLiveEnvironmentPhase(environment.Status.Phase) {
			continue
		}
		active++
	}
	return active >= h.playgroundMaxActive, nil
}

// StartPlayground creates or adopts the user's playground. Creation returns
// as soon as the environment is named; readiness arrives through polling,
// never through a blocking request.
func (h *Handler) StartPlayground(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	target := h.playgroundTarget()
	env, err := h.findPlaygroundEnvironment(c.Request.Context(), user.ID, target)
	if err != nil && !errors.Is(err, errNoMatchingEnvironment) {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("find playground: %v", err)})
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
		// The capacity gate sits on the creation path only: adopting or
		// resuming an existing session, polling, reset, and close are never
		// blocked, and a user whose own drained environment was just cleared
		// above recreates against the reclaimed count.
		atCapacity, capacityErr := h.playgroundAtCapacity(c.Request.Context())
		if capacityErr != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("count playground fleet: %v", capacityErr)})
			return
		}
		if atCapacity {
			c.JSON(http.StatusTooManyRequests, api.ErrorResponse{Error: "playground is at capacity"})
			return
		}
		env, err = h.adoptOrCreatePlayground(c.Request.Context(), user, target)
		if err != nil {
			c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("start playground: %v", err)})
			return
		}
		slog.Info("playground started", "environment", env.Name, "user", user.ID, "runtime", env.Runtime)
	}
	c.JSON(http.StatusOK, playgroundResponse(env))
}

// adoptOrCreatePlayground creates the environment under its deterministic
// name and adopts a concurrent creation instead of racing it. Failed remains
// from an earlier attempt are cleared first: a retry means a fresh start.
func (h *Handler) adoptOrCreatePlayground(ctx context.Context, user *postgres.User, target environmentContentTarget) (*activeEnvironment, error) {
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
		existing, getErr := adapter.get(ctx, playgroundEnvironmentName(user.ID))
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
			return nil, fmt.Errorf("existing environment %q does not match the playground binding", existing.Name)
		}
		return existing, nil
	}
	return nil, fmt.Errorf("environment creation is still racing; retry the request")
}

// ResetPlayground wipes the playground to a fresh state. It is a Ready-session
// verb: while a reset is already running it stays idempotent, and a session
// that is not ready yet is a conflict.
func (h *Handler) ResetPlayground(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	target := h.playgroundTarget()
	env, err := h.findPlaygroundEnvironment(c.Request.Context(), user.ID, target)
	if err != nil {
		if errors.Is(err, errNoMatchingEnvironment) {
			c.JSON(http.StatusConflict, api.ErrorResponse{Error: "no active playground to reset"})
			return
		}
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("find playground: %v", err)})
		return
	}
	if env.Operation == runtimev2.OperationResetting {
		c.JSON(http.StatusOK, playgroundResponse(env))
		return
	}
	if env.Phase != runtimev2.PhaseReady {
		c.JSON(http.StatusConflict, api.ErrorResponse{Error: "playground is not ready to reset"})
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
	slog.Info("playground reset requested", "environment", env.Name, "user", user.ID)
	env.Operation = runtimev2.OperationResetting
	c.JSON(http.StatusOK, playgroundResponse(env))
}

// StopPlayground closes the user's playground and releases its resources.
// Closing without an active session is a successful no-op.
func (h *Handler) StopPlayground(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	target := h.playgroundTarget()
	env, err := h.findPlaygroundEnvironment(c.Request.Context(), user.ID, target)
	if err != nil {
		if errors.Is(err, errNoMatchingEnvironment) {
			c.JSON(http.StatusOK, api.PlaygroundCloseResponse{Closed: true})
			return
		}
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("find playground: %v", err)})
		return
	}
	if err := h.destroyEnvironmentAndWait(c.Request.Context(), env); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("stop environment: %v", err)})
		return
	}
	slog.Info("playground closed", "environment", env.Name, "user", user.ID)
	c.JSON(http.StatusOK, api.PlaygroundCloseResponse{Closed: true})
}

// CreatePlaygroundTerminalTicket mints the one-time ticket a browser
// exchanges for the playground terminal WebSocket.
func (h *Handler) CreatePlaygroundTerminalTicket(c *gin.Context) {
	h.createContentTerminalTicket(c, h.playgroundTarget(), playgroundContentID)
}

// HandlePlaygroundTerminalTicket upgrades the one-time ticket to the
// playground terminal WebSocket.
func (h *Handler) HandlePlaygroundTerminalTicket(c *gin.Context) {
	h.handleContentTerminalTicket(c, h.playgroundTarget(), playgroundContentID)
}
