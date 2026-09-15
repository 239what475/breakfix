package scenario

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	testNodeImageFingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testK8sImageDigest       = "registry.example/breakfix/k8s@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testContentRevision      = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testScenarioRevisionID   = "chrev-1111111111111111"
)

func TestListAndGet(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo-task-source", testScenarioRevisionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "scenario.yaml"), validPublishedNodeManifest("id: demo-task\nrevision_id: "+testScenarioRevisionID+"\nsource_slug: demo-task-source\ntitle: Demo\npublished_at: 2026-07-23T07:33:11Z\n"))
	writeScenarioAssets(t, dir)

	scenarios, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("expected 1 scenario, got %d", len(scenarios))
	}
	if scenarios[0].ID != "demo-task" {
		t.Fatalf("unexpected scenario id %q", scenarios[0].ID)
	}
	if got := scenarios[0].PublishedAt; !got.Equal(time.Date(2026, time.July, 23, 7, 33, 11, 0, time.UTC)) {
		t.Fatalf("unexpected published time %s", got)
	}

	scenario, err := Get(root, "demo-task", testScenarioRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if scenario.Title != "Demo" {
		t.Fatalf("unexpected title %q", scenario.Title)
	}
	if scenario.Runtime != RuntimeNode {
		t.Fatalf("unexpected runtime %q", scenario.Runtime)
	}
}

