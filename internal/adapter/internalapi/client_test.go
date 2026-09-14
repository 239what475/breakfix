package internalapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/domain/runnable"
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

func TestRunnableActionClientMapsConflictToLeaseLoss(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"runnable action lease lost"}`))
	}))
	defer server.Close()

	client, err := NewRunnableActionClient(server.URL, "internal-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Claim(context.Background(), "worker-01", 5*time.Second); !errors.Is(err, runnable.ErrActionLeaseLost) {
		t.Fatalf("claim conflict = %v, want runnable action lease loss", err)
	}
}

func TestRunnableActionClientReadsOnlyDigestMatchedSource(t *testing.T) {
	archive := []byte("immutable source archive")
	sum := sha256.Sum256(archive)
	source := runnable.SourceArchive{FormatVersion: runnable.FormatVersion, Reference: "archives/source.tar.gz", Digest: "sha256:" + hex.EncodeToString(sum[:])}
	credential := runnable.LeaseCredential{Identity: runnable.ActionIdentity{
		Content:    runnable.ContentIdentity{Kind: "operations", ID: "runtime-test", Revision: "revision-01"},
		SpecDigest: "sha256:" + strings.Repeat("a", 64), Phase: runnable.ActionMaterializeArtifact, StateVersion: 1,
	}, LeaseOwner: "worker-01"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/internal/runnable-actions/source" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"archive":"aW1tdXRhYmxlIHNvdXJjZSBhcmNoaXZl"}`))
	}))
	defer server.Close()
	client, err := NewRunnableActionClient(server.URL, "internal-key")
	if err != nil {
		t.Fatal(err)
	}
	actual, err := client.ReadSource(context.Background(), credential, source)
	if err != nil || string(actual) != string(archive) {
		t.Fatalf("read source = %q, %v", actual, err)
	}
}

func TestRunnableActionClientStoresOnlyMatchingExecutionOutputReference(t *testing.T) {
	capture := runnable.OutputCapture{Stdout: []byte("standard output"), Stderr: []byte("standard error")}
	expected, _, err := capture.Reference()
	if err != nil {
		t.Fatal(err)
	}
	credential := runnable.LeaseCredential{Identity: runnable.ActionIdentity{
		Content:    runnable.ContentIdentity{Kind: "operations", ID: "runtime-test", Revision: "revision-01"},
		SpecDigest: "sha256:" + strings.Repeat("a", 64), Phase: runnable.ActionVerify, StateVersion: 1,
	}, LeaseOwner: "worker-01"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/internal/runnable-actions/output" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		var body struct {
			Credential runnable.LeaseCredential `json:"credential"`
			Capture    runnable.OutputCapture   `json:"capture"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Credential != credential || string(body.Capture.Stdout) != string(capture.Stdout) || string(body.Capture.Stderr) != string(capture.Stderr) {
			t.Fatalf("request = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(struct {
			Reference runnable.ImmutableReference `json:"reference"`
		}{Reference: expected}); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()
	client, err := NewRunnableActionClient(server.URL, "internal-key")
	if err != nil {
		t.Fatal(err)
	}
	actual, err := client.StoreExecutionOutput(context.Background(), credential, capture)
	if err != nil || actual != expected {
		t.Fatalf("store execution output = %#v, %v", actual, err)
	}
}

func TestRunnableActionClientRejectsMismatchedExecutionOutputReference(t *testing.T) {
	capture := runnable.OutputCapture{Stdout: []byte("standard output")}
	credential := runnable.LeaseCredential{Identity: runnable.ActionIdentity{
		Content:    runnable.ContentIdentity{Kind: "operations", ID: "runtime-test", Revision: "revision-01"},
		SpecDigest: "sha256:" + strings.Repeat("a", 64), Phase: runnable.ActionVerify, StateVersion: 1,
	}, LeaseOwner: "worker-01"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"reference":{"reference":"runnable-output://sha256/` + strings.Repeat("b", 64) + `","digest":"sha256:` + strings.Repeat("b", 64) + `","size_bytes":1}}`))
	}))
	defer server.Close()
	client, err := NewRunnableActionClient(server.URL, "internal-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.StoreExecutionOutput(context.Background(), credential, capture); err == nil || !strings.Contains(err.Error(), "another capture") {
		t.Fatalf("mismatched reference error = %v", err)
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
