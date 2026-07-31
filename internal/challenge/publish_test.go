package challenge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyRegularFilesPreservesNestedAssets(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	for name, content := range map[string]string{
		"challenge.yaml":          "title: demo\n",
		"nodes/host/checks.sh":    "#!/bin/sh\n",
		"hints/checkpoint-one.md": "hint\n",
	} {
		path := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	if err := CopyRegularFiles(src, dst); err != nil {
		t.Fatalf("CopyRegularFiles: %v", err)
	}
	for _, name := range []string{"challenge.yaml", "nodes/host/checks.sh", "hints/checkpoint-one.md"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Fatalf("published copy missing %s: %v", name, err)
		}
	}
}
