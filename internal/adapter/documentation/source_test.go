package docsource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	domain "github.com/breakfix/breakfix/internal/domain/documentpractice"
)

func testContext() domain.DocumentContext {
	return domain.DocumentContext{FormatVersion: domain.FormatVersion, SourceID: "kubernetes", Repository: "https://github.com/kubernetes/website", Commit: strings.Repeat("a", 40), Version: "v1.34.0", Language: "en", License: "CC BY 4.0", MirrorOrigin: "https://docs.example.test", MirrorDigest: "sha256:" + strings.Repeat("b", 64), PagePath: "docs/pods.md", Anchor: "pod-lifecycle"}
}

func TestSnapshotReadsPinnedFilesAndEvidence(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs/pods.md"), []byte("# Pod lifecycle\n\n## Running\n"), 0644); err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewSnapshot(testContext(), root)
	if err != nil {
		t.Fatal(err)
	}
	page, err := snapshot.ReadPage("docs/pods.md", "pod-lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	if page.Digest == "" || !strings.Contains(page.Content, "Pod lifecycle") {
		t.Fatalf("unexpected page: %#v", page)
	}
	meta, err := snapshot.ReadMetadata("docs/pods.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Anchors) != 2 {
		t.Fatalf("anchors: %#v", meta.Anchors)
	}
	if _, err := snapshot.ReadPage("../secret", ""); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestSnapshotRejectsSymlinksAndOversizedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "secret"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "secret"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewSnapshot(testContext(), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.ReadPage("link", ""); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "large"), []byte(strings.Repeat("x", MaxReadBytes+1)), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.ReadPage("large", ""); err == nil {
		t.Fatal("oversized file accepted")
	}
}
