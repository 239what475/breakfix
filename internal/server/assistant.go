package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/breakfix/breakfix/internal/assistant"
	"github.com/breakfix/breakfix/internal/challenge"
	"github.com/breakfix/breakfix/internal/db"
	breakfixv1 "github.com/breakfix/breakfix/internal/k8s/apis/breakfix/v1"
	"github.com/gin-gonic/gin"
)

type assistantMessageRequest struct {
	Content       string   `json:"content"`
	CurrentWindow string   `json:"current_window"`
	OpenWindows   []string `json:"open_windows"`
}

type assistantConversationResponse struct {
	ID          string              `json:"id"`
	ChallengeID string              `json:"challenge_id"`
	Messages    []assistant.Message `json:"messages"`
	ActiveTurn  *assistant.Turn     `json:"active_turn,omitempty"`
}

func (h *Handler) GetChallengeAssistant(c *gin.Context, challengeID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	request, err := h.assistantRequest(c.Request.Context(), user, challengeID, "shell-1", []string{"shell-1"})
	if err != nil {
		h.writeAssistantError(c, err)
		return
	}
	session, messages, err := h.assistant.GetOrCreate(c.Request.Context(), request)
	if err != nil {
		h.writeAssistantError(c, err)
		return
	}
	c.JSON(http.StatusOK, assistantConversationResponse{
		ID:          session.ID,
		ChallengeID: challengeID,
		Messages:    messages,
		ActiveTurn:  h.assistant.ActiveTurn(session.ID),
	})
}

func (h *Handler) SendChallengeAssistantMessage(c *gin.Context, challengeID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	var body assistantMessageRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	request, err := h.assistantRequest(c.Request.Context(), user, challengeID, body.CurrentWindow, body.OpenWindows)
	if err != nil {
		h.writeAssistantError(c, err)
		return
	}

	adapter, err := h.environmentRuntimeAdapter(request.Runtime)
	if err != nil {
		h.writeAssistantError(c, err)
		return
	}
	session, turn, err := h.assistant.StartTurn(c.Request.Context(), request, body.Content, func(turnCtx context.Context) {
		keepEnvironmentLeaseAlive(turnCtx, adapter, request.EnvironmentName, request.IdleTTL, nil)
	})
	if err != nil {
		h.writeAssistantError(c, err)
		return
	}
	subscription, err := h.assistant.Subscribe(session.ID, turn.ID)
	if err != nil {
		h.writeAssistantError(c, err)
		return
	}
	h.streamAssistantTurn(c, subscription)
}

func (h *Handler) StreamChallengeAssistantTurn(c *gin.Context, challengeID, turnID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	request, err := h.assistantRequest(c.Request.Context(), user, challengeID, "shell-1", []string{"shell-1"})
	if err != nil {
		h.writeAssistantError(c, err)
		return
	}
	session, _, err := h.assistant.GetOrCreate(c.Request.Context(), request)
	if err != nil {
		h.writeAssistantError(c, err)
		return
	}
	subscription, err := h.assistant.Subscribe(session.ID, turnID)
	if err != nil {
		h.writeAssistantError(c, err)
		return
	}
	h.streamAssistantTurn(c, subscription)
}

func (h *Handler) streamAssistantTurn(c *gin.Context, subscription *assistant.Subscription) {
	defer subscription.Close()
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case event := <-subscription.Events:
			writeAssistantSSE(c, event.Type, event)
			if event.Type == "complete" || event.Type == "error" {
				return
			}
		case <-heartbeat.C:
			_, _ = fmt.Fprint(c.Writer, ": keepalive\n\n")
			c.Writer.Flush()
		case <-c.Request.Context().Done():
			return
		}
	}
}

func (h *Handler) assistantRequest(ctx context.Context, user *db.User, challengeID, currentWindow string, openWindows []string) (assistant.Request, error) {
	entry, err := h.publishedChallenge(challengeID)
	if err != nil {
		return assistant.Request{}, err
	}
	env, err := h.findEnvironment(ctx, user.ID, entry)
	if err != nil {
		return assistant.Request{}, fmt.Errorf("no active environment for this challenge")
	}
	if env.Phase == breakfixv1.EnvironmentDraining {
		if err := h.resumeEnvironment(ctx, env); err != nil {
			return assistant.Request{}, fmt.Errorf("resume environment: %w", err)
		}
		env, err = h.getEnvironment(ctx, env.Runtime, env.Name)
		if err != nil {
			return assistant.Request{}, err
		}
	}
	if env.Phase != breakfixv1.EnvironmentReady || env.WorkspacePod == "" {
		return assistant.Request{}, fmt.Errorf("environment is not ready for assistant context")
	}
	windows, current, err := assistantWindows(currentWindow, openWindows)
	if err != nil {
		return assistant.Request{}, err
	}
	content, err := challenge.ReadContent(entry)
	if err != nil {
		return assistant.Request{}, err
	}
	return assistant.Request{
		UserID:           user.ID,
		EnvironmentUID:   env.UID,
		EnvironmentName:  env.Name,
		Runtime:          env.Runtime,
		ChallengeID:      entry.ID,
		ChallengeTitle:   entry.Title,
		Problem:          content.Problem,
		CurrentWindow:    current,
		OpenWindows:      windows,
		EnvironmentPhase: string(env.Phase),
		IdleTTL:          environmentIdleTTL(env, time.Duration(h.cooldownMin)*time.Minute),
		Checkpoints:      assistantCheckpointSnapshot(entry, env),
		Reader: &environmentAssistantReader{
			client:         h.k8s,
			getEnvironment: h.getEnvironment,
			env:            env,
			entry:          entry,
			content:        content,
		},
	}, nil
}

