package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/api"
	"github.com/breakfix/breakfix/internal/assistant"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/gin-gonic/gin"
)

// InternalAssistantContext resolves Server-owned environment facts for one
// attempt. It never returns Kubernetes credentials or a Reader implementation.
func (h *Handler) InternalAssistantContext(c *gin.Context) {
	var credential assistant.LeaseCredential
	if !h.decodeInternalAgentRequest(c, &credential) {
		return
	}
	claim, request, err := h.assistantRequestForClaim(c.Request.Context(), c.Param("id"), credential)
	if err != nil {
		h.writeInternalAssistantError(c, err)
		return
	}
	_ = claim
	c.JSON(http.StatusOK, assistant.ExecutionContext{
		UserID:           request.UserID,
		EnvironmentUID:   request.EnvironmentUID,
		EnvironmentName:  request.EnvironmentName,
		Runtime:          request.Runtime,
		ChallengeID:      request.ChallengeID,
		ChallengeTitle:   request.ChallengeTitle,
		Problem:          request.Problem,
		CurrentWindow:    request.CurrentWindow,
		OpenWindows:      request.OpenWindows,
		EnvironmentPhase: request.EnvironmentPhase,
		Checkpoints:      request.Checkpoints,
	})
}

// InternalAssistantTool performs exactly one Server-owned, read-only
// environment lookup for a live Assistant attempt.
func (h *Handler) InternalAssistantTool(c *gin.Context) {
	var body assistant.InternalToolRequest
	if !h.decodeInternalAgentRequest(c, &body) {
		return
	}
	_, request, err := h.assistantRequestForClaim(c.Request.Context(), c.Param("id"), body.LeaseCredential)
	if err != nil {
		h.writeInternalAssistantError(c, err)
		return
	}
	if err := h.runInternalAssistantTool(c, request, c.Param("tool"), body.Arguments); err != nil {
		h.writeInternalAssistantError(c, err)
	}
}

// InternalAssistantEvent forwards transient output to currently connected
// browsers. The event is deliberately not written to PostgreSQL.
func (h *Handler) InternalAssistantEvent(c *gin.Context) {
	var body assistant.InternalEventRequest
	if !h.decodeInternalAgentRequest(c, &body) {
		return
	}
	claim, err := h.internalAssistantClaim(c.Request.Context(), c.Param("id"), body.LeaseCredential)
	if err != nil {
		h.writeInternalAssistantError(c, err)
		return
	}
	if body.Type == "delta" && strings.TrimSpace(body.Content) == "" {
		h.writeInternalAssistantError(c, errors.New("assistant delta content is required"))
		return
	}
	if body.Type == "tool" && strings.TrimSpace(body.Tool) == "" {
		h.writeInternalAssistantError(c, errors.New("assistant tool name is required"))
		return
	}
	if body.Type != "delta" && body.Type != "tool" && body.Type != "reset" {
		h.writeInternalAssistantError(c, fmt.Errorf("unsupported assistant event type %q", body.Type))
		return
	}
	h.assistant.Publish(claim.Run.ID, assistant.Event{Type: body.Type, Content: body.Content, Tool: body.Tool})
	c.Status(http.StatusNoContent)
}

func (h *Handler) decodeInternalAgentRequest(c *gin.Context, value any) bool {
	if h.internalAPIKey == "" {
		c.JSON(http.StatusServiceUnavailable, api.ErrorResponse{Error: "internal agent API is disabled"})
		return false
	}
	if c.GetHeader("X-Breakfix-Internal-Key") != h.internalAPIKey {
		c.JSON(http.StatusUnauthorized, api.ErrorResponse{Error: "invalid internal key"})
		return false
	}
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: fmt.Sprintf("invalid internal agent request: %v", err)})
		return false
	}
	if err := requireJSONEOF(decoder); err != nil {
		c.JSON(http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return false
	}
	return true
}

