package challenge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestListAndGet(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo-task-source")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "challenge.yaml"), validManifest("id: demo-task\nsource_slug: demo-task-source\ntitle: Demo\npublished_at: 2026-07-23T07:33:11Z\n"))
	writeFile(t, filepath.Join(dir, "Dockerfile"), "FROM alpine:3.20\n")
	writeFile(t, filepath.Join(dir, "generate.sh"), "#!/bin/sh\n")
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
	if got := challenges[0].PublishedAt; !got.Equal(time.Date(2026, time.July, 23, 7, 33, 11, 0, time.UTC)) {
		t.Fatalf("unexpected published time %s", got)
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
	_, err := MaterializeWithSlug(root, "fresh-task", "fresh-task-source", func(dst string) error {
		writeFile(t, filepath.Join(dst, "challenge.yaml"), validManifest("id: fresh-task\nsource_slug: fresh-task-source\ntitle: Fresh\npublished_at: 2026-07-24T08:00:00Z\n"))
		writeFile(t, filepath.Join(dst, "Dockerfile"), "FROM alpine:3.20\n")
		writeFile(t, filepath.Join(dst, "generate.sh"), "#!/bin/sh\n")
		writeChallengeAssets(t, dst)
		writeFile(t, filepath.Join(dst, "notes.txt"), "hello\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, "fresh-task-source", "challenge.yaml")); err != nil {
		t.Fatalf("expected finalized challenge, stat failed: %v", err)
	}
}

func TestMaterializeRejectsMissingRequiredFiles(t *testing.T) {
	root := t.TempDir()
	_, err := MaterializeWithSlug(root, "broken-task", "broken-task-source", func(dst string) error {
		writeFile(t, filepath.Join(dst, "challenge.yaml"), "id: broken-task\nsource_slug: broken-task-source\ntitle: Broken\ntype: script\nruntime: container\ndifficulty: easy\nimage: broken-task:v1\ndescription: demo\n")
		return nil
	})
	if err == nil {
		t.Fatal("expected error")
	}

	if _, statErr := os.Stat(filepath.Join(root, "broken-task-source")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no promoted directory, got %v", statErr)
	}
}

func TestValidateDirRejectsMissingMetadata(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "challenge.yaml"), "id: invalid\ntitle: Invalid\ntype: script\nruntime: container\ndifficulty: \ndescription: \"\"\ncheckpoints: []\n")
	writeFile(t, filepath.Join(root, "Dockerfile"), "FROM alpine:3.20\n")
	writeFile(t, filepath.Join(root, "generate.sh"), "#!/bin/sh\n")
	writeChallengeAssets(t, root)

	if _, err := ValidateDir(root); err == nil {
		t.Fatal("expected ValidateDir to reject missing metadata")
	}
}

func TestValidateDirAcceptsVClusterRuntime(t *testing.T) {
	root := t.TempDir()
	manifest := validManifest("id: vcluster-demo\nsource_slug: vcluster-demo\ntitle: VCluster Demo\nimage: vcluster-demo:v1\npublished_at: 2026-07-24T08:00:00Z\n")
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

func TestLoadDirRejectsMissingPublishedTime(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "challenge.yaml"), validManifest("id: unpublished\ntitle: Unpublished\n"))

	if _, err := LoadDir(root); err == nil {
		t.Fatal("expected published challenge without published_at to fail")
	}
}

func TestChallengeRevisionCoversAllArtifactFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "challenge.yaml"), validManifest("id: revision-demo\ntitle: Revision Demo\npublished_at: 2026-07-24T08:00:00Z\n"))
	writeFile(t, filepath.Join(root, "Dockerfile"), "FROM alpine:3.20\n")
	writeFile(t, filepath.Join(root, "generate.sh"), "#!/bin/sh\n")
	writeChallengeAssets(t, root)

	first, err := LoadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "solution.md"), "revised solution\n")
	second, err := LoadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision == second.Revision {
		t.Fatalf("revision did not change after solution update: %s", first.Revision)
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

	publishedAt := time.Date(2026, time.July, 24, 8, 15, 0, 0, time.UTC)
	published, err := PromoteDirectoryAt(filepath.Join(root, "challenges"), source, "opaque-challenge", "registry.example/verify:latest", publishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if published.ID != "opaque-challenge" || published.Image != "registry.example/verify:latest" {
		t.Fatalf("unexpected published entry: %#v", published)
	}
	if published.SourceSlug != "verified-source-opaque-c" || filepath.Base(published.Dir) != published.SourceSlug {
		t.Fatalf("published source slug = %q at %q", published.SourceSlug, published.Dir)
	}
	if !published.PublishedAt.Equal(publishedAt) {
		t.Fatalf("published time = %s, want %s", published.PublishedAt, publishedAt)
	}
	sourceEntry, err := LoadSubmissionDir(source)
	if err != nil {
		t.Fatal(err)
	}
	if sourceEntry.ID != sourceBefore.ID || sourceEntry.Image != sourceBefore.Image {
		t.Fatalf("platform fields leaked into immutable source artifact: %#v", sourceEntry)
	}
	if !sourceEntry.PublishedAt.IsZero() {
		t.Fatalf("published timestamp leaked into immutable source artifact: %s", sourceEntry.PublishedAt)
	}
	if sourceEntry.SourceSlug != "" {
		t.Fatalf("platform source slug leaked into immutable source artifact: %q", sourceEntry.SourceSlug)
	}
}

func TestMaterializeWithSlugSeparatesOpaqueIDFromReadableDirectory(t *testing.T) {
	root := t.TempDir()
	entry, err := MaterializeWithSlug(root, "chal-4m6q8r2t9v3x", "批量压缩旧日志-4m6q8r2", func(dst string) error {
		writeFile(t, filepath.Join(dst, "challenge.yaml"), validManifest("id: chal-4m6q8r2t9v3x\nsource_slug: 批量压缩旧日志-4m6q8r2\ntitle: 批量压缩旧日志\npublished_at: 2026-07-24T08:00:00Z\n"))
		writeFile(t, filepath.Join(dst, "Dockerfile"), "FROM alpine:3.20\n")
		writeFile(t, filepath.Join(dst, "generate.sh"), "#!/bin/sh\n")
		writeChallengeAssets(t, dst)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "chal-4m6q8r2t9v3x" || filepath.Base(entry.Dir) != "批量压缩旧日志-4m6q8r2" {
		t.Fatalf("opaque ID and source directory were not separated: %#v", entry)
	}
	loaded, err := Get(root, entry.ID)
	if err != nil || loaded.Dir != entry.Dir {
		t.Fatalf("lookup by opaque ID through source directory = %#v, %v", loaded, err)
	}
}

func TestListRejectsPublishedChallengeWithoutMatchingSourceSlug(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "readable-directory")
	writeFile(t, filepath.Join(dir, "challenge.yaml"), validManifest("id: chal-4m6q8r2t9v3x\nsource_slug: another-directory\ntitle: Mismatch\npublished_at: 2026-07-24T08:00:00Z\n"))
	writeFile(t, filepath.Join(dir, "Dockerfile"), "FROM alpine:3.20\n")
	writeFile(t, filepath.Join(dir, "generate.sh"), "#!/bin/sh\n")
	writeChallengeAssets(t, dir)

	if _, err := List(root); err == nil {
		t.Fatal("expected published directory/source_slug mismatch to be rejected")
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
	return prefix + "type: script\nruntime: container\ndifficulty: easy\ndescription: demo\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n"
}

func writeChallengeAssets(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "problem.md"), "problem\n")
	writeFile(t, filepath.Join(root, "solution.md"), "solution\n")
	writeFile(t, filepath.Join(root, "hints", "complete.md"), "hint\n")
	writeFile(t, filepath.Join(root, "checks", "checkpoints.sh"), "#!/bin/sh\nprintf '{\"checks\":[{\"id\":\"complete\",\"passed\":true,\"summary\":\"complete\",\"details\":\"done\"}]}'\n")
	writeFile(t, filepath.Join(root, "answer.sh"), "#!/bin/sh\nexit 0\n")
}
