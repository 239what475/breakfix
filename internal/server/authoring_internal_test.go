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
	"github.com/breakfix/breakfix/internal/authoring"
	"github.com/breakfix/breakfix/internal/config"
	"github.com/breakfix/breakfix/internal/testpostgres"
	"github.com/gin-gonic/gin"
)

func TestInternalAuthoringEndpointsFenceAttemptsAndFinalizeAtomically(t *testing.T) {
	database := testpostgres.New(t)
	ctx := context.Background()
	session, err := database.CreateAuthoringSession(ctx, authoring.Session{ID: "authoring-session", UserID: "author"}, authoring.Plan{})
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := database.StartAuthoringRun(ctx, session.ID, session.UserID, agentruntime.Message{
		ID: "authoring-user", Role: "user", Content: "创建一题明确的服务修复题",
	}, agentruntime.CreateRun{
		ID: "authoring-run", SessionID: session.RuntimeSessionID, Purpose: "authoring", OwnerKind: "authoring-session", OwnerRef: session.ID,
		Input: json.RawMessage(`{"base_revision":0}`), Model: "deepseek-v4-pro", PromptVersion: "authoring-v1", DeadlineAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := database.ClaimNext(ctx, "worker-one", time.Minute, time.Now().UTC())
	if err != nil || first == nil {
		t.Fatalf("claim first attempt = %#v, %v", first, err)
	}

	handler := NewHandler(database, nil, config.Config{InternalAPIKey: "internal-test-key"})
	router := gin.New()
	router.POST("/api/internal/agent-runs/:id/authoring/context", handler.InternalAuthoringContext)
	router.POST("/api/internal/agent-runs/:id/authoring/stage", handler.InternalAuthoringStage)
	router.POST("/api/internal/agent-runs/:id/authoring/finalize", handler.InternalAuthoringFinalize)

	postInternalAuthoring(t, router, "context", *first, authoring.LeaseCredential{Attempt: first.Run.Attempt, LeaseOwner: first.LeaseOwner}, http.StatusOK)
	now := time.Now().UTC()
	if err := database.Requeue(ctx, *first, now, "worker replaced", now); err != nil {
		t.Fatal(err)
	}
	second, err := database.ClaimNext(ctx, "worker-two", time.Minute, now.Add(time.Millisecond))
	if err != nil || second == nil || second.Run.Attempt != 2 {
		t.Fatalf("claim second attempt = %#v, %v", second, err)
	}
	postInternalAuthoring(t, router, "context", *first, authoring.LeaseCredential{Attempt: first.Run.Attempt, LeaseOwner: first.LeaseOwner}, http.StatusConflict)

	plan := authoring.Plan{
		Metadata:    authoring.Metadata{Title: "Repair a service", Description: "Repair the service and restore its health endpoint.", Difficulty: "easy", Runtime: "container"},
		Overview:    "修复服务配置，并确认健康检查恢复正常。",
		Checkpoints: []authoring.Checkpoint{{ID: "health", Title: "健康检查", Markdown: "健康检查返回成功。", Position: 1}},
	}
	postInternalAuthoring(t, router, "stage", *second, internalAuthoringStageRequest{
		LeaseCredential: authoring.LeaseCredential{Attempt: second.Run.Attempt, LeaseOwner: second.LeaseOwner},
		StageRevision:   0,
		Plan:            plan,
		Change:          authoring.Change{Kind: "full-plan", Summary: "补全题目约定", DifficultyImpact: "难度不变"},
	}, http.StatusOK)
	postInternalAuthoring(t, router, "finalize", *second, internalAuthoringFinalizeRequest{
		LeaseCredential: authoring.LeaseCredential{Attempt: second.Run.Attempt, LeaseOwner: second.LeaseOwner},
		Content:         "题意约定已更新，请审核左侧方案。",
	}, http.StatusNoContent)

	stored, err := database.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != agentruntime.RunSucceeded || stored.Attempt != 2 {
		t.Fatalf("finalized run = %#v", stored)
	}
	updated, err := database.GetAuthoringSession(ctx, session.ID, session.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CurrentRevision != 1 || updated.State != authoring.StateIntentReview {
		t.Fatalf("finalized session = %#v", updated)
	}
	if _, err := database.GetAuthoringStage(ctx, run.ID); err != authoring.ErrNotFound {
		t.Fatalf("private stage remained after finalization: %v", err)
	}
}

func postInternalAuthoring(t *testing.T, router *gin.Engine, endpoint string, claim agentruntime.Claim, body any, expectedStatus int) {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/internal/agent-runs/"+claim.Run.ID+"/authoring/"+endpoint, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Breakfix-Internal-Key", "internal-test-key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != expectedStatus {
		t.Fatalf("internal authoring %s = %d, want %d: %s", endpoint, response.Code, expectedStatus, response.Body.String())
	}
}
