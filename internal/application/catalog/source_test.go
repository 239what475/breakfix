package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/breakfix/breakfix/internal/adapter/oci"
	"github.com/breakfix/breakfix/internal/challenge"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
)

func TestPortableSourceBuildsDeterministicBundle(t *testing.T) {
	root, challengeRevision, taxonomyRevision := writePortableRelease(t)

	source, err := LoadPortableSource(root)
	if err != nil {
		t.Fatalf("load portable source: %v", err)
	}
	if got := source.Manifest.Entries[0].ContentRevision; got != challengeRevision {
		t.Fatalf("challenge contentRevision = %q, want %q", got, challengeRevision)
	}
	if got := source.Manifest.Taxonomy.ContentRevision; got != taxonomyRevision {
		t.Fatalf("taxonomy contentRevision = %q, want %q", got, taxonomyRevision)
	}
	if len(source.Challenges) != 1 || source.Challenges[0].Entry.Title != "Cleanup logs" {
		t.Fatalf("loaded challenges = %#v", source.Challenges)
	}

	first, err := BuildPortableBundle(root)
	if err != nil {
		t.Fatalf("build first portable bundle: %v", err)
	}
	second, err := BuildPortableBundle(root)
	if err != nil {
		t.Fatalf("build second portable bundle: %v", err)
	}
	if !bytes.Equal(first.SourceLayer, second.SourceLayer) {
		t.Fatal("portable source layer is not deterministic")
	}
	if first.ArtifactType != ReleaseArtifactType || first.LayerMediaType != ReleaseSourceLayerType {
		t.Fatalf("bundle media types = (%q, %q)", first.ArtifactType, first.LayerMediaType)
	}
	files := sourceLayerFiles(t, first.SourceLayer)
	if mode := files["challenges/linux/cleanup-logs/nodes/host/generate.sh"].Mode; mode != 0o755 {
		t.Fatalf("generate.sh mode = %04o, want 0755", mode)
	}
	if got := string(files["challenges/linux/cleanup-logs/challenge.yaml"].Content); !bytes.Contains([]byte(got), []byte("description: |")) {
		t.Fatalf("source layer changed challenge YAML:\n%s", got)
	}
}

func TestContentRevisionIncludesExecutableBit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "script.sh")
	writeCatalogFile(t, path, []byte("#!/bin/sh\nexit 0\n"), 0o644)

	regular, err := ContentRevision(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	executable, err := ContentRevision(root)
	if err != nil {
		t.Fatal(err)
	}
	if regular == executable {
		t.Fatal("content revision did not include executable bit")
	}
	if err := os.Chmod(path, 0o750); err != nil {
		t.Fatal(err)
	}
	alternateExecutable, err := ContentRevision(root)
	if err != nil {
		t.Fatal(err)
	}
	if executable != alternateExecutable {
		t.Fatalf("content revision distinguished equivalent executable modes: %q != %q", executable, alternateExecutable)
	}
}

func TestPortableBundleWritesOCIReleaseArtifact(t *testing.T) {
	root, _, _ := writePortableRelease(t)
	bundle, err := BuildPortableBundle(root)
	if err != nil {
		t.Fatalf("build portable bundle: %v", err)
	}
	archive := filepath.Join(t.TempDir(), "foundation.oci.tar")
	_, err = oci.WriteArtifactArchive(archive, oci.Artifact{
		ArtifactType: bundle.ArtifactType,
		Blobs: []oci.ArtifactBlob{{
			MediaType: bundle.LayerMediaType,
			Data:      bundle.SourceLayer,
		}},
		Annotations: bundle.Annotations,
	})
	if err != nil {
		t.Fatalf("write OCI release artifact: %v", err)
	}
	manifest, err := oci.ReadArtifactArchive(archive)
	if err != nil {
		t.Fatalf("read OCI release artifact: %v", err)
	}
	if manifest.ArtifactType != ReleaseArtifactType || len(manifest.Blobs) != 1 {
		t.Fatalf("OCI release manifest = %#v", manifest)
	}
	source, err := oci.ReadArtifactBlob(archive, manifest.Blobs[0].Digest)
	if err != nil {
		t.Fatalf("read OCI release source layer: %v", err)
	}
	if !bytes.Equal(source, bundle.SourceLayer) {
		t.Fatal("OCI release source layer differs from the portable bundle")
	}
}

