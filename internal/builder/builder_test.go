package builder

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/breakfix/breakfix/internal/incusprovider"
)

func TestNodeImageFilesPreserveBundlePathsAndModes(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nodes", "proxy"), 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "nodes", "proxy", "generate.sh")
	//nolint:gosec // This fixture verifies that executable script modes are preserved in Node bundles.
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o750); err != nil {
		t.Fatal(err)
	}

	files, err := incusprovider.ImageFilesFromDirectory(root)
	if err != nil {
		t.Fatalf("collect node image files: %v", err)
	}
	if len(files) != 1 || files[0].Path != "nodes/proxy/generate.sh" || files[0].Mode != 0o750 || string(files[0].Content) != "#!/bin/sh\n" {
		t.Fatalf("node image files = %#v", files)
	}
}

func TestNodeImageFilesRejectSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("/etc/passwd", filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := incusprovider.ImageFilesFromDirectory(root); err == nil {
		t.Fatal("symlink unexpectedly accepted in node image bundle")
	}
}
