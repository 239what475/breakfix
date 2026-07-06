package challenge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListAndGet(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo-task")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "challenge.yaml"), "id: demo-task\ntitle: Demo\ntype: script\nruntime: container\ndifficulty: easy\ntags:\n  - linux\nimage: demo-task:v1\ndescription: demo\n")
	writeFile(t, filepath.Join(dir, "Dockerfile"), "FROM alpine:3.20\n")
	writeFile(t, filepath.Join(dir, "verify.sh"), "#!/bin/sh\nexit 0\n")
	writeFile(t, filepath.Join(dir, "answer.sh"), "#!/bin/sh\nexit 0\n")

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
		writeFile(t, filepath.Join(dst, "challenge.yaml"), "id: fresh-task\ntitle: Fresh\ntype: script\nruntime: container\ndifficulty: easy\ntags:\n  - linux\nimage: fresh-task:v1\ndescription: demo\n")
		writeFile(t, filepath.Join(dst, "Dockerfile"), "FROM alpine:3.20\n")
		writeFile(t, filepath.Join(dst, "generate.sh"), "#!/bin/sh\n")
		writeFile(t, filepath.Join(dst, "question.md"), "fix it\n")
		writeFile(t, filepath.Join(dst, "verify.sh"), "#!/bin/sh\nexit 0\n")
		writeFile(t, filepath.Join(dst, "answer.sh"), "#!/bin/sh\nexit 0\n")
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
	writeFile(t, filepath.Join(root, "challenge.yaml"), "id: invalid\ntitle: Invalid\ntype: script\nruntime: container\ndifficulty: \ntags: []\ndescription: \"\"\n")
	writeFile(t, filepath.Join(root, "Dockerfile"), "FROM alpine:3.20\n")
	writeFile(t, filepath.Join(root, "generate.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "question.md"), "fix it\n")
	writeFile(t, filepath.Join(root, "verify.sh"), "#!/bin/sh\nexit 0\n")
	writeFile(t, filepath.Join(root, "answer.sh"), "#!/bin/sh\nexit 0\n")

	if _, err := ValidateDir(root); err == nil {
		t.Fatal("expected ValidateDir to reject missing metadata")
	}
}

func TestValidateDirAcceptsVClusterRuntime(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "challenge.yaml"), "id: vcluster-demo\ntitle: VCluster Demo\ntype: script\nruntime: vcluster\ndifficulty: easy\ntags:\n  - kubernetes\ndescription: demo\nimage: vcluster-demo:v1\n")
	writeFile(t, filepath.Join(root, "Dockerfile"), "FROM breakfix-k8s-base:latest\n")
	writeFile(t, filepath.Join(root, "generate.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "question.md"), "fix it\n")
	writeFile(t, filepath.Join(root, "verify.sh"), "#!/bin/sh\nexit 0\n")
	writeFile(t, filepath.Join(root, "answer.sh"), "#!/bin/sh\nexit 0\n")

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
	writeFile(t, filepath.Join(root, "challenge.yaml"), "title: Draft Demo\ntype: script\nruntime: container\ndifficulty: easy\ntags:\n  - linux\ndescription: demo\nimage: demo:v1\n")
	writeFile(t, filepath.Join(root, "Dockerfile"), "FROM alpine:3.20\n")
	writeFile(t, filepath.Join(root, "generate.sh"), "#!/bin/sh\n")
	writeFile(t, filepath.Join(root, "question.md"), "fix it\n")
	writeFile(t, filepath.Join(root, "verify.sh"), "#!/bin/sh\nexit 0\n")
	writeFile(t, filepath.Join(root, "answer.sh"), "#!/bin/sh\nexit 0\n")

	entry, err := ValidateSubmissionDir(root)
	if err != nil {
		t.Fatalf("expected submission dir to validate, got %v", err)
	}
	if entry.ID != "" {
		t.Fatalf("expected empty submission id, got %q", entry.ID)
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
