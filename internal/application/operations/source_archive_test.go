package operations

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/breakfix/breakfix/internal/content/scenario"
)

func TestBuildSourceArchiveIsCanonicalAndPreservesExecutableBits(t *testing.T) {
	root := t.TempDir()
	if err := scenario.CopyRegularFiles(fixtureDirectory(t, "node-runtime-fixture"), root); err != nil {
		t.Fatal(err)
	}
	first, firstBytes, err := BuildSourceArchive(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(root, "problem.md"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	second, secondBytes, err := BuildSourceArchive(root)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("source archive changed without a source change: %#v / %#v", first, second)
	}
	if err := first.Validate(); err != nil || !strings.HasPrefix(first.Reference, "runnable-source://sha256/") || len(firstBytes) == 0 {
		t.Fatalf("invalid frozen source archive: %#v, %v", first, err)
	}

	reader, err := gzip.NewReader(bytes.NewReader(firstBytes))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	tarReader := tar.NewReader(reader)
	paths := make([]string, 0)
	modes := make(map[string]int64)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, header.Name)
		modes[header.Name] = header.Mode
	}
	if !slices.IsSorted(paths) || modes["nodes/host/assertions/initial-runtime-marker-absent.sh"] != 0o755 || modes["scenario.yaml"] != 0o644 {
		t.Fatalf("unexpected canonical archive entries: paths=%#v modes=%#v", paths, modes)
	}
}

func TestBuildSourceArchiveRejectsUnsafeOrEmptyTree(t *testing.T) {
	if _, _, err := BuildSourceArchive(t.TempDir()); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty source archive error = %v", err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "regular.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("regular.txt", filepath.Join(root, "linked.txt")); err != nil {
		t.Skipf("create symlink: %v", err)
	}
	if _, _, err := BuildSourceArchive(root); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("unsafe source archive error = %v", err)
	}
}