func TestMaterializePromotesValidatedScenario(t *testing.T) {
	root := t.TempDir()
	_, err := MaterializeWithPath(root, "fresh-task", testScenarioRevisionID, "fresh-task-source/"+testScenarioRevisionID, func(dst string) error {
		writeFile(t, filepath.Join(dst, "scenario.yaml"), validPublishedNodeManifest("id: fresh-task\nrevision_id: "+testScenarioRevisionID+"\nsource_slug: fresh-task-source\ntitle: Fresh\npublished_at: 2026-07-24T08:00:00Z\n"))
		writeScenarioAssets(t, dst)
		writeFile(t, filepath.Join(dst, "notes.txt"), "hello\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(root, "fresh-task-source", testScenarioRevisionID, "scenario.yaml")); err != nil {
		t.Fatalf("expected finalized scenario, stat failed: %v", err)
	}
}

func TestMaterializeRejectsMissingRequiredFiles(t *testing.T) {
	root := t.TempDir()
	_, err := MaterializeWithPath(root, "broken-task", testScenarioRevisionID, "broken-task-source/"+testScenarioRevisionID, func(dst string) error {
		writeFile(t, filepath.Join(dst, "scenario.yaml"), "id: broken-task\nrevision_id: "+testScenarioRevisionID+"\nsource_slug: broken-task-source\ntitle: Broken\nruntime: node\nimage: broken-task:v1\ndescription: demo\n")
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
	writeFile(t, filepath.Join(root, "scenario.yaml"), "id: invalid\nsource_slug: invalid\ntitle: Invalid\nruntime: node\ndescription: \"\"\nnodes:\n  - name: host\n    title: Host\ncheckpoints: []\npublished_at: 2026-07-24T08:00:00Z\n")
	writeScenarioAssets(t, root)

	if _, err := ValidateDir(root); err == nil {
		t.Fatal("expected ValidateDir to reject missing metadata")
	}
}

func TestValidateDirAcceptsK8sRuntime(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "scenario.yaml"), validPublishedK8sManifest("id: k8s-demo\nrevision_id: "+testScenarioRevisionID+"\nsource_slug: k8s-demo\ntitle: Kubernetes Demo\npublished_at: 2026-07-24T08:00:00Z\n"))
	writeK8sScenarioAssets(t, root)

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
	writeFile(t, filepath.Join(root, "scenario.yaml"), validManifest("id: unpublished\ntitle: Unpublished\n"))

	if _, err := LoadDir(root); err == nil {
		t.Fatal("expected published scenario without published_at to fail")
	}
}

func TestScenarioRevisionCoversAllArtifactFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "scenario.yaml"), validManifest("id: revision-demo\nrevision_id: "+testScenarioRevisionID+"\ntitle: Revision Demo\npublished_at: 2026-07-24T08:00:00Z\n"))
	writeScenarioAssets(t, root)

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
	writeFile(t, filepath.Join(root, "scenario.yaml"), validManifest("title: Draft Demo\n"))
	writeScenarioAssets(t, root)

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
	writeFile(t, filepath.Join(source, "scenario.yaml"), validManifest("title: Verified source\n"))
	writeScenarioAssets(t, source)
	sourceBefore, err := LoadCandidateDir(source)
	if err != nil {
		t.Fatal(err)
	}

	publishedAt := time.Date(2026, time.July, 24, 8, 15, 0, 0, time.UTC)
	const sourceSlug = "verified-source-opaque-c"
	published, err := PromoteDirectoryAt(filepath.Join(root, "scenarios"), source, "opaque-scenario", testScenarioRevisionID, sourceSlug, testNodeImageFingerprint, testContentRevision, publishedAt)
	if err != nil {
		t.Fatal(err)
	}
	if published.ID != "opaque-scenario" || published.Image != testNodeImageFingerprint {
		t.Fatalf("unexpected published entry: %#v", published)
	}
	if published.SourceSlug != sourceSlug || filepath.Base(published.Dir) != testScenarioRevisionID || filepath.Base(filepath.Dir(published.Dir)) != sourceSlug {
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

func TestMaterializeWithPathSeparatesOpaqueIDFromReadableDirectory(t *testing.T) {
	root := t.TempDir()
	entry, err := MaterializeWithPath(root, "chal-4m6q8r2t9v3x", testScenarioRevisionID, "示例节点题-4m6q8r2/"+testScenarioRevisionID, func(dst string) error {
		writeFile(t, filepath.Join(dst, "scenario.yaml"), validPublishedNodeManifest("id: chal-4m6q8r2t9v3x\nrevision_id: "+testScenarioRevisionID+"\nsource_slug: 示例节点题-4m6q8r2\ntitle: 示例节点题\npublished_at: 2026-07-24T08:00:00Z\n"))
		writeScenarioAssets(t, dst)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.ID != "chal-4m6q8r2t9v3x" || filepath.Base(entry.Dir) != testScenarioRevisionID || filepath.Base(filepath.Dir(entry.Dir)) != "示例节点题-4m6q8r2" {
		t.Fatalf("opaque ID and source directory were not separated: %#v", entry)
	}
	loaded, err := Get(root, entry.ID, entry.RevisionID)
	if err != nil || loaded.Dir != entry.Dir {
		t.Fatalf("lookup by opaque ID through source directory = %#v, %v", loaded, err)
	}
}

func TestMaterializeWithPathKeepsRevisionsInSeparateDirectories(t *testing.T) {
	root := t.TempDir()
	sourceSlug := "revision-history"
	firstRevision := testScenarioRevisionID
	secondRevision := "chrev-2222222222222222"
	for _, revisionID := range []string{firstRevision, secondRevision} {
		_, err := MaterializeWithPath(root, "chal-history", revisionID, sourceSlug+"/"+revisionID, func(dst string) error {
			writeFile(t, filepath.Join(dst, "scenario.yaml"), validPublishedNodeManifest("id: chal-history\nrevision_id: "+revisionID+"\nsource_slug: "+sourceSlug+"\ntitle: Historical\npublished_at: 2026-07-24T08:00:00Z\n"))
			writeScenarioAssets(t, dst)
			return nil
		})
		if err != nil {
			t.Fatalf("materialize revision %s: %v", revisionID, err)
		}
	}
	for _, revisionID := range []string{firstRevision, secondRevision} {
		if _, err := os.Stat(filepath.Join(root, sourceSlug, revisionID, "scenario.yaml")); err != nil {
			t.Fatalf("revision %s was not retained: %v", revisionID, err)
		}
	}
}

func TestMaterializeWithPathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := MaterializeWithPath(root, "chal-traversal", testScenarioRevisionID, "../outside", func(string) error {
		t.Fatal("populate called for invalid path")
		return nil
	}); err == nil {
		t.Fatal("expected path traversal to be rejected")
	}
}

func TestSourceSlugForDoesNotEndWithTruncatedIdentifierSeparator(t *testing.T) {
	if got, want := SourceSlugFor("Verified publish title", "chal-publish-recovery"), "verified-publish-title-publish"; got != want {
		t.Fatalf("SourceSlugFor() = %q, want %q", got, want)
	}
}

func TestListRejectsPublishedScenarioWithoutMatchingSourceSlug(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "readable-directory", testScenarioRevisionID)
	writeFile(t, filepath.Join(dir, "scenario.yaml"), validPublishedNodeManifest("id: chal-4m6q8r2t9v3x\nrevision_id: "+testScenarioRevisionID+"\nsource_slug: another-directory\ntitle: Mismatch\npublished_at: 2026-07-24T08:00:00Z\n"))
	writeScenarioAssets(t, dir)

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
	return prefix + "runtime: node\ndescription: demo\nversions:\n  - component: fixture\n    version: v1\ntopology: One host node.\ninitialization: initialize.sh removes the completion marker.\nreproduction:\n  objective: The completion marker is absent.\n  evidence:\n    - id: completion-marker-absent\n      description: The completion marker does not exist.\n      node: host\nnodes:\n  - name: host\n    title: Host\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n    node: host\n"
}

func validPublishedNodeManifest(prefix string) string {
	return prefix + "image: " + testNodeImageFingerprint + "\ncontent_revision: " + testContentRevision + "\n" + validManifest("")
}

func validK8sManifest(prefix string) string {
	return prefix + "runtime: k8s\ndescription: demo\nversions:\n  - component: kubernetes\n    version: v1\ntopology: One isolated Kubernetes control plane.\ninitialization: initialize.sh removes the completion marker.\nreproduction:\n  objective: The completion marker is absent.\n  evidence:\n    - id: completion-marker-absent\n      description: The completion marker does not exist.\ncheckpoints:\n  - id: complete\n    title: Complete\n    description: Complete the task\n    hint: hints/complete.md\n"
}

func validPublishedK8sManifest(prefix string) string {
	return prefix + "image: " + testK8sImageDigest + "\ncontent_revision: " + testContentRevision + "\n" + validK8sManifest("")
}

func writeScenarioAssets(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "problem.md"), "problem\n")
	writeFile(t, filepath.Join(root, "solution.md"), "<!-- checkpoint: complete -->\nsolution\n")
	writeFile(t, filepath.Join(root, "hints", "complete.md"), "hint\n")
	writeFile(t, filepath.Join(root, "nodes", "host", "initialize.sh"), "#!/bin/sh\nexit 0\n")
	writeFile(t, filepath.Join(root, "nodes", "host", "assertions", "initial-completion-marker-absent.sh"), "#!/bin/sh\nprintf '{\"assertions\":[{\"id\":\"completion-marker-absent\",\"satisfied\":true,\"summary\":\"absent\",\"details\":\"fixture\"}]}'\n")
	writeFile(t, filepath.Join(root, "nodes", "host", "assertions", "final-complete.sh"), "#!/bin/sh\nprintf '{\"assertions\":[{\"id\":\"complete\",\"satisfied\":true,\"summary\":\"complete\",\"details\":\"done\"}]}'\n")
	writeFile(t, filepath.Join(root, "nodes", "host", "actions", "apply.sh"), "#!/bin/sh\nexit 0\n")
}

func writeK8sScenarioAssets(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "problem.md"), "problem\n")
	writeFile(t, filepath.Join(root, "solution.md"), "<!-- checkpoint: complete -->\nsolution\n")
	writeFile(t, filepath.Join(root, "hints", "complete.md"), "hint\n")
	writeFile(t, filepath.Join(root, "k8s", "initialize.sh"), "#!/bin/sh\nexit 0\n")
	writeFile(t, filepath.Join(root, "k8s", "assertions", "initial-completion-marker-absent.sh"), "#!/bin/sh\nprintf '{\"assertions\":[{\"id\":\"completion-marker-absent\",\"satisfied\":true,\"summary\":\"absent\",\"details\":\"fixture\"}]}'\n")
	writeFile(t, filepath.Join(root, "k8s", "assertions", "final-complete.sh"), "#!/bin/sh\nprintf '{\"assertions\":[{\"id\":\"complete\",\"satisfied\":true,\"summary\":\"complete\",\"details\":\"done\"}]}'\n")
	writeFile(t, filepath.Join(root, "k8s", "actions", "apply.sh"), "#!/bin/sh\nexit 0\n")
}
