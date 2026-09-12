package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	breakfixv1 "github.com/breakfix/breakfix/api/v1"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	assistant "github.com/breakfix/breakfix/internal/application/assistant"
	"github.com/breakfix/breakfix/internal/content/scenario"
	"github.com/breakfix/breakfix/internal/transport/httpapi/stream"
	"github.com/gin-gonic/gin"
)

type assistantMessageRequest struct {
	Content       string                      `json:"content"`
	CurrentNode   string                      `json:"current_node"`
	CurrentWindow string                      `json:"current_window"`
	Terminals     []assistant.TerminalContext `json:"terminals"`
}

type assistantConversationResponse struct {
	ID         string              `json:"id"`
	ScenarioID string              `json:"scenario_id"`
	Messages   []assistant.Message `json:"messages"`
}

type assistantStreamEvent struct {
	RunID   string `json:"run_id"`
	Content string `json:"content,omitempty"`
	Tool    string `json:"tool,omitempty"`
}

type assistantStreamComplete struct {
	RunID   string            `json:"run_id"`
	Message assistant.Message `json:"message"`
}

func (h *Handler) GetScenarioAssistant(c *gin.Context, scenarioID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	request, err := h.assistantRequest(c.Request.Context(), user, scenarioID, assistant.RunInput{CurrentWindow: "shell-1"})
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
		ID:         session.ID,
		ScenarioID: scenarioID,
		Messages:   messages,
	})
}

func (h *Handler) SendScenarioAssistantMessage(c *gin.Context, scenarioID string) {
	user := h.requireUser(c)
	if user == nil {
		return
	}
	var body assistantMessageRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	request, err := h.assistantRequest(c.Request.Context(), user, scenarioID, assistant.RunInput{
		CurrentNode: body.CurrentNode, CurrentWindow: body.CurrentWindow, Terminals: body.Terminals,
	})
	if err != nil {
		h.writeAssistantError(c, err)
		return
	}

	session, run, err := h.assistant.StartTurn(c.Request.Context(), request, body.Content)
	if err != nil {
		h.writeAssistantError(c, err)
		return
	}
	h.streamAssistantTurn(c, session.ID, run.ID, request)
}

func (h *Handler) streamAssistantTurn(c *gin.Context, sessionID, runID string, request assistant.Request) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	stream.WriteSSE(c, "ready", assistantStreamEvent{RunID: runID})

	requestCtx := c.Request.Context()
	message, err := h.assistant.RunTurn(h.agentRuntimeContext(), sessionID, runID, request, func(event assistant.StreamEvent) {
		if requestCtx.Err() == nil {
			stream.WriteSSE(c, event.Type, assistantStreamEvent{RunID: runID, Content: event.Content, Tool: event.Tool})
		}
	})
	if err == nil {
		if requestCtx.Err() == nil {
			stream.WriteSSE(c, "complete", assistantStreamComplete{RunID: runID, Message: message})
		}
		return
	}
	if requestCtx.Err() == nil {
		stream.WriteSSE(c, "error", assistantStreamEvent{RunID: runID, Content: err.Error()})
	}
}

func (h *Handler) assistantRequest(ctx context.Context, user *postgres.User, scenarioID string, input assistant.RunInput) (assistant.Request, error) {
	entry, env, err := h.resolveEnvironmentScenario(ctx, user.ID, scenarioID, false)
	if err != nil {
		if errors.Is(err, errAmbiguousEnvironment) {
			return assistant.Request{}, scenarioEnvironmentError(err)
		}
		return assistant.Request{}, fmt.Errorf("no active environment for this scenario")
	}
	return h.assistantRequestForEnvironment(ctx, user.ID, entry, env, input)
}

