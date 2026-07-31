package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/assistant"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/testpostgres"
	"github.com/gin-gonic/gin"
)

func TestInternalAssistantEventsRequireTheCurrentLease(t *testing.T) {
	database := testpostgres.New(t)
	ctx := context.Background()
	if _, err := database.CreateSession(ctx, agentruntime.Session{
		ID: "assistant-session", Purpose: "assistant", OwnerKind: "environment", OwnerRef: "environment-one", UserRef: "user-one",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.CreateMessageAndRun(ctx, agentruntime.Message{
		ID: "assistant-user", SessionID: "assistant-session", Role: "user", Content: "help",
	}, agentruntime.CreateRun{
		ID:               "assistant-run",
		SessionID:        "assistant-session",
		Purpose:          "assistant",
		OwnerKind:        "environment",
		OwnerRef:         "environment-one",
		Input:            json.RawMessage(`{"current_window":"shell-1","open_windows":["shell-1"]}`),
		Model:            "deepseek-v4-pro",
		PromptVersion:    "assistant-v1",
		ExecutionTimeout: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	first, err := database.ClaimNext(ctx, "worker-one", time.Minute, time.Now().UTC())
	if err != nil || first == nil {
		t.Fatalf("claim assistant run = %#v, %v", first, err)
	}

	handler := NewHandler(database, nil, config.Config{InternalWorkers: testInternalWorkerKeys()})
	subscription, err := handler.assistant.Subscribe(ctx, "assistant-session", "assistant-run")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	select {
	case event := <-subscription.Events:
		if event.Type != "ready" {
			t.Fatalf("initial event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for assistant ready event")
	}

	router := gin.New()
	router.POST("/api/internal/agent-runs/:id/assistant/events", handler.InternalAssistantEvent)
	postAssistantInternalEvent(t, router, *first, http.StatusNoContent)
	select {
	case event := <-subscription.Events:
		if event.Type != "delta" || event.Content != "draft" {
			t.Fatalf("published event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for assistant delta")
	}

	now := time.Now().UTC()
	if err := database.Requeue(ctx, *first, now, "restart", now); err != nil {
		t.Fatal(err)
	}
	second, err := database.ClaimNext(ctx, "worker-two", time.Minute, now.Add(time.Millisecond))
	if err != nil || second == nil || second.Attempt != 2 {
		t.Fatalf("claim replacement attempt = %#v, %v", second, err)
	}
	postAssistantInternalEvent(t, router, *first, http.StatusConflict)
}

func postAssistantInternalEvent(t *testing.T, router *gin.Engine, claim agentruntime.Claim, expectedStatus int) {
	t.Helper()
	body, err := json.Marshal(assistant.InternalEventRequest{
		LeaseCredential: claim.Credential(),
		Type:            "delta",
		Content:         "draft",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/internal/agent-runs/"+claim.Run.ID+"/assistant/events", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Breakfix-Internal-Key", "agent-test-key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != expectedStatus {
		t.Fatalf("internal assistant event status = %d, want %d: %s", response.Code, expectedStatus, response.Body.String())
	}
}