func (h *Handler) internalAssistantClaim(ctx context.Context, runID string, credential assistant.LeaseCredential) (*agentruntime.Claim, error) {
	if h.db == nil || h.assistant == nil {
		return nil, errors.New("assistant runtime is unavailable")
	}
	if strings.TrimSpace(runID) == "" || credential.Attempt < 1 || strings.TrimSpace(credential.LeaseOwner) == "" {
		return nil, errors.New("assistant run lease credentials are required")
	}
	run, err := h.db.GetRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Purpose != "assistant" || run.OwnerKind != "environment" || run.SessionID == "" {
		return nil, errors.New("agent run is not a current assistant attempt")
	}
	if run.Attempt != credential.Attempt {
		return nil, agentruntime.ErrLeaseLost
	}
	claim := &agentruntime.Claim{Run: *run, LeaseOwner: credential.LeaseOwner}
	if err := h.db.ValidateLease(ctx, *claim, time.Now().UTC()); err != nil {
		return nil, err
	}
	return claim, nil
}

func (h *Handler) assistantRequestForClaim(ctx context.Context, runID string, credential assistant.LeaseCredential) (*agentruntime.Claim, assistant.Request, error) {
	claim, err := h.internalAssistantClaim(ctx, runID, credential)
	if err != nil {
		return nil, assistant.Request{}, err
	}
	session, err := h.db.GetSession(ctx, claim.Run.SessionID)
	if err != nil {
		return nil, assistant.Request{}, err
	}
	if session.Purpose != "assistant" || session.OwnerKind != "environment" || session.OwnerRef != claim.Run.OwnerRef || strings.TrimSpace(session.UserRef) == "" {
		return nil, assistant.Request{}, errors.New("assistant session does not own this agent run")
	}
	input, err := decodeAssistantRunInput(claim.Run.Input)
	if err != nil {
		return nil, assistant.Request{}, err
	}
	env, err := h.findActiveEnvironmentByUID(ctx, session.UserRef, session.OwnerRef)
	if err != nil {
		return nil, assistant.Request{}, err
	}
	if env.Phase != "Ready" || strings.TrimSpace(env.WorkspacePod) == "" {
		return nil, assistant.Request{}, errors.New("assistant environment is not ready")
	}
	entry, err := h.publishedChallenge(env.ChallengeRef)
	if err != nil {
		return nil, assistant.Request{}, err
	}
	content, err := challenge.ReadContent(entry)
	if err != nil {
		return nil, assistant.Request{}, err
	}
	request := assistant.Request{
		UserID:           session.UserRef,
		EnvironmentUID:   env.UID,
		EnvironmentName:  env.Name,
		Runtime:          env.Runtime,
		ChallengeID:      entry.ID,
		ChallengeTitle:   entry.Title,
		Problem:          content.Problem,
		CurrentWindow:    input.CurrentWindow,
		OpenWindows:      input.OpenWindows,
		EnvironmentPhase: string(env.Phase),
		Checkpoints:      assistantCheckpointSnapshot(entry, env),
		Reader: &environmentAssistantReader{
			client:         h.k8s,
			getEnvironment: h.getEnvironment,
			env:            env,
			entry:          entry,
			content:        content,
		},
	}
	return claim, request, nil
}

