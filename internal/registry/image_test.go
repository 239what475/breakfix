package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeleteImageDeletesResolvedManifestDigest(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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

	registry := strings.TrimPrefix(server.URL, "http://")
	if err := DeleteImage(context.Background(), registry+"/team/challenge:latest", true); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(requests, ", "), "HEAD /v2/team/challenge/manifests/latest, DELETE /v2/team/challenge/manifests/sha256:verified"; got != want {
		t.Fatalf("registry requests = %q, want %q", got, want)
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
