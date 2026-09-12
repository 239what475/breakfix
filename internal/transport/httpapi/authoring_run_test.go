package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	appauthoring "github.com/breakfix/breakfix/internal/application/authoring"
	"github.com/breakfix/breakfix/internal/bootstrap/config"
	"github.com/breakfix/breakfix/internal/domain/authoring"
	testpostgres "github.com/breakfix/breakfix/internal/testkit/postgres"
	api "github.com/breakfix/breakfix/internal/transport/httpapi/generated"
)

func TestSendAuthoringMessageReceiptDoesNotStartASecondStream(t *testing.T) {
	database := testpostgres.New(t)
	if _, err := database.Identity.CreateUserWithAuth("user-one", "user-one", "", ""); err != nil {
		t.Fatalf("create authoring API user: %v", err)
	}
	session, err := database.Authoring.CreateAuthoringSession(context.Background(), authoring.Session{
		ID: "authoring-session-one", UserID: "user-one",
	}, authoring.Plan{})
	if err != nil {
		t.Fatalf("create authoring session: %v", err)
	}
	executor := &authoringHTTPExecutor{}
	runtime := appauthoring.NewRuntimeService(database.Authoring, "test-model", time.Minute, executor)
	cfg := config.Config{DataDir: t.TempDir(), JWTSecret: "authoring-http-jwt-secret"}
	handler, err := NewHandlerWithDependencies(database, nil, cfg, Dependencies{Authoring: runtime})
	if err != nil {
		t.Fatalf("create authoring API handler: %v", err)
	}
	router, err := SetupRouter(handler, cfg, nil)
	if err != nil {
		t.Fatalf("register authoring API routes: %v", err)
	}
	request := api.AuthoringMessageRequest{Content: "继续完善场景。", IdempotencyKey: "authoring-message-one"}
	first := generatorHTTPRequest(t, router, cfg, http.MethodPost, "/api/authoring/sessions/"+session.ID+"/messages", request)
	if first.Code != http.StatusOK || executor.calls != 1 {
		t.Fatalf("first authoring request: status=%d calls=%d body=%s", first.Code, executor.calls, first.Body.String())
	}
	replayed := generatorHTTPRequest(t, router, cfg, http.MethodPost, "/api/authoring/sessions/"+session.ID+"/messages", request)
	if replayed.Code != http.StatusAccepted || executor.calls != 1 {
		t.Fatalf("replayed authoring request: status=%d calls=%d body=%s", replayed.Code, executor.calls, replayed.Body.String())
	}
	var receipt api.AuthoringRunReceipt
	decodeGeneratorHTTPResponse(t, replayed, &receipt)
	if receipt.RunId == "" || receipt.Status != api.AuthoringRunReceiptStatusSucceeded {
		t.Fatalf("replayed authoring receipt = %#v", receipt)
	}
	messages, err := database.Authoring.ListMessages(context.Background(), session.RuntimeSessionID)
	if err != nil {
		t.Fatalf("list authoring messages: %v", err)
	}
	runs, err := database.Agent.ListRunsForOwner(context.Background(), "authoring-session", session.ID)
	if err != nil {
		t.Fatalf("list authoring runs: %v", err)
	}
	if len(messages) != 2 || len(runs) != 1 {
		t.Fatalf("idempotent HTTP records: messages=%#v runs=%#v", messages, runs)
	}
}

func TestRunningAuthoringMessageReceiptDoesNotStartAStream(t *testing.T) {
	database := testpostgres.New(t)
	if _, err := database.Identity.CreateUserWithAuth("user-one", "user-one", "", ""); err != nil {
		t.Fatalf("create authoring API user: %v", err)
	}
	session, err := database.Authoring.CreateAuthoringSession(context.Background(), authoring.Session{
		ID: "authoring-session-running", UserID: "user-one",
	}, authoring.Plan{})
	if err != nil {
		t.Fatalf("create authoring session: %v", err)
	}
	executor := &authoringHTTPExecutor{}
	runtime := appauthoring.NewRuntimeService(database.Authoring, "test-model", time.Minute, executor)
	request := api.AuthoringMessageRequest{Content: "继续完善场景。", IdempotencyKey: "authoring-message-running"}
	if _, _, created, err := runtime.StartTurn(context.Background(), "user-one", session.ID, request.IdempotencyKey, request.Content); err != nil || !created {
		t.Fatalf("start pending authoring turn: created=%t err=%v", created, err)
	}
	cfg := config.Config{DataDir: t.TempDir(), JWTSecret: "authoring-http-jwt-secret"}
	handler, err := NewHandlerWithDependencies(database, nil, cfg, Dependencies{Authoring: runtime})
	if err != nil {
		t.Fatalf("create authoring API handler: %v", err)
	}
	router, err := SetupRouter(handler, cfg, nil)
	if err != nil {
		t.Fatalf("register authoring API routes: %v", err)
	}
	replayed := generatorHTTPRequest(t, router, cfg, http.MethodPost, "/api/authoring/sessions/"+session.ID+"/messages", request)
	if replayed.Code != http.StatusAccepted || executor.calls != 0 {
		t.Fatalf("running authoring receipt: status=%d calls=%d body=%s", replayed.Code, executor.calls, replayed.Body.String())
	}
	var receipt api.AuthoringRunReceipt
	decodeGeneratorHTTPResponse(t, replayed, &receipt)
	if receipt.Status != api.AuthoringRunReceiptStatusRunning {
		t.Fatalf("running authoring receipt = %#v", receipt)
	}
}

func TestAuthoringRunEventProjectsAsTypedAPIState(t *testing.T) {
	event, err := authoring.NewRunEvent("authoring-run", authoring.RunTerminationDeadlineExceeded, "workspace")
	if err != nil {
		t.Fatalf("create authoring run event: %v", err)
	}
	messages := toAPIAuthoringMessages([]authoring.Message{{
		ID: "authoring-event", Role: "event", Content: "persisted event JSON", Event: &event, CreatedAt: time.Now().UTC(),
	}})
	if len(messages) != 1 || messages[0].Event == nil || messages[0].Event.Reason != api.DeadlineExceeded ||
		messages[0].Event.Recovery != api.Workspace || messages[0].Event.Kind != api.AuthoringRunInterrupted {
		t.Fatalf("projected authoring event = %#v", messages)
	}
}

type authoringHTTPExecutor struct {
	calls int
}

func (e *authoringHTTPExecutor) Run(context.Context, appauthoring.Execution, appauthoring.StageUpdater, func(appauthoring.StreamEvent)) (string, error) {
	e.calls++
	return "本轮已经完成。", nil
}

var _ appauthoring.Executor = (*authoringHTTPExecutor)(nil)
