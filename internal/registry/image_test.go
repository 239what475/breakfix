package registry

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewClientAppendsOperatorTrustBundle(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v2/" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	bundlePath := filepath.Join(t.TempDir(), "registry-ca.crt")
	certificate := server.Certificate()
	if certificate == nil {
		t.Fatal("test Registry has no certificate")
	}
	if err := os.WriteFile(bundlePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientOptions{Endpoint: testTLSRegistryAddress(t, server), TrustBundleFile: bundlePath})
	if err != nil {
		t.Fatalf("create Registry client with internal CA: %v", err)
	}
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("ping Registry with internal CA: %v", err)
	}
}

func TestNewClientRejectsInvalidTrustBundle(t *testing.T) {
	bundlePath := filepath.Join(t.TempDir(), "registry-ca.crt")
	if err := os.WriteFile(bundlePath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClient(ClientOptions{Endpoint: "registry.example.com", TrustBundleFile: bundlePath}); err == nil {
		t.Fatal("Registry client accepted an invalid trust bundle")
	}
}

func TestDeleteImageDeletesResolvedManifestDigest(t *testing.T) {
	var requests []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.Path)
		switch request.Method {
		case http.MethodHead:
			if request.URL.Path != "/v2/team/challenge/manifests/latest" {
				t.Fatalf("unexpected manifest lookup path %q", request.URL.Path)
			}
			writer.Header().Set("Docker-Content-Digest", "sha256:verified")
			writer.WriteHeader(http.StatusOK)
		case http.MethodDelete:
			if request.URL.Path != "/v2/team/challenge/manifests/sha256:verified" {
				t.Fatalf("unexpected manifest delete path %q", request.URL.Path)
			}
			writer.WriteHeader(http.StatusAccepted)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	registry := testTLSRegistryAddress(t, server)
	if err := testTLSRegistryClient(t, server, Credentials{}).DeleteImage(context.Background(), registry+"/team/challenge:latest"); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(requests, ", "), "HEAD /v2/team/challenge/manifests/latest, DELETE /v2/team/challenge/manifests/sha256:verified"; got != want {
		t.Fatalf("registry requests = %q, want %q", got, want)
	}
}

func TestDeleteImageUsesRegistryCredentials(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		if !ok || username != "controller" || password != "secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.Method == http.MethodHead {
			writer.Header().Set("Docker-Content-Digest", "sha256:verified")
			writer.WriteHeader(http.StatusOK)
			return
		}
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	address := testTLSRegistryAddress(t, server)
	client := testTLSRegistryClient(t, server, Credentials{Username: "controller", Password: "secret"})
	if err := client.DeleteImage(context.Background(), address+"/team/challenge:latest"); err != nil {
		t.Fatal(err)
	}
}

func TestPingChecksRegistryCredentials(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		username, password, ok := request.BasicAuth()
		if request.URL.Path != "/v2/" || !ok || username != "publisher" || password != "secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := testTLSRegistryClient(t, server, Credentials{Username: "publisher", Password: "secret"})
	if err := client.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestImageReference(t *testing.T) {
	tests := []struct {
		image                     string
		registry, repository, ref string
	}{
		{image: "registry.example/team/challenge:latest", registry: "registry.example", repository: "team/challenge", ref: "latest"},
		{image: "registry.example:5000/team/challenge", registry: "registry.example:5000", repository: "team/challenge", ref: "latest"},
		{image: "registry.example/team/challenge@sha256:abc", registry: "registry.example", repository: "team/challenge", ref: "sha256:abc"},
	}
	for _, test := range tests {
		registry, repository, reference, err := imageReference(test.image)
		if err != nil {
			t.Fatalf("imageReference(%q): %v", test.image, err)
		}
		if registry != test.registry || repository != test.repository || reference != test.ref {
			t.Fatalf("imageReference(%q) = (%q, %q, %q), want (%q, %q, %q)", test.image, registry, repository, reference, test.registry, test.repository, test.ref)
		}
	}
}
