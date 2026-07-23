package authoring

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifiedArtifactAssetsAndDiffStayWithinAuthoringDirectory(t *testing.T) {
	dataDir := t.TempDir()
	empty, err := ReadAssets(dataDir, nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty artifact must serialize as an empty asset list, got %#v, %v", empty, err)
	}
	firstDir := ArtifactDirectory(dataDir, "author-one", 1)
	secondDir := ArtifactDirectory(dataDir, "author-one", 2)
	writeArtifactAsset(t, filepath.Join(firstDir, "problem.md"), "# Initial problem\n")
	writeArtifactAsset(t, filepath.Join(firstDir, "checks", "checkpoints.sh"), "#!/bin/sh\nexit 1\n")
	writeArtifactAsset(t, filepath.Join(secondDir, "problem.md"), "# Revised problem\n")
	writeArtifactAsset(t, filepath.Join(secondDir, "checks", "checkpoints.sh"), "#!/bin/sh\nexit 0\n")
	writeArtifactAsset(t, filepath.Join(secondDir, "solution.md"), "# Solution\n")

	first := &Artifact{Directory: ArtifactRelativePath("author-one", 1)}
	second := &Artifact{Directory: ArtifactRelativePath("author-one", 2)}
	assets, err := ReadAssets(dataDir, second)
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 3 || assets[0].Path != "checks/checkpoints.sh" || assets[2].Path != "solution.md" {
		t.Fatalf("unexpected artifact assets: %#v", assets)
	}
	diffs, err := DiffAssets(dataDir, second, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 3 {
		t.Fatalf("expected three artifact changes, got %#v", diffs)
	}
	if !strings.Contains(diffs[0].Diff, "-exit 1") || !strings.Contains(diffs[0].Diff, "+exit 0") {
		t.Fatalf("checkpoint diff is not readable: %q", diffs[0].Diff)
	}
	if _, err := ReadAssets(dataDir, &Artifact{Directory: "../../outside"}); err == nil {
		t.Fatal("expected path traversal artifact to be rejected")
	}
}

func TestReadVerifiedChallengeUsesActualArtifactMetadata(t *testing.T) {
	dataDir := t.TempDir()
	dir := ArtifactDirectory(dataDir, "author-two", 3)
	writeArtifactAsset(t, filepath.Join(dir, "challenge.yaml"), `title: Actual verified title
type: script
runtime: container
difficulty: medium
tags: [linux, service]
description: Actual verified description
checkpoints:
  - id: service-ready
    title: Service is ready
    description: The service responds successfully.
    hint: hints/service-ready.md
`)
	writeArtifactAsset(t, filepath.Join(dir, "Dockerfile"), "FROM breakfix-base:latest\n")
	writeArtifactAsset(t, filepath.Join(dir, "generate.sh"), "#!/bin/sh\n")
	writeArtifactAsset(t, filepath.Join(dir, "problem.md"), "# Actual problem\n")
	writeArtifactAsset(t, filepath.Join(dir, "solution.md"), "# Actual solution\n")
	writeArtifactAsset(t, filepath.Join(dir, "hints", "service-ready.md"), "hint\n")
	writeArtifactAsset(t, filepath.Join(dir, "checks", "checkpoints.sh"), "#!/bin/sh\n")
	writeArtifactAsset(t, filepath.Join(dir, "answer.sh"), "#!/bin/sh\n")

	verified, err := ReadVerifiedChallenge(dataDir, &Artifact{Directory: ArtifactRelativePath("author-two", 3)})
	if err != nil {
		t.Fatal(err)
	}
	if verified.Metadata.Title != "Actual verified title" || verified.Metadata.Difficulty != "medium" {
		t.Fatalf("metadata was not loaded from artifact: %#v", verified.Metadata)
	}
	if len(verified.Checkpoints) != 1 || verified.Checkpoints[0].Description != "The service responds successfully." {
		t.Fatalf("checkpoints were not loaded from artifact: %#v", verified.Checkpoints)
	}
}

func writeArtifactAsset(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
