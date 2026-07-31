package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/breakfix/breakfix/internal/agentruntime"
)

func TestInternalClientSendsAttemptFencedRequests(t *testing.T) {
	claim := agentruntime.Claim{Run: agentruntime.Run{ID: "assistant-run"}, WorkItemID: "work-agent-run", Attempt: 4, LeaseOwner: "worker-lease"}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/internal/agent-runs/assistant-run/assistant/context" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.Header.Get("X-Breakfix-Internal-Key") != "internal-key" {
			t.Fatalf("missing internal key")
		}
		var body LeaseCredential
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.WorkItemID != "work-agent-run" || body.Attempt != 4 || body.LeaseOwner != "worker-lease" {
			t.Fatalf("lease credentials = %#v", body)
		}
		_ = json.NewEncoder(response).Encode(ExecutionContext{
			UserID: "user-one", EnvironmentUID: "environment-one", ChallengeID: "cleanup-logs", Nodes: []string{"host"}, CurrentNode: "host", CurrentWindow: "shell-1", Terminals: []TerminalContext{{Node: "host", Windows: []string{"shell-1"}}},
		})
	}))
	defer server.Close()

	client, err := NewInternalClient(server.URL, "internal-key")
	if err != nil {
		t.Fatal(err)
	}
	contextSnapshot, err := client.LoadContext(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if contextSnapshot.EnvironmentUID != "environment-one" || contextSnapshot.CurrentWindow != "shell-1" {
		t.Fatalf("context = %#v", contextSnapshot)
	}
}
