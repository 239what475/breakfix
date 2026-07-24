package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGetOrCreateIsScopedToEnvironmentUID(t *testing.T) {
	repo := newMemoryRepository()
	service := NewService(repo, structLLMConfig())
	request := testRequest("env-a")
	first, messages, err := service.GetOrCreate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 0 {
		t.Fatalf("new session messages = %#v, want none", messages)
	}
	second, _, err := service.GetOrCreate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("same environment created two sessions: %q != %q", second.ID, first.ID)
	}
	request.EnvironmentUID = "env-b"
	third, _, err := service.GetOrCreate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if third.ID == first.ID {
		t.Fatal("different environment UID reused an assistant session")
	}
}

func TestConversationPromptExcludesSolutionAndToolsStayReadOnly(t *testing.T) {
	reader := &fakeReader{}
	request := testRequest("env-a")
	request.Reader = reader
	conversation := &conversation{request: request}
	prompt, err := conversation.prompt("下一步怎么做？")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "secret solution") {
		t.Fatal("solution must not be part of the initial prompt")
	}
	if !strings.Contains(prompt, "当前检查点快照") {
		t.Fatal("checkpoint snapshot missing from the stable prompt")
	}

	if _, err := conversation.getTerminalScrollback(context.Background(), `{"window":"shell-2"}`); err == nil {
		t.Fatal("assistant read a terminal window not declared by the workspace")
	}
	if _, err := conversation.getTerminalScrollback(context.Background(), `{"window":"shell-1","offset":0,"lines":20}`); err != nil {
		t.Fatal(err)
	}
	if reader.scrollbackCalls != 1 {
		t.Fatalf("scrollback calls = %d, want 1", reader.scrollbackCalls)
	}
	if _, err := conversation.getCheckpointStatus(context.Background(), `{}`); err != nil {
		t.Fatal(err)
	}
	if reader.checkpointCalls != 1 {
		t.Fatalf("checkpoint calls = %d, want 1", reader.checkpointCalls)
	}
	if _, err := conversation.listEnvironmentFiles(context.Background(), `{"path":"/workspace","offset":0,"limit":10}`); err != nil {
		t.Fatal(err)
	}
	if reader.listCalls != 1 {
		t.Fatalf("file list calls = %d, want 1", reader.listCalls)
	}
	if _, err := conversation.readEnvironmentFile(context.Background(), `{"path":"/workspace/config.yaml","offset":0,"max_bytes":64}`); err != nil {
		t.Fatal(err)
	}
	if reader.readCalls != 1 {
		t.Fatalf("file read calls = %d, want 1", reader.readCalls)
	}
	if _, err := conversation.getSolution(context.Background(), `{}`); err != nil {
		t.Fatal(err)
	}
	evidence := conversation.evidence()
	if len(evidence) != 5 {
		t.Fatalf("evidence = %#v, want five read-only sources", evidence)
	}
}

func TestDeleteEnvironmentRemovesAllSessionData(t *testing.T) {
	repo := newMemoryRepository()
	service := NewService(repo, structLLMConfig())
	request := testRequest("env-a")
	session, _, err := service.GetOrCreate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AppendAssistantMessage(context.Background(), session.ID, Message{ID: "m1", Role: "user", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteEnvironment(context.Background(), "env-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetAssistantSession(context.Background(), "user-a", "env-a", "cleanup-logs"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted environment session error = %v, want ErrNotFound", err)
	}
	if len(repo.messages[session.ID]) != 0 {
		t.Fatalf("messages remained after environment delete: %#v", repo.messages[session.ID])
	}
}
