package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/adapter/incus"
	"github.com/breakfix/breakfix/internal/adapter/postgres"
	documentdomain "github.com/breakfix/breakfix/internal/domain/documentpractice"
	"github.com/gin-gonic/gin"
)

// fakeNodeTerminal satisfies the terminal readiness contract so ticket tests
// can pass the node-runtime readiness gate without an Incus provider.
type fakeNodeTerminal struct{}

func (fakeNodeTerminal) ExecNodePTY(context.Context, incus.ExecNodePTYRequest) error { return nil }
func (fakeNodeTerminal) CloseNodePTYWindow(context.Context, incus.CloseNodePTYWindowRequest) error {
	return nil
}
func (fakeNodeTerminal) ExecNode(context.Context, incus.ExecNodeRequest) (incus.ExecNodeResult, error) {
	return incus.ExecNodeResult{}, nil
}

func practiceTicketRequest(t *testing.T, handler *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/documentation/practices/practice-01/terminal-ticket", strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = gin.Params{{Key: "id", Value: "practice-01"}}
	ctx.Set("user_id", "u-demo")
	handler.CreatePracticeTerminalTicket(ctx)
	return recorder
}

func TestPracticeTerminalTicketIsBoundOneTimeAndReusable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newEnvironmentAPITestState()
	handler := newPracticeEnvironmentHandler(t, state, practiceTestRevision(), nil)
	handler.nodeTerminal = fakeNodeTerminal{}
	if recorder := practiceRequest(t, handler, http.MethodPost, "/start"); recorder.Code != http.StatusOK {
		t.Fatalf("start status = %d: %s", recorder.Code, recorder.Body.String())
	}

	recorder := practiceTicketRequest(t, handler, `{"window":"shell-1","node":"host"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("ticket status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Ticket string `json:"ticket"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Ticket == "" {
		t.Fatalf("ticket response = %s, %v", recorder.Body.String(), err)
	}

	// The ticket claims against the practice identifier with the existing
	// storage semantics: one-time, window-bound, and practice-bound.
	tokenHash := terminalTicketHash(response.Ticket)
	claimed, err := handler.db.Environment.ClaimTerminalTicket(context.Background(), tokenHash, "practice-01", "shell-1", time.Now().UTC())
	if err != nil || claimed.ScenarioID != "practice-01" || claimed.UserID != "u-demo" {
		t.Fatalf("claim = %#v, %v", claimed, err)
	}
	if _, err := handler.db.Environment.ClaimTerminalTicket(context.Background(), tokenHash, "practice-01", "shell-1", time.Now().UTC()); err == nil {
		t.Fatal("ticket replay was accepted")
	}
	if _, err := handler.db.Environment.ClaimTerminalTicket(context.Background(), terminalTicketHash("other-ticket"), "practice-01", "shell-1", time.Now().UTC()); err == nil {
		t.Fatal("unknown ticket was accepted")
	}
}

func TestPracticeTerminalTicketRejectsExpiryAndWrongWindow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newEnvironmentAPITestState()
	handler := newPracticeEnvironmentHandler(t, state, practiceTestRevision(), nil)
	handler.nodeTerminal = fakeNodeTerminal{}
	if recorder := practiceRequest(t, handler, http.MethodPost, "/start"); recorder.Code != http.StatusOK {
		t.Fatalf("start status = %d: %s", recorder.Code, recorder.Body.String())
	}

	now := time.Now().UTC()
	ticket, err := newTerminalTicket()
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.db.Environment.CreateTerminalTicket(context.Background(), postgres.TerminalTicket{
		TokenHash:      terminalTicketHash(ticket),
		UserID:         "u-demo",
		EnvironmentUID: "environment-1",
		ScenarioID:     "practice-01",
		WindowName:     "shell-1",
		ExpiresAt:      now.Add(time.Second),
	}, now); err != nil {
		t.Fatal(err)
	}
	hash := terminalTicketHash(ticket)
	if _, err := handler.db.Environment.ClaimTerminalTicket(context.Background(), hash, "practice-01", "shell-2", now); err == nil {
		t.Fatal("ticket bound to another window was accepted")
	}
	if _, err := handler.db.Environment.ClaimTerminalTicket(context.Background(), hash, "practice-01", "shell-1", now.Add(2*time.Second)); err == nil {
		t.Fatal("expired ticket was accepted")
	}
}

func TestPracticeTerminalTicketRejectsUnknownPractice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newEnvironmentAPITestState()
	handler := newPracticeEnvironmentHandler(t, state, documentdomain.PracticeRevision{}, postgres.ErrPublishedPracticeNotFound)

	recorder := practiceTicketRequest(t, handler, `{"window":"shell-1","node":"host"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown practice ticket status = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
	// A practice without a live environment has no terminal to ticket.
	handler = newPracticeEnvironmentHandler(t, state, practiceTestRevision(), nil)
	handler.nodeTerminal = fakeNodeTerminal{}
	recorder = practiceTicketRequest(t, handler, `{"window":"shell-1","node":"host"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("environment-less ticket status = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.createSuccesses != 0 {
		t.Fatalf("ticket creation created environments: %#v", state.environments)
	}
}

func TestPracticeTerminalWebSocketRejectsForeignOrigins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	state := newEnvironmentAPITestState()
	handler := newPracticeEnvironmentHandler(t, state, practiceTestRevision(), nil)
	handler.uiOrigin = "https://reader.example"

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/documentation/practices/practice-01/terminal?ticket=x&window=shell-1", nil)
	ctx.Request.Header.Set("Origin", "https://evil.example")
	ctx.Params = gin.Params{{Key: "id", Value: "practice-01"}}
	handler.HandlePracticeTerminalTicket(ctx)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("foreign origin status = %d, want 403", recorder.Code)
	}

	allowed := httptest.NewRecorder()
	ctx2, _ := gin.CreateTestContext(allowed)
	ctx2.Request = httptest.NewRequest(http.MethodGet, "/api/documentation/practices/practice-01/terminal?ticket=invalid&window=shell-1", nil)
	ctx2.Request.Header.Set("Origin", "https://reader.example")
	ctx2.Params = gin.Params{{Key: "id", Value: "practice-01"}}
	handler.HandlePracticeTerminalTicket(ctx2)
	if allowed.Code != http.StatusUnauthorized {
		t.Fatalf("allowed origin with an invalid ticket status = %d, want 401", allowed.Code)
	}
}
