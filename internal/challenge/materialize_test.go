package challenge

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	testNodeImageFingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testK8sImageDigest       = "registry.example/breakfix/k8s@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestListAndGet(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo-task-source")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "challenge.yaml"), validPublishedNodeManifest("id: demo-task\nsource_slug: demo-task-source\ntitle: Demo\npublished_at: 2026-07-23T07:33:11Z\n"))
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
	if challenge.Runtime != RuntimeNode {
		t.Fatalf("unexpected runtime %q", challenge.Runtime)
	}
}

func TestMaterializePromotesValidatedChallenge(t *testing.T) {
	root := t.TempDir()
	_, err := MaterializeWithSlug(root, "fresh-task", "fresh-task-source", func(dst string) error {
		writeFile(t, filepath.Join(dst, "challenge.yaml"), validPublishedNodeManifest("id: fresh-task\nsource_slug: fresh-task-source\ntitle: Fresh\npublished_at: 2026-07-24T08:00:00Z\n"))
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
		writeFile(t, filepath.Join(dst, "challenge.yaml"), "id: broken-task\nsource_slug: broken-task-source\ntitle: Broken\nruntime: node\ndifficulty: easy\nimage: broken-task:v1\ndescription: demo\n")
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
	writeFile(t, filepath.Join(root, "challenge.yaml"), "id: invalid\nsource_slug: invalid\ntitle: Invalid\nruntime: node\ndifficulty: \ndescription: \"\"\nnodes:\n  - name: host\n    title: Host\ncheckpoints: []\npublished_at: 2026-07-24T08:00:00Z\n")
	writeChallengeAssets(t, root)

	if _, err := ValidateDir(root); err == nil {
		t.Fatal("expected ValidateDir to reject missing metadata")
	}
}

func TestValidateDirAcceptsK8sRuntime(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "challenge.yaml"), validPublishedK8sManifest("id: k8s-demo\nsource_slug: k8s-demo\ntitle: Kubernetes Demo\npublished_at: 2026-07-24T08:00:00Z\n"))
	writeK8sChallengeAssets(t, root)

	entry, err := ValidateDir(root)
	if err != nil {
		t.Fatalf("expected k8s runtime to validate, got %v", err)
	}
	if entry.Runtime != RuntimeK8s {
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

func TestValidateCandidateDirAllowsMissingPlatformFields(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "challenge.yaml"), validManifest("title: Draft Demo\n"))
	writeChallengeAssets(t, root)

	entry, err := ValidateCandidateDir(root)
	if err != nil {
		t.Fatalf("expected candidate dir to validate, got %v", err)
	}
	if entry.ID != "" {
		t.Fatalf("expected empty candidate id, got %q", entry.ID)
	}
}

func TestPromoteDirectoryKeepsVerifiedArtifactImmutable(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "authoring", "revision")
	writeFile(t, filepath.Join(source, "challenge.yaml"), validManifest("title: Verified source\n"))
	writeChallengeAssets(t, source)
	sourceBefore, err := LoadCandidateDir(source)
	if err != nil {
		t.Fatal(err)
	}

	publishedAt := time.Date(2026, time.July, 24, 8, 15, 0, 0, time.UTC)
	published, err := PromoteDirectoryAt(filepath.Join(root, "challenges"), source, "opaque-challenge", testNodeImageFingerprint, publishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if published.ID != "opaque-challenge" || published.Image != testNodeImageFingerprint {
		t.Fatalf("unexpected published entry: %#v", published)
	}
	if published.SourceSlug != "verified-source-opaque-c" || filepath.Base(published.Dir) != published.SourceSlug {
		t.Fatalf("published source slug = %q at %q", published.SourceSlug, published.Dir)
	}
	if !published.PublishedAt.Equal(publishedAt) {
		t.Fatalf("published time = %s, want %s", published.PublishedAt, publishedAt)
	}
	sourceEntry, err := LoadCandidateDir(source)
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
		writeFile(t, filepath.Join(dst, "challenge.yaml"), validPublishedNodeManifest("id: chal-4m6q8r2t9v3x\nsource_slug: 批量压缩旧日志-4m6q8r2\ntitle: 批量压缩旧日志\npublished_at: 2026-07-24T08:00:00Z\n"))
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

func TestSourceSlugForDoesNotEndWithTruncatedIdentifierSeparator(t *testing.T) {
	if got, want := SourceSlugFor("Verified publish title", "chal-publish-recovery"), "verified-publish-title-publish"; got != want {
		t.Fatalf("SourceSlugFor() = %q, want %q", got, want)
	}
}

func TestListRejectsPublishedChallengeWithoutMatchingSourceSlug(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "readable-directory")
	writeFile(t, filepath.Join(dir, "challenge.yaml"), validPublishedNodeManifest("id: chal-4m6q8r2t9v3x\nsource_slug: another-directory\ntitle: Mismatch\npublished_at: 2026-07-24T08:00:00Z\n"))
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
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func validManifest(prefix string) string {
	return prefix + "runtime: node\ndifficulty: easy\ndescription: demo\nnodes:\n  - name: host\n    title: Host\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n    node: host\n"
}

func validPublishedNodeManifest(prefix string) string {
	return prefix + "image: " + testNodeImageFingerprint + "\n" + validManifest("")
}

func validK8sManifest(prefix string) string {
	return prefix + "runtime: k8s\ndifficulty: easy\ndescription: demo\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n"
}

func validPublishedK8sManifest(prefix string) string {
	return prefix + "image: " + testK8sImageDigest + "\n" + validK8sManifest("")
}

func writeChallengeAssets(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "problem.md"), "problem\n")
	writeFile(t, filepath.Join(root, "solution.md"), "<!-- checkpoint: complete -->\nsolution\n")
	writeFile(t, filepath.Join(root, "hints", "complete.md"), "hint\n")
	writeFile(t, filepath.Join(root, "nodes", "host", "generate.sh"), "#!/bin/sh\nexit 0\n")
	writeFile(t, filepath.Join(root, "nodes", "host", "checks.sh"), "#!/bin/sh\nprintf '{\"checks\":[{\"id\":\"complete\",\"passed\":true,\"summary\":\"complete\",\"details\":\"done\"}]}'\n")
	writeFile(t, filepath.Join(root, "nodes", "host", "answer.sh"), "#!/bin/sh\nexit 0\n")
}

func writeK8sChallengeAssets(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "problem.md"), "problem\n")
	writeFile(t, filepath.Join(root, "solution.md"), "<!-- checkpoint: complete -->\nsolution\n")
	writeFile(t, filepath.Join(root, "hints", "complete.md"), "hint\n")
	writeFile(t, filepath.Join(root, "k8s", "generate.sh"), "#!/bin/sh\nexit 0\n")
	writeFile(t, filepath.Join(root, "k8s", "checks.sh"), "#!/bin/sh\nprintf '{\"checks\":[{\"id\":\"complete\",\"passed\":true,\"summary\":\"complete\",\"details\":\"done\"}]}'\n")
	writeFile(t, filepath.Join(root, "k8s", "answer.sh"), "#!/bin/sh\nexit 0\n")
}
