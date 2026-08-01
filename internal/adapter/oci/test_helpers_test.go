package oci

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func testTLSRegistryClient(t *testing.T, server *httptest.Server, credentials Credentials) Client {
	t.Helper()
	client, err := NewClient(ClientOptions{Endpoint: testTLSRegistryAddress(t, server), Credentials: credentials})
	if err != nil {
		t.Fatalf("create Registry client: %v", err)
	}
	client.httpClientOverride = server.Client()
	return client
}

func testTLSRegistryAddress(t *testing.T, server *httptest.Server) string {
	t.Helper()
	return strings.TrimPrefix(server.URL, "https://")
}