func assistantWindows(currentWindow string, openWindows []string) ([]string, string, error) {
	if strings.TrimSpace(currentWindow) == "" {
		currentWindow = "shell-1"
	}
	if _, err := parseTerminalWindow(currentWindow); err != nil {
		return nil, "", err
	}
	windows := make([]string, 0, len(openWindows)+1)
	seen := make(map[string]struct{}, len(openWindows)+1)
	for _, window := range append(openWindows, currentWindow) {
		if _, err := parseTerminalWindow(window); err != nil {
			return nil, "", err
		}
		if _, ok := seen[window]; ok {
			continue
		}
		seen[window] = struct{}{}
		windows = append(windows, window)
	}
	return windows, currentWindow, nil
}

func assistantCheckpointSnapshot(entry *challenge.Entry, env *activeEnvironment) assistant.CheckpointSnapshot {
	titles := make(map[string]string, len(entry.Checkpoints))
	for _, checkpoint := range entry.Checkpoints {
		titles[checkpoint.ID] = checkpoint.Title
	}
	result := assistant.CheckpointSnapshot{Results: []assistant.CheckpointResult{}}
	if env.Checkpoints == nil {
		return result
	}
	result.Error = env.Checkpoints.Error
	if env.Checkpoints.CheckedAt != nil {
		checkedAt := env.Checkpoints.CheckedAt.Time
		result.CheckedAt = &checkedAt
	}
	for _, check := range env.Checkpoints.Results {
		result.Results = append(result.Results, assistant.CheckpointResult{
			ID: check.ID, Title: titles[check.ID], Passed: check.Passed, Summary: check.Summary, Details: check.Details,
		})
	}
	return result
}

func (h *Handler) writeAssistantError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	message := err.Error()
	if strings.Contains(message, "not found") || strings.Contains(message, "no active environment") {
		status = http.StatusNotFound
	}
	if strings.Contains(message, "not ready") || strings.Contains(message, "already responding") {
		status = http.StatusConflict
	}
	c.JSON(status, map[string]string{"error": message})
}

func writeAssistantSSE(c *gin.Context, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, data)
	c.Writer.Flush()
}

type environmentAssistantReader struct {
	client interface {
		CaptureTMUXPane(context.Context, string, string, string, string, int, int) ([]string, int, error)
		ListPodFiles(context.Context, string, string, string, int, int) ([]string, int, error)
		ReadPodFile(context.Context, string, string, string, int64, int) (string, int64, error)
	}
	getEnvironment func(context.Context, string, string) (*activeEnvironment, error)
	env            *activeEnvironment
	entry          *challenge.Entry
	content        *challenge.Content
}

func (r *environmentAssistantReader) TerminalScrollback(ctx context.Context, window string, offset, lines int) (assistant.Scrollback, error) {
	values, total, err := r.client.CaptureTMUXPane(ctx, r.env.Namespace, r.env.WorkspacePod, "breakfix-"+r.env.Name, window, offset, lines)
	if err != nil {
		return assistant.Scrollback{}, err
	}
	return assistant.Scrollback{Window: window, Offset: offset, Lines: values, TotalLines: total, HasMore: offset+len(values) < total}, nil
}

func (r *environmentAssistantReader) CheckpointStatus(ctx context.Context) (assistant.CheckpointSnapshot, error) {
	env, err := r.getEnvironment(ctx, r.env.Runtime, r.env.Name)
	if err != nil {
		return assistant.CheckpointSnapshot{}, err
	}
	if env.UID != r.env.UID {
		return assistant.CheckpointSnapshot{}, fmt.Errorf("assistant environment changed")
	}
	return assistantCheckpointSnapshot(r.entry, env), nil
}

func (r *environmentAssistantReader) ListEnvironmentFiles(ctx context.Context, path string, offset, limit int) (assistant.EnvironmentFiles, error) {
	entries, total, err := r.client.ListPodFiles(ctx, r.env.Namespace, r.env.WorkspacePod, path, offset, limit)
	if err != nil {
		return assistant.EnvironmentFiles{}, err
	}
	return assistant.EnvironmentFiles{Path: path, Offset: offset, Entries: entries, Total: total, HasMore: offset+len(entries) < total}, nil
}

func (r *environmentAssistantReader) ReadEnvironmentFile(ctx context.Context, path string, offset int64, maxBytes int) (assistant.EnvironmentFile, error) {
	content, size, err := r.client.ReadPodFile(ctx, r.env.Namespace, r.env.WorkspacePod, path, offset, maxBytes)
	if err != nil {
		return assistant.EnvironmentFile{}, err
	}
	nextOffset := offset + int64(len([]byte(content)))
	return assistant.EnvironmentFile{Path: path, Offset: offset, Content: content, Size: size, NextOffset: nextOffset, HasMore: nextOffset < size}, nil
}

func (r *environmentAssistantReader) Solution(context.Context) (string, error) {
	return r.content.Solution, nil
}