func decodeAssistantRunInput(raw []byte) (assistant.RunInput, error) {
	var input assistant.RunInput
	if len(raw) == 0 {
		return input, errors.New("assistant run has no persisted workspace input")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, fmt.Errorf("decode assistant run input: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return input, err
	}
	windows, current, err := assistantWindows(input.CurrentWindow, input.OpenWindows)
	if err != nil {
		return input, err
	}
	input.CurrentWindow = current
	input.OpenWindows = windows
	return input, nil
}

func (h *Handler) runInternalAssistantTool(c *gin.Context, request assistant.Request, name string, raw json.RawMessage) error {
	if request.Reader == nil {
		return errors.New("assistant environment reader is unavailable")
	}
	switch name {
	case "get_terminal_scrollback":
		var input struct {
			Window string `json:"window"`
			Offset int    `json:"offset"`
			Lines  int    `json:"lines"`
		}
		if err := decodeStrictJSON(raw, &input); err != nil {
			return err
		}
		if input.Window == "" {
			input.Window = request.CurrentWindow
		}
		if !assistantWindowOpen(request.OpenWindows, input.Window) {
			return fmt.Errorf("terminal window %q is not open in this workspace", input.Window)
		}
		input.Offset = clampAssistantInt(input.Offset, 0, 10000, 0)
		input.Lines = clampAssistantInt(input.Lines, 1, 250, 120)
		value, err := request.Reader.TerminalScrollback(c.Request.Context(), input.Window, input.Offset, input.Lines)
		if err != nil {
			return err
		}
		c.JSON(http.StatusOK, value)
		return nil
	case "get_checkpoint_status":
		if err := decodeStrictJSON(raw, &struct{}{}); err != nil {
			return err
		}
		value, err := request.Reader.CheckpointStatus(c.Request.Context())
		if err != nil {
			return err
		}
		c.JSON(http.StatusOK, value)
		return nil
	case "list_environment_files":
		var input struct {
			Path   string `json:"path"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
		}
		if err := decodeStrictJSON(raw, &input); err != nil {
			return err
		}
		if input.Path == "" {
			input.Path = "/"
		}
		input.Offset = clampAssistantInt(input.Offset, 0, 10000, 0)
		input.Limit = clampAssistantInt(input.Limit, 1, 200, 100)
		value, err := request.Reader.ListEnvironmentFiles(c.Request.Context(), input.Path, input.Offset, input.Limit)
		if err != nil {
			return err
		}
		c.JSON(http.StatusOK, value)
		return nil
	case "read_environment_file":
		var input struct {
			Path     string `json:"path"`
			Offset   int64  `json:"offset"`
			MaxBytes int    `json:"max_bytes"`
		}
		if err := decodeStrictJSON(raw, &input); err != nil {
			return err
		}
		if strings.TrimSpace(input.Path) == "" {
			return errors.New("path is required")
		}
		if input.Offset < 0 {
			input.Offset = 0
		}
		input.MaxBytes = clampAssistantInt(input.MaxBytes, 1, 32768, 16384)
		value, err := request.Reader.ReadEnvironmentFile(c.Request.Context(), input.Path, input.Offset, input.MaxBytes)
		if err != nil {
			return err
		}
		c.JSON(http.StatusOK, value)
		return nil
	case "get_solution":
		if err := decodeStrictJSON(raw, &struct{}{}); err != nil {
			return err
		}
		value, err := request.Reader.Solution(c.Request.Context())
		if err != nil {
			return err
		}
		c.JSON(http.StatusOK, gin.H{"content": value})
		return nil
	default:
		return fmt.Errorf("unsupported assistant tool %q", name)
	}
}

func decodeStrictJSON(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode assistant tool arguments: %w", err)
	}
	return requireJSONEOF(decoder)
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("internal assistant request has a second JSON document")
		}
		return fmt.Errorf("decode internal assistant request suffix: %w", err)
	}
	return nil
}

func assistantWindowOpen(windows []string, target string) bool {
	for _, window := range windows {
		if window == target {
			return true
		}
	}
	return false
}

func clampAssistantInt(value, min, max, fallback int) int {
	if value == 0 && fallback != 0 {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func (h *Handler) writeInternalAssistantError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, agentruntime.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, agentruntime.ErrLeaseLost):
		status = http.StatusConflict
	case strings.Contains(err.Error(), "not ready"), strings.Contains(err.Error(), "no active environment"):
		status = http.StatusConflict
	}
	c.JSON(status, api.ErrorResponse{Error: err.Error()})
}
