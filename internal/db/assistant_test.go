package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/breakfix/breakfix/internal/assistant"
)

func TestAssistantSessionPersistsMessagesAndDeletesByEnvironment(t *testing.T) {
	database, err := New(filepath.Join(t.TempDir(), "breakfix.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()

	session, err := database.CreateAssistantSession(context.Background(), assistant.Session{
		ID:              "assistant-a",
		UserID:          "user-a",
		EnvironmentUID:  "uid-a",
		EnvironmentName: "environment-a",
		Runtime:         "container",
		ChallengeID:     "cleanup-logs",
		AgentSessionID:  "claude-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := database.CreateAssistantSession(context.Background(), assistant.Session{
		ID:              "assistant-duplicate",
		UserID:          "user-a",
		EnvironmentUID:  "uid-a",
		EnvironmentName: "environment-a",
		Runtime:         "container",
		ChallengeID:     "cleanup-logs",
		AgentSessionID:  "claude-duplicate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != session.ID {
		t.Fatalf("duplicate session = %q, want %q", duplicate.ID, session.ID)
	}
	if err := database.AppendAssistantMessage(context.Background(), session.ID, assistant.Message{
		ID: "message-a", Role: "assistant", Content: "Inspect the script.", Evidence: []assistant.Evidence{{Kind: "terminal", Label: "终端 shell-1 的近期输出"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetAssistantAgentStarted(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
	loaded, err := database.GetAssistantSession(context.Background(), "user-a", "uid-a", "cleanup-logs")
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.AgentStarted || loaded.EnvironmentName != "environment-a" {
		t.Fatalf("loaded session = %#v", loaded)
	}
	messages, err := database.ListAssistantMessages(context.Background(), session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || len(messages[0].Evidence) != 1 || messages[0].Evidence[0].Kind != "terminal" {
		t.Fatalf("loaded messages = %#v", messages)
	}
	if err := database.DeleteAssistantSessionsForEnvironment(context.Background(), "uid-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetAssistantSession(context.Background(), "user-a", "uid-a", "cleanup-logs"); !errors.Is(err, assistant.ErrNotFound) {
		t.Fatalf("deleted session error = %v, want ErrNotFound", err)
	}
	var count int
	if err := database.conn.QueryRow(`SELECT COUNT(*) FROM assistant_messages WHERE session_id = ?`, session.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("orphaned assistant messages = %d", count)
	}
}
