package authoring

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidateArchiveAssetsAndDiff(t *testing.T) {
	empty, err := ReadAssets(nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty candidate must serialize as an empty asset list, got %#v, %v", empty, err)
	}

	firstDir := filepath.Join(t.TempDir(), "first")
	secondDir := filepath.Join(t.TempDir(), "second")
	writeCandidateAssets(t, firstDir, "Initial title", "Initial description", "# Initial problem\n", "#!/bin/sh\nexit 1\n", "# Initial solution\n<!-- checkpoint: service-ready -->\n")
	writeCandidateAssets(t, secondDir, "Revised title", "Revised description", "# Revised problem\n", "#!/bin/sh\nexit 0\n", "# Revised solution\n<!-- checkpoint: service-ready -->\n")
	first := archiveCandidateDir(t, firstDir)
	second := archiveCandidateDir(t, secondDir)

	assets, err := ReadAssets(second)
	if err != nil {
		t.Fatal(err)
	}
	if !hasAsset(assets, "nodes/host/checks.sh") || !hasAsset(assets, "solution.md") || !hasAsset(assets, "challenge.yaml") {
		t.Fatalf("candidate assets are incomplete: %#v", assets)
	}
	diffs, err := DiffAssets(second, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 4 {
		t.Fatalf("expected manifest, problem, solution, and checkpoint changes, got %#v", diffs)
	}
	var checkpointDiff string
	for _, diff := range diffs {
		if diff.Path == "nodes/host/checks.sh" {
			checkpointDiff = diff.Diff
		}
	}
	if !strings.Contains(checkpointDiff, "-exit 1") || !strings.Contains(checkpointDiff, "+exit 0") {
		t.Fatalf("checkpoint diff is not readable: %q", checkpointDiff)
	}
}

func TestReadVerifiedChallengeUsesCandidateManifest(t *testing.T) {
	dir := t.TempDir()
	writeCandidateAssets(t, dir, "Actual verified title", "Actual verified description", "# Actual problem\n", "#!/bin/sh\nexit 0\n", "# Actual solution\n<!-- checkpoint: service-ready -->\n")

	verified, err := ReadVerifiedChallenge(archiveCandidateDir(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if verified.Metadata.Title != "Actual verified title" || verified.Metadata.Runtime != "node" {
		t.Fatalf("metadata was not loaded from candidate: %#v", verified.Metadata)
	}
	if len(verified.Checkpoints) != 1 || verified.Checkpoints[0].Description != "The service responds successfully." || verified.Checkpoints[0].Node != "host" {
		t.Fatalf("checkpoints were not loaded from candidate: %#v", verified.Checkpoints)
	}
}

func TestReadAssetsRejectsArchivePathTraversal(t *testing.T) {
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	content := []byte("outside")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "../outside", Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAssets(archive.Bytes()); err == nil {
		t.Fatal("path-traversing candidate archive was accepted")
	}
}

func writeCandidateAssets(t *testing.T, root, title, description, problem, checks, solution string) {
	t.Helper()
	writeCandidateAsset(t, filepath.Join(root, "challenge.yaml"), "title: "+title+"\nruntime: node\ndescription: "+description+"\nnodes:\n  - name: host\n    title: Host\ncheckpoints:\n  - id: service-ready\n    title: Service ready\n    description: The service responds successfully.\n    hint: hints/service-ready.md\n    node: host\n")
	writeCandidateAsset(t, filepath.Join(root, "problem.md"), problem)
	writeCandidateAsset(t, filepath.Join(root, "solution.md"), solution)
	writeCandidateAsset(t, filepath.Join(root, "hints", "service-ready.md"), "hint\n")
	writeCandidateAsset(t, filepath.Join(root, "nodes", "host", "generate.sh"), "#!/bin/sh\n")
	writeCandidateAsset(t, filepath.Join(root, "nodes", "host", "checks.sh"), checks)
	writeCandidateAsset(t, filepath.Join(root, "nodes", "host", "answer.sh"), "#!/bin/sh\n")
}

func writeCandidateAsset(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func archiveCandidateDir(t *testing.T, root string) []byte {
	t.Helper()
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rootFS.Close() }()
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		content, err := rootFS.ReadFile(relative)
		if err != nil {
			return err
		}
		if err := tarWriter.WriteHeader(&tar.Header{Name: filepath.ToSlash(relative), Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = tarWriter.Write(content)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func hasAsset(assets []Asset, path string) bool {
	for _, asset := range assets {
		if asset.Path == path {
			return true
		}
	}
	return false
}
