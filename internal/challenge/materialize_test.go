package challenge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListAndGet(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo-task")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "challenge.yaml"), validManifest("id: demo-task\ntitle: Demo\n"))
	writeFile(t, filepath.Join(dir, "Dockerfile"), "FROM alpine:3.20\n")
	writeChallengeAssets(t, dir)

	challenges, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(challenges) != 1 {
		t.Fatalf("expected 1 challenge, got %d", len(challenges))
	}
	if challenges[0].ID != "demo-task" {
		t.Fatalf("unexpected challenge id %q", challenges[0].ID)
	}

	challenge, err := Get(root, "demo-task")
	if err != nil {
		t.Fatal(err)
	}
	if challenge.Title != "Demo" {
		t.Fatalf("unexpected title %q", challenge.Title)
	}
	if challenge.Runtime != "container" {
		t.Fatalf("unexpected runtime %q", challenge.Runtime)
	}
}

func TestMaterializePromotesValidatedChallenge(t *testing.T) {
	root := t.TempDir()
	_, err := Materialize(root, "fresh-task", func(dst string) error {
		writeFile(t, filepath.Join(dst, "challenge.yaml"), validManifest("id: fresh-task\ntitle: Fresh\n"))
		writeFile(t, filepath.Join(dst, "Dockerfile"), "FROM alpine:3.20\n")
		writeFile(t, filepath.Join(dst, "generate.sh"), "#!/bin/sh\n")
		writeChallengeAssets(t, dst)
		writeFile(t, filepath.Join(dst, "notes.txt"), "hello\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, "fresh-task", "challenge.yaml")); err != nil {
		t.Fatalf("expected finalized challenge, stat failed: %v", err)
	}
}

func TestMaterializeRejectsMissingRequiredFiles(t *testing.T) {
	root := t.TempDir()
	_, err := Materialize(root, "broken-task", func(dst string) error {
		writeFile(t, filepath.Join(dst, "challenge.yaml"), "id: broken-task\ntitle: Broken\ntype: script\nruntime: container\ndifficulty: easy\ntags:\n  - linux\nimage: broken-task:v1\ndescription: demo\n")
		return nil
	})
	if err == nil {
		t.Fatal("expected error")
	}

	if _, statErr := os.Stat(filepath.Join(root, "broken-task")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no promoted directory, got %v", statErr)
	}
}

func TestValidateDirRejectsMissingMetadata(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "challenge.yaml"), "id: invalid\ntitle: Invalid\ntype: script\nruntime: container\ndifficulty: \ntags: []\ndescription: \"\"\ncheckpoints: []\n")
	writeFile(t, filepath.Join(root, "Dockerfile"), "FROM alpine:3.20\n")
	writeFile(t, filepath.Join(root, "generate.sh"), "#!/bin/sh\n")
	writeChallengeAssets(t, root)

	if _, err := ValidateDir(root); err == nil {
		t.Fatal("expected ValidateDir to reject missing metadata")
	}
}

func TestValidateDirAcceptsVClusterRuntime(t *testing.T) {
	root := t.TempDir()
	manifest := validManifest("id: vcluster-demo\ntitle: VCluster Demo\nimage: vcluster-demo:v1\n")
	writeFile(t, filepath.Join(root, "challenge.yaml"), strings.Replace(manifest, "runtime: container", "runtime: vcluster", 1))
	writeFile(t, filepath.Join(root, "Dockerfile"), "FROM breakfix-k8s-base:latest\n")
	writeFile(t, filepath.Join(root, "generate.sh"), "#!/bin/sh\n")
	writeChallengeAssets(t, root)

	entry, err := ValidateDir(root)
	if err != nil {
		t.Fatalf("expected vcluster runtime to validate, got %v", err)
	}
	if entry.Runtime != "vcluster" {
		t.Fatalf("unexpected runtime %q", entry.Runtime)
	}
}

func TestValidateSubmissionDirAllowsMissingID(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "challenge.yaml"), validManifest("title: Draft Demo\nimage: demo:v1\n"))
	writeFile(t, filepath.Join(root, "Dockerfile"), "FROM alpine:3.20\n")
	writeFile(t, filepath.Join(root, "generate.sh"), "#!/bin/sh\n")
	writeChallengeAssets(t, root)

	entry, err := ValidateSubmissionDir(root)
	if err != nil {
		t.Fatalf("expected submission dir to validate, got %v", err)
	}
	if entry.ID != "" {
		t.Fatalf("expected empty submission id, got %q", entry.ID)
	}
}

func TestPromoteDirectoryKeepsVerifiedArtifactImmutable(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "authoring", "revision")
	writeFile(t, filepath.Join(source, "challenge.yaml"), validManifest("title: Verified source\n"))
	writeFile(t, filepath.Join(source, "Dockerfile"), "FROM breakfix-base:latest\n")
	writeFile(t, filepath.Join(source, "generate.sh"), "#!/bin/sh\n")
	writeChallengeAssets(t, source)
	sourceBefore, err := LoadSubmissionDir(source)
	if err != nil {
		t.Fatal(err)
	}

	published, err := PromoteDirectory(filepath.Join(root, "challenges"), source, "opaque-challenge", "registry.example/verify:latest")
	if err != nil {
		t.Fatal(err)
	}
	if published.ID != "opaque-challenge" || published.Image != "registry.example/verify:latest" {
		t.Fatalf("unexpected published entry: %#v", published)
	}
	sourceEntry, err := LoadSubmissionDir(source)
	if err != nil {
		t.Fatal(err)
	}
	if sourceEntry.ID != sourceBefore.ID || sourceEntry.Image != sourceBefore.Image {
		t.Fatalf("platform fields leaked into immutable source artifact: %#v", sourceEntry)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func validManifest(prefix string) string {
	return prefix + "type: script\nruntime: container\ndifficulty: easy\ntags:\n  - linux\ndescription: demo\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n"
}

func writeChallengeAssets(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "problem.md"), "problem\n")
	writeFile(t, filepath.Join(root, "solution.md"), "solution\n")
	writeFile(t, filepath.Join(root, "hints", "complete.md"), "hint\n")
	writeFile(t, filepath.Join(root, "checks", "checkpoints.sh"), "#!/bin/sh\nprintf '{\"checks\":[{\"id\":\"complete\",\"passed\":true,\"summary\":\"complete\",\"details\":\"done\"}]}'\n")
	writeFile(t, filepath.Join(root, "answer.sh"), "#!/bin/sh\nexit 0\n")
}
