package revision

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryIncludesPathsContentsAndExecutableBits(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first.sh")
	second := filepath.Join(root, "nested", "second.txt")
	writeTreeFile(t, first, "first\n", 0o644)
	writeTreeFile(t, second, "second\n", 0o644)

	baseline, err := Directory(root)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G302 -- this test intentionally toggles the executable bit.
	if err := os.Chmod(first, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := Directory(root)
	if err != nil {
		t.Fatal(err)
	}
	if baseline == executable {
		t.Fatal("executable bit did not change content tree revision")
	}
	if err := os.Rename(second, filepath.Join(root, "nested", "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	renamed, err := Directory(root)
	if err != nil {
		t.Fatal(err)
	}
	if executable == renamed {
		t.Fatal("path change did not change content tree revision")
	}
	writeTreeFile(t, first, "changed\n", 0o755)
	changed, err := Directory(root)
	if err != nil {
		t.Fatal(err)
	}
	if renamed == changed {
		t.Fatal("content change did not change content tree revision")
	}
}

func writeTreeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