func TestExportPublishedCandidatePreservesRemainingManifestBytes(t *testing.T) {
	source := filepath.Join(t.TempDir(), "published")
	writeChallengeSource(t, source, true)
	if _, err := challenge.ValidateDir(source); err != nil {
		t.Fatalf("validate published challenge fixture: %v", err)
	}

	destination := filepath.Join(t.TempDir(), "portable")
	revision, err := ExportPublishedCandidate(source, destination)
	if err != nil {
		t.Fatalf("export published candidate: %v", err)
	}
	if !revision.Valid() {
		t.Fatalf("export revision is invalid: %q", revision)
	}
	if _, err := challenge.ValidatePortableDir(destination); err != nil {
		t.Fatalf("validate exported candidate: %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(destination, "challenge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := `runtime: node
title: Cleanup logs
difficulty: easy
description: |
  Keep this indentation.
nodes:
  - name: host
    title: Host
checkpoints:
  - id: cleanup-script-ready
    title: Create cleanup script
    description: The script exists.
    hint: hints/cleanup-script-ready.md
    node: host
`
	if string(manifest) != want {
		t.Fatalf("exported challenge manifest = %q, want %q", manifest, want)
	}
}

type sourceLayerFile struct {
	Mode    int64
	Content []byte
}

func sourceLayerFiles(t *testing.T, layer []byte) map[string]sourceLayerFile {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(layer))
	if err != nil {
		t.Fatalf("open source layer: %v", err)
	}
	defer reader.Close()
	tarReader := tar.NewReader(reader)
	files := map[string]sourceLayerFile{}
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read source layer: %v", err)
		}
		content, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatalf("read source layer file %s: %v", header.Name, err)
		}
		files[header.Name] = sourceLayerFile{Mode: header.Mode, Content: content}
	}
	return files
}

func writePortableRelease(t *testing.T) (string, catalogdomain.ContentRevision, catalogdomain.ContentRevision) {
	t.Helper()
	root := t.TempDir()
	challengePath := filepath.Join(root, "challenges", "linux", "cleanup-logs")
	writeChallengeSource(t, challengePath, false)
	challengeRevision, err := ContentRevision(challengePath)
	if err != nil {
		t.Fatal(err)
	}

	taxonomyRoot := filepath.Join(root, "taxonomy")
	writeCatalogFile(t, filepath.Join(taxonomyRoot, "skills", "cleanup-script.yaml"), []byte(`kind: Skill
id: skill-1111111111111111
title: Cleanup script
definition: Create a shell script that safely cleans old logs.
mapping_guidance:
  outcome_when:
    - The task requires a reusable cleanup script.
`), 0o644)
	writeCatalogFile(t, filepath.Join(taxonomyRoot, "tags", "shell.yaml"), []byte(`kind: Tag
id: tag-1111111111111111
title: Shell
definition: Tasks centered on shell scripting.
mapping_guidance:
  include_when:
    - Shell behavior is central to the task.
`), 0o644)
	writeCatalogFile(t, filepath.Join(taxonomyRoot, "mappings", "challenges", "cleanup-logs.yaml"), []byte(`challenge:
  path: challenges/linux/cleanup-logs
  title: Cleanup logs
  contentRevision: `+string(challengeRevision)+`
tags:
  - id: tag-1111111111111111
    title: Shell
outcomes:
  - id: skill-1111111111111111
    title: Cleanup script
    primary: true
`), 0o644)
	taxonomyRevision, err := ContentRevision(taxonomyRoot)
	if err != nil {
		t.Fatal(err)
	}
	writeCatalogFile(t, filepath.Join(root, "release.yaml"), []byte(`apiVersion: breakfix.dev/catalog/v1
kind: CatalogRelease
metadata:
  name: foundation
  version: 2026.08.01
entries:
  - path: challenges/linux/cleanup-logs
    contentRevision: `+string(challengeRevision)+`
taxonomy:
  contentRevision: `+string(taxonomyRevision)+`
`), 0o644)
	return root, challengeRevision, taxonomyRevision
}

func writeChallengeSource(t *testing.T, root string, published bool) {
	t.Helper()
	manifest := `runtime: node
title: Cleanup logs
difficulty: easy
description: |
  Keep this indentation.
nodes:
  - name: host
    title: Host
checkpoints:
  - id: cleanup-script-ready
    title: Create cleanup script
    description: The script exists.
    hint: hints/cleanup-script-ready.md
    node: host
`
	if published {
		manifest = `id: chal-example
source_slug: cleanup-logs
runtime: node
title: Cleanup logs
difficulty: easy
image: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
published_at: 2026-08-01T00:00:00Z
description: |
  Keep this indentation.
nodes:
  - name: host
    title: Host
checkpoints:
  - id: cleanup-script-ready
    title: Create cleanup script
    description: The script exists.
    hint: hints/cleanup-script-ready.md
    node: host
`
	}
	writeCatalogFile(t, filepath.Join(root, "challenge.yaml"), []byte(manifest), 0o644)
	writeCatalogFile(t, filepath.Join(root, "problem.md"), []byte("# Cleanup logs\n"), 0o644)
	writeCatalogFile(t, filepath.Join(root, "solution.md"), []byte("<!-- checkpoint: cleanup-script-ready -->\n"), 0o644)
	writeCatalogFile(t, filepath.Join(root, "hints", "cleanup-script-ready.md"), []byte("Create the script.\n"), 0o644)
	for _, name := range []string{"generate.sh", "answer.sh", "checks.sh"} {
		writeCatalogFile(t, filepath.Join(root, "nodes", "host", name), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	}
}

func writeCatalogFile(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}
