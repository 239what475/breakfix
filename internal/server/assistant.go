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
	Content       string                      `json:"content"`
	CurrentNode   string                      `json:"current_node"`
	CurrentWindow string                      `json:"current_window"`
	Terminals     []assistant.TerminalContext `json:"terminals"`
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
	request, err := h.assistantRequest(c.Request.Context(), user, challengeID, assistant.RunInput{CurrentWindow: "shell-1"})
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
		ActiveTurn:  h.assistant.ActiveTurn(c.Request.Context(), session.ID),
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
	request, err := h.assistantRequest(c.Request.Context(), user, challengeID, assistant.RunInput{
		CurrentNode: body.CurrentNode, CurrentWindow: body.CurrentWindow, Terminals: body.Terminals,
	})
	if err != nil {
		h.writeAssistantError(c, err)
		return
	}

	session, turn, err := h.assistant.StartTurn(c.Request.Context(), request, body.Content)
	if err != nil {
		h.writeAssistantError(c, err)
		return
	}
	subscription, err := h.assistant.Subscribe(c.Request.Context(), session.ID, turn.ID)
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
	request, err := h.assistantRequest(c.Request.Context(), user, challengeID, assistant.RunInput{CurrentWindow: "shell-1"})
	if err != nil {
		h.writeAssistantError(c, err)
		return
	}
	session, _, err := h.assistant.GetOrCreate(c.Request.Context(), request)
	if err != nil {
		h.writeAssistantError(c, err)
		return
	}
	subscription, err := h.assistant.Subscribe(c.Request.Context(), session.ID, turnID)
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

func (h *Handler) assistantRequest(ctx context.Context, user *db.User, challengeID string, input assistant.RunInput) (assistant.Request, error) {
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
	if env.Phase != breakfixv1.EnvironmentReady || !terminalEnvironmentReady(env, h.nodeTerminal) {
		return assistant.Request{}, fmt.Errorf("environment is not ready for assistant context")
	}
	input, nodes, err := normalizeAssistantWorkspace(env, input)
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
	if env.Runtime == challenge.RuntimeNode {
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
