package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/agentruntime"
	"github.com/breakfix/breakfix/internal/testpostgres"
)

func TestRuntimeServiceScopesSessionAndPersistsPendingTurn(t *testing.T) {
	database := testpostgres.New(t)
	service := NewService(database, "deepseek-v4-pro")
	request := testRequest("env-a")
	first, messages, err := service.GetOrCreate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("new messages = %#v", messages)
	}
	second, _, err := service.GetOrCreate(context.Background(), request)
	if err != nil || second.ID != first.ID {
		t.Fatalf("same environment session = %#v, %v", second, err)
	}
	request.EnvironmentUID = "env-b"
	third, _, err := service.GetOrCreate(context.Background(), request)
	if err != nil || third.ID == first.ID {
		t.Fatalf("different environment session = %#v, %v", third, err)
	}

	request.EnvironmentUID = "env-a"
	session, turn, err := service.StartTurn(context.Background(), request, "下一步怎么做？")
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != first.ID || turn.Status != TurnRunning {
		t.Fatalf("started turn = %#v", turn)
	}
	run, err := database.GetRun(context.Background(), turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	var input RunInput
	if err := json.Unmarshal(run.Input, &input); err != nil {
		t.Fatalf("decode persisted assistant input: %v", err)
	}
	if input.CurrentNode != "host" || input.CurrentWindow != "shell-1" || len(input.Terminals) != 1 || input.Terminals[0].Node != "host" || len(input.Terminals[0].Windows) != 1 || input.Terminals[0].Windows[0] != "shell-1" {
		t.Fatalf("persisted assistant input = %#v", input)
	}
	if _, _, err := service.StartTurn(context.Background(), request, "another message"); err != ErrTurnRunning {
		t.Fatalf("second active turn error = %v, want %v", err, ErrTurnRunning)
	}
	active := service.ActiveTurn(context.Background(), session.ID)
	if active == nil || active.ID != turn.ID {
		t.Fatalf("active turn = %#v", active)
	}

	claim, err := database.ClaimNext(context.Background(), "worker", time.Minute, time.Now().UTC())
	if err != nil || claim == nil {
		t.Fatalf("claim assistant run = %#v, %v", claim, err)
	}
	if err := database.CompleteWithMessage(context.Background(), *claim, agentruntime.Message{
		ID: "assistant-final", SessionID: session.ID, Role: "assistant", Content: "检查 /var/log。",
		Metadata: json.RawMessage(`{"evidence":[{"kind":"terminal","label":"终端 shell-1 的近期输出"}]}`),
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, persisted, err := service.GetOrCreate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 2 || len(persisted[1].Evidence) != 1 || persisted[1].Evidence[0].Kind != "terminal" {
		t.Fatalf("persisted assistant evidence = %#v", persisted)
	}
	subscription, err := service.Subscribe(context.Background(), session.ID, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	select {
	case event := <-subscription.Events:
		if event.Type != "complete" || event.Message == nil || event.Message.Content != "检查 /var/log。" {
			t.Fatalf("terminal event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for completed assistant run")
	}
}

func TestConversationToolsStayReadOnly(t *testing.T) {
	reader := &fakeReader{}
	request := testRequest("env-a")
	request.Reader = reader
	conversation := &conversation{request: request}

	if _, err := conversation.getTerminalScrollback(context.Background(), `{"window":"shell-2"}`); err == nil {
		t.Fatal("assistant read a terminal window not declared by the workspace")
	}
	if _, err := conversation.getTerminalScrollback(context.Background(), `{"window":"shell-1","offset":0,"lines":20}`); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.getCheckpointStatus(context.Background(), `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.listEnvironmentFiles(context.Background(), `{"path":"/workspace","offset":0,"limit":10}`); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.readEnvironmentFile(context.Background(), `{"path":"/workspace/config.yaml","offset":0,"max_bytes":64}`); err != nil {
		t.Fatal(err)
	}
	if _, err := conversation.getSolution(context.Background(), `{}`); err != nil {
		t.Fatal(err)
	}
	if evidence := conversation.evidence(); len(evidence) != 5 {
		t.Fatalf("evidence = %#v, want five read-only sources", evidence)
	}
}
