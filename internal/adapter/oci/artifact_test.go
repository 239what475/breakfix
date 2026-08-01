package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteArtifactArchiveProducesDeterministicOCIArtifact(t *testing.T) {
	artifact := Artifact{
		ArtifactType: "application/vnd.breakfix.catalog.release.v1",
		Blobs: []ArtifactBlob{{
			MediaType:   "application/vnd.breakfix.catalog.source.v1.tar+gzip",
			Data:        []byte("portable catalog source"),
			Annotations: map[string]string{"org.opencontainers.image.title": "source"},
		}},
		Annotations: map[string]string{
			"org.opencontainers.image.title":   "foundation",
			"org.opencontainers.image.version": "2026.08.01",
		},
	}
	firstPath := filepath.Join(t.TempDir(), "first.oci.tar")
	firstDigest, err := WriteArtifactArchive(firstPath, artifact)
	if err != nil {
		t.Fatalf("write first artifact archive: %v", err)
	}
	secondPath := filepath.Join(t.TempDir(), "second.oci.tar")
	secondDigest, err := WriteArtifactArchive(secondPath, artifact)
	if err != nil {
		t.Fatalf("write second artifact archive: %v", err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("artifact digest = %q, want %q", secondDigest, firstDigest)
	}
	first, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(secondPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("OCI artifact archive is not deterministic")
	}
	if err := ValidateOCIArchive(firstPath); err != nil {
		t.Fatalf("validate artifact archive: %v", err)
	}
	layout := t.TempDir()
	if err := ExtractOCIArchive(firstPath, layout); err != nil {
		t.Fatalf("extract artifact archive: %v", err)
	}
	root, err := loadOCIRootDescriptor(layout)
	if err != nil {
		t.Fatal(err)
	}
	if root.MediaType != ociManifestMediaType {
		t.Fatalf("root media type = %q, want %q", root.MediaType, ociManifestMediaType)
	}
	rawManifest, err := os.ReadFile(ociBlobPath(layout, root.Digest))
	if err != nil {
		t.Fatal(err)
	}
	var encoded ociManifest
	if err := json.Unmarshal(rawManifest, &encoded); err != nil {
		t.Fatal(err)
	}
	if encoded.SchemaVersion != 2 || encoded.MediaType != ociManifestMediaType || encoded.ArtifactType != artifact.ArtifactType {
		t.Fatalf("encoded artifact manifest = %#v", encoded)
	}
	if encoded.Config.MediaType != emptyJSONMediaType || encoded.Config.Digest != digestBytes([]byte(emptyJSONPayload)) || encoded.Config.Size != int64(len(emptyJSONPayload)) {
		t.Fatalf("encoded artifact config = %#v", encoded.Config)
	}
	if len(encoded.Layers) != len(artifact.Blobs) || encoded.Layers[0].MediaType != artifact.Blobs[0].MediaType {
		t.Fatalf("encoded artifact layers = %#v", encoded.Layers)
	}
	manifest, err := ReadArtifactArchive(firstPath)
	if err != nil {
		t.Fatalf("read artifact archive: %v", err)
	}
	if manifest.ArtifactType != artifact.ArtifactType || len(manifest.Blobs) != 1 {
		t.Fatalf("artifact manifest = %#v", manifest)
	}
	if manifest.Blobs[0].MediaType != artifact.Blobs[0].MediaType {
		t.Fatalf("artifact blob media type = %q", manifest.Blobs[0].MediaType)
	}
	data, err := ReadArtifactBlob(firstPath, manifest.Blobs[0].Digest)
	if err != nil {
		t.Fatalf("read artifact blob: %v", err)
	}
	if !bytes.Equal(data, artifact.Blobs[0].Data) {
		t.Fatalf("artifact blob = %q, want %q", data, artifact.Blobs[0].Data)
	}
}

func TestPullOCIArchivePreservesArtifactManifest(t *testing.T) {
	artifact := Artifact{
		ArtifactType: "application/vnd.breakfix.catalog.release.v1",
		Blobs: []ArtifactBlob{{
			MediaType: "application/vnd.breakfix.catalog.source.v1.tar+gzip",
			Data:      []byte("portable catalog source"),
		}},
	}
	sourceArchive := filepath.Join(t.TempDir(), "source.oci.tar")
	if _, err := WriteArtifactArchive(sourceArchive, artifact); err != nil {
		t.Fatalf("write source artifact archive: %v", err)
	}
	layout := t.TempDir()
	if err := ExtractOCIArchive(sourceArchive, layout); err != nil {
		t.Fatalf("extract source artifact archive: %v", err)
	}
	root, err := loadOCIRootDescriptor(layout)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(ociBlobPath(layout, root.Digest))
	if err != nil {
		t.Fatal(err)
	}
	var encoded ociManifest
	if err := json.Unmarshal(manifest, &encoded); err != nil {
		t.Fatal(err)
	}
	parsed, err := ReadArtifactArchive(sourceArchive)
	if err != nil {
		t.Fatal(err)
	}
	blobs := make(map[string][]byte, 1+len(encoded.Layers))
	for _, descriptor := range append([]ociDescriptor{encoded.Config}, encoded.Layers...) {
		blob, err := os.ReadFile(ociBlobPath(layout, descriptor.Digest))
		if err != nil {
			t.Fatal(err)
		}
		blobs[descriptor.Digest] = blob
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v2/catalog/foundation/manifests/source":
			writer.Header().Set("Content-Type", ociManifestMediaType)
			writer.Header().Set("Docker-Content-Digest", root.Digest)
			_, _ = writer.Write(manifest)
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v2/catalog/foundation/blobs/"):
			digest := strings.TrimPrefix(request.URL.Path, "/v2/catalog/foundation/blobs/")
			blob, ok := blobs[digest]
			if !ok {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = writer.Write(blob)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	destination := filepath.Join(t.TempDir(), "pulled.oci.tar")
	client := testTLSRegistryClient(t, server, Credentials{})
	imageName := testTLSRegistryAddress(t, server) + "/catalog/foundation:source"
	if err := client.PullOCIArchive(context.Background(), imageName, destination); err != nil {
		t.Fatalf("pull OCI artifact archive: %v", err)
	}
	pulled, err := ReadArtifactArchive(destination)
	if err != nil {
		t.Fatalf("read pulled artifact archive: %v", err)
	}
	if pulled.ArtifactType != artifact.ArtifactType || len(pulled.Blobs) != 1 || pulled.Blobs[0].Digest != parsed.Blobs[0].Digest {
		t.Fatalf("pulled artifact manifest = %#v", pulled)
	}
}
