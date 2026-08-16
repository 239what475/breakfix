package workspacearchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEncodeIsCanonicalAndRoundTripsWorkspaceTree(t *testing.T) {
	first, err := Encode([]Entry{
		{Path: "checks/checkpoints.sh", Mode: 0o700, Content: []byte("#!/bin/sh\n")},
		{Path: "challenge.yaml", Mode: 0o600, Content: []byte("title: test\n")},
		{Path: "empty", Directory: true, Mode: 0o700},
	})
	if err != nil {
		t.Fatalf("encode first archive: %v", err)
	}
	second, err := Encode([]Entry{
		{Path: "challenge.yaml", Mode: 0o644, Content: []byte("title: test\n")},
		{Path: "empty", Directory: true, Mode: 0o755},
		{Path: "checks/checkpoints.sh", Mode: 0o755, Content: []byte("#!/bin/sh\n")},
	})
	if err != nil {
		t.Fatalf("encode second archive: %v", err)
	}
	if !bytes.Equal(first, second) || Digest(first) != Digest(second) {
		t.Fatalf("canonical archives differ: %s != %s", Digest(first), Digest(second))
	}
	entries, err := Decode(first)
	if err != nil {
		t.Fatalf("decode archive: %v", err)
	}
	if len(entries) != 4 || entries[0].Path != "challenge.yaml" || entries[1].Path != "checks" || entries[2].Path != "checks/checkpoints.sh" || entries[3].Path != "empty" {
		t.Fatalf("decoded entries = %#v", entries)
	}
	if entries[2].Mode != 0o755 || entries[0].Mode != 0o644 {
		t.Fatalf("decoded modes = %#v", entries)
	}

	destination := t.TempDir()
	if err := Extract(destination, first); err != nil {
		t.Fatalf("extract archive: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(destination, "checks", "checkpoints.sh"))
	if err != nil || string(content) != "#!/bin/sh\n" {
		t.Fatalf("extracted script = %q, err=%v", content, err)
	}
	info, err := os.Stat(filepath.Join(destination, "checks", "checkpoints.sh"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("extracted script mode = %v, err=%v", info.Mode(), err)
	}
}

func TestDecodeRejectsUnsafeEntries(t *testing.T) {
	for _, test := range []struct {
		name   string
		header tar.Header
	}{
		{name: "path traversal", header: tar.Header{Name: "../outside", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}},
		{name: "absolute", header: tar.Header{Name: "/outside", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}},
		{name: "symbolic link", header: tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "target"}},
		{name: "hard link", header: tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "target"}},
		{name: "fifo", header: tar.Header{Name: "pipe", Typeflag: tar.TypeFifo}},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := unsafeArchive(t, test.header, []byte("x"))
			if _, err := Decode(archive); err == nil {
				t.Fatalf("Decode accepted %s", test.name)
			}
		})
	}
}

func TestCanonicalizeRejectsDuplicateAndFileParent(t *testing.T) {
	duplicate := unsafeArchive(t, tar.Header{Name: "same", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}, []byte("a"), tar.Header{Name: "same", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}, []byte("b"))
	if _, err := Canonicalize(duplicate); err == nil {
		t.Fatal("Canonicalize accepted duplicate path")
	}
	parent := unsafeArchive(t, tar.Header{Name: "parent", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}, []byte("a"), tar.Header{Name: "parent/child", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}, []byte("b"))
	if _, err := Canonicalize(parent); err == nil {
		t.Fatal("Canonicalize accepted file parent")
	}
}

func TestStoreProtectsReferencedSnapshotsAndRemovesStaleOrphans(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	archive, err := Encode([]Entry{{Path: "challenge.yaml", Content: []byte("title: one\n")}})
	if err != nil {
		t.Fatal(err)
	}
	canonical, digest, err := store.Save("workflow-one", archive)
	if err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	if !bytes.Equal(canonical, archive) {
		t.Fatal("store changed an already canonical archive")
	}
	path, err := store.Path("workflow-one", digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("workflow-one", digest); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	orphan, err := store.Path("workflow-two", digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(orphan), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, archive, 0o440); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(orphan, old, old); err != nil {
		t.Fatal(err)
	}
	if err := store.Cleanup([]Reference{{WorkflowID: "workflow-one", Digest: digest}}, time.Now()); err != nil {
		t.Fatalf("cleanup snapshots: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("referenced snapshot removed: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan snapshot still exists: %v", err)
	}
}

func TestStoreRepairsCorruptSnapshotAtItsCanonicalDigest(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	archive, err := Encode([]Entry{{Path: "draft.md", Content: []byte("saved draft\n")}})
	if err != nil {
		t.Fatal(err)
	}
	canonical, digest, err := store.Save("workflow-repair", archive)
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.Path("workflow-repair", digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0o440); err != nil {
		t.Fatal(err)
	}
	if _, restoredDigest, err := store.Save("workflow-repair", archive); err != nil || restoredDigest != digest {
		t.Fatalf("repair corrupted snapshot = %q, %v", restoredDigest, err)
	}
	read, err := store.Read("workflow-repair", digest)
	if err != nil || !bytes.Equal(read, canonical) {
		t.Fatalf("read repaired snapshot = %q, %v", read, err)
	}
}

func unsafeArchive(t *testing.T, values ...any) []byte {
	t.Helper()
	var destination bytes.Buffer
	writer := gzip.NewWriter(&destination)
	tarWriter := tar.NewWriter(writer)
	for index := 0; index < len(values); index += 2 {
		header := values[index].(tar.Header)
		content := values[index+1].([]byte)
		if err := tarWriter.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := tarWriter.Write(content); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return destination.Bytes()
}
