package registry

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCopyImageUsesRegistryContentDigests(t *testing.T) {
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	layer := []byte("layer-data")
	configDigest := "sha256:" + sha256Hex(config)
	layerDigest := "sha256:" + sha256Hex(layer)
	manifest, err := json.Marshal(ociManifest{
		Config: ociDescriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: configDigest, Size: int64(len(config))},
		Layers: []ociDescriptor{{MediaType: "application/vnd.oci.image.layer.v1.tar", Digest: layerDigest, Size: int64(len(layer))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := "sha256:" + sha256Hex(manifest)
	blobs := map[string][]byte{configDigest: config, layerDigest: layer}
	uploaded := map[string][]byte{}
	var publishedManifest []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if username, password, ok := request.BasicAuth(); !ok || username != "registry" || password != "secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v2/team/base/manifests/"+manifestDigest:
			writer.Header().Set("Content-Type", ociManifestMediaType)
			writer.Header().Set("Docker-Content-Digest", manifestDigest)
			_, _ = writer.Write(manifest)
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v2/team/base/blobs/"):
			digest := strings.TrimPrefix(request.URL.Path, "/v2/team/base/blobs/")
			data, ok := blobs[digest]
			if !ok {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = writer.Write(data)
		case request.Method == http.MethodHead && strings.HasPrefix(request.URL.Path, "/v2/team/published/blobs/"):
			digest := strings.TrimPrefix(request.URL.Path, "/v2/team/published/blobs/")
			if _, ok := uploaded[digest]; ok {
				writer.WriteHeader(http.StatusOK)
				return
			}
			writer.WriteHeader(http.StatusNotFound)
		case request.Method == http.MethodPost && request.URL.Path == "/v2/team/published/blobs/uploads/":
			writer.Header().Set("Location", "/uploads/next")
			writer.WriteHeader(http.StatusAccepted)
		case request.Method == http.MethodPut && request.URL.Path == "/uploads/next":
			data, readErr := io.ReadAll(request.Body)
			if readErr != nil {
				t.Error(readErr)
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			digest := request.URL.Query().Get("digest")
			if digest != "sha256:"+sha256Hex(data) {
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			uploaded[digest] = data
			writer.WriteHeader(http.StatusCreated)
		case request.Method == http.MethodPut && request.URL.Path == "/v2/team/published/manifests/verified":
			publishedManifest, _ = io.ReadAll(request.Body)
			writer.WriteHeader(http.StatusCreated)
		case request.Method == http.MethodHead && request.URL.Path == "/v2/team/published/manifests/verified":
			if len(publishedManifest) == 0 {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			writer.Header().Set("Docker-Content-Digest", manifestDigest)
			writer.WriteHeader(http.StatusOK)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	address := testTLSRegistryAddress(t, server)
	client := testTLSRegistryClient(t, server, Credentials{Username: "registry", Password: "secret"})
	if err := client.CopyImage(context.Background(), address+"/team/base@"+manifestDigest, address+"/team/published:verified"); err != nil {
		t.Fatalf("copy OCI image: %v", err)
	}
	if string(publishedManifest) != string(manifest) {
		t.Fatalf("published manifest = %q, want %q", publishedManifest, manifest)
	}
	for _, digest := range []string{configDigest, layerDigest, manifestDigest} {
		if _, ok := uploaded[digest]; !ok {
			t.Fatalf("registry did not receive blob %s", digest)
		}
	}
	got, err := client.ResolveImmutableReference(context.Background(), address+"/team/published:verified")
	if err != nil {
		t.Fatalf("resolve copied image: %v", err)
	}
	if want := address + "/team/published@" + manifestDigest; got != want {
		t.Fatalf("resolved copied image = %q, want %q", got, want)
	}
}

func TestExtractOCIArchiveAllowsDirectoriesAndRejectsLinks(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, header := range []*tar.Header{
		{Name: "blobs/", Typeflag: tar.TypeDir, Mode: 0755},
		{Name: "blobs/sha256/", Typeflag: tar.TypeDir, Mode: 0755},
		{Name: "blobs/sha256/abc", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len("content"))},
	} {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := writer.Write([]byte("content")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/layout.tar"
	if err := os.WriteFile(path, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := ExtractOCIArchive(path, destination); err != nil {
		t.Fatalf("extract OCI archive with directories: %v", err)
	}
	data, err := os.ReadFile(destination + "/blobs/sha256/abc")
	if err != nil || string(data) != "content" {
		t.Fatalf("extracted blob = %q, %v", data, err)
	}

	archive.Reset()
	writer = tar.NewWriter(&archive)
	if err := writer.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ExtractOCIArchive(path, t.TempDir()); err == nil {
		t.Fatal("expected symbolic link to be rejected")
	}
}
