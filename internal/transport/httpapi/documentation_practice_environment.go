package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	runtimev2 "github.com/breakfix/breakfix/api/v2"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	documentdomain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/breakfix/breakfix/internal/domain/runnable"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
	"github.com/gin-gonic/gin"
	"k8s.io/apimachinery/pkg/types"
)

// practiceRevisionToken bounds the revision identity that fences one
// practice's environment. Runnable revision IDs exceed the CRD label value
// limit of 63 bytes, so the immutable digest pins the identity instead: its
// leading hex is deterministic and stays stable for the lifetime of the
// revision.
func practiceRevisionToken(reference runnable.RevisionReference) string {
	return strings.TrimPrefix(reference.Digest, "sha256:")[:32]
}

// practiceEnvironmentTarget pins an environment to one published practice
// revision. The runnable revision reference is already frozen in the
// revision, so the binding resolves without another lookup. The runtime comes
// from the resolved runnable revision profile and is set by the caller.
func (h *Handler) practiceEnvironmentTarget(revision documentdomain.PracticeRevision) environmentContentTarget {
	reference := revision.RunnableRevisionRef
	return environmentContentTarget{
		kind: environmentContentDocumentationPractice, id: revision.ID, revisionID: practiceRevisionToken(reference),
		title:          revision.ReaderProjection.Title,
		resolveBinding: func(context.Context) (runnable.RevisionReference, error) { return reference, nil },
	}
}

// resolvePracticeTarget loads the reader-visible practice and its runtime into
// an environment target. Unknown IDs and projection-less revisions both report
// not found.
func (h *Handler) resolvePracticeTarget(c *gin.Context, practiceID string) (environmentContentTarget, bool) {
	if h.documentationReader == nil {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "documentation practices are unavailable"})
		return environmentContentTarget{}, false
	}
	revision, err := h.documentationReader.GetPublishedPractice(c.Request.Context(), practiceID)
	if errors.Is(err, postgres.ErrPublishedPracticeNotFound) {
		c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "documentation practice is unavailable"})
		return environmentContentTarget{}, false
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "documentation practice query failed"})
		return environmentContentTarget{}, false
	}
	profile, err := h.documentationReader.ResolveRunnableRevision(c.Request.Context(), revision.RunnableRevisionRef.ID, revision.RunnableRevisionRef.Digest)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: "documentation practice runtime is unavailable"})
		return environmentContentTarget{}, false
	}
	target := h.practiceEnvironmentTarget(revision)
	target.runtime = string(profile.Spec.RuntimeProfile.Runtime)
	return target, true
}

// StartDocumentationPracticeEnvironment starts or resumes the practice's
// environment with the Operations find-or-create semantics: an existing live
// environment resumes (Draining renews its lease) and waits until Ready; a
// missing one is created under a deterministic name that fences concurrent
// starts.
func (h *Handler) StartDocumentationPracticeEnvironment(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	target, ok := h.resolvePracticeTarget(c, c.Param("id"))
	if !ok {
		return
	}

	existing, err := h.findEnvironment(c.Request.Context(), user.ID, target)
	if err != nil && !errors.Is(err, errNoMatchingEnvironment) {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("find existing environment: %v", err)})
		return
	}
	if existing != nil {
		if existing.Phase == runtimev2.PhaseDraining {
			if err := h.resumeEnvironment(c.Request.Context(), existing); err != nil {
				c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("resume environment: %v", err)})
				return
			}
		}
		if existing.Phase != runtimev2.PhaseReady {
			if _, err := h.waitEnvironmentReady(c.Request.Context(), existing.Runtime, existing.Name, environmentDeletionTimeout(existing.Runtime)); err != nil {
				c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("wait for existing environment: %v", err)})
				return
			}
		}
		slog.Info("resuming existing environment", "environment", existing.Name, "practice", target.id, "runtime", existing.Runtime)
		c.JSON(http.StatusOK, documentationPracticeEnvironmentResponse(existing))
		return
	}

	env, err := h.createEnvironment(c.Request.Context(), user, target)
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: err.Error()})
		return
	}
	slog.Info("environment started", "environment", env.Name, "user", user.ID, "practice", target.id, "runtime", target.runtime)
	c.JSON(http.StatusOK, documentationPracticeEnvironmentResponse(env))
}

// GetDocumentationPracticeEnvironment reports the live environment's phase
// and terminal node info so the panel can poll Pending→Provisioning→Ready.
func (h *Handler) GetDocumentationPracticeEnvironment(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	target, ok := h.resolvePracticeTarget(c, c.Param("id"))
	if !ok {
		return
	}
	env, err := h.findEnvironment(c.Request.Context(), user.ID, target)
	if err != nil {
		if errors.Is(err, errNoMatchingEnvironment) {
			c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active environment for this practice"})
			return
		}
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("find environment: %v", err)})
		return
	}
	c.JSON(http.StatusOK, documentationPracticeEnvironmentResponse(env))
}

// StopDocumentationPracticeEnvironment destroys the practice's environment and
// waits. Practices keep no learning record, so no attempt is written.
func (h *Handler) StopDocumentationPracticeEnvironment(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	target, ok := h.resolvePracticeTarget(c, c.Param("id"))
	if !ok {
		return
	}
	env, err := h.findEnvironment(c.Request.Context(), user.ID, target)
	if err != nil {
		if errors.Is(err, errNoMatchingEnvironment) {
			c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active environment for this practice"})
			return
		}
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("find environment: %v", err)})
		return
	}
	if err := h.destroyEnvironmentAndWait(c.Request.Context(), env); err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("stop environment: %v", err)})
		return
	}
	c.JSON(http.StatusOK, api.DocumentationPracticeStopResponse{Stopped: true})
}

// ResetDocumentationPracticeEnvironment bumps the environment's reset nonce so
// the controller re-provisions it from the same immutable revision.
func (h *Handler) ResetDocumentationPracticeEnvironment(c *gin.Context) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	target, ok := h.resolvePracticeTarget(c, c.Param("id"))
	if !ok {
		return
	}
	env, err := h.findEnvironment(c.Request.Context(), user.ID, target)
	if err != nil {
		if errors.Is(err, errNoMatchingEnvironment) {
			c.JSON(http.StatusNotFound, api.ErrorResponse{Error: "no active environment for this practice"})
			return
		}
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("find environment: %v", err)})
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
	nonce, err := adapter.requestReset(c.Request.Context(), env.Name, types.UID(env.UID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, api.ErrorResponse{Error: fmt.Sprintf("request environment reset: %v", err)})
		return
	}
	slog.Info("environment reset requested", "environment", env.Name, "runtime", env.Runtime)
	c.JSON(http.StatusOK, api.DocumentationPracticeResetResponse{Reset: true, ResetNonce: int(nonce)})
}

func documentationPracticeEnvironmentResponse(env *activeEnvironment) api.DocumentationPracticeEnvironment {
	nodes := make([]string, 0, len(env.Nodes))
	for _, node := range env.Nodes {
		nodes = append(nodes, node.Name)
	}
	return api.DocumentationPracticeEnvironment{
		Phase:   string(env.Phase),
		Runtime: env.Runtime,
		Nodes:   nodes,
	}
}
