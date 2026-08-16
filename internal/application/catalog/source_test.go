package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/breakfix/breakfix/internal/adapter/oci"
	"github.com/breakfix/breakfix/internal/content/challenge"
	catalogdomain "github.com/breakfix/breakfix/internal/domain/catalog"
)

func TestCheckedInCatalogFixtureSourceIsPortable(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate catalog source test")
	}
	repository := filepath.Join(filepath.Dir(file), "..", "..", "..")
	root := filepath.Join(repository, "test", "fixtures", "catalog-release")
	if _, err := LoadPortableSource(root); err != nil {
		t.Fatalf("load checked-in catalog fixture source: %v", err)
	}
}

func TestPortableSourceBuildsDeterministicBundle(t *testing.T) {
	root, challengeRevision, roadmapRevision := writePortableRelease(t)

	source, err := LoadPortableSource(root)
	if err != nil {
		t.Fatalf("load portable source: %v", err)
	}
	if got := source.Manifest.Entries[0].ContentRevision; got != challengeRevision {
		t.Fatalf("challenge contentRevision = %q, want %q", got, challengeRevision)
	}
	if got := source.Manifest.Roadmap.ContentRevision; got != roadmapRevision {
		t.Fatalf("roadmap contentRevision = %q, want %q", got, roadmapRevision)
	}
	if len(source.Challenges) != 1 || source.Challenges[0].Entry.Title != "Cleanup logs" || len(source.Roadmap.ChallengeBindings) != 1 {
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

func TestCalculateContentRevisionsIgnoresStaleManifestValues(t *testing.T) {
	root, challengeRevision, roadmapRevision := writePortableRelease(t)
	manifestPath := filepath.Join(root, "release.yaml")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestText := strings.Replace(string(manifest), string(challengeRevision), "sha256:"+strings.Repeat("1", 64), 1)
	manifestText = strings.Replace(manifestText, string(roadmapRevision), "sha256:"+strings.Repeat("2", 64), 1)
	if err := os.WriteFile(manifestPath, []byte(manifestText), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := CalculateContentRevisions(root)
	if err != nil {
		t.Fatalf("calculate content revisions: %v", err)
	}
	if len(report.Entries) != 1 || report.Entries[0].ContentRevision != challengeRevision {
		t.Fatalf("challenge revisions = %#v, want %q", report.Entries, challengeRevision)
	}
	if report.Roadmap.ContentRevision != roadmapRevision {
		t.Fatalf("roadmap revision = %q, want %q", report.Roadmap.ContentRevision, roadmapRevision)
	}
	if _, err := LoadPortableSource(root); err == nil {
		t.Fatal("stale manifest unexpectedly loaded as a valid source")
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
	// #nosec G302 -- this test explicitly verifies that an executable fixture changes the revision.
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
	// #nosec G302 -- this test verifies equivalent executable modes have the same revision.
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
	t.Cleanup(func() {
		if err := reader.Close(); err != nil {
			t.Errorf("close source layer: %v", err)
		}
	})
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

	roadmapRoot := filepath.Join(root, "roadmap")
	writeCatalogFile(t, filepath.Join(roadmapRoot, "domains", "linux.yaml"), []byte(`kind: Domain
source_ref: linux
title: Linux operations
definition: Operate and recover Linux systems from observable evidence.
scope: Files, services, and local operational tooling.
non_goals: Kernel development and provider implementation details.
`), 0o644)
	writeCatalogFile(t, filepath.Join(roadmapRoot, "topics", "linux", "shell-files.yaml"), []byte(`kind: Topic
source_ref: linux/shell-files
title: Shell and files
domain:
  source_ref: linux
  title: Linux operations
definition: Locate, inspect, and repair shell and file state.
scope: Shell execution and file content used by operational tasks.
non_goals: Service orchestration and network configuration.
challenge_guidance: Use for challenges whose root cause and recovery are primarily shell or file state.
`), 0o644)
	writeCatalogFile(t, filepath.Join(roadmapRoot, "tags", "shell.yaml"), []byte(`kind: Tag
source_ref: shell
title: Shell
description: The challenge substantially depends on shell behavior or shell tooling.
`), 0o644)
	writeCatalogFile(t, filepath.Join(roadmapRoot, "challenge-bindings", "cleanup-logs.yaml"), []byte(`kind: Challenge
challenge:
  path: challenges/linux/cleanup-logs
  source_ref: linux/shell-files/cleanup-logs
  title: Cleanup logs
  content_revision: `+string(challengeRevision)+`
topic:
  source_ref: linux/shell-files
  title: Shell and files
tags:
  - source_ref: shell
    title: Shell
`), 0o644)
	writeCatalogFile(t, filepath.Join(roadmapRoot, "topic-edges.yaml"), []byte("[]\n"), 0o644)
	writeCatalogFile(t, filepath.Join(roadmapRoot, "challenge-edges.yaml"), []byte("[]\n"), 0o644)
	roadmapRevision, err := ContentRevision(roadmapRoot)
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
roadmap:
  contentRevision: `+string(roadmapRevision)+`
`), 0o644)
	return root, challengeRevision, roadmapRevision
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
revision_id: chrev-aaaaaaaaaaaaaaaa
source_slug: cleanup-logs
runtime: node
title: Cleanup logs
difficulty: easy
image: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
content_revision: sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
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
	// #nosec G703 -- test paths are always constructed below a test-owned temporary root.
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}
