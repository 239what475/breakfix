package mcpconnector

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientForwardsBearerTokenAndUsesAPIRoot(t *testing.T) {
	const token = "local-user-token"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if request.URL.Path != "/api/generator/workflows" {
			t.Fatalf("request path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Method != http.MethodGet {
			t.Fatalf("request method = %s", request.Method)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"workflows":[]}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, token, nil)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	workflows, err := client.ListActiveGenerations(context.Background())
	if err != nil {
		t.Fatalf("list active generations: %v", err)
	}
	if requests != 1 || len(workflows.Workflows) != 0 {
		t.Fatalf("client result = %#v requests=%d", workflows, requests)
	}
}

func TestClientPreservesExplicitAPIPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/breakfix/api/generator/workflows" {
			t.Fatalf("request path = %q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"workflows":[]}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL+"/breakfix/api", "token", nil)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if _, err := client.ListActiveGenerations(context.Background()); err != nil {
		t.Fatalf("list active generations: %v", err)
	}
}
