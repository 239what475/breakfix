package internalapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNilClientMethodsReturnConfigurationError(t *testing.T) {
	var client *Client
	if err := client.Post(context.Background(), "/internal", nil, nil); err == nil {
		t.Fatal("Post on nil client unexpectedly succeeded")
	}
	if err := client.PostLong(context.Background(), "/internal", nil, nil); err == nil {
		t.Fatal("PostLong on nil client unexpectedly succeeded")
	}
	if err := client.PostStream(context.Background(), "/internal", nil, func(json.RawMessage) error { return nil }); err == nil {
		t.Fatal("PostStream on nil client unexpectedly succeeded")
	}
}

func TestPostLongUsesRequestContextInsteadOfOrdinaryClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(25 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := New(server.URL, "internal-key")
	if err != nil {
		t.Fatal(err)
	}
	client.http.Timeout = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.PostLong(ctx, "/slow", struct{}{}, nil); err != nil {
		t.Fatalf("PostLong() = %v", err)
	}
}

func TestRunnableActionClientClaimsFromPublicEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/internal/runnable-actions/claim" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.Header.Get("X-Breakfix-Internal-Key") != "internal-key" {
			t.Fatal("internal key was not supplied")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client, err := NewRunnableActionClient(server.URL, "internal-key")
	if err != nil {
		t.Fatal(err)
	}
	action, err := client.Claim(context.Background(), "worker-01", 5*time.Second)
	if err != nil || action != nil {
		t.Fatalf("claim = %#v, %v", action, err)
	}
}