// assistantRequestForEnvironment turns a previously resolved learning
// Environment into the assistant's fixed read-only tool boundary. Startup
// recovery uses the same path after resolving the Environment by its durable
// UID instead of by the browser route's scenario id.
func (h *Handler) assistantRequestForEnvironment(ctx context.Context, userID string, entry *scenario.Entry, env *activeEnvironment, input assistant.RunInput) (assistant.Request, error) {
	if entry == nil || env == nil || strings.TrimSpace(userID) == "" {
		return assistant.Request{}, fmt.Errorf("assistant environment context is incomplete")
	}
	if env.ScenarioRef != entry.ID {
		return assistant.Request{}, fmt.Errorf("assistant environment scenario does not match the requested scenario")
	}
	if env.Phase == breakfixv1.EnvironmentDraining {
		if err := h.resumeEnvironment(ctx, env); err != nil {
			return assistant.Request{}, fmt.Errorf("resume environment: %w", err)
		}
		refreshed, err := h.getEnvironment(ctx, env.Runtime, env.Name)
		if err != nil {
			return assistant.Request{}, err
		}
		env = refreshed
	}
	if env.Phase != breakfixv1.EnvironmentReady || !terminalEnvironmentReady(env, h.nodeTerminal) {
		return assistant.Request{}, fmt.Errorf("environment is not ready for assistant context")
	}
	input, nodes, err := normalizeAssistantWorkspace(env, input)
	if err != nil {
		return assistant.Request{}, err
	}
	content, err := scenario.ReadContent(entry)
	if err != nil {
		return assistant.Request{}, err
	}
	problem := ""
	if content.Problem != nil {
		problem = *content.Problem
	}
	return assistant.Request{
		UserID:           userID,
		EnvironmentUID:   env.UID,
		EnvironmentName:  env.Name,
		Runtime:          env.Runtime,
		ScenarioID:       entry.ID,
		ScenarioTitle:    entry.Title,
		Problem:          problem,
		Nodes:            nodes,
		CurrentNode:      input.CurrentNode,
		CurrentWindow:    input.CurrentWindow,
		Terminals:        input.Terminals,
		EnvironmentPhase: string(env.Phase),
		IdleTTL:          environmentIdleTTL(env, time.Duration(h.cooldownMin)*time.Minute),
		Checkpoints:      assistantCheckpointSnapshot(entry, env),
		Reader: &environmentAssistantReader{
			k8s:            h.k8s,
			node:           h.nodeTerminal,
			getEnvironment: h.getEnvironment,
			env:            env,
			entry:          entry,
			content:        content,
		},
	}, nil
}

func normalizeAssistantWorkspace(env *activeEnvironment, input assistant.RunInput) (assistant.RunInput, []string, error) {
	if env == nil {
		return input, nil, fmt.Errorf("assistant environment is required")
	}
	nodes := make([]string, len(env.Nodes))
	for index, node := range env.Nodes {
		nodes[index] = node.Name
	}
	currentNode := strings.TrimSpace(input.CurrentNode)
	if env.Runtime == scenario.RuntimeNode {
		if currentNode == "" && len(nodes) > 0 {
			currentNode = nodes[0]
		}
		if _, err := terminalNodeName(env, &currentNode); err != nil {
			return input, nil, err
		}
	} else if _, err := terminalNodeName(env, nilIfEmpty(currentNode)); err != nil {
		return input, nil, err
	}

	currentWindow, err := parseTerminalWindow(input.CurrentWindow)
	if err != nil {
		return input, nil, err
	}
	terminals := make([]assistant.TerminalContext, 0, len(input.Terminals)+1)
	seenNodes := make(map[string]struct{}, len(input.Terminals)+1)
	for _, terminalContext := range input.Terminals {
		node := strings.TrimSpace(terminalContext.Node)
		if _, err := terminalNodeName(env, nilIfEmpty(node)); err != nil {
			return input, nil, err
		}
		if _, duplicate := seenNodes[node]; duplicate {
			return input, nil, fmt.Errorf("duplicate terminal context for node %q", node)
		}
		windows, _, err := assistantWindows("", terminalContext.Windows)
		if err != nil {
			return input, nil, err
		}
		seenNodes[node] = struct{}{}
		terminals = append(terminals, assistant.TerminalContext{Node: node, Windows: windows})
	}
	currentIndex := -1
	for index := range terminals {
		if terminals[index].Node == currentNode {
			currentIndex = index
			break
		}
	}
	if currentIndex < 0 {
		terminals = append(terminals, assistant.TerminalContext{Node: currentNode, Windows: []string{currentWindow}})
	} else if !assistantWindowOpen(terminals[currentIndex].Windows, currentWindow) {
		terminals[currentIndex].Windows = append(terminals[currentIndex].Windows, currentWindow)
	}
	input.CurrentNode = currentNode
	input.CurrentWindow = currentWindow
	input.Terminals = terminals
	return input, nodes, nil
}

func nilIfEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func assistantWindows(currentWindow string, openWindows []string) ([]string, string, error) {
	currentWindow = strings.TrimSpace(currentWindow)
	if currentWindow != "" {
		if _, err := parseTerminalWindow(currentWindow); err != nil {
			return nil, "", err
		}
	}
	windows := make([]string, 0, len(openWindows)+1)
	seen := make(map[string]struct{}, len(openWindows)+1)
	values := append([]string(nil), openWindows...)
	if currentWindow != "" {
		values = append(values, currentWindow)
	}
	for _, rawWindow := range values {
		window, err := parseTerminalWindow(strings.TrimSpace(rawWindow))
		if err != nil {
			return nil, "", err
		}
		if _, ok := seen[window]; ok {
			continue
		}
		seen[window] = struct{}{}
		windows = append(windows, window)
	}
	if len(windows) == 0 {
		windows = append(windows, "shell-1")
	}
	if currentWindow == "" {
		currentWindow = windows[0]
	}
	return windows, currentWindow, nil
}

func assistantWindowOpen(windows []string, wanted string) bool {
	for _, window := range windows {
		if window == wanted {
			return true
		}
	}
	return false
}

func assistantCheckpointSnapshot(entry *scenario.Entry, env *activeEnvironment) assistant.CheckpointSnapshot {
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
